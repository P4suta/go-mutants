// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/drift"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/validate"
	"github.com/P4suta/go-mutants/trace"
)

const (
	BaselineCap = 10 * time.Minute

	MinDerivedTimeout = 10 * time.Second

	TimeoutFactor = 5

	MinDerivedMemory = 1 << 30

	MemoryFactor = 4

	scratchPrefix = "go-mutants-tmp-"

	binDirName = "bin"

	workerDirName = "workers"

	envPrefix = "GO_MUTANTS_"
)

var tempPrefixes = []string{snapshot.DirPrefix, scratchPrefix}

var tempKeys = []string{"TMP", "TEMP", "TMPDIR"}

type Options struct {
	Config config.Config

	WorkspaceRoot string

	TestArgv []string

	ToolVersion string

	MutantPrefix string

	Changed    bool
	ChangedRef string

	Shard report.Shard

	TempDirectory string

	KeepTemp KeepTemp

	HistoryRoot string

	CacheRoot string

	Events chan<- Event

	RunID string

	TraceSink trace.Sink

	PublishTrace bool

	Notes []trace.NoteRecord

	TraceDirectory string

	now func() time.Time
}

type RunOutcome struct {
	RunID    string
	Status   Status
	Started  time.Time
	Duration time.Duration

	WorkspaceRoot string
	Workers       int
	Toolchain     gocmd.Toolchain

	SnapshotRoot    string
	SnapshotFiles   int
	WorkspaceDigest string

	TestCommand         []string
	ResolvedTestCommand []string
	BaselineRuns        []time.Duration
	AverageBaseline     time.Duration
	SlowestBaseline     time.Duration
	Timeout             time.Duration
	TimeoutSource       TimeoutSource
	PeakBaseline        int64
	Memory              int64
	MemorySource        MemorySource

	Report *report.Report

	WorkspaceReport *report.WorkspaceReport
	RunPath         string
	LatestPath      string
	Artifacts       report.Artifacts
	Verdict         mutation.Verdict

	Preserved []PreservedDir

	Warnings []Warning
	Summary  string

	Validation       ValidationFacts
	Snapshot         SnapshotFacts
	Timing           Timing
	CoverageFallback string
	Probe            ProbeFacts
}

type ProbeFacts struct {
	Binaries int
	Settled  int
	Narrowed int
}

type ValidationFacts struct {
	Builds int
}

type SnapshotFacts struct {
	StableDir bool
	Files     int
}

type PhaseDuration struct {
	Phase    Phase
	Duration time.Duration
}

type StageDuration struct {
	Phase    Phase
	Name     string
	Duration time.Duration
	Result   string
}

type Timing struct {
	Phases []PhaseDuration
	Stages []StageDuration
}

func (t Timing) clone() Timing {
	t.Phases = slices.Clone(t.Phases)
	t.Stages = slices.Clone(t.Stages)
	return t
}

func Run(ctx context.Context, opts Options) (RunOutcome, error) {
	s := &session{events: opts.Events, clock: opts.now}
	defer s.close()

	started := s.now()
	runID, idErr := resolveRunID(opts.RunID, started)
	out := RunOutcome{
		RunID:   runID,
		Status:  StatusFailed,
		Started: started,
	}
	s.trace = trace.New(s.sink(opts), s.now, trace.StartRecord{
		Kind:        trace.StartKindRun,
		RunID:       out.RunID,
		ToolVersion: or(opts.ToolVersion, unknownValue),
		PID:         os.Getpid(),
		Root:        opts.WorkspaceRoot,
		Args:        slices.Clone(os.Args),
	})
	for _, note := range opts.Notes {
		s.trace.Note(note.Kind, note.Code, note.Detail)
	}

	err := idErr
	if err == nil {
		err = s.pipeline(ctx, opts, &out)
	}

	s.closePhase()

	out.Duration = s.now().Sub(started)
	out.Warnings = slices.Clone(s.warnings)
	out.Timing = s.timing.clone()
	switch {
	case err == nil:
		out.Status = StatusOK
	case interrupted(err):
		out.Status = StatusInterrupted
	default:
		out.Status = StatusFailed
	}
	if err != nil {
		out.Summary = firstLine(err.Error())
	}
	s.trace.RunEnd(string(out.Status), exitCodeOf(out), err)
	s.drainPublished()
	s.emit(RunCompleted{Status: out.Status, Summary: out.Summary, Run: s.summary})
	return out, err
}

func (s *session) sink(opts Options) trace.Sink {
	if opts.TraceSink == nil || !opts.PublishTrace {
		return opts.TraceSink
	}
	s.published = newEventSink(s)
	return trace.NewTeeSink(opts.TraceSink, trace.Digested(s.published))
}

const publishBuffer = 1024

type eventSink struct {
	session *session
	queue   chan trace.Event
	done    chan struct{}
}

func newEventSink(s *session) *eventSink {
	sink := &eventSink{
		session: s,
		queue:   make(chan trace.Event, publishBuffer),
		done:    make(chan struct{}),
	}
	go sink.forward()
	return sink
}

func (sink *eventSink) forward() {
	defer close(sink.done)
	for event := range sink.queue {
		sink.session.emit(Traced{Event: event})
	}
}

func (sink *eventSink) Emit(event trace.Event) error {
	sink.queue <- event.Clone()
	return nil
}

func (*eventSink) Close() error { return nil }

func (s *session) drainPublished() {
	if s.published == nil {
		return
	}
	close(s.published.queue)
	<-s.published.done
	s.published = nil
}

func exitCodeOf(out RunOutcome) int {
	switch out.Status {
	case StatusOK:
		return int(out.Verdict.Code)
	case StatusInterrupted:
		return int(mutation.ExitInterrupted)
	case StatusFailed:
	}
	return int(mutation.ExitInfrastructure)
}

type state struct {
	workspace    bool
	found        discovered
	catalog      *mutation.Catalog
	mode         report.SelectionMode
	changed      *gitdiff.Changed
	shard        *report.Shard
	selected     int
	rejections   []report.Rejection
	notRun       map[string]report.NotRunReason
	results      map[string]report.MutantResult
	display      map[string]MutantResult
	packages     map[string]string
	neverReturns map[string]bool
	coverage     coverageResult
	cache        cacheState
}

func (s *session) pipeline(ctx context.Context, opts Options, out *RunOutcome) (err error) {
	cfg := opts.Config

	root, err := workspaceRoot(opts.WorkspaceRoot)
	if err != nil {
		return err
	}
	out.WorkspaceRoot = root

	workspace, err := discover.DetectWorkspace(root)
	if err != nil {
		return err
	}
	s.patterns = treePatterns(workspaceModulesOf(workspace))

	command, err := testCommand(cfg, opts.TestArgv)
	if err != nil {
		return err
	}
	if workspace != nil {
		command = expandWholeTree(command, workspace.Modules)
	}
	out.TestCommand = command
	out.Workers = cfg.Execution.Jobs

	changed, err := s.changedLines(ctx, opts, root)
	if err != nil {
		return err
	}

	s.emit(RunPlanned{RunID: out.RunID, Workers: cfg.Execution.Jobs})
	s.enterPhase(PhaseDiscover, "locating the Go toolchain and copying the workspace")

	endToolchain := s.stage("toolchain", "")
	toolchain, err := gocmd.LocateContext(ctx, gocmd.Options{Trace: s.trace})
	endToolchain(err)
	if err != nil {
		return err
	}
	out.Toolchain = toolchain

	tempParent := temporaryParent(opts.TempDirectory)
	endSweep := s.stage("sweep", tempParent)
	s.sweepTemporary(tempParent)
	endSweep(nil)

	var temps temporaries
	defer func() { s.release(&temps, opts.KeepTemp, out, err) }()

	endSnapshot := s.stage("snapshot", root)
	snapshotStarted := s.now()
	snap, err := snapshot.Create(root, snapshot.Options{
		ReportDir:  cfg.Report.Directory,
		DestParent: tempParent,
	})
	s.recordSnapshot(root, snap, s.now().Sub(snapshotStarted), err)
	endSnapshot(err)
	if err != nil {
		return err
	}
	out.SnapshotRoot = snap.Root
	out.SnapshotFiles = len(snap.Manifest)
	out.WorkspaceDigest = snap.WorkspaceDigest
	out.Snapshot = SnapshotFacts{StableDir: snap.StableDir, Files: len(snap.Manifest)}
	temps.snapshot = snap

	scratch, err := os.MkdirTemp(snap.Parent(), scratchPrefix)
	if err != nil {
		return &Error{
			Code:    CodeScratchDir,
			Message: "the per-run temporary directory could not be created",
			Err:     err,
		}
	}
	scratchOwner, err := tempowner.Claim(scratch, s.now())
	if err != nil {
		return &Error{
			Code:    CodeScratchDir,
			Message: "the per-run temporary directory could not be claimed",
			Err:     errors.Join(err, os.RemoveAll(scratch)),
		}
	}
	temps.scratch, temps.scratchOwner = scratch, scratchOwner
	env := workspaceEnv(childEnv(scratch), workspace != nil)
	ownEnv := engineCommandEnv(childEnv(scratch), workspace != nil)

	patterns, scoped := testScope(out.TestCommand)
	if scoped {
		endScope := s.stage("scope", strings.Join(patterns, " "))
		err := s.resolveTestScope(ctx, toolchain, snap.Root, ownEnv, patterns)
		endScope(err)
		if err != nil {
			return err
		}
	}

	if err := s.baseline(ctx, cfg, command, toolchain, snap.Root, env, ownEnv, out); err != nil {
		return err
	}
	if cfg.Execution.Isolate {
		endSweep := s.stage("isolate-sweep", "")
		_, sweepErr := snap.Restore()
		endSweep(sweepErr)
		if sweepErr != nil {
			return &Error{
				Code: CodeWorkspaceDrift,
				Message: "the snapshot could not be put back after the baseline, so --isolate cannot " +
					"give the workers a tree the baseline's own writes are not already in",
				Err: sweepErr,
			}
		}
	}

	st := &state{
		workspace: workspace != nil,
		mode:      selectionMode(opts),
		changed:   changed,
		shard:     shardOf(opts),
		results:   make(map[string]report.MutantResult),
		display:   make(map[string]MutantResult),
		notRun:    make(map[string]report.NotRunReason),
	}
	mutateErr := s.mutate(ctx, opts, toolchain, snap, scratch, env, ownEnv, out, st, &temps)
	if mutateErr != nil {
		if interrupted(mutateErr) && st.catalog != nil {
			if pubErr := s.publish(opts, out, st, report.StatusInterrupted); pubErr != nil {
				s.warn(CodeReportNotPublished,
					"the interrupted run could not be filed in the history: "+pubErr.Error())
			}
		}
		return mutateErr
	}
	return s.publish(opts, out, st, report.StatusCompleted)
}

func (s *session) baseline(
	ctx context.Context,
	cfg config.Config,
	command []string,
	toolchain gocmd.Toolchain,
	root string,
	env []string,
	ownEnv []string,
	out *RunOutcome,
) error {
	runs := cfg.Test.BaselineRuns
	s.enterPhase(PhaseBaseline, fmt.Sprintf("building the snapshot, then %s of %s",
		countNoun(runs, "timed run"), strings.Join(command, " ")))

	build := toolchain.Command(append([]string{"build"}, s.patterns...)...)
	build.Dir = root
	build.Env = ownEnv
	build.Timeout = BaselineCap
	build.Trace = s.trace
	build.Kind = trace.ExecKindBaselineBuild
	endBuild := s.stage("build", "")
	buildErr := check(ctx, build, runner.Run(ctx, build), CodeBaselineBuildFailed,
		"the snapshot does not build")
	endBuild(buildErr)
	if buildErr != nil {
		return buildErr
	}

	argv := resolveProgram(command, toolchain)
	out.ResolvedTestCommand = slices.Clone(argv)
	durations := make([]time.Duration, 0, runs)
	observed := make([]baselineObservation, 0, runs)
	var peak int64
	for i := 1; i <= runs; i++ {
		runEnv := env
		if i > 1 {
			runEnv = gocmd.AppendGoflags(env, gocmd.CountOnce)
		}
		spec := runner.Spec{
			Argv:    argv,
			Dir:     root,
			Env:     runEnv,
			Timeout: BaselineCap,
			Trace:   s.trace,
			Kind:    trace.ExecKindBaselineTest,
		}
		endRun := s.stage("test", fmt.Sprintf("run %d/%d", i, runs))
		result := runner.Run(ctx, spec)
		runErr := check(ctx, spec, result, CodeBaselineTestFailed,
			fmt.Sprintf("baseline run %d of %d failed", i, runs))
		endRun(runErr)
		if runErr != nil {
			return runErr
		}
		durations = append(durations, result.Duration)
		observed = append(observed, baselineObservation{
			Duration: result.Duration,
			Cached:   servedFromTestCache(result.Output),
		})
		peak = max(peak, result.PeakMemory)
		s.emit(BaselineProgress{Run: i, Of: runs, Duration: result.Duration})
	}
	out.BaselineRuns = durations
	out.AverageBaseline = mean(durations)
	out.SlowestBaseline = budgetBaseline(observed)
	if everyRunCached(observed) {
		s.warn(CodeBaselineFromTestCache, cachedBaselineWarning(len(observed)))
	}

	endTimeout := s.stage("timeout", "")
	timeout, source, err := deriveTimeout(cfg.Test.Timeout, out.SlowestBaseline)
	endTimeout(err)
	if err != nil {
		return err
	}
	out.Timeout = timeout
	out.TimeoutSource = source
	s.emit(BaselineCompleted{
		Runs:          durations,
		Average:       out.AverageBaseline,
		Slowest:       out.SlowestBaseline,
		Timeout:       timeout,
		TimeoutSource: source,
	}.clone())

	endMemory := s.stage("memory", "")
	out.PeakBaseline = peak
	out.Memory, out.MemorySource = enforceableMemory(deriveMemory(cfg.Test.Memory, peak))
	endMemory(nil)
	s.emit(MemoryDerived{Limit: out.Memory, Source: out.MemorySource, Peak: peak})
	if reason, warn := unenforcedMemoryReason(out.Memory, out.MemorySource, runner.MemoryBoundSupported()); warn {
		s.warn(CodeMemoryBoundUnavailable, reason)
	}
	return nil
}

func unenforcedMemoryReason(limit int64, source MemorySource, enforced bool) (string, bool) {
	if source != MemorySourceExplicit || limit <= 0 || enforced {
		return "", false
	}
	return "the memory bound in test.memory is recorded but not enforced: this platform can report " +
		"what a process cost once it is gone but cannot watch one while it runs, so a runaway mutant " +
		"is stopped by its timeout alone", true
}

func (s *session) mutate(
	ctx context.Context,
	opts Options,
	toolchain gocmd.Toolchain,
	snap *snapshot.Snapshot,
	scratch string,
	env []string,
	ownEnv []string,
	out *RunOutcome,
	st *state,
	temps *temporaries,
) error {
	cfg := opts.Config
	s.enterPhase(PhaseMutate, "discovering candidates, validating them, then executing the mutants")

	rules, err := SelectRules(cfg)
	if err != nil {
		return err
	}
	include, err := discover.CompilePatterns(cfg.Mutation.Include)
	if err != nil {
		return err
	}
	exclude, err := discover.CompilePatterns(cfg.Mutation.Exclude)
	if err != nil {
		return err
	}

	endDiscover := s.stage("discover", "")
	found, err := discoverTree(ctx, st.workspace, discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
		Rules:        rules,
		Include:      include,
		Exclude:      exclude,
		Workspace:    st.workspace,
	})
	endDiscover(err)
	if err != nil {
		return err
	}
	st.found = found
	candidates := found.candidates()
	s.emit(Discovered{Candidates: len(candidates), Skips: skipTotal(found.skips())})

	endCatalog := s.stage("catalog", "")
	catalog, err := discover.BuildCatalogOf(found.results)
	if err != nil {
		endCatalog(err)
		return err
	}
	st.catalog = catalog
	st.display, st.packages, st.neverReturns = displayIndex(catalog, candidates, found.modules)

	hints, err := instrument.HintsOf(candidates)
	endCatalog(err)
	if err != nil {
		return err
	}

	endValidate := s.stage("validate", countNoun(catalog.Len(), "mutant"))
	validated, err := validate.Validate(ctx, validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		Hints:        hints,
		Modules:      found.validateModules(),
		Packages:     s.patterns,
		Toolchain:    toolchain,
		Jobs:         cfg.Execution.Jobs,
		BuildTimeout: BaselineCap,
		Env:          ownEnv,
		Trace:        s.trace,
	})
	endValidate(err)
	st.rejections = rejectionsOf(validated.Rejected)
	out.Validation = ValidationFacts{Builds: validated.Builds}
	if err != nil {
		return err
	}
	s.emit(Validated{Accepted: len(validated.AcceptedIDs), Rejected: len(validated.Rejected)})

	var trees []string
	if cfg.Execution.Isolate {
		endIsolate := s.stage("isolate", countNoun(cfg.Execution.Jobs, "worker"))
		trees, err = s.isolate(snap, cfg.Execution.Jobs, temps)
		endIsolate(err)
		if err != nil {
			return err
		}
	}

	baselineRoot := snap.Root
	if len(trees) > 0 {
		baselineRoot = trees[0]
	}
	endInstrumented := s.stage("instrumented-baseline", "")
	err = s.instrumentedBaseline(ctx, out.TestCommand, toolchain, baselineRoot, env,
		divergenceCensus(scratch))
	endInstrumented(err)
	if err != nil {
		return err
	}
	if len(trees) > 0 {
		if err = s.restoreWorker(temps, 0); err != nil {
			return err
		}
	}

	endDrift := s.stage("drift", "")
	err = driftGate(snap, validated.Instrumented, validated.RuntimeDirs()...)
	endDrift(err)
	if err != nil {
		return err
	}

	endCeilings := s.stage("divergence", divergenceDetail(validated.Loops()))
	loopLimits := s.deriveLoopLimits(scratch, validated.Runtimes)
	endCeilings(nil)

	endSelection := s.stage("selection", "")
	runs, err := s.selection(opts, catalog, validated.AcceptedIDs, out.Timeout, out.Memory, st)
	endSelection(err)
	if err != nil {
		return err
	}

	execOpts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		Workspace:    st.workspace,
		BinDir:       filepath.Join(scratch, binDirName),
		ScratchDir:   filepath.Join(scratch, workerDirName),
		Jobs:         cfg.Execution.Jobs,
		Trees:        trees,
		Restores:     restoresOf(s, temps, trees),
		Timeout:      BaselineCap,
		MemoryLimit:  out.Memory,
		LoopLimits:   loopLimits,
		Trace:        s.trace,
	}
	patterns, scoped := testScope(out.TestCommand)
	if scoped {
		execOpts.Packages = patterns
		execOpts.CoverPkg = found.coverPkg(coverPkgSuffix)
	} else {
		s.warnCode(string(coverage.CodeCustomTestCommand), customTestCommand(out.TestCommand))
	}

	bins, err := s.buildTestBinaries(ctx, &execOpts, &st.coverage)
	if err != nil {
		return err
	}
	if err = scopedBinaries(patterns, len(bins)); err != nil {
		return err
	}

	if execOpts.CoverPkg != "" {
		runs, st.coverage, err = s.coveragePhase(ctx, execOpts, scratch, found.modulePath(), bins, runs, st,
			cfg.Test.Narrowing)
		if err != nil {
			return err
		}
	}
	out.CoverageFallback = st.coverage.coverageFallback

	if probingEnabled(&cfg) {
		runs, out.Probe, err = s.probePhase(ctx, probeOptions{
			root:       snap.SourceRoot,
			catalog:    catalog,
			hints:      hints,
			modulePath: found.modulePath(),
			toolchain:  toolchain,
			env:        env,
			jobs:       cfg.Execution.Jobs,
			scratch:    scratch,
			exec:       execOpts,
			bins:       bins,
		}, runs, st, temps)
		if err != nil {
			return err
		}
	}

	endLookup := s.stage("cache-lookup", countNoun(len(runs), "mutant"))
	runs = s.cachePhase(opts, catalog.Digest(), out, runs, st)
	endLookup(nil)

	endExecute := s.stage("execute", countNoun(len(runs), "mutant"))
	results, err := execute.Schedule(ctx, execOpts, runs, bins, s.hooks(st, out.Memory))
	endExecute(err)
	for _, result := range results {
		st.results[result.ID] = report.MutantResult{
			ID:                   result.ID,
			Outcome:              result.Final,
			Duration:             result.Duration,
			KilledBy:             result.KilledBy,
			Attempts:             len(result.Attempts),
			Executions:           executionsOf(result),
			OutputTail:           result.OutputTail,
			CoveringTestPackages: st.coverage.covering[result.ID],
			CoveringTests:        st.coverage.coveringTests[result.ID],
		}
	}
	endStore := s.stage("cache-store", countNoun(len(results), "result"))
	s.storeOutcomes(opts, results, st)
	endStore(nil)
	return err
}

func executionsOf(result execute.MutantResult) []report.Execution {
	if result.Final == mutation.OutcomeNotRun {
		return nil
	}
	executions := make([]report.Execution, 0, len(result.Attempts))
	for i, attempt := range result.Attempts {
		outcome, err := report.OutcomeOf(attempt.Outcome)
		if err != nil {
			return nil
		}
		if !outcome.Observed() {
			return nil
		}
		executions = append(executions, report.Execution{
			Attempt:         i + 1,
			Worker:          attempt.Worker,
			Outcome:         outcome,
			KilledBy:        attempt.KilledBy,
			DurationMS:      attempt.Duration.Milliseconds(),
			Binaries:        slices.Clone(attempt.Binaries),
			Tests:           testRefsOf(attempt.Tests),
			MemoryExceeded:  attempt.MemoryExceeded,
			PeakMemoryBytes: attempt.PeakMemory,
			Diverged:        attempt.Diverged,
		})
	}
	return executions
}

func testRefsOf(tests map[string][]string) []report.TestRef {
	if len(tests) == 0 {
		return nil
	}
	refs := make([]report.TestRef, 0, len(tests))
	for importPath, names := range tests {
		for _, name := range names {
			refs = append(refs, report.TestRef{Package: importPath, Name: name})
		}
	}
	slices.SortFunc(refs, compareTestRefs)
	return refs
}

func compareTestRefs(a, b report.TestRef) int {
	if c := strings.Compare(a.Package, b.Package); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

func (s *session) buildTestBinaries(
	ctx context.Context,
	opts *execute.Options,
	cov *coverageResult,
) ([]execute.TestBinary, error) {
	endBuild := s.stage("build-binaries", buildDetail(opts.CoverPkg))
	bins, err := execute.BuildTestBinaries(ctx, *opts)
	endBuild(err)
	if err == nil || opts.CoverPkg == "" || interrupted(err) {
		return bins, err
	}
	cov.coverageFallback = fallbackText(err)
	s.unavailableInFull("the test binaries do not compile with coverage instrumentation ("+
		firstLine(err.Error())+")", cov.coverageFallback)
	opts.CoverPkg = ""
	endPlain := s.stage("build-binaries", buildDetail(opts.CoverPkg))
	bins, err = execute.BuildTestBinaries(ctx, *opts)
	endPlain(err)
	return bins, err
}

func buildDetail(coverPkg string) string {
	if coverPkg == "" {
		return "plain"
	}
	return "coverage"
}

func fallbackText(err error) string {
	text := err.Error()
	if output := execute.OutputOf(err); output != "" {
		text += "\n" + output
	}
	return text
}

func (s *session) instrumentedBaseline(
	ctx context.Context,
	command []string,
	toolchain gocmd.Toolchain,
	root string,
	env []string,
	census string,
) error {
	spec := runner.Spec{
		Argv: resolveProgram(command, toolchain),
		Dir:  root,
		Env: append(
			gocmd.AppendGoflags(gocmd.AppendGoflags(env, gocmd.VetOff), gocmd.CountOnce),
			instrument.LoopCensusEnv+"="+census),
		Timeout: BaselineCap,
		Trace:   s.trace,
		Kind:    trace.ExecKindInstrumentedBaseline,
	}
	result := runner.Run(ctx, spec)
	if err := check(ctx, spec, result, CodeInstrumentedBaselineFailed,
		"the instrumented snapshot does not pass its own tests with no mutant active"); err != nil {
		return err
	}
	s.emit(BaselineProgress{Run: 1, Of: 1, Duration: result.Duration})
	return nil
}

func driftGate(snap *snapshot.Snapshot, instrumented instrument.Result, generated ...string) error {
	unexpected, err := drift.Unexpected(snap, instrumented, generated...)
	if err != nil {
		return &Error{
			Code:    CodeWorkspaceDrift,
			Message: "the snapshot could not be checked for drift after the instrumented baseline",
			Err:     err,
		}
	}
	if len(unexpected) == 0 {
		return nil
	}
	return &Error{
		Code: CodeWorkspaceDrift,
		Message: countNoun(len(unexpected), "file") + " in the snapshot changed while the tests ran, " +
			"so every mutant after the first would be measured against a different tree; " +
			"the tests write into the package directory they run in. Re-run with --isolate, or set " +
			"execution.isolate, to give every worker its own copy of the tree and put it back between mutants",
		Output: strings.Join(unexpected, "\n"),
	}
}

func (s *session) selection(
	opts Options,
	catalog *mutation.Catalog,
	acceptedIDs []string,
	timeout time.Duration,
	memoryLimit int64,
	st *state,
) ([]execute.MutantRun, error) {
	accepted := make(map[string]bool, len(acceptedIDs))
	for _, id := range acceptedIDs {
		accepted[id] = true
	}

	ids := acceptedIDs
	if opts.MutantPrefix != "" {
		chosen, err := catalog.ResolvePrefix(opts.MutantPrefix)
		if err != nil {
			return nil, &SelectionError{Prefix: opts.MutantPrefix, Err: err}
		}
		ids = nil
		if accepted[chosen.ID] {
			ids = []string{chosen.ID}
		} else {
			s.warn(CodeSelectedMutantRejected, rejectedSelection(opts.MutantPrefix, chosen, st))
		}
	}
	ids = s.narrowSelection(ids, st)

	runs := make([]execute.MutantRun, 0, len(ids))
	for _, id := range ids {
		run := execute.MutantRun{
			ID:           id,
			Timeout:      timeout,
			MemoryLimit:  memoryLimit,
			Package:      st.packages[id],
			NeverReturns: st.neverReturns[id],
		}
		if m, ok := catalog.ByID(id); ok {
			run.DisplayID = m.DisplayID
		}
		runs = append(runs, run)
	}
	st.selected = len(runs)
	recordNotRun(acceptedIDs, runs, st)
	return runs, nil
}

func rejectedSelection(prefix string, chosen mutation.Mutant, st *state) string {
	var b strings.Builder
	b.WriteString("--mutant ")
	b.WriteString(strconv.Quote(prefix))
	b.WriteString(" selected ")
	if prefix == chosen.DisplayID {
		b.WriteString("the mutant")
	} else {
		b.WriteString(chosen.DisplayID)
	}
	if where := st.display[chosen.ID]; where.Line > 0 {
		b.WriteString(" at ")
		b.WriteString(where.Path)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(where.Line))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(where.Column))
	}
	b.WriteString(" (")
	b.WriteString(chosen.Rule.Name)
	b.WriteString("), which validation rejected because it does not compile, so this run executed nothing")
	if diagnostic := foldLines(diagnosticFor(st.rejections, chosen.ID)); diagnostic != "" {
		b.WriteString(": ")
		b.WriteString(diagnostic)
	}
	return b.String()
}

func diagnosticFor(rejections []report.Rejection, id string) string {
	for _, rejection := range rejections {
		if rejection.ID == id {
			return rejection.Diagnostic
		}
	}
	return ""
}

func foldLines(diagnostic string) string {
	lines := strings.Split(diagnostic, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "; ")
}

func (s *session) hooks(st *state, memoryLimit int64) execute.Hooks {
	return execute.Hooks{
		Started: func(id string, worker int) {
			shown := st.display[id]
			s.emit(MutantStarted{
				ID:        id,
				DisplayID: shown.DisplayID,
				Path:      shown.Path,
				ModuleDir: shown.ModuleDir,
				Line:      shown.Line,
				Rule:      shown.Rule,
				Worker:    worker,
			})
		},
		Finished: func(result execute.MutantResult) {
			shown := st.display[result.ID]
			shown.Outcome = result.Final
			shown.Duration = result.Duration
			shown.KilledBy = result.KilledBy
			shown.Attempts = len(result.Attempts)
			shown.CoveringTestPackages = st.coverage.covering[result.ID]
			shown.CoveringTests = st.coverage.coveringTests[result.ID]
			shown.MemoryLimit = memoryLimit
			shown.PeakMemory, shown.MemoryExceeded, shown.Diverged = 0, false, false
			for _, attempt := range result.Attempts {
				shown.PeakMemory = max(shown.PeakMemory, attempt.PeakMemory)
				shown.MemoryExceeded = shown.MemoryExceeded || attempt.MemoryExceeded
				shown.Diverged = shown.Diverged || attempt.Diverged
			}
			s.emit(MutantFinished{Result: shown.clone()})
		},
	}
}

func restoresOf(s *session, temps *temporaries, trees []string) []func() error {
	if len(trees) == 0 {
		return nil
	}
	out := make([]func() error, 0, len(trees))
	for worker := range trees {
		out = append(out, func() error { return s.restoreWorker(temps, worker) })
	}
	return out
}

func (s *session) isolate(snap *snapshot.Snapshot, jobs int, temps *temporaries) ([]string, error) {
	if jobs < 1 {
		jobs = 1
	}
	trees := make([]string, 0, jobs)
	for worker := range jobs {
		copied, err := snapshot.Create(snap.Root, snapshot.Options{DestParent: snap.Parent()})
		if err != nil {
			return nil, &Error{
				Code: CodeWorkspaceDrift,
				Message: "worker " + strconv.Itoa(worker) + "'s copy of the instrumented tree could not " +
					"be made, so --isolate cannot give it one",
				Err: err,
			}
		}
		temps.workers = append(temps.workers, copied)
		s.trace.Snapshot(trace.SnapshotRecord{
			Kind:   trace.SnapshotKindWorker,
			Source: snap.Root,
			Dir:    copied.Root,
			Stable: copied.StableDir,
			Files:  len(copied.Manifest),
		})
		trees = append(trees, copied.Root)
	}
	return trees, nil
}

func (s *session) restoreWorker(temps *temporaries, worker int) error {
	if worker < 0 || worker >= len(temps.workers) {
		return nil
	}
	copied := temps.workers[worker]
	if copied == nil {
		return nil
	}
	drifts, err := copied.Restore()
	if err != nil {
		return &Error{
			Code: CodeWorkspaceDrift,
			Message: "worker " + strconv.Itoa(worker) + "'s copy of the instrumented tree could not be " +
				"put back, so every mutant after this one would be measured against a tree nobody can describe",
			Err: err,
		}
	}
	if len(drifts) > 0 {
		s.trace.Note(trace.NoteWorkerRestored, "",
			"worker "+strconv.Itoa(worker)+": "+countNoun(len(drifts), "file")+" restored")
	}
	return nil
}

func (s *session) publish(opts Options, out *RunOutcome, st *state, status report.Status) error {
	s.enterPhase(PhaseReport, "writing the run report")

	rejected := make(map[string]bool, len(st.rejections))
	for _, rejection := range st.rejections {
		rejected[rejection.ID] = true
	}
	results := make([]report.MutantResult, 0, st.catalog.Len())
	for _, m := range st.catalog.Mutants() {
		if rejected[m.ID] {
			continue
		}
		result, measured := st.results[m.ID]
		switch {
		case !measured:
			result = report.MutantResult{
				ID:           m.ID,
				Outcome:      mutation.OutcomeNotRun,
				NotRunReason: st.notRunReason(m.ID),
			}
		case result.Outcome == mutation.OutcomeNotRun && result.NotRunReason == "":
			result.NotRunReason = report.NotRunInterrupted
		}
		results = append(results, result)
	}

	endBuild := s.stage("build", countNoun(len(results), "result"))
	finished := s.now()
	buildOpts := report.Options{
		ToolVersion:               or(opts.ToolVersion, unknownValue),
		RunID:                     out.RunID,
		Status:                    status,
		Started:                   out.Started,
		Finished:                  finished,
		Config:                    opts.Config,
		Mode:                      st.mode,
		ChangedRef:                changedRef(st),
		Shard:                     st.shard,
		Selected:                  st.selected,
		ModulePath:                st.found.modulePath(),
		GoVersion:                 goVersion(st.found.goVersion(), out.Toolchain.Version.Release),
		WorkspaceDigest:           out.WorkspaceDigest,
		Catalog:                   st.catalog,
		Located:                   st.found.candidates(),
		Skips:                     st.found.skips(),
		Results:                   results,
		Rejections:                st.rejections,
		TestCommand:               out.TestCommand,
		Baseline:                  out.BaselineRuns,
		Timeout:                   out.Timeout,
		TimeoutSource:             reportTimeoutSource(out.TimeoutSource),
		Memory:                    out.Memory,
		MemorySource:              reportMemorySource(out.MemorySource),
		CoverageMode:              reportCoverageMode(st.coverage.Mode()),
		CoverageBinaries:          st.coverage.binaries,
		CoverageTests:             st.coverage.tests,
		CoverageUnavailableReason: out.CoverageFallback,
		CoverageBuildFallback:     out.CoverageFallback != "",
		CacheMode:                 st.cache.Mode(),
		CacheMisses:               st.cache.misses,
		CacheWrites:               st.cache.writes,
		Warnings:                  reportWarnings(s.warnings),
		Timing:                    reportTiming(s.timing),
		Validation:                reportValidation(out.Validation),
		Snapshot:                  reportSnapshot(out.Snapshot),
		Toolchain: &report.ToolchainFacts{
			GoBin:   out.Toolchain.GoBin,
			Version: out.Toolchain.Version.String(),
		},
		ResolvedCommand: out.ResolvedTestCommand,
	}
	rep, workspace, err := s.document(buildOpts, st)
	endBuild(err)
	if err != nil {
		return err
	}

	endHistory := s.stage("history", opts.HistoryRoot)
	history := report.History{Root: opts.HistoryRoot}
	var runPath, latestPath string
	if workspace != nil {
		runPath, latestPath, err = history.WriteWorkspace(workspace)
	} else {
		runPath, latestPath, err = history.Write(rep)
	}
	endHistory(err)
	if err != nil {
		return err
	}
	out.Report = rep
	out.WorkspaceReport = workspace
	out.RunPath = runPath
	out.LatestPath = latestPath
	s.trace.Artifact(trace.ArtifactReportRun, runPath)
	s.trace.Artifact(trace.ArtifactReportLatest, latestPath)

	endArtifacts := s.stage("artifacts", opts.Config.Report.Directory)
	artifacts, artifactErr := report.WriteArtifacts(report.ArtifactOptions{
		Report:        rep,
		Workspace:     workspace,
		WorkspaceRoot: out.WorkspaceRoot,
		Directory:     opts.Config.Report.Directory,
		Formats:       opts.Config.Report.Formats,
		High:          opts.Config.Report.High,
		Low:           opts.Config.Report.Low,
	})
	endArtifacts(artifactErr)
	out.Artifacts = artifacts
	if artifacts.ProjectionPath != "" {
		s.trace.Artifact(trace.ArtifactReportJSON, artifacts.ProjectionPath)
	}
	if artifacts.HTMLPath != "" {
		s.trace.Artifact(trace.ArtifactReportHTML, artifacts.HTMLPath)
	}
	s.emit(ReportPublished{
		RunPath:        runPath,
		LatestPath:     latestPath,
		ProjectionPath: artifacts.ProjectionPath,
		HTMLPath:       artifacts.HTMLPath,
		TracePath:      opts.TraceDirectory,
	})
	if artifactErr != nil {
		return artifactErr
	}

	published := publishedRun(rep, workspace)
	tally, err := published.tally()
	if err != nil {
		return err
	}
	out.Verdict = mutation.Decide(tally, opts.Config.Policy, mutation.Signals{
		ExpectationFailure: published.expectationFailure(),
	})

	summary := s.compose(out, st, tally, published)
	s.summary = &summary
	return nil
}

func reportTiming(timing Timing) *report.Timing {
	if len(timing.Phases) == 0 && len(timing.Stages) == 0 {
		return nil
	}
	return &report.Timing{Phases: reportPhases(timing), Stages: reportStages(timing)}
}

func reportValidation(facts ValidationFacts) *report.Validation {
	if facts.Builds == 0 {
		return nil
	}
	return &report.Validation{Builds: facts.Builds}
}

func reportSnapshot(facts SnapshotFacts) *report.SnapshotFacts {
	if facts.Files == 0 {
		return nil
	}
	return &report.SnapshotFacts{StableDir: facts.StableDir, Files: facts.Files}
}

func reportPhases(timing Timing) []report.PhaseTiming {
	phases := make([]report.PhaseTiming, 0, len(timing.Phases))
	for _, phase := range timing.Phases {
		phases = append(phases, report.PhaseTiming{
			Name:       phase.Phase.String(),
			DurationMS: phase.Duration.Milliseconds(),
		})
	}
	return phases
}

func reportStages(timing Timing) []report.StageTiming {
	stages := make([]report.StageTiming, 0, len(timing.Stages))
	for _, stage := range timing.Stages {
		stages = append(stages, report.StageTiming{
			Phase:      stage.Phase.String(),
			Name:       stage.Name,
			DurationMS: stage.Duration.Milliseconds(),
			Result:     report.StageResult(stage.Result),
		})
	}
	return stages
}

func (s *session) compose(out *RunOutcome, st *state, tally mutation.Tally, published publishedRunView) RunSummary {
	summary := RunSummary{
		RunID:    out.RunID,
		ExitCode: out.Verdict.Code,
		Notable:  published.notable(st),
		Counts: Counts{
			Total:        tally.Total(),
			Killed:       tally.Killed,
			Survived:     tally.Survived(),
			TimedOut:     tally.TimedOut,
			Inconclusive: tally.Inconclusive,
			Errored:      tally.Errored,
			NotRun:       tally.NotRun,
			Rejected:     published.rejected(),
			Uncovered:    published.uncovered(),
			Cached:       published.cacheHits(),
		},
		Coverage: st.coverage.Mode(),
		Cache:    cacheMode(published.cacheMode()),
		Score:    mutation.ScoreOf(tally),
		Warnings: len(s.warnings),
		Skips:    skipCounts(st.found.skips()),
	}
	if len(out.Verdict.Failures) > 0 {
		summary.Failure = out.Verdict.Failures[0]
	}
	for _, expectation := range published.expectations() {
		switch expectation.State {
		case report.StateFulfilled:
			summary.Expectations.Fulfilled++
		case report.StateStale:
			summary.Expectations.Stale++
		case report.StateUnfulfilled:
			summary.Expectations.Unfulfilled++
		}
	}
	return summary.clone()
}

var notableRank = map[mutation.Outcome]int{
	mutation.OutcomeSurvived:     0,
	mutation.OutcomeTimedOut:     1,
	mutation.OutcomeInconclusive: 2,
	mutation.OutcomeErrored:      3,
}

func notable(st *state, mutants []report.Mutant) []MutantResult {
	out := make([]MutantResult, 0, len(mutants))
	for _, m := range mutants {
		core, err := m.Outcome.Mutation()
		if err != nil {
			continue
		}
		if _, listed := notableRank[core]; !listed {
			continue
		}
		shown := st.display[m.ID]
		shown.Outcome = core
		shown.Duration = time.Duration(m.DurationMS) * time.Millisecond
		shown.Uncovered = m.Uncovered
		shown.Cached = m.Cached
		if m.KilledBy != nil {
			shown.KilledBy = *m.KilledBy
		}
		shown.Attempts = m.Attempts
		shown.Diverged = m.Diverged
		shown.CoveringTestPackages = slices.Clone(m.CoveringTestPackages)
		shown.CoveringTests = slices.Clone(m.CoveringTests)
		out = append(out, shown)
	}
	slices.SortFunc(out, func(x, y MutantResult) int {
		if c := notableRank[x.Outcome] - notableRank[y.Outcome]; c != 0 {
			return c
		}
		if c := boolRank(x.Uncovered) - boolRank(y.Uncovered); c != 0 {
			return c
		}
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		if c := x.Line - y.Line; c != 0 {
			return c
		}
		if c := x.Column - y.Column; c != 0 {
			return c
		}
		if c := strings.Compare(x.Rule, y.Rule); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func uncoveredOf(mutants []report.Mutant) int {
	count := 0
	for _, m := range mutants {
		if m.Uncovered {
			count++
		}
	}
	return count
}

func displayIndex(
	catalog *mutation.Catalog,
	candidates []discover.Located,
	modules []discover.WorkspaceModule,
) (display map[string]MutantResult, packages map[string]string, neverReturns map[string]bool) {
	type key struct {
		module string
		path   string
		span   mutation.Span
		rule   string
	}
	located := make(map[key]discover.Located, len(candidates))
	for _, candidate := range candidates {
		k := key{
			module: candidate.ModulePath,
			path:   candidate.Path,
			span:   candidate.Span,
			rule:   candidate.Rule.Name,
		}
		if _, seen := located[k]; !seen {
			located[k] = candidate
		}
	}
	dirs := make(map[string]string, len(modules))
	for _, module := range modules {
		dirs[module.Path] = module.Dir
	}

	display = make(map[string]MutantResult, catalog.Len())
	packages = make(map[string]string, catalog.Len())
	neverReturns = make(map[string]bool)
	for _, m := range catalog.Mutants() {
		where := located[key{module: m.ModulePath, path: m.Path, span: m.Span, rule: m.Rule.Name}]
		display[m.ID] = MutantResult{
			ID:          m.ID,
			DisplayID:   m.DisplayID,
			Path:        m.Path,
			ModuleDir:   dirs[m.ModulePath],
			Line:        where.Line,
			Column:      where.Column,
			Rule:        m.Rule.Name,
			Original:    m.Original,
			Replacement: m.Replacement,
		}
		if where.Package != "" {
			packages[m.ID] = where.Package
		}
		if where.Termination != nil && where.Termination.Verdict == discover.TerminationUnbounded {
			neverReturns[m.ID] = true
		}
	}
	return display, packages, neverReturns
}

func changedRef(st *state) string {
	if st.changed == nil {
		return ""
	}
	return st.changed.Ref
}

func rejectionsOf(rejected []validate.Rejection) []report.Rejection {
	out := make([]report.Rejection, 0, len(rejected))
	for _, rejection := range rejected {
		out = append(out, report.Rejection{ID: rejection.ID, Diagnostic: rejection.Diagnostic})
	}
	return out
}

func reportWarnings(warnings []Warning) []report.Warning {
	out := make([]report.Warning, 0, len(warnings))
	for _, warning := range warnings {
		out = append(out, report.Warning{Code: warning.Code, Message: warning.Message})
	}
	return out
}

func reportMemorySource(source MemorySource) report.MemorySource {
	switch source {
	case MemorySourceExplicit:
		return report.MemoryExplicit
	case MemorySourceDerived:
		return report.MemoryDerived
	case MemorySourceUnavailable:
	}
	return report.MemoryUnavailable
}

func reportTimeoutSource(source TimeoutSource) report.TimeoutSource {
	if source == TimeoutExplicit {
		return report.TimeoutExplicit
	}
	return report.TimeoutDerived
}

func skipTotal(skips []discover.Skip) int {
	total := 0
	for _, skip := range skips {
		total += skip.Count
	}
	return total
}

func skipCounts(skips []discover.Skip) []SkipCount {
	totals := make(map[string]int, len(skips))
	for _, skip := range skips {
		totals[string(skip.Reason)] += skip.Count
	}
	out := make([]SkipCount, 0, len(totals))
	for reason, count := range totals {
		out = append(out, SkipCount{Reason: reason, Count: count})
	}
	slices.SortFunc(out, func(x, y SkipCount) int { return strings.Compare(x.Reason, y.Reason) })
	return out
}

const unknownValue = "unknown"

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func goVersion(module, toolchain string) string {
	if module != "" {
		return module
	}
	return toolchain
}

type session struct {
	events             chan<- Event
	warnings           []Warning
	closed             bool
	patterns           []string
	published          *eventSink
	trace              *trace.Recorder
	clock              func() time.Time
	timing             Timing
	openPhase          Phase
	phaseEnd           func() time.Duration
	phaseStarted       time.Time
	summary            *RunSummary
	cache              *cache.Cache
	cacheCorruptWarned bool
	cacheWriteWarned   bool
}

func (s *session) emit(e Event) {
	if s.events == nil {
		return
	}
	s.events <- e
}

func (s *session) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

func (s *session) enterPhase(phase Phase, detail string) {
	s.closePhase()
	s.emit(PhaseChanged{Phase: phase, Detail: detail})
	s.openPhase = phase
	s.phaseStarted = s.now()
	s.phaseEnd = s.trace.PhaseStart(string(phase))
}

func (s *session) closePhase() {
	if s.phaseEnd == nil {
		return
	}
	duration := s.phaseEnd()
	s.phaseEnd = nil
	if s.trace == nil {
		duration = s.now().Sub(s.phaseStarted)
	}
	s.timing.Phases = append(s.timing.Phases, PhaseDuration{Phase: s.openPhase, Duration: duration})
	s.emit(PhaseCompleted{Phase: s.openPhase, Duration: duration})
}

func (s *session) stage(name, detail string) func(err error) {
	end := s.trace.Stage(name, detail)
	phase := s.openPhase
	started := s.now()
	done := false
	return func(err error) {
		if done {
			return
		}
		done = true
		result := trace.ResultSucceeded
		if err != nil {
			result = trace.ResultFailed
		}
		duration := end(result)
		if s.trace == nil {
			duration = s.now().Sub(started)
		}
		s.timing.Stages = append(s.timing.Stages, StageDuration{
			Phase:    phase,
			Name:     name,
			Duration: duration,
			Result:   result,
		})
	}
}

func (s *session) sweepTemporary(parent string) tempowner.Result {
	result, err := tempowner.Sweep(parent, tempPrefixes, s.now())
	record := trace.SweepRecord{
		Parent:       parent,
		Removed:      result.Removed,
		RemovedBytes: result.RemovedBytes,
		Live:         result.Live,
		Kept:         result.Kept,
	}
	if err != nil {
		record.Error = err.Error()
		s.warn(CodeOrphanNotRemoved,
			"temporary directories left by earlier runs could not be removed: "+err.Error())
	}
	s.trace.Sweep(record)
	return result
}

func (s *session) recordSnapshot(source string, snap *snapshot.Snapshot, took time.Duration, err error) {
	record := trace.SnapshotRecord{
		Kind:       trace.SnapshotKindWorkspace,
		Source:     source,
		DurationMS: took.Milliseconds(),
	}
	if err != nil {
		record.Error = err.Error()
	}
	if snap != nil {
		record.Dir = snap.Root
		record.Stable = snap.StableDir
		record.Files = len(snap.Manifest)
		record.Digest = snap.WorkspaceDigest
	}
	s.trace.Snapshot(record)
}

func (s *session) warn(code Code, message string) {
	s.warnCode(string(code), message)
}

func (s *session) warnCode(code, message string) {
	s.warnDetail(code, message, "")
}

func (s *session) warnDetail(code, message, detail string) {
	w := Warning{Code: code, Message: message, Detail: detail}
	s.warnings = append(s.warnings, w)
	s.trace.Note(trace.NoteWarning, code, message)
	s.emit(w)
}

func (s *session) close() {
	if s.events == nil || s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

func check(ctx context.Context, spec runner.Spec, result runner.Result, code Code, what string) error {
	switch {
	case result.Err != nil:
		return &Error{
			Code:       code,
			Message:    what + ": the command could not be run",
			Output:     tail(result.Output),
			Err:        result.Err,
			Invocation: runner.CommandOf(spec, result),
		}
	case result.TimedOut:
		return &Error{
			Code:       CodeBaselineTimedOut,
			Message:    what + ": no answer within " + BaselineCap.String(),
			Output:     tail(result.Output),
			Invocation: runner.CommandOf(spec, result),
		}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &Error{
			Code:       CodeDeadlineExceeded,
			Message:    what + ": the run's deadline expired",
			Output:     tail(result.Output),
			Err:        ctx.Err(),
			Invocation: runner.CommandOf(spec, result),
		}
	case ctx.Err() != nil:
		return &Error{
			Code:       CodeInterrupted,
			Message:    "the run was interrupted",
			Err:        ctx.Err(),
			Invocation: runner.CommandOf(spec, result),
		}
	case result.ExitCode != 0:
		return &Error{
			Code:       code,
			Message:    what + ": exited with status " + strconv.Itoa(result.ExitCode),
			Output:     tail(result.Output),
			Invocation: runner.CommandOf(spec, result),
		}
	}
	return nil
}

func interrupted(err error) bool {
	return errors.Is(err, context.Canceled)
}

func Interrupted(err error) bool { return interrupted(err) }

func cachedBaselineWarning(runs int) string {
	const preamble = " was answered from the go test result cache, so the per-mutant budgets are " +
		"sized on a cache lookup rather than on the suite; "
	if runs == 1 {
		return "the only baseline run" + preamble +
			"raise test.baseline_runs so that a run after the first times the tests, " +
			"or set test.timeout rather than deriving one"
	}
	return fmt.Sprintf("every one of the %d baseline runs", runs) + preamble +
		"every run after the first was given " + gocmd.CountOnce + " through GOFLAGS and " +
		"answered that way anyway, so this test.command does not obey GOFLAGS -- make the " +
		"command itself run the tests, or set test.timeout rather than deriving one"
}

func budgetBaseline(runs []baselineObservation) time.Duration {
	ran := make([]baselineObservation, 0, len(runs))
	for _, run := range runs {
		if !run.Cached {
			ran = append(ran, run)
		}
	}
	if len(ran) == 0 {
		return slowest(runs)
	}
	return slowest(ran[min(len(ran)-1, 1):])
}

func everyRunCached(runs []baselineObservation) bool {
	if len(runs) == 0 {
		return false
	}
	for _, run := range runs {
		if !run.Cached {
			return false
		}
	}
	return true
}

func slowest(runs []baselineObservation) time.Duration {
	var most time.Duration
	for _, run := range runs {
		most = max(most, run.Duration)
	}
	return most
}

type baselineObservation struct {
	Duration time.Duration
	Cached   bool
}

const cachedMarker = "(cached)"

func servedFromTestCache(output []byte) bool {
	for line := range strings.Lines(string(output)) {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ok" && fields[2] == cachedMarker {
			return true
		}
	}
	return false
}

func deriveTimeout(explicit, slowest time.Duration) (time.Duration, TimeoutSource, error) {
	if explicit > 0 {
		if explicit <= slowest {
			return 0, "", &Error{
				Code: CodeTimeoutTooSmall,
				Message: fmt.Sprintf(
					"test.timeout %s is not above the slowest baseline run (%s): every mutant would time out",
					explicit, slowest),
			}
		}
		return explicit, TimeoutExplicit, nil
	}
	return max(MinDerivedTimeout, TimeoutFactor*slowest), TimeoutDerived, nil
}

func deriveMemory(explicit, peak int64) (int64, MemorySource) {
	if explicit > 0 {
		return explicit, MemorySourceExplicit
	}
	if peak <= 0 {
		return 0, MemorySourceUnavailable
	}
	return max(int64(MinDerivedMemory), MemoryFactor*peak), MemorySourceDerived
}

func enforceableMemory(limit int64, source MemorySource) (int64, MemorySource) {
	if limit > 0 && source == MemorySourceDerived && !runner.MemoryBoundSupported() {
		return 0, MemorySourceUnavailable
	}
	return limit, source
}

const RunIDPattern = `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`

var runIDPattern = regexp.MustCompile(RunIDPattern)

func resolveRunID(given string, at time.Time) (string, error) {
	if given == "" {
		return NewRunID(at), nil
	}
	if !runIDPattern.MatchString(given) {
		return NewRunID(at), &Error{
			Code: CodeRunID,
			Message: "the run id " + strconv.Quote(given) +
				" is not one: a run id is a UTC timestamp and four lowercase hex digits, " +
				"as in \"20260907T120000Z-a1b2\"",
		}
	}
	return given, nil
}

func NewRunID(t time.Time) string {
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix[:])
}

func workspaceRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", &Error{Code: CodeWorkspaceRoot, Message: "no workspace root was given"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", &Error{
			Code:    CodeWorkspaceRoot,
			Message: "the workspace root " + strconv.Quote(root) + " cannot be resolved",
			Err:     err,
		}
	}
	return abs, nil
}

func temporaryParent(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return os.TempDir()
	}
	return dir
}

func testCommand(cfg config.Config, override []string) ([]string, error) {
	command := cfg.Test.Command
	if len(override) > 0 {
		command = override
	}
	if len(command) == 0 {
		return nil, &Error{Code: CodeTestCommand, Message: "the test command is empty"}
	}
	if strings.TrimSpace(command[0]) == "" {
		return nil, &Error{Code: CodeTestCommand, Message: "the test command's program name is empty"}
	}
	return slices.Clone(command), nil
}

func resolveProgram(command []string, toolchain gocmd.Toolchain) []string {
	argv := slices.Clone(command)
	if argv[0] == "go" && toolchain.GoBin != "" {
		argv[0] = toolchain.GoBin
	}
	return argv
}

func childEnv(scratch string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(tempKeys))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), envPrefix) || isTempKey(key) {
			continue
		}
		env = append(env, entry)
	}
	for _, key := range tempKeys {
		env = append(env, key+"="+scratch)
	}
	return env
}

func isTempKey(key string) bool {
	return slices.ContainsFunc(tempKeys, func(k string) bool { return strings.EqualFold(key, k) })
}

func mean(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total / time.Duration(len(durations))
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
