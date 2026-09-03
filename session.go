// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	probePrefix          = "probe-"
	infectionLogName     = "infection.log"
	maximumArtifacts     = 128
	maximumArtifactBytes = 2 << 20
	maximumArtifactsSize = 16 << 20
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

	// probeSnapshot is the second instrumented copy of the same source, or nil
	// when the session was prepared without one. Its presence is what
	// [Session.Probe] refuses on, because a session with no probe tree cannot
	// answer the infection question at all — and answering it emptily would be
	// the one wrong answer.
	probeSnapshot *snapshot.Snapshot
	// probeBinaries and probeOptions are that tree's own test binaries and the
	// options they are started with. They are separate values rather than a
	// mode on the mutant ones because the two trees are different directories:
	// a binary of one started against the other would measure a program nobody
	// built.
	probeBinaries []execute.TestBinary
	probeOptions  execute.Options

	// keepTemp is the workspace's OpenOptions.KeepTemp. A kept session leaves
	// its probe tree on disk and leaves its own scratch directory alone: the
	// scratch lives inside the workspace's, which is being kept too.
	keepTemp bool
	// preserved names what Close left behind, for the Workspace to report.
	preserved []string
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

	// The probe tree is copied here and nowhere later, and the ordering is
	// forced rather than chosen. A workspace holds one snapshot, validation
	// instruments it *in place*, and the probe tree has to be the same source
	// as the mutant tree — so the copy is taken from the pristine snapshot
	// before the next line rewrites it, and from the snapshot rather than from
	// the user's tree, which may have moved since Open froze it.
	var probeSnap *snapshot.Snapshot
	if resolved.Probe {
		probeSnap, err = snapshot.Create(w.snapshot.Root, snapshot.Options{
			DestParent: w.snapshot.Parent(),
		})
		if err != nil {
			return nil, fmt.Errorf("gomutants: prepare probe snapshot: %w", err)
		}
		if probeSnap.WorkspaceDigest != w.snapshot.WorkspaceDigest {
			return failPrepare(probeSnap, fmt.Errorf(
				"gomutants: prepare probe snapshot digest %s does not match the mutant snapshot's %s",
				probeSnap.WorkspaceDigest, w.snapshot.WorkspaceDigest))
		}
	}
	// Every return from here on goes through fail, so that a probe tree copied
	// and then abandoned does not outlive the call that made it — as Open
	// cleans up its own snapshot when the scratch directory beside it fails.
	fail := func(err error) (*Session, error) { return failPrepare(probeSnap, err) }

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
		return fail(fmt.Errorf("gomutants: prepare validation: %w", err))
	}

	verify := resolved.Verify
	verifyBase, err := overlayEnvironment(w.env, verify.Env)
	if err != nil {
		return fail(fmt.Errorf("gomutants: prepare verification environment: %w", err))
	}
	verify.Env = nil
	verifyBase = gocmd.AppendGoflags(verifyBase, gocmd.VetOff)
	verified, err := w.runCommand(ctx, verify, verifyBase)
	if err != nil {
		return fail(fmt.Errorf("gomutants: prepare instrumented verification: %w", err))
	}
	switch {
	case verified.TimedOut:
		return fail(fmt.Errorf("gomutants: prepare instrumented verification timed out after %s", verify.Timeout))
	case verified.ExitCode != 0:
		return fail(fmt.Errorf("gomutants: prepare instrumented verification exited with status %d: %s",
			verified.ExitCode, outputSummary(verified.Output)))
	}
	if driftErr := checkInitialDrift(w.snapshot, validated.Instrumented, "verification"); driftErr != nil {
		return fail(driftErr)
	}

	scratch, err := os.MkdirTemp(w.scratch, sessionPrefix)
	if err != nil {
		return fail(fmt.Errorf("gomutants: prepare session scratch: %w", err))
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
		return fail(fmt.Errorf("gomutants: prepare test binaries: %w", err))
	}
	preparedFiles, err := scanFiles(w.snapshot.Root)
	if err != nil {
		return fail(fmt.Errorf("gomutants: prepare snapshot state: %w", err))
	}

	probeOptions, probeBinaries, probed, err := prepareProbeTree(ctx, probeTreeOptions{
		snap:         probeSnap,
		catalog:      catalog,
		hints:        hints,
		modulePath:   found.ModulePath,
		toolchain:    w.toolchain,
		jobs:         resolved.Jobs,
		buildTimeout: resolved.BuildTimeout,
		packages:     resolved.Packages,
		env:          w.env,
		validateEnv:  validationEnv,
		scratch:      scratch,
	})
	if err != nil {
		return fail(err)
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
		probed,
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
		probeSnapshot:  probeSnap,
		probeBinaries:  probeBinaries,
		probeOptions:   probeOptions,
		keepTemp:       w.keepTemp,
	}
	w.session = session
	return session, nil
}

// failPrepare returns a preparation failure, removing the probe tree first when
// one had already been copied.
//
// A probe tree is a whole second copy of somebody's module, so a Prepare that
// gives up after taking one has to remove it: nothing else knows it exists —
// the Session it would have belonged to is never returned — and the Workspace's
// own Close cleans up only the snapshot it made itself.
func failPrepare(probeSnap *snapshot.Snapshot, err error) (*Session, error) {
	if probeSnap == nil {
		return nil, err
	}
	if cleanupErr := probeSnap.Cleanup(); cleanupErr != nil {
		return nil, errors.Join(err, cleanupErr)
	}
	return nil, err
}

// probeTreeOptions is what [prepareProbeTree] needs, gathered so that the one
// caller reads as the decision it is making rather than as eleven arguments.
type probeTreeOptions struct {
	snap         *snapshot.Snapshot
	catalog      *mutation.Catalog
	hints        instrument.Hints
	modulePath   string
	toolchain    gocmd.Toolchain
	jobs         int
	buildTimeout time.Duration
	packages     []string
	env          []string
	validateEnv  []string
	scratch      string
}

// prepareProbeTree instruments, validates and builds the probe tree, and
// reports which mutants it ends up speaking for.
//
// A nil snapshot is the session prepared without one: no work, no binaries, and
// an empty probed set, which is what makes every [Mutant.Probed] false and
// [Session.Probe] refuse.
//
// The tree goes through the same [validate.Validate] the mutant tree does, with
// [instrument.ModeProbe], so a probe site that does not compile is bisected out
// by the phase that already knows how — and a rejection here is not a rejected
// mutant. The mutant is untouched in its own tree; what it loses is its probe.
//
// There is deliberately no verification command on this tree. The mutant tree's
// verify exists because a whole run is scored against it and one broken build
// would falsify every number; a probe pass is per call and already reports a
// failing target as "no facts", so a suite-wide gate here would buy a guarantee
// the per-call rule already gives and cost a full test run to get it.
//
// Probed is the conjunction of two things and not either alone. The mutant must
// have a probe form — [instrument.Hints.Probes] — because a mutant with none
// leaves its file untouched and is therefore *accepted* by this validation
// exactly as a probed one is; and its site must have survived that validation,
// because a probe that did not compile was bisected back out of the tree.
// Reading either half as the whole would mark a mutant nothing can record as
// one whose silence means something.
func prepareProbeTree(ctx context.Context, opts probeTreeOptions) (
	execute.Options, []execute.TestBinary, map[string]bool, error,
) {
	if opts.snap == nil {
		return execute.Options{}, nil, nil, nil
	}

	validated, err := validate.Validate(ctx, validate.Options{
		Snap:         opts.snap,
		Catalog:      opts.catalog,
		Hints:        opts.hints,
		ModulePath:   opts.modulePath,
		Toolchain:    opts.toolchain,
		Jobs:         opts.jobs,
		BuildTimeout: opts.buildTimeout,
		Env:          opts.validateEnv,
		Mode:         instrument.ModeProbe,
	})
	if err != nil {
		return execute.Options{}, nil, nil, fmt.Errorf("gomutants: prepare probe validation: %w", err)
	}
	if driftErr := checkInitialDrift(opts.snap, validated.Instrumented, "probe instrumentation"); driftErr != nil {
		return execute.Options{}, nil, nil, driftErr
	}

	probeOptions := execute.Options{
		Toolchain:    opts.toolchain,
		SnapshotRoot: opts.snap.Root,
		Packages:     slices.Clone(opts.packages),
		BinDir:       filepath.Join(opts.scratch, "probe-bin"),
		ScratchDir:   filepath.Join(opts.scratch, "probe-targets"),
		Env:          slices.Clone(opts.env),
		Jobs:         opts.jobs,
		Timeout:      opts.buildTimeout,
	}
	binaries, err := execute.BuildTestBinaries(ctx, probeOptions)
	if err != nil {
		return execute.Options{}, nil, nil, fmt.Errorf("gomutants: prepare probe test binaries: %w", err)
	}

	survived := make(map[string]bool, len(validated.AcceptedIDs))
	for _, id := range validated.AcceptedIDs {
		survived[id] = true
	}
	probed := make(map[string]bool, len(survived))
	for _, m := range opts.catalog.Mutants() {
		if survived[m.ID] && opts.hints.Probes(m) {
			probed[m.ID] = true
		}
	}
	return probeOptions, binaries, probed, nil
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
	binaryIndexes, err := selectTestPackages(s.root, s.binaries, request.Package, "exec")
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
	targetArgs, err := sessionTargetArgs(request.Args, scratch, "exec")
	if err != nil {
		return MutantResult{}, err
	}

	opts := s.executeOptions
	opts.ScratchDir = scratch
	opts.Env = env
	runBinaries := s.binaries
	artifactRoot := ""
	if hasFuzzTarget(request.Args) {
		artifactRoot = filepath.Join(scratch, "fuzz-workspace")
		runBinaries, err = prepareFuzzWorkspace(s.root, artifactRoot, s.binaries)
		if err != nil {
			return MutantResult{}, fmt.Errorf("gomutants: session exec fuzz workspace: %w", err)
		}
	}
	attempt := execute.RunOne(ctx, opts, execute.MutantRun{
		ID:       mutant.ID,
		Timeout:  timeout,
		Binaries: binaryIndexes,
		Args:     targetArgs,
	}, runBinaries)
	artifacts, artifactErr := captureFuzzArtifacts(artifactRoot)
	result := MutantResult{
		ID:         mutant.ID,
		DisplayID:  mutant.DisplayID,
		Outcome:    Outcome(attempt.Outcome.String()),
		KilledBy:   attempt.KilledBy,
		Duration:   attempt.Duration,
		OutputTail: attempt.OutputTail,
		Artifacts:  artifacts,
	}
	if artifactErr != nil {
		return result, fmt.Errorf("gomutants: session exec artifacts: %w", artifactErr)
	}
	if attempt.Err != nil {
		return result, fmt.Errorf("gomutants: session exec: %w", attempt.Err)
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("gomutants: session exec: %w", err)
	}
	return result, nil
}

// Probe runs one test or fuzz target against the session's probe tree and
// reports which catalogued mutants that target could have observed.
//
// The probe tree is the same source as the mutant tree with no mutant ever
// active: the program the user wrote runs, and each site go-mutants has a probe
// form for records — without side effects — whether the mutated value would
// have differed from the one the original produced. So a mutant that is
// [Mutant.Probed] and absent from [ProbeResult.Infected] is one this target
// never saw a differing value at, and a target that cannot distinguish a mutant
// from the original program cannot kill it. That is the licence: the caller may
// skip executing that (mutant, target) pair, and the result it would have got
// is "survived".
//
// The licence is only ever given by a [ProbeMeasured] outcome, and only for a
// probed mutant. Every other outcome, and every error, means the pass has no
// facts — not that nothing was infected. **A caller that reads an error, a
// failed target, a timeout or an unavailable runtime as "not infected" is
// unsound**: it will drop executions that would have found kills, and the
// mutation score it reports will be higher than the truth with nothing in the
// output saying so. An unprobed mutant is the same trap in a different shape.
// It is absent from every measurement there will ever be, because nothing was
// compiled that could record it, and a caller has to treat it as infected by
// every target.
//
// An error is returned when the session was prepared without a probe tree
// ([ErrProbeNotPrepared]), when the request names a package with no prepared
// test binary or is otherwise malformed — the same checks [Session.Exec]
// makes — and when the infrastructure fails: a scratch directory that cannot be
// made, a process that will not start, or an infection log that is there and
// cannot be read. A *missing* log after a clean exit is not a failure but the
// empty set: the probe runtime writes its header in `init`, before any test
// code runs, so a binary that wrote no log linked no probe and ran no probed
// site.
//
// Each call gets its own scratch directory and its own log, which is what makes
// the answer a statement about this target and this call. Probe is safe to call
// concurrently with itself and with [Session.Exec]; the two share the session
// and nothing else.
func (s *Session) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if s == nil {
		return ProbeResult{}, errors.New("gomutants: session probe: nil session")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ProbeResult{}, errors.New("gomutants: session probe: session is closed")
	}
	if s.probeSnapshot == nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe: %w", ErrProbeNotPrepared)
	}
	if request.Timeout < 0 {
		return ProbeResult{}, errors.New("gomutants: session probe: timeout is negative")
	}
	binaryIndexes, err := selectTestPackages(s.probeSnapshot.Root, s.probeBinaries, request.Package, "probe")
	if err != nil {
		return ProbeResult{}, err
	}
	env, err := overlayEnvironment(s.env, request.Env)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe environment: %w", err)
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = s.mutantTimeout
	}
	scratch, err := os.MkdirTemp(s.scratch, probePrefix)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe scratch: %w", err)
	}
	// The log lives and dies with the call. Two passes appending to one file
	// would each read the other's indices as their own, and a file left behind
	// would do the same to the next run over the same directory.
	defer func() { _ = os.RemoveAll(scratch) }()
	targetArgs, err := sessionTargetArgs(request.Args, scratch, "probe")
	if err != nil {
		return ProbeResult{}, err
	}

	opts := s.probeOptions
	opts.ScratchDir = scratch
	opts.Env = env
	attempt := execute.RunProbe(ctx, opts, execute.ProbeRun{
		Timeout:  timeout,
		Binaries: binaryIndexes,
		Args:     targetArgs,
		LogPath:  filepath.Join(scratch, infectionLogName),
		Digest:   s.catalog.Digest(),
		Mutants:  s.catalog.Len(),
	}, s.probeBinaries)
	if attempt.Err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe: %w", attempt.Err)
	}
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe: %w", err)
	}
	return ProbeResult{
		Outcome:    ProbeOutcome(attempt.Outcome),
		Infected:   attempt.Infected,
		ExitCode:   attempt.ExitCode,
		Duration:   attempt.Duration,
		OutputTail: attempt.OutputTail,
	}, nil
}

func hasFuzzTarget(arguments []string) bool {
	return slices.ContainsFunc(arguments, func(argument string) bool {
		return testflag.Match(argument, "test.fuzz")
	})
}

func prepareFuzzWorkspace(root, destination string, binaries []execute.TestBinary) ([]execute.TestBinary, error) {
	if err := copyTree(root, destination); err != nil {
		return nil, err
	}
	cloned := slices.Clone(binaries)
	for i := range cloned {
		relative, err := snapshotRelativePath(root, cloned[i].Dir)
		if err != nil {
			return nil, fmt.Errorf("test package %q is outside the snapshot", cloned[i].ImportPath)
		}
		cloned[i].Dir = filepath.Join(destination, relative)
	}
	return cloned, nil
}

func snapshotRelativePath(root, path string) (string, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the snapshot")
	}
	return relative, nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if entry.Type()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refuses non-regular fuzz-workspace entry %s", relative)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOutErr := output.Close()
		closeInErr := input.Close()
		return errors.Join(copyErr, closeOutErr, closeInErr)
	})
}

func captureFuzzArtifacts(root string) ([]Artifact, error) {
	if root == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	artifacts := make([]Artifact, 0)
	total := int64(0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refuses symbolic link %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuses irregular artifact %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		header := make([]byte, len("go test fuzz v1\n"))
		_, headerErr := io.ReadFull(file, header)
		_ = file.Close()
		if headerErr != nil || string(header) != "go test fuzz v1\n" {
			return nil
		}
		if info.Size() > maximumArtifactBytes || total+info.Size() > maximumArtifactsSize || len(artifacts) >= maximumArtifacts {
			return fmt.Errorf("fuzz artifacts exceed the session capture limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		artifacts = append(artifacts, Artifact{
			Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(sum[:]), Data: data,
		})
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(artifacts, func(a, b Artifact) int { return strings.Compare(a.Path, b.Path) })
	return artifacts, nil
}

// sessionTargetArgs supplies the one flag cmd/go normally adds around a fuzz
// target. A test binary refuses -test.fuzz without -test.fuzzcachedir; keeping
// that cache in the execution scratch preserves the read-only snapshot and
// lets the standard seed corpus compiled from testdata/fuzz run unchanged.
//
// The call names itself so that a diagnostic says which of the session's two
// measurements refused the target. Both go through this function because a
// caller composing arguments for one has to be able to hand them to the other:
// one request vocabulary, one set of reserved flags, one message shape.
func sessionTargetArgs(args []string, scratch, call string) ([]string, error) {
	out := slices.Clone(args)
	fuzz := false
	for _, argument := range args {
		switch {
		case testflag.Match(argument, "test.fuzz"):
			fuzz = true
		case testflag.Match(argument, "test.fuzzcachedir"):
			return nil, fmt.Errorf("gomutants: session %s: -test.fuzzcachedir is reserved by the session", call)
		case testflag.Match(argument, "test.fuzzworker"):
			return nil, fmt.Errorf("gomutants: session %s: -test.fuzzworker is reserved by the Go fuzz coordinator", call)
		}
	}
	if !fuzz {
		return out, nil
	}
	cache := filepath.Join(scratch, "fuzz-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, fmt.Errorf("gomutants: session %s fuzz cache: %w", call, err)
	}
	return append(out, "-test.fuzzcachedir="+cache), nil
}

func selectTestPackages(root string, binaries []execute.TestBinary, selected, call string) ([]int, error) {
	if selected == "" {
		return nil, nil
	}
	var selectedDir string
	if selected == "." || strings.HasPrefix(selected, "./") {
		var err error
		selectedDir, err = moduleDirectory(root, selected)
		if err != nil {
			return nil, fmt.Errorf("gomutants: session %s package: %w", call, err)
		}
	}
	var indexes []int
	for i, binary := range binaries {
		if binary.ImportPath == selected || selectedDir != "" && samePath(binary.Dir, selectedDir) {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return nil, fmt.Errorf("gomutants: session %s package %q has no prepared test binary", call, selected)
	}
	return indexes, nil
}

func samePath(a, b string) bool {
	// macOS exposes temporary directories through aliases such as /var and
	// /private/var. go list may report the canonical spelling while the
	// workspace retains the spelling returned by os.MkdirTemp, so compare the
	// filesystem-resolved paths when both still exist.
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Close waits for target executions and releases the session's binaries, its
// scratch files, and the probe tree when it has one. It is idempotent. Closing
// a Session does not close its parent Workspace; closing the Workspace closes
// both.
//
// The probe tree goes here rather than with the Workspace's own snapshot
// because the session is what made it: a second copy of the module, built for
// this session's catalogue, useless to anything that outlives it.
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

	var closeErr error
	if s.scratch != "" && !s.keepTemp {
		closeErr = os.RemoveAll(s.scratch)
	}
	if s.probeSnapshot != nil {
		kept, err := keepOrRemove(s.keepTemp, s.probeSnapshot.Keep, s.probeSnapshot.Cleanup)
		closeErr = errors.Join(closeErr, err)
		if kept {
			s.preserved = append(s.preserved, s.probeSnapshot.Dir())
		}
	}
	if closeErr != nil {
		return fmt.Errorf("gomutants: close session: %w", closeErr)
	}
	return nil
}

// preserved names the directories this session left on disk, which is the probe
// tree of a kept session and nothing otherwise. The session scratch is not
// among them: it lives inside the workspace's scratch directory, which the
// Workspace reports on its own.
//
// It is unexported because [Workspace.Preserved] is the one place a caller asks
// this question: a session whose probe tree was kept is always closed by, or
// before, the workspace that owns it.
func (s *Session) preservedDirs() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.preserved)
}

func makeCatalog(
	workspaceDigest string,
	toolchain gocmd.Toolchain,
	profile string,
	found discover.Result,
	catalog *mutation.Catalog,
	rejected []validate.Rejection,
	accepted map[string]bool,
	probed map[string]bool,
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
			Branch:       publicBranch(where.Branch),
			Probed:       probed[internal.ID],
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

// publicBranch converts discovery's branch proof into the public one. Nil
// stays nil: no proof is not the same statement as no branch.
func publicBranch(proof *discover.BranchProof) *BranchProof {
	if proof == nil {
		return nil
	}
	return &BranchProof{
		Direction:       proof.Direction,
		BodyStartLine:   proof.BodyStartLine,
		BodyStartColumn: proof.BodyStartColumn,
		BodyEndLine:     proof.BodyEndLine,
		BodyEndColumn:   proof.BodyEndColumn,
	}
}

// cloneCatalog copies everything a caller could write through, the branch
// proofs included. Session.Catalog promises a deep copy, and an aliased
// pointer would leave a caller one assignment away from rewriting the
// session's own catalogue.
func cloneCatalog(c Catalog) Catalog {
	c.Mutants = slices.Clone(c.Mutants)
	for i := range c.Mutants {
		c.Mutants[i].Branch = publicBranchCopy(c.Mutants[i].Branch)
	}
	c.Rejections = slices.Clone(c.Rejections)
	c.TestPackages = slices.Clone(c.TestPackages)
	return c
}

// publicBranchCopy duplicates a proof, keeping nil as nil.
func publicBranchCopy(proof *BranchProof) *BranchProof {
	if proof == nil {
		return nil
	}
	copied := *proof
	return &copied
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

// checkInitialDrift asserts that a freshly instrumented tree holds nothing but
// what instrumentation put there.
//
// The `what` names the step being held to account, because the two trees reach
// this gate having done different things: the mutant tree has just run the
// user's verification command in it, and the probe tree has run nothing at all,
// so a drift there is instrumentation's own and saying "verification" about it
// would send a reader looking in the wrong place.
func checkInitialDrift(snap *snapshot.Snapshot, instrumented instrument.Result, what string) error {
	unexpected, err := drift.Unexpected(snap, instrumented)
	if err != nil {
		return fmt.Errorf("gomutants: prepare drift check: %w", err)
	}
	if len(unexpected) != 0 {
		return fmt.Errorf("gomutants: prepare %s changed the snapshot outside instrumentation:\n%s",
			what, strings.Join(unexpected, "\n"))
	}
	return nil
}
