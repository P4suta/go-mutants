// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/drift"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/operatorselect"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testflag"
	"github.com/P4suta/go-mutants/internal/validate"
)

const (
	defaultProfile       = "balanced"
	defaultMutantTimeout = 10 * time.Second
	defaultBuildTimeout  = 10 * time.Minute
	sessionPrefix        = "session-"
	execPrefix           = "exec-"
)

// Session is a discovered, validated, instrumented snapshot with test binaries
// compiled once. Its zero value is not usable. A Session permits concurrent
// Exec calls; Changes and Close wait for those calls to finish.
type Session struct {
	mu             sync.RWMutex
	root           string
	scratch        string
	env            []string
	catalog        *mutation.Catalog
	publicCatalog  Catalog
	accepted       map[string]bool
	rejections     map[string]Rejection
	binaries       []execute.TestBinary
	executeOptions execute.Options
	mutantTimeout  time.Duration
	preparedFiles  map[string]fileState
	closed         bool
}

// Prepare discovers, validates, instruments, verifies, and builds one reusable
// mutation session. A Workspace may be prepared exactly once, including when
// preparation fails after it has begun.
func (w *Workspace) Prepare(ctx context.Context, options PrepareOptions) (*Session, error) {
	if w == nil {
		return nil, errors.New("gomutants: prepare: nil workspace")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, errors.New("gomutants: prepare: workspace is closed")
	}
	if w.prepared {
		return nil, errors.New("gomutants: prepare: workspace has already been prepared")
	}
	w.prepared = true
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("gomutants: prepare: %w", err)
	}

	resolved, err := resolvePrepareOptions(options)
	if err != nil {
		return nil, err
	}
	rules, err := selectRules(resolved.Profile, resolved.Operators)
	if err != nil {
		return nil, err
	}
	include, err := discover.CompilePatterns(resolved.Include)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare include patterns: %w", err)
	}
	exclude, err := discover.CompilePatterns(resolved.Exclude)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare exclude patterns: %w", err)
	}

	found, err := discover.Discover(ctx, discover.Options{
		SnapshotRoot: w.snapshot.Root,
		Toolchain:    w.toolchain,
		Env:          slices.Clone(w.env),
		Rules:        rules,
		Include:      include,
		Exclude:      exclude,
	})
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare discovery: %w", err)
	}
	catalog, err := discover.BuildCatalog(found)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare catalog: %w", err)
	}
	hints, err := instrument.HintsOf(found.Candidates)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare instrumentation hints: %w", err)
	}

	validationEnv, err := overlayEnvironment(w.env, []string{"GOWORK=off"})
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare validation environment: %w", err)
	}
	validationEnv = prependEnvironmentPath(validationEnv, filepath.Dir(w.toolchain.GoBin))
	validated, err := validate.Validate(ctx, validate.Options{
		Snap:         w.snapshot,
		Catalog:      catalog,
		Hints:        hints,
		ModulePath:   found.ModulePath,
		Toolchain:    w.toolchain,
		Jobs:         resolved.Jobs,
		BuildTimeout: resolved.BuildTimeout,
		Env:          validationEnv,
	})
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare validation: %w", err)
	}

	verify := resolved.Verify
	verifyBase, err := overlayEnvironment(w.env, verify.Env)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare verification environment: %w", err)
	}
	verify.Env = nil
	verifyBase = gocmd.AppendGoflags(verifyBase, gocmd.VetOff)
	verified, err := w.runCommand(ctx, verify, verifyBase)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare instrumented verification: %w", err)
	}
	switch {
	case verified.TimedOut:
		return nil, fmt.Errorf("gomutants: prepare instrumented verification timed out after %s", verify.Timeout)
	case verified.ExitCode != 0:
		return nil, fmt.Errorf("gomutants: prepare instrumented verification exited with status %d: %s",
			verified.ExitCode, outputSummary(verified.Output))
	}
	if driftErr := checkInitialDrift(w.snapshot, validated.Instrumented); driftErr != nil {
		return nil, driftErr
	}

	scratch, err := os.MkdirTemp(w.scratch, sessionPrefix)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare session scratch: %w", err)
	}
	execOptions := execute.Options{
		Toolchain:    w.toolchain,
		SnapshotRoot: w.snapshot.Root,
		Packages:     slices.Clone(resolved.Packages),
		BinDir:       filepath.Join(scratch, "bin"),
		ScratchDir:   filepath.Join(scratch, "targets"),
		Env:          slices.Clone(w.env),
		Jobs:         resolved.Jobs,
		Timeout:      resolved.BuildTimeout,
	}
	binaries, err := execute.BuildTestBinaries(ctx, execOptions)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare test binaries: %w", err)
	}
	preparedFiles, err := scanFiles(w.snapshot.Root)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare snapshot state: %w", err)
	}

	accepted := make(map[string]bool, len(validated.AcceptedIDs))
	for _, id := range validated.AcceptedIDs {
		accepted[id] = true
	}
	publicCatalog, rejectionIndex := makeCatalog(
		w.snapshot.WorkspaceDigest,
		w.toolchain,
		resolved.Profile,
		found,
		catalog,
		validated.Rejected,
		accepted,
		binaries,
	)
	session := &Session{
		root:           w.snapshot.Root,
		scratch:        scratch,
		env:            slices.Clone(w.env),
		catalog:        catalog,
		publicCatalog:  publicCatalog,
		accepted:       accepted,
		rejections:     rejectionIndex,
		binaries:       slices.Clone(binaries),
		executeOptions: execOptions,
		mutantTimeout:  resolved.MutantTimeout,
		preparedFiles:  preparedFiles,
	}
	w.session = session
	return session, nil
}

func resolvePrepareOptions(opts PrepareOptions) (PrepareOptions, error) {
	if opts.Profile == "" {
		opts.Profile = defaultProfile
	}
	if _, err := mutation.ParseTier(opts.Profile); err != nil {
		return PrepareOptions{}, fmt.Errorf("gomutants: prepare profile %q: expected balanced, strong, or all", opts.Profile)
	}
	if opts.Jobs < 0 || opts.Jobs > 32 {
		return PrepareOptions{}, fmt.Errorf("gomutants: prepare jobs %d: expected 0 through 32", opts.Jobs)
	}
	if opts.Jobs == 0 {
		opts.Jobs = min(runtime.NumCPU(), 8)
	}
	if opts.BuildTimeout < 0 {
		return PrepareOptions{}, errors.New("gomutants: prepare build timeout is negative")
	}
	if opts.BuildTimeout == 0 {
		opts.BuildTimeout = defaultBuildTimeout
	}
	if opts.MutantTimeout < 0 {
		return PrepareOptions{}, errors.New("gomutants: prepare mutant timeout is negative")
	}
	if opts.MutantTimeout == 0 {
		opts.MutantTimeout = defaultMutantTimeout
	}
	if len(opts.Packages) == 0 {
		opts.Packages = []string{"./..."}
	}
	for _, pattern := range opts.Packages {
		if !relativePackagePattern(pattern) {
			return PrepareOptions{}, fmt.Errorf("gomutants: prepare package pattern %q is not module-relative", pattern)
		}
	}
	if len(opts.Verify.Argv) == 0 {
		opts.Verify.Argv = []string{"go", "test", "./..."}
	}
	if opts.Verify.Timeout == 0 {
		opts.Verify.Timeout = opts.BuildTimeout
	}
	return opts, nil
}

func relativePackagePattern(pattern string) bool {
	if pattern != "." && !strings.HasPrefix(pattern, "./") {
		return false
	}
	for element := range strings.SplitSeq(strings.ReplaceAll(pattern, `\`, "/"), "/") {
		if element == ".." {
			return false
		}
	}
	return true
}

func selectRules(profile string, operators []string) ([]mutation.Rule, error) {
	tier, _ := mutation.ParseTier(profile)
	rules, unknown := operatorselect.Select(tier, operators)
	if unknown != "" {
		return nil, fmt.Errorf("gomutants: prepare operator %q is not a canonical family or rule", unknown)
	}
	return rules, nil
}

// Catalog returns a deep copy of the session's deterministic catalog.
func (s *Session) Catalog() Catalog {
	if s == nil {
		return Catalog{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCatalog(s.publicCatalog)
}

// Exec runs one mutant against a selected test or fuzz target without
// rebuilding the prepared test binaries.
func (s *Session) Exec(ctx context.Context, request ExecRequest) (MutantResult, error) {
	if s == nil {
		return MutantResult{}, errors.New("gomutants: session exec: nil session")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return MutantResult{}, errors.New("gomutants: session exec: session is closed")
	}
	if request.Timeout < 0 {
		return MutantResult{}, errors.New("gomutants: session exec: timeout is negative")
	}
	mutant, err := s.catalog.ResolvePrefix(request.Mutant)
	if err != nil {
		return MutantResult{}, fmt.Errorf("gomutants: session exec mutant %q: %w", request.Mutant, err)
	}
	if !s.accepted[mutant.ID] {
		rejection := s.rejections[mutant.ID]
		return MutantResult{}, fmt.Errorf("gomutants: session exec mutant %s was rejected during validation: %s",
			mutant.DisplayID, rejection.Diagnostic)
	}
	binaryIndexes, err := selectTestPackages(s.root, s.binaries, request.Package)
	if err != nil {
		return MutantResult{}, err
	}
	env, err := overlayEnvironment(s.env, request.Env)
	if err != nil {
		return MutantResult{}, fmt.Errorf("gomutants: session exec environment: %w", err)
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = s.mutantTimeout
	}
	scratch, err := os.MkdirTemp(s.scratch, execPrefix)
	if err != nil {
		return MutantResult{}, fmt.Errorf("gomutants: session exec scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	targetArgs, err := sessionTargetArgs(request.Args, scratch)
	if err != nil {
		return MutantResult{}, err
	}

	opts := s.executeOptions
	opts.ScratchDir = scratch
	opts.Env = env
	attempt := execute.RunOne(ctx, opts, execute.MutantRun{
		ID:       mutant.ID,
		Timeout:  timeout,
		Binaries: binaryIndexes,
		Args:     targetArgs,
	}, s.binaries)
	result := MutantResult{
		ID:         mutant.ID,
		DisplayID:  mutant.DisplayID,
		Outcome:    Outcome(attempt.Outcome.String()),
		KilledBy:   attempt.KilledBy,
		Duration:   attempt.Duration,
		OutputTail: attempt.OutputTail,
	}
	if attempt.Err != nil {
		return result, fmt.Errorf("gomutants: session exec: %w", attempt.Err)
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("gomutants: session exec: %w", err)
	}
	return result, nil
}

// sessionTargetArgs supplies the one flag cmd/go normally adds around a fuzz
// target. A test binary refuses -test.fuzz without -test.fuzzcachedir; keeping
// that cache in the execution scratch preserves the read-only snapshot and
// lets the standard seed corpus compiled from testdata/fuzz run unchanged.
func sessionTargetArgs(args []string, scratch string) ([]string, error) {
	out := slices.Clone(args)
	fuzz := false
	for _, argument := range args {
		switch {
		case testflag.Match(argument, "test.fuzz"):
			fuzz = true
		case testflag.Match(argument, "test.fuzzcachedir"):
			return nil, errors.New("gomutants: session exec: -test.fuzzcachedir is reserved by the session")
		case testflag.Match(argument, "test.fuzzworker"):
			return nil, errors.New("gomutants: session exec: -test.fuzzworker is reserved by the Go fuzz coordinator")
		}
	}
	if !fuzz {
		return out, nil
	}
	cache := filepath.Join(scratch, "fuzz-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, fmt.Errorf("gomutants: session exec fuzz cache: %w", err)
	}
	return append(out, "-test.fuzzcachedir="+cache), nil
}

func selectTestPackages(root string, binaries []execute.TestBinary, selected string) ([]int, error) {
	if selected == "" {
		return nil, nil
	}
	var selectedDir string
	if selected == "." || strings.HasPrefix(selected, "./") {
		var err error
		selectedDir, err = moduleDirectory(root, selected)
		if err != nil {
			return nil, fmt.Errorf("gomutants: session exec package: %w", err)
		}
	}
	var indexes []int
	for i, binary := range binaries {
		if binary.ImportPath == selected || selectedDir != "" && samePath(binary.Dir, selectedDir) {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return nil, fmt.Errorf("gomutants: session exec package %q has no prepared test binary", selected)
	}
	return indexes, nil
}

func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Close waits for target executions and releases the session's binaries and
// scratch files. It is idempotent. Closing a Session does not close its parent
// Workspace; closing the Workspace closes both.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.scratch == "" {
		return nil
	}
	if err := os.RemoveAll(s.scratch); err != nil {
		return fmt.Errorf("gomutants: close session: %w", err)
	}
	return nil
}

func makeCatalog(
	workspaceDigest string,
	toolchain gocmd.Toolchain,
	profile string,
	found discover.Result,
	catalog *mutation.Catalog,
	rejected []validate.Rejection,
	accepted map[string]bool,
	binaries []execute.TestBinary,
) (Catalog, map[string]Rejection) {
	type locationKey struct {
		path string
		span mutation.Span
		rule string
	}
	locations := make(map[locationKey]discover.Located, len(found.Candidates))
	for _, candidate := range found.Candidates {
		key := locationKey{candidate.Path, candidate.Span, candidate.Rule.Name}
		if _, exists := locations[key]; !exists {
			locations[key] = candidate
		}
	}
	mutants := make([]Mutant, 0, catalog.Len())
	byID := make(map[string]Mutant, catalog.Len())
	for _, internal := range catalog.Mutants() {
		where := locations[locationKey{internal.Path, internal.Span, internal.Rule.Name}]
		public := Mutant{
			Index:        internal.Index,
			ID:           internal.ID,
			DisplayID:    internal.DisplayID,
			Path:         internal.Path,
			Package:      where.Package,
			Line:         where.Line,
			Column:       where.Column,
			StartByte:    internal.Span.StartByte,
			EndByte:      internal.Span.EndByte,
			Family:       string(internal.Rule.Family),
			Rule:         internal.Rule.Name,
			RuleVersion:  internal.Rule.Version,
			SourceDigest: internal.SourceDigest,
			Original:     internal.Original,
			Replacement:  internal.Replacement,
			Accepted:     accepted[internal.ID],
		}
		mutants = append(mutants, public)
		byID[public.ID] = public
	}
	rejections := make([]Rejection, 0, len(rejected))
	rejectionIndex := make(map[string]Rejection, len(rejected))
	for _, internal := range rejected {
		mutant := byID[internal.ID]
		public := Rejection{
			ID:         internal.ID,
			DisplayID:  mutant.DisplayID,
			Path:       mutant.Path,
			Line:       mutant.Line,
			Column:     mutant.Column,
			Rule:       mutant.Rule,
			Diagnostic: internal.Diagnostic,
		}
		rejections = append(rejections, public)
		rejectionIndex[public.ID] = public
	}
	packages := make([]string, len(binaries))
	for i, binary := range binaries {
		packages[i] = binary.ImportPath
	}
	return Catalog{
		WorkspaceDigest: workspaceDigest,
		Digest:          catalog.Digest(),
		ModulePath:      found.ModulePath,
		GoVersion:       found.GoVersion,
		Toolchain:       toolchain.Version.Raw,
		Profile:         profile,
		Mutants:         mutants,
		Rejections:      rejections,
		TestPackages:    packages,
	}, rejectionIndex
}

func cloneCatalog(c Catalog) Catalog {
	c.Mutants = slices.Clone(c.Mutants)
	c.Rejections = slices.Clone(c.Rejections)
	c.TestPackages = slices.Clone(c.TestPackages)
	return c
}

func outputSummary(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "no output"
	}
	if line, _, ok := strings.Cut(trimmed, "\n"); ok {
		return strconv.Quote(strings.TrimSpace(line))
	}
	return strconv.Quote(trimmed)
}

func checkInitialDrift(snap *snapshot.Snapshot, instrumented instrument.Result) error {
	unexpected, err := drift.Unexpected(snap, instrumented)
	if err != nil {
		return fmt.Errorf("gomutants: prepare drift check: %w", err)
	}
	if len(unexpected) != 0 {
		return fmt.Errorf("gomutants: prepare verification changed the snapshot outside instrumentation:\n%s",
			strings.Join(unexpected, "\n"))
	}
	return nil
}
