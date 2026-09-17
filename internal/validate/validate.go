// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/trace"
)

const DefaultBuildTimeout = 10 * time.Minute

type Options struct {
	Snap *snapshot.Snapshot

	Catalog *mutation.Catalog

	Hints instrument.Hints

	Modules []Module

	Toolchain gocmd.Toolchain

	Jobs int

	BuildTimeout time.Duration

	Env []string

	Mode instrument.Mode

	Packages []string

	Trace *trace.Recorder
}

type Module struct {
	Dir  string
	Path string
}

func (m Module) mutants(catalog *mutation.Catalog) string {
	if first, ok := catalog.At(0); ok && first.ModulePath == "" {
		return ""
	}
	return m.Path
}

type Rejection struct {
	ID         string
	DisplayID  string
	Path       string
	Line       int
	Column     int
	Rule       string
	Diagnostic string
}

type Result struct {
	AcceptedIDs []string

	Rejected []Rejection

	Instrumented instrument.Result

	Runtimes []instrument.Result

	Builds int
}

func (r Result) Loops() int {
	total := 0
	for _, tree := range r.Runtimes {
		total += tree.Loops
	}
	return total
}

func (r Result) RuntimeDirs() []string {
	dirs := make([]string, 0, len(r.Runtimes))
	for _, runtime := range r.Runtimes {
		if runtime.RuntimeDir != "" {
			dirs = append(dirs, runtime.RuntimeDir)
		}
	}
	return dirs
}

func Validate(ctx context.Context, opts Options) (Result, error) {
	if err := opts.validate(); err != nil {
		return Result{}, err
	}
	v := &validator{
		root:      opts.Snap.Root,
		catalog:   opts.Catalog,
		hints:     opts.Hints,
		mode:      opts.Mode,
		packages:  slices.Clone(opts.Packages),
		toolchain: opts.Toolchain,
		jobs:      opts.Jobs,
		timeout:   opts.BuildTimeout,
		env:       opts.Env,
		recorder:  opts.Trace,
		modules:   slices.Clone(opts.Modules),
		byPath:    make(map[string][]mutation.Mutant),
		pristine:  make(map[string][]byte),
		guards:    make(map[string]int),
		files:     make(map[string]fileRef),
	}
	v.timeout = buildTimeout(opts.BuildTimeout)
	v.build = v.buildSnapshot
	v.apply = v.instrumentFile
	return v.run(ctx)
}

func buildTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultBuildTimeout
	}
	return configured
}

func (o Options) validate() error {
	switch {
	case o.Snap == nil:
		return &Error{Code: CodeOptions, Message: "no snapshot was given"}
	case strings.TrimSpace(o.Snap.Root) == "":
		return &Error{Code: CodeOptions, Message: "the snapshot has no root directory"}
	case o.Catalog == nil:
		return &Error{Code: CodeOptions, Message: "no catalogue was given"}
	case len(o.Modules) == 0:
		return &Error{Code: CodeOptions, Message: "no module was given to instrument"}
	case strings.TrimSpace(o.Toolchain.GoBin) == "":
		return &Error{Code: CodeOptions, Message: "no Go toolchain was located"}
	}
	for _, module := range o.Modules {
		if strings.TrimSpace(module.Dir) == "" || strings.TrimSpace(module.Path) == "" {
			return &Error{
				Code:    CodeOptions,
				Message: "a module was given with no directory or no import path",
			}
		}
	}
	named := make(map[string]bool, len(o.Modules))
	for _, module := range o.Modules {
		named[module.mutants(o.Catalog)] = true
	}
	for _, m := range o.Catalog.Mutants() {
		if !named[m.ModulePath] {
			return &Error{
				Code: CodeOptions,
				Message: "the catalogue holds mutants of " + strconv.Quote(m.ModulePath) +
					", which is not one of the modules given",
			}
		}
	}
	return nil
}

type validator struct {
	root      string
	catalog   *mutation.Catalog
	hints     instrument.Hints
	mode      instrument.Mode
	packages  []string
	toolchain gocmd.Toolchain
	jobs      int
	timeout   time.Duration
	env       []string
	recorder  *trace.Recorder

	runtimes []instrument.Result

	modules []Module

	paths    []string
	byPath   map[string][]mutation.Mutant
	files    map[string]fileRef
	pristine map[string][]byte
	guards   map[string]int
	builds   int

	apply func(path string, subset []mutation.Mutant) error
	build func(ctx context.Context) (verdict, error)
}

func (v *validator) run(ctx context.Context) (Result, error) {
	if err := v.readPristine(); err != nil {
		return Result{}, err
	}
	if err := v.instrumentModules(); err != nil {
		return Result{}, err
	}
	v.recorder.Validate(trace.ValidateRecord{
		Tree:       v.tree(),
		Op:         trace.ValidateOpInstrument,
		Candidates: v.catalog.Len(),
	})

	rejected, searchErr := v.search(ctx)
	result := v.result()
	result.Rejected, result.AcceptedIDs = v.report(rejected)
	if searchErr != nil {
		return result, searchErr
	}
	return result, nil
}

func (v *validator) search(ctx context.Context) ([]condemned, error) {
	rejected, err := v.bisect(ctx)
	if err != nil {
		return rejected, err
	}
	v.recorder.Validate(trace.ValidateRecord{
		Tree:     v.tree(),
		Op:       trace.ValidateOpDone,
		Builds:   v.builds,
		Accepted: v.candidates() - len(rejected),
		Rejected: len(rejected),
	})
	return rejected, nil
}

func (v *validator) bisect(ctx context.Context) ([]condemned, error) {
	pending := slices.Clone(v.paths)
	failing, err := v.compile(ctx, pending, "")
	if err != nil {
		return nil, err
	}
	if !failing.failed {
		return nil, nil
	}

	if restoreErr := v.restore(pending); restoreErr != nil {
		return nil, restoreErr
	}
	gate, err := v.compile(ctx, nil, "")
	if err != nil {
		return nil, err
	}
	v.recorder.Validate(trace.ValidateRecord{
		Tree:    v.tree(),
		Op:      trace.ValidateOpGate,
		Build:   v.builds,
		Failed:  gate.failed,
		ExecSeq: gate.execSeq,
	})
	if gate.failed {
		return nil, &Error{
			Code: CodeNotMutantInduced,
			Message: "the snapshot does not build with every mutant removed, so the failure is not " +
				"something go-mutants introduced; nothing was rejected",
			Output: gate.output,
		}
	}

	var rejected []condemned
	for range len(pending) + 1 {
		for _, path := range failing.blamed {
			accepted, condemnedHere, err := isolate(ctx, v.byPath[path], v.probe(path))
			if err != nil {
				return rejected, err
			}
			v.recordIsolation(path, len(accepted), condemnedHere)
			if err := v.apply(path, accepted); err != nil {
				return rejected, err
			}
			rejected = append(rejected, condemnedHere...)
			pending = slices.DeleteFunc(pending, func(p string) bool { return p == path })
		}

		if err := v.reinstate(pending); err != nil {
			return rejected, err
		}
		result, err := v.compile(ctx, pending, "")
		if err != nil {
			return rejected, err
		}
		if !result.failed {
			return rejected, nil
		}
		if len(pending) == 0 {
			return rejected, &Error{
				Code: CodeStillFailing,
				Message: "the snapshot does not build although every catalogued file was isolated and " +
					"each accepted subset compiled on its own, which means candidates in different files " +
					"interact; the accepted set cannot be trusted",
				Output: result.output,
			}
		}
		if err := v.restore(pending); err != nil {
			return rejected, err
		}
		failing = result
	}
	return rejected, &Error{
		Code: CodeStillFailing,
		Message: "the snapshot does not build after one isolation pass per catalogued file, " +
			"which means candidates in different files interact; the accepted set cannot be trusted",
	}
}

func (v *validator) restore(paths []string) error {
	for _, path := range paths {
		if err := v.apply(path, nil); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) reinstate(paths []string) error {
	for _, path := range paths {
		if err := v.apply(path, v.byPath[path]); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) blame(failing verdict, pending []string) []string {
	named := make(map[string]bool)
	for _, path := range blamedPaths(parseDiagnostics(failing.output, v.root)) {
		named[path] = true
	}
	blamed := make([]string, 0, len(pending))
	for _, path := range pending {
		if named[path] {
			blamed = append(blamed, path)
		}
	}
	if len(blamed) == 0 {
		return slices.Clone(pending)
	}
	return blamed
}

func (v *validator) probe(path string) probe {
	return func(ctx context.Context, subset []mutation.Mutant) (verdict, error) {
		if err := v.apply(path, subset); err != nil {
			return verdict{}, err
		}
		return v.compile(ctx, nil, path)
	}
}

func (v *validator) compile(ctx context.Context, pending []string, path string) (verdict, error) {
	v.builds++
	result, err := v.build(ctx)
	if err != nil {
		return result, err
	}
	if result.failed && len(pending) > 0 {
		result.blamed = v.blame(result, pending)
	}
	record := trace.ValidateRecord{
		Tree:    v.tree(),
		Op:      trace.ValidateOpBuild,
		Build:   v.builds,
		Failed:  result.failed,
		Path:    path,
		Blamed:  result.blamed,
		ExecSeq: result.execSeq,
	}
	if len(result.blamed) > 0 {
		record.Pending = len(pending)
	}
	v.recorder.Validate(record)
	return result, nil
}

func (v *validator) recordIsolation(path string, accepted int, rejected []condemned) {
	v.recorder.Validate(trace.ValidateRecord{
		Tree:       v.tree(),
		Op:         trace.ValidateOpIsolate,
		Path:       path,
		Candidates: len(v.byPath[path]),
		Accepted:   accepted,
	})
	for _, c := range rejected {
		v.recorder.Validate(trace.ValidateRecord{
			Tree:       v.tree(),
			Op:         trace.ValidateOpReject,
			Path:       c.mutant.Path,
			MutantID:   c.mutant.ID,
			Diagnostic: v.condemnation(c),
		})
	}
}

func (v *validator) condemnation(c condemned) string {
	return firstLine(v.rejection(c.mutant, c.output).Diagnostic)
}

func (v *validator) tree() string {
	if v.mode == instrument.ModeProbe {
		return trace.ValidateTreeProbe
	}
	return trace.ValidateTreeMutant
}

func (v *validator) candidates() int {
	total := 0
	for _, path := range v.paths {
		total += len(v.byPath[path])
	}
	return total
}

func (v *validator) readPristine() error {
	dirs := make(map[string]string, len(v.modules))
	for _, module := range v.modules {
		dirs[module.mutants(v.catalog)] = module.Dir
	}
	for _, m := range v.catalog.Mutants() {
		key := snapshotPath(dirs[m.ModulePath], m.Path)
		if _, seen := v.byPath[key]; !seen {
			v.paths = append(v.paths, key)
			v.files[key] = fileRef{module: m.ModulePath, path: m.Path}
		}
		v.byPath[key] = append(v.byPath[key], m)
	}
	slices.Sort(v.paths)

	for _, path := range v.paths {
		src, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(path)))
		if err != nil {
			return &Error{
				Code:    CodeSourceUnreadable,
				Message: "cannot read " + strconv.Quote(path) + " in the snapshot",
				Err:     err,
			}
		}
		v.pristine[path] = src
	}
	return nil
}

type fileRef struct {
	module        string
	path          string
	root          string
	runtimeImport string
	loopBase      uint32
}

func snapshotPath(dir, rel string) string {
	return path.Join(dir, rel)
}

func (v *validator) instrumentModules() error {
	for _, module := range v.modules {
		root := filepath.Join(v.root, filepath.FromSlash(module.Dir))
		instrumented, err := instrument.Instrument(instrument.Options{
			SnapshotRoot: root,
			ModulePath:   module.Path,
			Module:       module.mutants(v.catalog),
			Catalog:      v.catalog,
			Hints:        v.hints,
			Mode:         v.mode,
		})
		if err != nil {
			return err
		}
		instrumented.RuntimeDir = snapshotPath(module.Dir, instrumented.RuntimeDir)
		for i, rel := range instrumented.FilesInstrumented {
			instrumented.FilesInstrumented[i] = snapshotPath(module.Dir, rel)
		}
		lifted := make(map[string]int, len(instrumented.GuardsByFile))
		for rel, count := range instrumented.GuardsByFile {
			key := snapshotPath(module.Dir, rel)
			lifted[key] = count
			v.guards[key] = count
		}
		instrumented.GuardsByFile = lifted
		bases := make(map[string]uint32, len(instrumented.LoopBase))
		for rel, base := range instrumented.LoopBase {
			bases[snapshotPath(module.Dir, rel)] = base
		}
		instrumented.LoopBase = bases
		for _, key := range instrumented.FilesInstrumented {
			ref := v.files[key]
			ref.root = root
			ref.runtimeImport = instrumented.RuntimeImport
			ref.loopBase = bases[key]
			v.files[key] = ref
		}
		v.runtimes = append(v.runtimes, instrumented)
	}
	for key, ref := range v.files {
		if ref.root != "" {
			continue
		}
		module, ok := v.moduleOf(ref.module)
		if !ok {
			return &Error{
				Code:    CodeOptions,
				Message: "no module was given for " + strconv.Quote(ref.module),
			}
		}
		ref.root = filepath.Join(v.root, filepath.FromSlash(module.Dir))
		ref.runtimeImport = v.runtimeImportOf(module)
		ref.loopBase = v.treeOf(module).LoopBase[key]
		v.files[key] = ref
	}
	return nil
}

func (v *validator) moduleOf(modulePath string) (Module, bool) {
	for _, module := range v.modules {
		if module.mutants(v.catalog) == modulePath {
			return module, true
		}
	}
	return Module{}, false
}

func (v *validator) treeOf(module Module) instrument.Result {
	for i, candidate := range v.modules {
		if candidate == module && i < len(v.runtimes) {
			return v.runtimes[i]
		}
	}
	return instrument.Result{}
}

func (v *validator) runtimeImportOf(module Module) string {
	return v.treeOf(module).RuntimeImport
}

func (v *validator) instrumentFile(path string, subset []mutation.Mutant) error {
	ref := v.files[path]
	guards, err := instrument.InstrumentFile(instrument.FileOptions{
		SnapshotRoot:  ref.root,
		RuntimeImport: ref.runtimeImport,
		Path:          ref.path,
		Source:        v.pristine[path],
		Mutants:       subset,
		Hints:         v.hints,
		Mode:          v.mode,
		LoopBase:      ref.loopBase,
	})
	if err != nil {
		return err
	}
	if guards == 0 {
		delete(v.guards, path)
		return nil
	}
	v.guards[path] = guards
	return nil
}

func (v *validator) buildSnapshot(ctx context.Context) (verdict, error) {
	spec := v.toolchain.Command(buildArgs(v.jobs, v.packages)...)
	spec.Dir = v.root
	spec.Env = v.env
	spec.Timeout = v.timeout
	spec.Trace = v.recorder
	spec.Kind = trace.ExecKindValidateBuild

	result := runner.Run(ctx, spec)
	spent := verdict{execSeq: result.TraceSeq}
	switch {
	case result.Err != nil:
		return spent, &Error{
			Code:       CodeBuildFailed,
			Message:    "the snapshot could not be built: the command could not be run",
			Output:     string(result.Output),
			Err:        result.Err,
			Invocation: runner.CommandOf(spec, result),
		}
	case result.TimedOut:
		return spent, &Error{
			Code:       CodeBuildTimedOut,
			Message:    "the snapshot did not build within " + v.timeout.String(),
			Output:     string(result.Output),
			Invocation: runner.CommandOf(spec, result),
			TimedOut:   true,
		}
	case ctx.Err() != nil:
		return spent, &Error{
			Code:       CodeInterrupted,
			Message:    "validation was interrupted",
			Err:        ctx.Err(),
			Invocation: runner.CommandOf(spec, result),
		}
	}
	spent.failed = result.ExitCode != 0
	spent.output = string(result.Output)
	return spent, nil
}

func buildArgs(jobs int, packages []string) []string {
	args := make([]string, 0, 5+len(packages))
	args = append(args, "build", "-o", os.DevNull)
	if jobs > 0 {
		args = append(args, "-p", strconv.Itoa(jobs))
	}
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	return append(args, packages...)
}

func (v *validator) result() Result {
	files := make([]string, 0, len(v.guards))
	guards := make(map[string]int, len(v.guards))
	for _, path := range v.paths {
		if count, ok := v.guards[path]; ok {
			files = append(files, path)
			guards[path] = count
		}
	}
	merged := instrument.Result{FilesInstrumented: files, GuardsByFile: guards}
	if len(v.runtimes) == 1 {
		merged.RuntimeDir = v.runtimes[0].RuntimeDir
		merged.RuntimeImport = v.runtimes[0].RuntimeImport
	}
	return Result{Instrumented: merged, Runtimes: slices.Clone(v.runtimes), Builds: v.builds}
}

func (v *validator) report(rejected []condemned) ([]Rejection, []string) {
	condemnedBy := make(map[string]condemned, len(rejected))
	for _, c := range rejected {
		condemnedBy[c.mutant.ID] = c
	}

	rejections := make([]Rejection, 0, len(rejected))
	accepted := make([]string, 0, v.catalog.Len())
	for _, m := range v.catalog.Mutants() {
		c, ok := condemnedBy[m.ID]
		if !ok {
			accepted = append(accepted, m.ID)
			continue
		}
		rejections = append(rejections, v.rejection(m, c.output))
	}
	return rejections, accepted
}

func (v *validator) rejection(m mutation.Mutant, output string) Rejection {
	src := v.pristine[m.Path]
	startLine, column := position(src, m.Span.StartByte)
	endLine, _ := position(src, m.Span.EndByte)

	diagnostic := chooseDiagnostic(parseDiagnostics(output, v.root), m.Path, startLine, endLine)
	if diagnostic == "" {
		diagnostic = firstLine(output)
	}
	if diagnostic == "" {
		diagnostic = "the build failed without printing a diagnostic"
	}
	return Rejection{
		ID:         m.ID,
		DisplayID:  m.DisplayID,
		Path:       m.Path,
		Line:       startLine,
		Column:     column,
		Rule:       m.Rule.Name,
		Diagnostic: diagnostic,
	}
}

func position(src []byte, offset uint32) (int, int) {
	end := min(int(offset), len(src))
	before := src[:end]
	line := 1 + bytes.Count(before, []byte("\n"))
	column := end - (bytes.LastIndexByte(before, '\n') + 1) + 1
	return line, column
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
