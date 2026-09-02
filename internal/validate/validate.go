// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"bytes"
	"context"
	"os"
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
)

// DefaultBuildTimeout bounds one `go build ./...` when [Options.BuildTimeout]
// is not set.
//
// It is the same fixed, generous budget internal/engine puts on a baseline
// command, and for the same reason: this phase runs before there is any
// measurement to derive a budget from, so the number's only job is to stop a
// hung toolchain from hanging the run forever.
const DefaultBuildTimeout = 10 * time.Minute

// Options is everything [Validate] needs.
//
// The zero value is not usable: a snapshot, a catalogue, a module path and a
// located toolchain have no defaults that could not be somebody's working tree
// or somebody else's `go`.
type Options struct {
	// Snap is the snapshot to validate. It is rewritten in place — that is what
	// a snapshot is for — and it must be the same copy the catalogue was
	// discovered in.
	Snap *snapshot.Snapshot

	// Catalog is the mutant set to instrument and validate. Its dense indices
	// size the generated activation array, so the catalogue validated here and
	// the catalogue the runner activates against must be the same one.
	Catalog *mutation.Catalog

	// Hints are the rewrite sites discovery chose, one per catalogued mutant,
	// as [instrument.HintsOf] indexes them. They travel through this phase
	// untouched: every rewrite it makes, the first and every one the search
	// makes afterwards, is composed from the same hints against the same
	// pristine bytes.
	Hints instrument.Hints

	// ModulePath is the import path of the main module at the snapshot root,
	// which the generated runtime's import path is built from.
	ModulePath string

	// Toolchain is the located Go toolchain every build goes through.
	Toolchain gocmd.Toolchain

	// Jobs is the build parallelism, passed to `go build -p`. Zero or negative
	// leaves the go command to its own default. It is the *compiler's*
	// parallelism and never this phase's: the builds are strictly serial,
	// because each one is a statement about the bytes currently in the snapshot
	// and two concurrent builds of one directory would be statements about
	// bytes neither of them chose.
	Jobs int

	// BuildTimeout bounds one build. Zero selects [DefaultBuildTimeout].
	BuildTimeout time.Duration

	// Env is the complete environment for every build, in "KEY=VALUE" form. Nil
	// inherits this process's environment, which is os/exec's rule; a run
	// composes the set explicitly so that a GOFLAGS or a GOWORK from the
	// developer's shell cannot decide what the snapshot resolves against.
	Env []string

	// Mode selects which tree is instrumented and validated. The zero value is
	// [instrument.ModeMutant], so a caller written before the probe tree existed
	// keeps validating exactly what it always did.
	//
	// Everything this phase does is the same either way — one build, the
	// pristine gate, per-file isolation, rejections in catalogue order — and so
	// is the meaning of accepting a mutant. What changes is what a *rejection*
	// says: in the mutant tree it is "this mutant cannot be compiled and will
	// not be run", and in the probe tree it is "this mutant's probe site cannot
	// be compiled and will not be measured". The mutant itself is untouched
	// either way, since the two trees are different snapshots.
	//
	// The result does not carry the mode back. A caller that cannot say which
	// tree it asked for has a bigger problem than this field would solve, and a
	// [Result] that answered it would invite exactly the code that reads the
	// answer instead of knowing it.
	Mode instrument.Mode
}

// A Rejection is one catalogued mutant that cannot be compiled, and the
// compiler's own explanation of why.
//
// Rejections are data rather than errors. A candidate whose guard does not
// compile is an ordinary, expected outcome of the design — see internal/instrument
// on why compiling is how that is established — and the run reports it, scores
// around it, and carries on. What must never happen is a candidate disappearing silently, which is why
// every field here is filled in: an ID nothing can look up, a coordinate nobody
// can jump to, or a rejection with no diagnostic would each amount to the same
// silence in a different disguise.
type Rejection struct {
	// ID is the full 64 hex character stable identity.
	ID string
	// DisplayID is the short form, as the console prints it.
	DisplayID string
	// Path is the '/'-normalized module-relative source path.
	Path string
	// Line is the 1-based line the candidate's span starts on, and Column the
	// 1-based byte offset within that line — the same coordinates discovery
	// reports, so a rejected mutant and a live one are named the same way.
	Line   int
	Column int
	// Rule is the operator that proposed the edit.
	Rule string
	// Diagnostic is what the compiler said about the build that condemned this
	// candidate, location prefix and all.
	Diagnostic string
}

// A Result is everything one validation pass established.
type Result struct {
	// AcceptedIDs are the mutants that compile, in catalogue order. It is the
	// set the execution phase may run, and its order is the catalogue's so that
	// two runs over one workspace produce the same sequence.
	//
	// It means "compiles" only when [Validate] returned no error. Alongside an
	// error it is what had not been rejected when the phase stopped, which is
	// something to report and never something to run.
	AcceptedIDs []string

	// Rejected are the mutants that do not compile, in catalogue order.
	Rejected []Rejection

	// Instrumented describes the snapshot as it finally stands: the generated
	// runtime, and the guards that survived validation. Its GuardsByFile and
	// FilesInstrumented are the state after isolation rather than before it, so
	// a file whose every candidate was rejected is absent from both.
	Instrumented instrument.Result

	// Builds is how many `go build` invocations the phase spent. One means the
	// whole catalogue compiled on the first try, which is the ordinary case and
	// the one the schemata design exists to make ordinary.
	Builds int
}

// Validate instruments the snapshot with the whole catalogue and establishes,
// by compiling it, which mutants are real.
//
// The fast path is one build. Instrumentation writes every catalogued mutant
// into the tree at once, `go build ./...` compiles it, and a green build means
// every candidate is accepted — no bisection, no second compile, nothing else
// to decide.
//
// A red build means at least one guard cannot compile, and the work is to find
// out which without losing the ones that can. Every catalogued file is restored
// to its pristine bytes and the tree is built again: if it still fails, nothing
// this phase could reject would fix it and it says so ([CodeNotMutantInduced])
// rather than bisecting a tree that was already broken. Otherwise the files the
// compiler named are searched one at a time — halving while that is cheaper,
// scanning when it is not, verifying every join — and each ends up carrying the
// largest subset of its candidates that was seen to compile. The undecided
// files go back in, the tree is built again, and whatever the compiler names
// this time is searched in the same way, until a build comes back green.
//
// Determinism is a promise about the whole of that: the same snapshot and the
// same catalogue produce the same accepted set, the same rejections in
// catalogue order, and the same bytes on disk. It rests on the compiler being
// deterministic too — the same source naming the same files — which is a
// property go builds have and is worth stating because the search would inherit
// any drift in it.
//
// On failure the Result is returned filled in as far as the phase got, so that
// a caller can report what was established before it stopped.
func Validate(ctx context.Context, opts Options) (Result, error) {
	if err := opts.validate(); err != nil {
		return Result{}, err
	}
	v := &validator{
		root:      opts.Snap.Root,
		catalog:   opts.Catalog,
		hints:     opts.Hints,
		mode:      opts.Mode,
		toolchain: opts.Toolchain,
		jobs:      opts.Jobs,
		timeout:   opts.BuildTimeout,
		env:       opts.Env,
		byPath:    make(map[string][]mutation.Mutant),
		pristine:  make(map[string][]byte),
		guards:    make(map[string]int),
	}
	if v.timeout <= 0 {
		v.timeout = DefaultBuildTimeout
	}
	v.build = v.buildSnapshot
	v.apply = v.instrumentFile
	return v.run(ctx, opts.ModulePath)
}

// validate rejects options that cannot describe a validation pass.
func (o Options) validate() error {
	switch {
	case o.Snap == nil:
		return &Error{Code: CodeOptions, Message: "no snapshot was given"}
	case strings.TrimSpace(o.Snap.Root) == "":
		return &Error{Code: CodeOptions, Message: "the snapshot has no root directory"}
	case o.Catalog == nil:
		return &Error{Code: CodeOptions, Message: "no catalogue was given"}
	case strings.TrimSpace(o.ModulePath) == "":
		return &Error{Code: CodeOptions, Message: "no module path was given"}
	case strings.TrimSpace(o.Toolchain.GoBin) == "":
		// Refused here rather than left to surface from the first build as a
		// spec error about an empty program name, which describes the symptom
		// and not the mistake.
		return &Error{Code: CodeOptions, Message: "no Go toolchain was located"}
	}
	return nil
}

// A validator is one validation pass.
//
// The two function fields are the seam the search is tested through. Everything
// above them — restoring bytes, writing guards, running a toolchain, reading an
// exit status — is what a fake replaces, and what is left is an algorithm over
// "does this subset compile", which is the part worth testing exhaustively and
// the part a real toolchain makes far too slow to test that way.
type validator struct {
	root    string
	catalog *mutation.Catalog
	hints   instrument.Hints
	// mode is the tree being validated, carried into both instrumentation
	// calls: the whole-tree one that starts the phase and the per-file one every
	// step of the search rewrites through. A mode that reached only the first
	// would produce a tree that turned back into the other the moment anything
	// was bisected.
	mode      instrument.Mode
	toolchain gocmd.Toolchain
	jobs      int
	timeout   time.Duration
	env       []string

	// runtimeImport is the import path of the generated activation package, as
	// the full instrumentation pass settled it. Every later rewrite is handed
	// the same one: the package is written once and never regenerated, because
	// its dense indices are what every guard in the tree spells.
	runtimeImport string

	// paths are the catalogued files in sorted order, and byPath their mutants
	// in catalogue order.
	paths  []string
	byPath map[string][]mutation.Mutant
	// pristine holds the bytes of every catalogued file as they were before
	// instrumentation. They are read once, up front, and every rewrite in the
	// phase is composed against them.
	pristine map[string][]byte
	// guards counts the guards each file currently carries.
	guards map[string]int
	// builds counts the builds spent.
	builds int

	apply func(path string, subset []mutation.Mutant) error
	build func(ctx context.Context) (verdict, error)
}

// run is the phase proper.
func (v *validator) run(ctx context.Context, modulePath string) (Result, error) {
	if err := v.readPristine(); err != nil {
		return Result{}, err
	}
	instrumented, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: v.root,
		ModulePath:   modulePath,
		Catalog:      v.catalog,
		Hints:        v.hints,
		Mode:         v.mode,
	})
	if err != nil {
		return Result{}, err
	}
	v.runtimeImport = instrumented.RuntimeImport
	for path, count := range instrumented.GuardsByFile {
		v.guards[path] = count
	}

	rejected, searchErr := v.search(ctx)
	result := v.result(instrumented)
	result.Rejected, result.AcceptedIDs = v.report(rejected)
	if searchErr != nil {
		return result, searchErr
	}
	return result, nil
}

// search establishes which candidates compile, leaving the snapshot holding
// exactly those.
//
// The first build is the whole phase in the ordinary case. Everything after it
// exists for the case where a guard did not compile, and the shape of that work
// is fixed by one requirement: whether a subset of one file compiles must be a
// question about that file. So before anything is searched, every catalogued
// file is put back to its pristine bytes — which both proves the failure is
// something this phase can fix and makes the empty subset a known-good starting
// point — and the files that have not been decided yet stay pristine while
// their neighbours are searched. A file left instrumented would answer for
// itself in every build and the search would reject candidates until it ran out
// of them.
func (v *validator) search(ctx context.Context) ([]condemned, error) {
	failing, err := v.build(ctx)
	if err != nil {
		return nil, err
	}
	if !failing.failed {
		return nil, nil
	}

	pending := slices.Clone(v.paths)
	if restoreErr := v.restore(pending); restoreErr != nil {
		return nil, restoreErr
	}
	gate, err := v.build(ctx)
	if err != nil {
		return nil, err
	}
	if gate.failed {
		return nil, &Error{
			Code: CodeNotMutantInduced,
			Message: "the snapshot does not build with every mutant removed, so the failure is not " +
				"something go-mutants introduced; nothing was rejected",
			Output: gate.output,
		}
	}

	var rejected []condemned
	for {
		for _, path := range v.blame(failing, pending) {
			accepted, condemnedHere, err := isolate(ctx, v.byPath[path], v.probe(path))
			if err != nil {
				return rejected, err
			}
			// The last probe left whichever subset it tried on disk, which is
			// not necessarily the accepted one.
			if err := v.apply(path, accepted); err != nil {
				return rejected, err
			}
			rejected = append(rejected, condemnedHere...)
			pending = slices.DeleteFunc(pending, func(p string) bool { return p == path })
		}

		// Everything still undecided goes back in whole, and the build says
		// whether the phase is finished or has just learned where to look next.
		if err := v.reinstate(pending); err != nil {
			return rejected, err
		}
		result, err := v.build(ctx)
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
}

// restore puts files back to their pristine bytes, and reinstate writes every
// candidate of theirs back in.
//
// The pair is the state machine the search runs on, and they are named rather
// than written out at each of their three call sites because the difference
// between them is the difference between "this file is not being asked about"
// and "this file is being asked about whole".
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

// blame returns the undecided files a failing build pointed at, in pending
// order.
//
// A failure that names no undecided file is not a reason to stop. The compiler
// can report a guard's damage at a line in another file, or say nothing about a
// file whose package never got compiled because a dependency failed first, and
// in both cases the candidates are still in the tree and still have to be
// found. So the fallback is to search everything still undecided: slower, and
// it terminates with the same answer, which is the trade this phase should
// always make.
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

// probe binds the search's one operation to a file: write exactly this subset,
// build, and report whether the snapshot still fails.
func (v *validator) probe(path string) probe {
	return func(ctx context.Context, subset []mutation.Mutant) (verdict, error) {
		if err := v.apply(path, subset); err != nil {
			return verdict{}, err
		}
		return v.build(ctx)
	}
}

// readPristine reads every catalogued file before instrumentation touches it.
//
// Reading up front rather than on demand is what makes the phase possible at
// all: after the first pass the files on disk hold guards, and the spans in the
// catalogue describe the bytes underneath them. It also fixes the order of the
// files, which is the order everything downstream reports in.
func (v *validator) readPristine() error {
	for _, m := range v.catalog.Mutants() {
		if _, seen := v.byPath[m.Path]; !seen {
			v.paths = append(v.paths, m.Path)
		}
		v.byPath[m.Path] = append(v.byPath[m.Path], m)
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

// instrumentFile is the real [validator.apply]: rewrite one file so that it
// carries exactly this subset of its candidates.
func (v *validator) instrumentFile(path string, subset []mutation.Mutant) error {
	guards, err := instrument.InstrumentFile(instrument.FileOptions{
		SnapshotRoot:  v.root,
		RuntimeImport: v.runtimeImport,
		Path:          path,
		Source:        v.pristine[path],
		Mutants:       subset,
		Hints:         v.hints,
		Mode:          v.mode,
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

// buildSnapshot is the real [validator.build]: one `go build ./...` in the
// snapshot, with whatever it links sent to the null device.
//
// The order of the cases is the contract, and it is internal/engine's. A
// cancelled run comes back from the runner as an unavailable exit code with no
// error and no timeout, which is indistinguishable from a build failure unless
// the context is asked — so it is asked before the exit status is judged, and
// after the two conditions that are definitely not cancellations. A non-zero
// exit is the only one of the four that is not an error: it is the compiler
// answering the question this phase asked it.
func (v *validator) buildSnapshot(ctx context.Context) (verdict, error) {
	v.builds++

	spec := v.toolchain.Command(buildArgs(v.jobs)...)
	spec.Dir = v.root
	spec.Env = v.env
	spec.Timeout = v.timeout

	result := runner.Run(ctx, spec)
	switch {
	case result.Err != nil:
		return verdict{}, &Error{
			Code:    CodeBuildFailed,
			Message: "the snapshot could not be built: the command could not be run",
			Output:  string(result.Output),
			Err:     result.Err,
		}
	case result.TimedOut:
		return verdict{}, &Error{
			Code:    CodeBuildTimedOut,
			Message: "the snapshot did not build within " + v.timeout.String(),
			Output:  string(result.Output),
		}
	case ctx.Err() != nil:
		return verdict{}, &Error{
			Code:    CodeInterrupted,
			Message: "validation was interrupted",
			Err:     ctx.Err(),
		}
	}
	return verdict{failed: result.ExitCode != 0, output: string(result.Output)}, nil
}

// buildArgs is the argument vector of one validation build.
//
// `-o os.DevNull` is the load-bearing part of it, and it is not tidiness. The
// go command writes a linked executable into its working directory whenever the
// pattern it is given resolves to exactly one package and that package is
// `main` — cmd/go decides it with `len(pkgs) == 1 && pkgs[0].Name == "main" &&
// cfg.BuildO == ""` — and the working directory here is the snapshot root.
// Nothing downstream would forgive that file: [snapshot.Snapshot.Redigest]
// applies no exclusions, because every byte under the root is go-mutants' own,
// and internal/engine's drift gate forgives exactly two things, a changed file
// that carries guards and an addition under the generated runtime directory. So
// a single-directory `package main` module — the shape of most Go command line
// tools — would end its run being told that its tests write into the package
// directory they run in, about a file go-mutants had written itself.
//
// The null device rather than a scratch directory outside the snapshot, which
// is how internal/execute keeps its test binaries out of the tree: `-o
// <directory>` makes cmd/go build the main packages *and only those*, skipping
// every package whose name is not `main`, so a guard in a library package would
// never be compiled and a library-only module — the shape of most Go modules —
// would fail outright with "go: no main packages to build". The null device is
// special-cased instead by clearing the output path, which lands on the same
// "compile every package, discard the objects" path a bare `go build ./...`
// takes: same packages, same diagnostics, same exit status, nothing written
// anywhere. Measured on go1.26.5 over four module shapes — single main, main
// plus library, library only, two mains — building and failing to build, the
// output was byte-identical to the bare form every time, which is what keeps
// [parseDiagnostics] and the search reading exactly what they read before. It
// asks nothing new of a toolchain either: cmd/go has had the case since at
// least go1.16, and internal/execute already needs the `go list -json=<fields>`
// form that arrived in go1.19.
//
// It is a function of its own so that the flag can be tested without a
// toolchain. Every other statement this package makes about a build is made by
// an integration test, and a missing `-o` is invisible to all of them: the
// generated runtime package is a second package under `./...`, so by the time
// this phase builds anything the go command has no single main package to name
// an executable after and writes nothing whatever this vector says. The flag is
// what makes that a property of the phase instead of a property of the tree it
// happens to be pointed at.
func buildArgs(jobs int) []string {
	args := make([]string, 0, 6)
	args = append(args, "build", "-o", os.DevNull)
	if jobs > 0 {
		args = append(args, "-p", strconv.Itoa(jobs))
	}
	return append(args, "./...")
}

// result assembles what the snapshot now holds, starting from what the full
// instrumentation pass reported and correcting it to the state isolation left
// behind.
func (v *validator) result(instrumented instrument.Result) Result {
	files := make([]string, 0, len(v.guards))
	guards := make(map[string]int, len(v.guards))
	for _, path := range v.paths {
		if count, ok := v.guards[path]; ok {
			files = append(files, path)
			guards[path] = count
		}
	}
	instrumented.FilesInstrumented = files
	instrumented.GuardsByFile = guards
	return Result{Instrumented: instrumented, Builds: v.builds}
}

// report turns the condemned candidates into the two lists a caller reads,
// both in catalogue order.
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

// rejection describes one rejected mutant, in the coordinates discovery uses
// and the compiler's own words.
func (v *validator) rejection(m mutation.Mutant, output string) Rejection {
	src := v.pristine[m.Path]
	startLine, column := position(src, m.Span.StartByte)
	endLine, _ := position(src, m.Span.EndByte)

	// Never empty. A rejection with no explanation is the silence this whole
	// phase exists to avoid, and RunReport v1 requires a non-empty diagnostic
	// on every rejected entry, so the last resort says that the compiler said
	// nothing rather than saying nothing itself.
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

// position returns the 1-based line and 1-based byte column of an offset.
//
// Bytes rather than runes, and 1-based on both axes, because that is what
// go/token reports and therefore what internal/discover puts on a candidate: a
// rejected mutant and a live one have to be named the same way, and nothing
// downstream would catch it if they were not.
func position(src []byte, offset uint32) (int, int) {
	if int(offset) > len(src) {
		offset = uint32(len(src))
	}
	before := src[:offset]
	line := 1 + bytes.Count(before, []byte("\n"))
	column := int(offset) - (bytes.LastIndexByte(before, '\n') + 1) + 1
	return line, column
}

// firstLine returns the first non-empty line of s, trimmed. It is the last
// resort for a rejection's diagnostic, for output that held no line this
// package could locate.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
