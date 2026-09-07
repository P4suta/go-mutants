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

// Fixed budgets and the timeout derivation constants.
//
// Each is a decision rather than a technical limit, and each is stated once
// here so that the engine, the help text, and the documentation cannot drift.
const (
	// BaselineCap bounds one baseline command — the build, or one measured
	// test run. It is fixed and generous on purpose: it exists before there is
	// any measurement to derive a budget from, so its only job is to stop a
	// hung toolchain from hanging the run forever. A project whose tests
	// legitimately take longer than this has told go-mutants nothing it can
	// work with anyway.
	BaselineCap = 10 * time.Minute

	// MinDerivedTimeout is the floor under a derived per-mutant timeout. Five
	// times a very fast suite is still a very small number, and a mutant that
	// makes a fast test slow — an infinite loop, a retry that never gives up —
	// is exactly the mutant a timeout is meant to catch rather than to
	// misreport as scheduling noise.
	MinDerivedTimeout = 10 * time.Second

	// TimeoutFactor multiplies the slowest baseline run.
	TimeoutFactor = 5

	// MinDerivedMemory is the floor under a derived per-mutant memory bound.
	//
	// It is what a suite that never measured above the noise gets, and a
	// gibibyte is that number because of what the floor has to survive rather
	// than what a small suite needs. Four times a peak of forty megabytes is a
	// hundred and sixty, and a bound that tight would be tripped by any of the
	// ordinary things a Go test binary does more of on somebody else's machine:
	// a `-cover` build's counters, the race detector's shadow memory, a
	// property test's corpus, a table that grew. None of those is a runaway,
	// and every one of them would come back as a mutant killed for something
	// its own suite did.
	//
	// A gibibyte is meanwhile nowhere near a machine. The incident this bound
	// exists for reached eleven gigabytes in twelve seconds and was still
	// climbing when the runner died; anything in that shape crosses a gibibyte
	// in the first second and is stopped there.
	MinDerivedMemory = 1 << 30

	// MemoryFactor multiplies the largest peak the baseline runs reached.
	//
	// Four rather than the timeout's five, and the asymmetry is the difference
	// between the two quantities. Wall-clock time is shared: a suite measured
	// alone and then run against N mutants at once is genuinely slower, and the
	// timeout's factor has to cover the machine as well as the mutant. Resident
	// memory is not — a test binary run beside fifteen others allocates what it
	// allocates — so the factor is covering variance in the program rather than
	// contention for the machine, and a smaller one catches a runaway sooner.
	MemoryFactor = 4

	// scratchPrefix names the per-run scratch directory. It sits beside the
	// snapshot rather than inside it, so that a test writing to the temporary
	// directory neither shows up as workspace drift nor is deleted out from
	// under itself when the snapshot is cleaned up.
	scratchPrefix = "go-mutants-tmp-"

	// binDirName is the scratch subdirectory the compiled test binaries live
	// in. It is inside the scratch directory and therefore outside the
	// snapshot, which internal/execute requires and the drift gate depends on:
	// a binary written into the tree would be indistinguishable from a test
	// that wrote into it.
	binDirName = "bin"

	// workerDirName is the scratch subdirectory holding one temporary directory
	// per execution worker.
	workerDirName = "workers"

	// envPrefix is the variable prefix a child process never inherits.
	// Activation is the engine's to set; a GO_MUTANTS_ACTIVE left in a user's
	// shell would otherwise silently turn a mutant on inside the baseline.
	envPrefix = "GO_MUTANTS_"
)

// tempPrefixes are the names a run creates directly in the temporary
// directory, and so the only names its sweep is allowed to collect. Nothing
// else in a directory shared with the whole machine is go-mutants' to touch.
var tempPrefixes = []string{snapshot.DirPrefix, scratchPrefix}

// tempKeys are the environment variables redirected at the scratch directory.
// All three are set on every platform: TMPDIR is the POSIX spelling, TMP and
// TEMP the Windows ones, and a cross-compiling toolchain or a test helper may
// read any of them.
var tempKeys = []string{"TMP", "TEMP", "TMPDIR"}

// Options is everything [Run] needs. Every field is supplied by the caller;
// the engine reads no global state beyond the process environment it hands to
// its children.
type Options struct {
	// Config is the fully resolved configuration: defaults, file, and flags
	// already merged and validated.
	Config config.Config

	// WorkspaceRoot is the directory to mutate, normally the current working
	// directory. It is resolved to an absolute path and is only ever read.
	WorkspaceRoot string

	// TestArgv overrides `test.command` with the argv the user wrote after
	// `--`. It is the single authoritative override: the command line does not
	// also push it through a config.Overlay, because two paths for one value
	// is one path too many. Empty means "use the configured command".
	TestArgv []string

	// ToolVersion is the go-mutants version the report records. internal/cli
	// owns the string — it cannot be imported here without the dependency
	// running backwards — so it travels in rather than being read.
	ToolVersion string

	// MutantPrefix narrows the run to the single mutant whose id starts with
	// it, as `--mutant` does. Everything else is catalogued and reported as
	// not-run, which is what keeps the score and `policy.require_mutants`
	// honest about the difference between "nothing to find" and "not looked at
	// this time". Empty runs every accepted mutant.
	//
	// A prefix matching no mutant, or more than one, is a [SelectionError]: the
	// point of naming one mutant is to be sure which.
	MutantPrefix string

	// Changed narrows execution to the mutants sitting on lines that have
	// changed since ChangedRef, as `--changed` does. Everything else is
	// discovered, catalogued, validated and reported exactly as in a full run —
	// only the execution is narrowed — so the ids and the rejections of a
	// changed run and of a whole one are the same, and the two documents can be
	// compared mutant for mutant.
	Changed bool
	// ChangedRef is the git ref the diff is taken against; the merge base of it
	// and HEAD is what is compared. Empty — or [gitdiff.UpstreamRef], which is
	// the same request written out and is what the bare `--changed` flag
	// carries — resolves the upstream branch of HEAD, and fails when there is
	// none. It is read only when Changed is set.
	ChangedRef string

	// Shard narrows execution to one shard of the run, as `--shard K/N` does.
	// The zero value is a run that was not split; a Total above zero is a shard,
	// and everything the shard does not own is reported as not-run with
	// [report.NotRunOtherShard] so that the document is a complete statement
	// about the catalogue rather than a fragment of one.
	//
	// It composes with Changed: a shard of a changed run executes the mutants it
	// owns that also sit on changed lines, which is what a CI matrix over a pull
	// request asks for.
	Shard report.Shard

	// TempDirectory is the parent of the run's own temporary directories: the
	// snapshot, the scratch directory beside it — the compiled test binaries,
	// the per-worker temporary directories and the coverage data underneath —
	// and the only directory the sweep ever collects under. Empty is
	// os.TempDir(), which is what every real run uses.
	//
	// It mirrors [github.com/P4suta/go-mutants.OpenOptions.TempDirectory], and
	// for the same two reasons. A run that names its own parent is a run whose
	// debris a caller can find and account for, and the sweep that takes the
	// disk back from a killed run is then confined to a directory this caller
	// owns rather than turned loose on one shared with the whole machine. It is
	// also what lets the engine's own tests be private without redirecting the
	// process-wide TMPDIR, which is a global they would have to hold serially.
	//
	// A relative path is resolved against the current working directory rather
	// than refused: internal/snapshot resolves the destination with
	// filepath.Abs, and the sweep reads the same name from the same directory,
	// so both mean what the caller wrote. Nothing here changes what the run's
	// children see — their TMP, TEMP and TMPDIR are pointed at a directory
	// under the run's scratch either way; see [childEnv].
	TempDirectory string

	// KeepTemp preserves the run's snapshot and scratch directory instead of
	// removing them: always, or only when the run failed. The zero value is
	// [KeepTempNever], which is what every caller that never heard of the option
	// gets and what every run did before it existed.
	//
	// It takes no part in a mutant identity, in a verdict, or in the key a
	// cached outcome is stored under — a keep is a decision about a directory on
	// the way out, taken after every mutant has been measured. See [KeepTemp]
	// for why it is opt-in and [RunOutcome.Preserved] for what it leaves behind.
	KeepTemp KeepTemp

	// HistoryRoot overrides the directory the run history is written under.
	// Empty is <os.UserCacheDir>/go-mutants, which is what every real run uses;
	// the tests set it so that they never touch the developer's own cache.
	HistoryRoot string

	// CacheRoot overrides the directory the outcome cache is kept under. Empty
	// falls back to HistoryRoot, and only then to <os.UserCacheDir> with
	// `cache.directory` underneath it.
	//
	// The fallback is what makes the tests safe rather than a convenience. The
	// two stores share a workspace directory in production, so a caller that
	// redirected the history and left this empty plainly meant to redirect both;
	// without the fallback such a caller would quietly write outcome entries
	// into the developer's own cache directory, which is the one place a test
	// must never touch.
	CacheRoot string

	// Events receives every [Event]. The engine closes it on return. A nil
	// channel publishes nothing and is not closed; see the package
	// documentation for the draining contract.
	Events chan<- Event

	// RunID is the identity this run is filed under, in [NewRunID]'s form.
	// Empty mints a fresh one from the run's own clock, which is what every
	// caller that has no use for the id before the run starts passes.
	//
	// internal/cli sets it because it names the trace directory before the
	// engine is called, and the recording and the report have to carry the same
	// id or the two cannot be paired afterwards.
	RunID string

	// TraceSink is where the run's recording goes. A nil sink is the disabled
	// trace and is the value every caller passed before there was one: the
	// engine records unconditionally into a nil [trace.Recorder], so the traced
	// and the untraced path are one path and there is no branch for a verdict
	// to come to depend on.
	//
	// The sink belongs to the caller, which opened it and knows when the last
	// thing that will write to it is done. The engine never closes it.
	TraceSink trace.Sink

	// PublishTrace also publishes every recorded event on Events, as [Traced].
	// It is what `run -v` and `-vv` ask for, and it does nothing without a sink:
	// there is no recording to forward.
	PublishTrace bool

	// Notes are what the caller learned about the recording before the run
	// began, recorded immediately after the run-start.
	//
	// Two things belong here and both are decided outside the engine: a trace
	// directory that was refused, and the collection of older recordings that
	// ran before this one opened its own. Each is a fact about the moment the
	// recording began, which is where it is recorded — a note appended
	// afterwards would have to displace the run-end that a reader relies on
	// being the last line of a recording. The recorder stamps them like every
	// other event, so a note carries the sequence number and the clock of the
	// moment it went in rather than a moment its author had to invent.
	Notes []trace.NoteRecord

	// TraceDirectory is where the caller opened this run's recording, and is
	// informational: the engine writes nothing there and reads nothing from it.
	//
	// It is published in [ReportPublished.TracePath] so that where the account
	// of a run went is printed beside where its documents went, by the one
	// renderer that already knows how to print a run's paths — which is also
	// what makes it obey --quiet without a second rule about when to print.
	TraceDirectory string

	// now is the run's clock, and the seam this package's own tests move by
	// hand. Nil is [time.Now], which is what every caller outside this package
	// gets. It is unexported for the reason [execute.Options]'s run seam is: a
	// clock is a test's business, and an exported one is an invitation to make
	// a run's timings a caller's opinion.
	now func() time.Time
}

// RunOutcome is everything one run learned. It is returned even when [Run]
// fails, filled in as far as the run got, so that a caller can report what was
// established before the failure.
type RunOutcome struct {
	// RunID is the identifier published in [RunPlanned].
	RunID string
	// Status is how the run ended.
	Status Status
	// Started is when [Run] was entered, in the local clock.
	Started time.Time
	// Duration is the wall-clock time the whole run took.
	Duration time.Duration

	// WorkspaceRoot is the absolute path of the tree that was copied.
	WorkspaceRoot string
	// Workers is the resolved worker count.
	Workers int
	// Toolchain is the Go toolchain the run used.
	Toolchain gocmd.Toolchain

	// SnapshotRoot is where the disposable copy lived: the tree itself, one
	// level inside the directory internal/snapshot owns. It is already removed
	// by the time Run returns unless [Options.KeepTemp] asked otherwise, and it
	// is retained for diagnostics and for the tests that prove the cleanup
	// happened.
	//
	// Preserved names that owning *directory* rather than this path, because
	// that is what carries the lock and the marker and what a sweep collects.
	// SnapshotRoot is the `tree` inside it; the two are deliberately different
	// strings and neither is derivable from the other by a caller.
	SnapshotRoot string
	// SnapshotFiles is how many regular files the snapshot held.
	SnapshotFiles int
	// WorkspaceDigest is the frozen digest of the snapshot manifest.
	WorkspaceDigest string

	// TestCommand is the argv the baseline was measured with, as the user
	// wrote it — before the toolchain path was substituted for a bare `go`.
	// It is the spelling that belongs in a report and in a message.
	TestCommand []string
	// ResolvedTestCommand is that same argv as it was really started: with
	// [Toolchain]'s absolute path in place of a bare `go`, and identical to
	// TestCommand for any other program.
	//
	// Both are reported because they answer different questions. The written
	// command is what the user configured and what they would run by hand; the
	// resolved one is which executable this run actually started, which under a
	// toolchain manager need not be the `go` on anybody's PATH. It is empty on
	// a run that stopped before the baseline resolved one.
	ResolvedTestCommand []string
	// BaselineRuns holds every baseline observation, in measurement order.
	BaselineRuns []time.Duration
	// AverageBaseline and SlowestBaseline summarise BaselineRuns.
	AverageBaseline time.Duration
	SlowestBaseline time.Duration
	// Timeout is the per-mutant timeout, and TimeoutSource says where it came
	// from.
	Timeout       time.Duration
	TimeoutSource TimeoutSource
	// PeakBaseline is the largest resident memory any baseline run was observed
	// to reach, in bytes, and is zero where the platform could not measure one.
	PeakBaseline int64
	// Memory is the per-mutant memory bound in bytes, and MemorySource says
	// where it came from. Both are the bound this run *applied*: a zero Memory
	// beside [MemorySourceUnavailable] is a run in which nothing was bounded.
	Memory       int64
	MemorySource MemorySource

	// Report is the published run report, or nil when the run stopped before
	// there was anything to publish. It is the document on disk, so a caller
	// deciding an exit code or writing `--json` is looking at exactly what the
	// user can read in the file.
	Report *report.Report
	// RunPath and LatestPath are where the report was filed, as published in
	// [ReportPublished].
	RunPath    string
	LatestPath string
	// Artifacts are the project artefacts written into `report.directory`, as
	// published in [ReportPublished]. Both paths are empty when
	// `report.formats` asked for nothing, and both are empty when publishing
	// them failed — in which case [Run] returns that failure.
	Artifacts report.Artifacts
	// Verdict is what [mutation.Decide] made of the report. It is the zero
	// value when no report was published, in which case the failure itself
	// decides the exit status.
	Verdict mutation.Verdict

	// Preserved names the temporary directories [Options.KeepTemp] asked the
	// run to leave behind, sorted by kind and then by path, and each also
	// published as a [DirectoryKept]. It is empty for every run that kept
	// nothing, which is every run that did not ask.
	Preserved []PreservedDir

	// Warnings are the warnings published during the run, in order.
	Warnings []Warning
	// Summary is the closing line published in [RunCompleted].
	Summary string

	// Validation is what compiling the instrumented snapshot cost.
	Validation ValidationFacts
	// Snapshot is what the copy of the workspace turned out to be.
	Snapshot SnapshotFacts
	// Timing is where the run's wall-clock time went, phase by phase and stage
	// by stage, in the order the spans were closed.
	Timing Timing
	// CoverageFallback is the whole failure that made a run give up
	// coverage-instrumented test binaries and build plain ones: the coded
	// message and the compiler's own diagnostics under it. Empty on every run
	// that did not fall back, which is nearly all of them.
	//
	// The console has already been told in one line. This is the copy for
	// somebody asking why, and the two are deliberately different lengths: a run
	// that is about to succeed does not print a compiler blob at the user.
	CoverageFallback string
}

// ValidationFacts is what the validation phase spent.
type ValidationFacts struct {
	// Builds is how many `go build` invocations it took to establish which
	// catalogued mutants compile. One means the whole catalogue compiled on the
	// first try, which is the ordinary case; anything more is a bisection, and
	// is where a slow run's minutes went.
	Builds int
}

// SnapshotFacts is what the disposable copy of the workspace turned out to be.
//
// Both fields are about the *copy* rather than about the code in it, which is
// why neither is in the report's workspace digest: a run whose snapshot could
// not take the stable name pays for a cold Go build cache, and that is a fact
// about the machine's temporary directory, not about the program under test.
type SnapshotFacts struct {
	// StableDir reports whether the copy carried the source tree's own derived
	// name rather than a random one, which is what lets the Go build cache be
	// reused between two runs of one workspace.
	StableDir bool
	// Files is how many regular files were copied.
	Files int
}

// A PhaseDuration is how long one phase took.
type PhaseDuration struct {
	Phase    Phase
	Duration time.Duration
}

// A StageDuration is how long one step inside a phase took, and what became of
// it.
type StageDuration struct {
	// Phase is the phase the stage started in.
	Phase Phase
	// Name is the stage's own name, which is unique only within its phase:
	// `build` happens in the baseline and again in the report.
	Name string
	// Duration is the wall-clock time the stage took.
	Duration time.Duration
	// Result is [trace.ResultSucceeded] or [trace.ResultFailed].
	Result string
}

// Timing is where a run's wall-clock time went.
//
// Both lists are in emission order — the order the spans were closed, which for
// the engine's linear pipeline is the order they were opened — so that a
// consumer can render the run as a timeline without sorting it, and so that two
// runs of one workspace produce two lists that line up row for row.
type Timing struct {
	Phases []PhaseDuration
	Stages []StageDuration
}

// clone returns a copy that shares no slice with the receiver.
func (t Timing) clone() Timing {
	t.Phases = slices.Clone(t.Phases)
	t.Stages = slices.Clone(t.Stages)
	return t
}

// Run executes one mutation run.
//
// The pipeline is: locate the toolchain, snapshot the workspace, build it,
// measure the unmutated tests, derive the per-mutant timeout, discover the
// candidates, catalogue them, instrument and compile-validate the snapshot,
// prove the instrumented tree still passes with nothing activated, check that
// nothing but the instrumentation moved, build the test binaries once, execute
// every accepted mutant against them, and publish the report. The snapshot is
// removed on every path.
//
// The returned error is nil exactly when the run completed. It always carries a
// stable GOM#### code — either this package's or that of the package that
// failed — except for a [SelectionError], which says why, and the outcome is
// returned alongside it.
//
// A completed run is not necessarily a passing one: whether the score or the
// survivors fail a policy gate is [RunOutcome.Verdict], and reporting that as
// an error would conflate "go-mutants could not do its job" with "your tests
// did not catch something".
func Run(ctx context.Context, opts Options) (RunOutcome, error) {
	s := &session{events: opts.Events, clock: opts.now}
	// Registered before anything else, so that it runs after everything else.
	// Every deferred step below — the snapshot cleanup in particular — may
	// still publish a warning, and a send on a closed channel panics.
	defer s.close()

	started := s.now()
	// The identity first, because everything below is filed under it — the
	// outcome, the recording, and the document the run publishes — and a caller
	// that named one that is not a run id has made a mistake about the
	// invocation that costs nothing to find and a snapshot to find later.
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
	// Right after the run-start, which is where they happened: see
	// [Options.Notes].
	for _, note := range opts.Notes {
		s.trace.Note(note.Kind, note.Code, note.Detail)
	}

	err := idErr
	if err == nil {
		err = s.pipeline(ctx, opts, &out)
	}

	// The phase the run was in when it stopped, whichever one that was and
	// whatever stopped it. [session.pipeline] has one exit, so closing it here
	// covers every path through it, and the closer is once-guarded so that a
	// phase already closed on the way out stays one span.
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
	// The recording is closed before the terminal event and never after it: a
	// reader who found the run-end has read the whole run, and an event
	// published afterwards would be a line beyond the last line. The sink itself
	// belongs to the caller and is left open; see [Options.TraceSink].
	s.trace.RunEnd(string(out.Status), exitCodeOf(out), err)
	// Everything the recording published reaches the stream before the terminal
	// event does, so that a renderer drawing the trace has drawn all of it by
	// the time the run says it is over.
	s.drainPublished()
	// Terminal on every path, including this one: a renderer that never sees
	// RunCompleted cannot tell a finished run from a crashed one.
	s.emit(RunCompleted{Status: out.Status, Summary: out.Summary, Run: s.summary})
	return out, err
}

// sink is where the run records, with the fan-out onto the event stream built
// in when a caller asked for one.
//
// The tee is built only for [Options.PublishTrace], so a run nobody asked to
// publish pays nothing for the option: no wrapper, no clone per event, no send.
// The caller's own sink is never closed by the tee either, because the engine
// never closes the tee.
//
// The published branch is wrapped in [trace.Digested] for the reason a bounded
// ring is: captured output grows with the run rather than with what reads it,
// and this branch is a copy for a screen. A renderer prints an execution's exit
// status, duration and argument vector and never its bytes, so publishing them
// would deep-copy up to a megabyte per mutant for nobody. The size and the
// digest stay, which is everything a reader joins on.
func (s *session) sink(opts Options) trace.Sink {
	if opts.TraceSink == nil || !opts.PublishTrace {
		return opts.TraceSink
	}
	s.published = newEventSink(s)
	return trace.NewTeeSink(opts.TraceSink, trace.Digested(s.published))
}

// publishBuffer is how many recorded events wait between the recorder and the
// event stream.
//
// It is a bound rather than a queue with a policy, and the number is the same
// order as the recorder's own ring: large enough that a burst of mutant
// executions never reaches it, small enough that a consumer which has stopped
// draining is felt rather than accommodated without limit.
const publishBuffer = 1024

// An eventSink publishes every recorded event on the engine's own stream.
//
// The hand-off is the whole of this type. A recorded event reaches its sinks
// under the recorder's own lock, so a sink that blocks blocks every goroutine
// that is recording anything — during execution, that is every worker — and the
// stream this one writes to is drained by a renderer at the speed of a terminal.
// Publishing straight onto it would therefore have made a run slower for having
// asked to watch it, which is a diagnostic changing the thing it is a diagnostic
// of.
//
// So [eventSink.Emit] does nothing but put a copy on a bounded channel, and one
// goroutine forwards from it. The recorder's lock is never held across a send
// onto the stream, and the single forwarder is what keeps the published order
// the recorded order. The bound is the trade-off, stated plainly: a consumer
// slower than [publishBuffer] events does eventually apply back-pressure, but it
// applies it to the forwarder rather than to the run.
//
// The clone is what makes a published event safe to keep: the recorder hands one
// value to every sink, and a renderer holding it must not be able to see the
// next sink — or a later caller reusing a record — write into it.
type eventSink struct {
	session *session
	queue   chan trace.Event
	done    chan struct{}
}

// newEventSink starts the forwarder. Its counterpart is
// [session.drainPublished], which the run calls once, before the terminal event.
func newEventSink(s *session) *eventSink {
	sink := &eventSink{
		session: s,
		queue:   make(chan trace.Event, publishBuffer),
		done:    make(chan struct{}),
	}
	go sink.forward()
	return sink
}

// forward publishes what the recorder queued, in the order it was recorded.
func (sink *eventSink) forward() {
	defer close(sink.done)
	for event := range sink.queue {
		sink.session.emit(Traced{Event: event})
	}
}

// Emit queues the event and never fails. Reporting a renderer's slowness as a
// dropped event would make a recording's accounting say it was lossy when it was
// not: the caller's own sink kept every line, and this is a copy for a screen.
func (sink *eventSink) Emit(event trace.Event) error {
	sink.queue <- event.Clone()
	return nil
}

// Close does nothing: the stream this sink writes to is the engine's own, and
// [session.close] owns it.
func (*eventSink) Close() error { return nil }

// drainPublished waits for every recorded event to reach the stream.
//
// It runs after the recording is closed and before [RunCompleted], which is what
// keeps the terminal event terminal: a renderer draining the stream has seen the
// whole recording, `run-end` included, by the time the run says it is over.
// Nothing records after [trace.Recorder.RunEnd] — the recorder refuses to, and
// every worker has joined long before — so closing the queue here cannot race a
// send.
func (s *session) drainPublished() {
	if s.published == nil {
		return
	}
	close(s.published.queue)
	<-s.published.done
	s.published = nil
}

// exitCodeOf is the status the process is about to exit with, as far as the
// engine can state it.
//
// A completed run's code is the verdict internal/mutation reached over the
// published document, which is the number internal/cli returns. The other two
// are the engine's own: a cancelled run exits on its signal, and the engine does
// not know which one, so it records the interruption's own code rather than the
// zero a policy verdict that was never computed would carry.
func exitCodeOf(out RunOutcome) int {
	switch out.Status {
	case StatusOK:
		return int(out.Verdict.Code)
	case StatusInterrupted:
		return int(mutation.ExitInterrupted)
	default:
		return int(mutation.ExitInfrastructure)
	}
}

// A state is what the mutation phases have established so far.
//
// It exists because the report is published from two places — the end of a
// successful run, and the interruption path — and both need the same partial
// picture. Threading a dozen values through would make the difference between
// them a matter of remembering which ones; a struct makes it one call.
type state struct {
	found   discover.Result
	catalog *mutation.Catalog
	mode    report.SelectionMode
	// changed is the changed-line set a `--changed` run narrowed itself by, or
	// nil. It is resolved before anything is copied or built, so that a bad ref
	// costs a second rather than a baseline.
	changed *gitdiff.Changed
	// shard is which shard of a split run this is, or nil.
	shard      *report.Shard
	selected   int
	rejections []report.Rejection
	// notRun records why each accepted mutant the selection left out was not
	// executed. Everything accepted and absent from here was selected, so a
	// mutant with no result and no entry is one the run did not reach —
	// [report.NotRunInterrupted] — which is what makes the reason total without
	// a fourth value for "we forgot".
	notRun map[string]report.NotRunReason
	// results holds one execution result per mutant the run reached, by full
	// id. Everything catalogued, accepted, and absent from here is reported as
	// not-run, which is the contract report.Build enforces.
	//
	// A mutant no test binary covers is in here too, as a survivor that was
	// never executed: coverage established its outcome without running it, and
	// leaving it out would report it as not-run and quietly take it out of the
	// score's denominator.
	results map[string]report.MutantResult
	// display holds the render data for every catalogued mutant, by full id.
	display map[string]MutantResult
	// packages holds the import path of the package each catalogued mutant's
	// file belongs to, by full id. It is what an execution's account is joined
	// on, and it is absent for a mutant discovery could not place — which
	// [displayIndex] documents as impossible.
	packages map[string]string
	// coverage is what the coverage phase decided. The zero value is a run with
	// coverage off, which is what every path that never reached the phase — an
	// early failure, a custom test command, nothing to execute — leaves behind.
	coverage coverageResult
	// cache is what the outcome cache did. The zero value is a run with the
	// cache off; see [cacheState].
	cache cacheState
}

// pipeline is the run proper, split out so that [Run] owns exactly two things:
// the terminal event and the channel close, in that order.
func (s *session) pipeline(ctx context.Context, opts Options, out *RunOutcome) (err error) {
	cfg := opts.Config

	root, err := workspaceRoot(opts.WorkspaceRoot)
	if err != nil {
		return err
	}
	out.WorkspaceRoot = root

	// A workspace is refused here rather than three phases later, and the code
	// is internal/discover's rather than a second one of this package's: it is
	// the same condition, and two identifiers for one condition is one for a
	// user to search for in vain. See [discover.CheckWorkspace].
	//
	// What the earliness buys is the *diagnosis*. Discovery would refuse this
	// tree too, but only after the copy, the scope resolution and a full
	// baseline — and the scope resolution gets there first with a different
	// story, because `go list ./...` in a workspace directory places no
	// package and the run therefore blamed the user's test command for
	// matching nothing. Asked before anything is copied, the answer names the
	// user's own `go.work` rather than the snapshot's.
	if workspaceErr := discover.CheckWorkspace(root); workspaceErr != nil {
		return workspaceErr
	}

	command, err := testCommand(cfg, opts.TestArgv)
	if err != nil {
		return err
	}
	out.TestCommand = command
	out.Workers = cfg.Execution.Jobs

	// Resolved before the workspace is copied and the baseline is measured. A
	// ref that does not exist, a directory that is not a repository, and a
	// branch with no upstream are all mistakes about the invocation, and finding
	// one out after several minutes of building and testing would be a poor way
	// to learn it. It reads the user's own tree, which is the only place a
	// repository is: see internal/gitdiff.
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

	// `.git`, the build and module caches, and the report directory are
	// excluded by internal/snapshot unconditionally, so they are deliberately
	// not repeated here: one place decides what a snapshot never contains.
	//
	// `mutation.exclude` is deliberately *not* among them, and no future edit
	// should route it here. It selects which files are worth mutating, and the
	// snapshot is the whole workspace as the compiler sees it. Feeding a
	// selection setting into the copy breaks two things at once:
	//
	//   - A file that is not copied is not built and not tested, so the
	//     commonest exclude of all — `**/*_test.go` — deletes the test suite
	//     from the snapshot and the baseline passes by running nothing. That
	//     is precisely the flattering green [CodeBaselineTestFailed] exists to
	//     refuse, and the [CodeBaselineBuildFailed] gate does not catch it
	//     either, because `go build ./...` never compiles a _test.go file.
	//   - [snapshot.Snapshot.WorkspaceDigest] is taken over the manifest, and
	//     it is what makes an outcome cache trustworthy and what proves two
	//     shards read the same code. A digest that moves when a pure selection
	//     setting changes is a cache-poisoning and shard-congruence bug.
	//
	// [snapshot.Options.Exclude] stays available as the escape hatch for a
	// symlink or junction the copy refuses, but it has to come from a
	// snapshot-scoped setting when one exists — never from a selection one.
	// Before the copy, so that a machine holding the leftovers of a run that
	// was killed has the disk back before this one asks for a module-sized
	// piece of it. A directory another run is using holds its own lock and is
	// left alone; see internal/tempowner.
	//
	// The parent is the run's own, so a caller that named one is swept there
	// and nowhere else. The scratch directory below needs no such argument: it
	// is created beside the snapshot, which is already inside it.
	tempParent := temporaryParent(opts.TempDirectory)
	endSweep := s.stage("sweep", tempParent)
	s.sweepTemporary(tempParent)
	endSweep(nil)

	// The one place the run's own temporary directories are settled, registered
	// before either of them exists so that no path out of the pipeline can miss
	// it. It replaced two independent deferred cleanups, and the merge is what
	// makes [Options.KeepTemp] expressible: keeping is a decision about the
	// *run*, and neither of those defers could see whether the run had failed.
	// A zero field is a directory that was never made; see [session.release].
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
	// Recorded only once the claim succeeded: a directory nobody owns cannot be
	// marked kept, and the failure above has already removed it.
	temps.scratch, temps.scratchOwner = scratch, scratchOwner
	env := childEnv(scratch)

	// The test command's own scope, proven before a single command is measured.
	// A pattern that names nothing is a mistake in the invocation, exactly like
	// the `--changed` ref resolved above, and finding it out after a build, three
	// timed test runs, discovery and an instrumentation pass would be several
	// minutes spent to report a typo. Nothing is resolved for an unrecognised
	// command: go-mutants has not read it as a scope and has no patterns to
	// check.
	patterns, scoped := testScope(out.TestCommand)
	if scoped {
		endScope := s.stage("scope", strings.Join(patterns, " "))
		err := s.resolveTestScope(ctx, toolchain, snap.Root, env, patterns)
		endScope(err)
		if err != nil {
			return err
		}
	}

	if err := s.baseline(ctx, cfg, command, toolchain, snap.Root, env, out); err != nil {
		return err
	}

	// The selection is described before it is made, so that a run interrupted
	// half way through still files a document saying how it had narrowed itself.
	// A partial report claiming to have run everything would be the one claim
	// nobody could check.
	st := &state{
		mode:    selectionMode(opts),
		changed: changed,
		shard:   shardOf(opts),
		results: make(map[string]report.MutantResult),
		display: make(map[string]MutantResult),
		notRun:  make(map[string]report.NotRunReason),
	}
	mutateErr := s.mutate(ctx, opts, toolchain, snap, scratch, env, out, st)
	if mutateErr != nil {
		// An interruption after the catalogue exists still has something true
		// to say: which mutants there were, which of them were measured, and
		// which the signal cut short. Anything earlier has nothing to publish,
		// and inventing an empty report for it would file a document claiming
		// the workspace holds no mutants.
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

// baseline builds the pristine snapshot, measures the unmutated tests, and
// derives the per-mutant timeout from what it measured.
func (s *session) baseline(
	ctx context.Context,
	cfg config.Config,
	command []string,
	toolchain gocmd.Toolchain,
	root string,
	env []string,
	out *RunOutcome,
) error {
	runs := cfg.Test.BaselineRuns
	s.enterPhase(PhaseBaseline, fmt.Sprintf("building the snapshot, then %s of %s",
		countNoun(runs, "timed run"), strings.Join(command, " ")))

	build := toolchain.Command("build", "./...")
	build.Dir = root
	build.Env = env
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

	// The program is resolved to the toolchain that was just located and
	// reported. A bare `go` would otherwise be resolved through the child's
	// PATH, which need not be — and under a toolchain manager usually is not —
	// the same `go` the run says it is using.
	argv := resolveProgram(command, toolchain)
	out.ResolvedTestCommand = slices.Clone(argv)
	durations := make([]time.Duration, 0, runs)
	// The baseline runs unbounded, which is the one thing the memory bound
	// cannot be applied to: it is the measurement the bound is derived from,
	// and a measurement taken under the budget it produces would be a budget
	// derived from itself.
	var peak int64
	for i := 1; i <= runs; i++ {
		spec := runner.Spec{
			Argv:    argv,
			Dir:     root,
			Env:     env,
			Timeout: BaselineCap,
			Trace:   s.trace,
			Kind:    trace.ExecKindBaselineTest,
		}
		// One stage per observation rather than one for the set: a suite that
		// got slower between the first run and the third is a fact about the
		// machine the run is on, and an average would hide it.
		endRun := s.stage("test", fmt.Sprintf("run %d/%d", i, runs))
		result := runner.Run(ctx, spec)
		runErr := check(ctx, spec, result, CodeBaselineTestFailed,
			fmt.Sprintf("baseline run %d of %d failed", i, runs))
		endRun(runErr)
		if runErr != nil {
			return runErr
		}
		durations = append(durations, result.Duration)
		// The largest of the runs rather than the mean, for the reason the
		// timeout takes the slowest: a budget sized on an average is a budget
		// half the observations already exceed.
		peak = max(peak, result.PeakMemory)
		s.emit(BaselineProgress{Run: i, Of: runs, Duration: result.Duration})
	}
	out.BaselineRuns = durations
	out.AverageBaseline = mean(durations)
	out.SlowestBaseline = slices.Max(durations)

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

	// The memory bound is derived from the same runs and published beside the
	// timeout, so that a reader sees one budget with two halves rather than two
	// unrelated numbers. It is a stage of its own for the same reason the
	// timeout is: it can decide there is no bound, and that decision is part of
	// the account of the run.
	endMemory := s.stage("memory", "")
	out.PeakBaseline = peak
	out.Memory, out.MemorySource = enforceableMemory(deriveMemory(cfg.Test.Memory, peak))
	endMemory(nil)
	s.emit(MemoryDerived{Limit: out.Memory, Source: out.MemorySource, Peak: peak})
	// Said once, and only for the one case a user can act on; see
	// [unenforcedMemoryReason].
	if reason, warn := unenforcedMemoryReason(out.Memory, out.MemorySource, runner.MemoryBoundSupported()); warn {
		s.warn(CodeMemoryBoundUnavailable, reason)
	}
	return nil
}

// unenforcedMemoryReason is the one memory situation worth a warning, and
// whether this run is in it.
//
// A warning is something a user can act on, and exactly one of the three ways a
// run can end up unbounded is: they wrote a `test.memory`, they believe it is
// holding, and this platform will not hold anybody to it. One line, once, and
// they can decide whether they mind.
//
// The other two are silent on purpose. A *derived* bound that cannot be
// enforced was never asked for, and a run without one is exactly the run
// go-mutants made before the bound existed — so warning about it would put a
// line on every clean macOS run forever, which is how a warning stops being
// read and takes the ones that matter with it. A run that measured no peak at
// all is the same story with a different cause. Both say what happened where a
// fact belongs rather than where an action belongs: [MemorySourceUnavailable]
// in the event and in the report, and one word on the `-v` line.
//
// The number is deliberately not repeated in the sentence. It is in the report
// the user is about to read and on the `-v` line beside the timeout, and this
// package cannot render bytes for a person without importing the renderer that
// imports it.
// The platform question is a parameter rather than a call, so that the rule
// itself is the same three lines on every machine and the table that pins it
// does not have to be three tables.
func unenforcedMemoryReason(limit int64, source MemorySource, enforced bool) (string, bool) {
	if source != MemorySourceExplicit || limit <= 0 || enforced {
		return "", false
	}
	return "the memory bound in test.memory is recorded but not enforced: this platform can report " +
		"what a process cost once it is gone but cannot watch one while it runs, so a runaway mutant " +
		"is stopped by its timeout alone", true
}

// mutate is everything between a proven baseline and a report: discovery, the
// catalogue, validation, the semantic preservation gate, the drift gate, the
// test binaries, and the execution.
//
// It fills in st as it goes rather than returning a result, because a run that
// is cut short half way through still has to publish what it had established.
func (s *session) mutate(
	ctx context.Context,
	opts Options,
	toolchain gocmd.Toolchain,
	snap *snapshot.Snapshot,
	scratch string,
	env []string,
	out *RunOutcome,
	st *state,
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

	// The include and exclude patterns are applied here and never to the
	// snapshot walk; see the long argument at the snapshot above.
	endDiscover := s.stage("discover", "")
	found, err := discover.Discover(ctx, discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
		Rules:        rules,
		Include:      include,
		Exclude:      exclude,
	})
	endDiscover(err)
	if err != nil {
		return err
	}
	st.found = found
	s.emit(Discovered{Candidates: len(found.Candidates), Skips: skipTotal(found.Skips)})

	endCatalog := s.stage("catalog", "")
	catalog, err := discover.BuildCatalog(found)
	if err != nil {
		endCatalog(err)
		return err
	}
	st.catalog = catalog
	st.display, st.packages = displayIndex(catalog, found.Candidates)

	// The guard hints travel with the catalogue from here on. They are the one
	// thing instrumentation cannot work out for itself — which rewrite form an
	// edit takes is a question about types, and only this pass had a type
	// checker — so losing them between the two phases would not be a missing
	// optimisation, it would be a run that instruments nothing.
	hints, err := instrument.HintsOf(found.Candidates)
	endCatalog(err)
	if err != nil {
		return err
	}

	endValidate := s.stage("validate", countNoun(catalog.Len(), "mutant"))
	validated, err := validate.Validate(ctx, validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		Hints:        hints,
		ModulePath:   found.ModulePath,
		Toolchain:    toolchain,
		Jobs:         cfg.Execution.Jobs,
		BuildTimeout: BaselineCap,
		Env:          env,
		Trace:        s.trace,
	})
	endValidate(err)
	// The rejections and what the search cost are recorded whatever happened:
	// they are what the phase established, and a run that was interrupted half
	// way through should still report the candidates it had already condemned
	// and the compiles it had already spent.
	st.rejections = rejectionsOf(validated.Rejected)
	out.Validation = ValidationFacts{Builds: validated.Builds}
	if err != nil {
		return err
	}
	s.emit(Validated{Accepted: len(validated.AcceptedIDs), Rejected: len(validated.Rejected)})

	endInstrumented := s.stage("instrumented-baseline", "")
	err = s.instrumentedBaseline(ctx, out.TestCommand, toolchain, snap.Root, env)
	endInstrumented(err)
	if err != nil {
		return err
	}

	endDrift := s.stage("drift", "")
	err = driftGate(snap, validated.Instrumented)
	endDrift(err)
	if err != nil {
		return err
	}

	endSelection := s.stage("selection", "")
	runs, err := s.selection(opts, catalog, validated.AcceptedIDs, out.Timeout, out.Memory, st)
	endSelection(err)
	if err != nil {
		return err
	}

	execOpts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(scratch, binDirName),
		ScratchDir:   filepath.Join(scratch, workerDirName),
		Jobs:         cfg.Execution.Jobs,
		Timeout:      BaselineCap,
		// The coverage profiling runs start the same binaries the mutants are
		// measured against, so they are measured under the same budget; the
		// toolchain commands this bounds nothing for are documented on the
		// field itself.
		MemoryLimit: out.Memory,
		Trace:       s.trace,
	}
	// One reading of the test command decides both of the run's optimisations,
	// because both rest on the same fact: go-mutants can state in full what a
	// recognised command does. The scope says which packages get a test binary,
	// so a project that measures itself with `go test ./internal/...` no longer
	// compiles and runs the rest of the module against every mutant; and the
	// coverage mapping — from a test binary to the lines it reached — attributes
	// to binaries this run built and named. An unrecognised command gets neither
	// and loses nothing it had: every binary, every mutant, and a warning saying
	// so. See [testScope] for why recognition is spelling-strict.
	//
	// The patterns are read back off the command rather than carried down from
	// [session.pipeline], where they were resolved. It is a pure function of a
	// value the outcome already holds, and one call site owning the reading is
	// worth more than one saved.
	//
	// Both are decided before the build because both are build options: one set
	// of binaries serves the profiling pass and the mutant runs alike, and
	// building twice to save a few milliseconds per mutant would cost more than
	// it saved on every run.
	patterns, scoped := testScope(out.TestCommand)
	if scoped {
		execOpts.Packages = patterns
		execOpts.CoverPkg = found.ModulePath + coverPkgSuffix
	} else {
		s.warnCode(string(coverage.CodeCustomTestCommand), customTestCommand(out.TestCommand))
	}

	// The fallback's record goes straight into the run's coverage result, and it
	// can only be written before [session.coveragePhase] replaces that value
	// wholesale: a run that fell back has no CoverPkg left, so the phase below
	// is not entered and there is nothing to overwrite it. The outcome reads it
	// back off the same value afterwards.
	bins, err := s.buildTestBinaries(ctx, &execOpts, &st.coverage)
	if err != nil {
		return err
	}
	if err = scopedBinaries(patterns, len(bins)); err != nil {
		return err
	}

	if execOpts.CoverPkg != "" {
		runs, st.coverage, err = s.coveragePhase(ctx, execOpts, scratch, found.ModulePath, bins, runs, st)
		if err != nil {
			return err
		}
	}
	out.CoverageFallback = st.coverage.coverageFallback
	// Last of the narrowing stages and after coverage, which is the order the
	// correctness argument in cache.go depends on: an uncovered mutant is
	// settled before the cache is ever asked about it.
	endLookup := s.stage("cache-lookup", countNoun(len(runs), "mutant"))
	runs = s.cachePhase(opts, catalog.Digest(), out, runs, st)
	endLookup(nil)

	endExecute := s.stage("execute", countNoun(len(runs), "mutant"))
	results, err := execute.Schedule(ctx, execOpts, runs, bins, s.hooks(st, out.Memory))
	endExecute(err)
	// As with validation: whatever was measured is kept, because an interrupted
	// run's report is exactly the record of what it got to.
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
		}
	}
	// Written back before the error is returned, interruption included: a mutant
	// that settled before the signal arrived settled, and throwing its answer
	// away would make a run somebody cancelled halfway through cost full price
	// twice. See [session.storeOutcomes].
	endStore := s.stage("cache-store", countNoun(len(results), "result"))
	s.storeOutcomes(opts, results, st)
	endStore(nil)
	return err
}

// executionsOf carries one mutant's attempts into the report, which is where
// they stop being a count.
//
// The attempts were reduced to `len(result.Attempts)` here until this build,
// and the number was all a reader ever got: a mutant that took eleven seconds
// over two attempts said so, and which worker made them, which binaries each
// one started and what each one observed were in the recording or nowhere. They
// are the same facts internal/execute already has in hand, and carrying them is
// what lets somebody answer "how was this mutant actually run" from the file.
//
// A mutant the run reached and could not settle carries none. Its outcome is
// not-run — [session.publish] gives it the interrupted reason — and a not-run
// mutant with rows of evidence would be a document claiming a measurement in
// detail while its own outcome says none was made; the attempt count still
// reports the pass that really happened, which is the honest half. See
// [report.Build], which refuses the other combination rather than trusting this
// one function to be careful.
func executionsOf(result execute.MutantResult) []report.Execution {
	if result.Final == mutation.OutcomeNotRun {
		return nil
	}
	executions := make([]report.Execution, 0, len(result.Attempts))
	for i, attempt := range result.Attempts {
		outcome, err := report.OutcomeOf(attempt.Outcome)
		if err != nil {
			// Unreachable for an attempt internal/execute produced, and not
			// worth failing a run over. The whole list goes rather than the one
			// row: a list one row short of the attempt count is a contradiction
			// [report.Build] refuses, so dropping a single row would turn an
			// impossible outcome into a failed run at the very last step. No
			// list at all is the document saying nothing about how this mutant
			// was run, which is what every older build said about every mutant.
			// The outcome the score is computed from is result.Final, and it is
			// checked where it matters.
			return nil
		}
		if !outcome.Observed() {
			// A verdict where an observation belongs. Nothing internal/execute
			// produces reaches here — a pass sees a kill, a survival, a timeout
			// or a harness failure, and the two judgements are made above it —
			// and it goes the same way as an unrenderable one for the same
			// reason: the whole list, rather than a row short of the count.
			return nil
		}
		executions = append(executions, report.Execution{
			Attempt:    i + 1,
			Worker:     attempt.Worker,
			Outcome:    outcome,
			KilledBy:   attempt.KilledBy,
			DurationMS: attempt.Duration.Milliseconds(),
			Binaries:   slices.Clone(attempt.Binaries),
			// What the pass cost the machine, and — for the one outcome that
			// needs it — why a kill names a binary that reported no failure.
			MemoryExceeded:  attempt.MemoryExceeded,
			PeakMemoryBytes: attempt.PeakMemory,
		})
	}
	return executions
}

// buildTestBinaries compiles the test binaries, and falls back to a plain build
// if the coverage-instrumented one will not compile.
//
// The fallback exists because coverage is on by default and was never asked
// for. A `-cover -coverpkg=<module>/...` build reaches packages an ordinary
// `go test -c` of one package does not, so it can fail where the plain build
// would have succeeded — and [execute.CodeTestBuildFailed] is a hard failure
// whose own documentation reads it as a go-mutants bug in the instrumented
// rewrite. Turning a run that would have worked into a red one, for the sake of
// an optimisation the user never requested, is the wrong trade in every
// direction; so the run says what happened, gives up the optimisation, and
// builds again without it.
//
// The second build is only ever paid for on the failure path, and a failure
// there is worth one wasted build.
//
// What the warning says and what is kept are deliberately different lengths.
// The console gets one line, because the run is about to carry on and succeed;
// cov keeps the whole failure — the coded message and the compiler's own
// diagnostics — because those diagnostics are the only evidence that
// go-mutants' `-coverpkg` build is what broke, and dropping them left a user
// who wanted to know why coverage was given up with nothing to look at.
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
	// One line to the console because the run is about to succeed anyway, and
	// the whole failure to the recording: the compiler's own diagnostics are the
	// only evidence there is that go-mutants' `-coverpkg` build is what broke.
	s.unavailableInFull("the test binaries do not compile with coverage instrumentation ("+
		firstLine(err.Error())+")", cov.coverageFallback)
	opts.CoverPkg = ""
	// A second stage of the same name, because it is a second build: the
	// recording says the run compiled its binaries twice and says which of the
	// two was the plain one, which is the fact that explains the wasted minutes.
	endPlain := s.stage("build-binaries", buildDetail(opts.CoverPkg))
	bins, err = execute.BuildTestBinaries(ctx, *opts)
	endPlain(err)
	return bins, err
}

// buildDetail says which of the two test-binary builds a stage is.
func buildDetail(coverPkg string) string {
	if coverPkg == "" {
		return "plain"
	}
	return "coverage"
}

// fallbackText is one failure written out in full: its own text, and underneath
// it whatever the failing command printed.
//
// It is the shape a reader needs and the opposite of what a console wants,
// which is why it is a separate value from the warning rather than a longer
// warning.
func fallbackText(err error) string {
	text := err.Error()
	if output := execute.OutputOf(err); output != "" {
		text += "\n" + output
	}
	return text
}

// instrumentedBaseline is the semantic preservation gate.
//
// The whole test command is run once against the instrumented snapshot with
// nothing activated, which is what every guard's `else` branch is for: with
// [instrument.ActiveEnv] unset the tree holds the user's own bytes on every
// path taken. A failure here means the rewrite changed the program, so every
// outcome measured afterwards would describe that change rather than a mutant,
// and the run stops instead.
//
// The environment is the run's own composed one, which has already had every
// GO_MUTANTS_ variable stripped out of it — so this cannot accidentally be
// measuring a mutant a developer's shell activated. It gains one thing here and
// only here: `-vet=off`, merged into whatever GOFLAGS the run inherited.
//
// That is scoped to this tree on purpose. The snapshot this command runs
// against is generated code in which every mutant of an expression sits beside
// the original, so `s == "." && s == ".."` — the or-to-and mutant of
// `s == "." || s == ".."` — is a normal shape in it, and `go test` runs vet's
// `bools` analyzer by default and rejects exactly that. Vetting the user's
// pristine tree is their own CI's job and go-mutants does not take it away: the
// [session.baseline] run above measures the same command with vet at its
// default, so a real `bools` finding in their source still stops the run before
// anything is instrumented.
func (s *session) instrumentedBaseline(
	ctx context.Context,
	command []string,
	toolchain gocmd.Toolchain,
	root string,
	env []string,
) error {
	spec := runner.Spec{
		Argv:    resolveProgram(command, toolchain),
		Dir:     root,
		Env:     gocmd.AppendGoflags(env, gocmd.VetOff),
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

// driftGate proves that nothing but the instrumentation moved.
//
// Every worker shares one snapshot, so a test that writes into its own package
// directory — a golden file it "updates", a database it creates in testdata —
// corrupts the tree every later mutant is measured against, and the run's
// results quietly stop being reproducible. This turns that into a named list of
// files and an exit code.
//
// Exactly two kinds of drift are the run's own doing: the files validation left
// carrying guards, and the generated runtime package. A file whose every
// candidate was rejected is not among the first — internal/validate restored it
// to its pristine bytes, so it does not drift at all — and the test binaries are
// not among either, because internal/execute refuses a binary directory inside
// the snapshot for precisely this reason.
func driftGate(snap *snapshot.Snapshot, instrumented instrument.Result) error {
	unexpected, err := drift.Unexpected(snap, instrumented)
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
			"the tests write into the package directory they run in",
		Output: strings.Join(unexpected, "\n"),
	}
}

// selection turns the accepted set into the mutants this run will execute, and
// records what the report has to say about the choice.
//
// `--mutant` is a selector here and not a filter, which is the opposite of what
// it means to `list`: naming one mutant is how a user asks "why did this one
// survive", and answering with two of them would be answering a question nobody
// asked. Everything not selected is still catalogued and still reported, as
// not-run.
//
// A prefix that resolves to a mutant validation rejected selects nothing, and
// that path warns rather than passing quietly. `list` does not validate, so
// every id it printed is one this can be handed; without the warning the run
// executes nothing, exits 0, and says so only in a `rejected[]` row nobody who
// asked about one mutant is looking at. See [CodeSelectedMutantRejected].
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
			// st.rejections is already populated here — validation fills it
			// before this phase can be reached, and an error out of validation
			// returns before the selection is made — and the accepted and the
			// rejected together are the whole catalogue, so the diagnostic that
			// explains this one is there to quote.
			s.warn(CodeSelectedMutantRejected, rejectedSelection(opts.MutantPrefix, chosen, st))
		}
	}
	ids = s.narrowSelection(ids, st)

	runs := make([]execute.MutantRun, 0, len(ids))
	for _, id := range ids {
		run := execute.MutantRun{ID: id, Timeout: timeout, MemoryLimit: memoryLimit, Package: st.packages[id]}
		// The short form the console and the report already print, carried so
		// that the account of an attempt reads in the same identities. It is
		// looked up rather than derived: how much of an id is short enough to
		// be unique is the catalogue's own decision.
		//
		// The package is looked up for the same reason and comes from the same
		// join: the import path a mutant's file belongs to is what discovery's
		// type checker resolved, and deriving one here from the module path and
		// the directory would be a second answer to a question already answered
		// — and a wrong one for a nested module or a package whose directory
		// name it does not share.
		if m, ok := catalog.ByID(id); ok {
			run.DisplayID = m.DisplayID
		}
		runs = append(runs, run)
	}
	st.selected = len(runs)
	recordNotRun(acceptedIDs, runs, st)
	return runs, nil
}

// rejectedSelection is what [CodeSelectedMutantRejected] says: which mutant the
// prefix named, where it is, and what the compiler said about it.
//
// The compiler's own words are quoted rather than summarised. "It did not
// compile" is the one thing the user can already infer from the fact that
// nothing ran; which type mismatch, on which line, is the part that tells them
// whether the rejection is a limit of the guard forms or a mutant that could
// never have meant anything, and it costs nothing to carry it here — the
// diagnostic is already in hand.
func rejectedSelection(prefix string, chosen mutation.Mutant, st *state) string {
	var b strings.Builder
	b.WriteString("--mutant ")
	b.WriteString(strconv.Quote(prefix))
	b.WriteString(" selected ")
	// The display id, unless the user typed it out in full. `list` prints a
	// short form and a few characters of one is what usually arrives here, so
	// saying which catalogued mutant those characters landed on is the point;
	// echoing back a string the user has just written is not.
	if prefix == chosen.DisplayID {
		b.WriteString("the mutant")
	} else {
		b.WriteString(chosen.DisplayID)
	}
	// Coordinates when there are any. displayIndex documents a catalogued
	// mutant with no candidate behind it as impossible and leaves it at zero
	// rather than raising, so this reads what it finds instead of printing a
	// ":0:0" that would look like a real location.
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

// diagnosticFor returns what validation said about one rejected mutant, or ""
// when the id is not among the rejections. The scan is linear because it
// happens at most once per run, and a map built for one lookup would be a data
// structure nobody reads twice.
func diagnosticFor(rejections []report.Rejection, id string) string {
	for _, rejection := range rejections {
		if rejection.ID == id {
			return rejection.Diagnostic
		}
	}
	return ""
}

// foldLines collapses a multi-line compiler diagnostic onto the single line a
// warning has to be. It is not [firstLine], which keeps only the first: nothing
// here is dropped.
//
// A rejection can carry several messages about the same guard, joined with
// newlines by internal/validate. A warning is one line by contract — the plain
// renderer writes "warning GOM4043: " in front of it, and the report stores it
// as one string — so the lines are joined with "; " rather than dropped: the
// second message is often the one that names the type the guard could not be.
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

// hooks forwards internal/execute's progress callbacks into the event stream.
//
// They are called from several worker goroutines at once, so they do nothing
// but send: a channel send is safe from any goroutine, and the warning slice
// [session.warn] appends to is not. The blocking send is what applies
// back-pressure to the workers, which is the documented contract on both sides.
func (s *session) hooks(st *state, memoryLimit int64) execute.Hooks {
	return execute.Hooks{
		Started: func(id string, worker int) {
			shown := st.display[id]
			s.emit(MutantStarted{
				ID:        id,
				DisplayID: shown.DisplayID,
				Path:      shown.Path,
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
			// The worst moment across every pass, and whether any of them was
			// stopped for it. Both are read off the attempts rather than kept
			// beside them, so a mutant retried serially reports the peak of the
			// two passes and not of whichever one happened to be last.
			shown.MemoryLimit = memoryLimit
			shown.PeakMemory, shown.MemoryExceeded = 0, false
			for _, attempt := range result.Attempts {
				shown.PeakMemory = max(shown.PeakMemory, attempt.PeakMemory)
				shown.MemoryExceeded = shown.MemoryExceeded || attempt.MemoryExceeded
			}
			s.emit(MutantFinished{Result: shown.clone()})
		},
	}
}

// publish builds the run report, files it in the history, and composes the
// closing summary from it.
//
// Everything the summary states is read back out of the document rather than
// counted beside it. That is the point: the exit code a user's CI branches on,
// the score the console prints, and the numbers in the file are then the same
// numbers by construction, and there is no second implementation of "what
// counts as a detection" to drift.
//
// One class of warning is deliberately not in the document. The snapshot and
// scratch cleanups run in deferred functions, which is to say after this, so
// [CodeSnapshotNotRemoved] and [CodeScratchNotRemoved] reach the event stream
// and [RunOutcome.Warnings] but never the filed report. Publishing the report
// last instead would mean deleting the tree before the run had a record of what
// it found, which is the worse trade: both warnings are about a directory left
// in the temporary area, and neither says anything about the mutants.
func (s *session) publish(opts Options, out *RunOutcome, st *state, status report.Status) error {
	s.enterPhase(PhaseReport, "writing the run report")

	rejected := make(map[string]bool, len(st.rejections))
	for _, rejection := range st.rejections {
		rejected[rejection.ID] = true
	}
	// One result per catalogued mutant that validation did not refuse,
	// including an explicit not-run for everything the run did not reach. That
	// is report.Build's contract, and it is what keeps a forgotten mutant from
	// silently leaving the score's denominator.
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
			// Reached and not settled, which internal/execute produces for a
			// cancelled run — including the mutant that timed out once and was
			// interrupted before the serial retry. It was selected, so the
			// selection has nothing to say about it; what happened to it is the
			// interruption.
			result.NotRunReason = report.NotRunInterrupted
		}
		results = append(results, result)
	}

	endBuild := s.stage("build", countNoun(len(results), "result"))
	finished := s.now()
	rep, err := report.Build(report.Options{
		// "unknown" rather than the empty string a caller that forgot would
		// pass. The document requires a non-empty version, and failing a whole
		// run at the very last step over a display field would throw away
		// everything it measured; an honest "unknown" says the same thing the
		// workspace block says when it does not know its own Go version.
		ToolVersion:      or(opts.ToolVersion, unknownValue),
		RunID:            out.RunID,
		Status:           status,
		Started:          out.Started,
		Finished:         finished,
		Config:           opts.Config,
		Mode:             st.mode,
		ChangedRef:       changedRef(st),
		Shard:            st.shard,
		Selected:         st.selected,
		ModulePath:       st.found.ModulePath,
		GoVersion:        goVersion(st.found.GoVersion, out.Toolchain.Version.Release),
		WorkspaceDigest:  out.WorkspaceDigest,
		Catalog:          st.catalog,
		Located:          st.found.Candidates,
		Skips:            st.found.Skips,
		Results:          results,
		Rejections:       st.rejections,
		TestCommand:      out.TestCommand,
		Baseline:         out.BaselineRuns,
		Timeout:          out.Timeout,
		TimeoutSource:    reportTimeoutSource(out.TimeoutSource),
		Memory:           out.Memory,
		MemorySource:     reportMemorySource(out.MemorySource),
		CoverageMode:     reportCoverageMode(st.coverage.Mode()),
		CoverageBinaries: st.coverage.binaries,
		// The same event the GOM7602 warning above reports, in the two forms a
		// document needs it: a flag a consumer can branch on and the whole
		// failure a person reads. See [RunOutcome.CoverageFallback].
		CoverageUnavailableReason: out.CoverageFallback,
		CoverageBuildFallback:     out.CoverageFallback != "",
		CacheMode:                 st.cache.Mode(),
		CacheMisses:               st.cache.misses,
		CacheWrites:               st.cache.writes,
		Warnings:                  reportWarnings(s.warnings),
		// What this run cost and what it ran with. The timing is taken here
		// rather than from [RunOutcome], which is only filled in after Run
		// returns: the report is written inside the run, so it carries the
		// spans that had closed by the time it was written — every phase but
		// the one it is in, and every stage but its own. See [report.Timing].
		Timing:     reportTiming(s.timing),
		Validation: reportValidation(out.Validation),
		Snapshot:   reportSnapshot(out.Snapshot),
		Toolchain: &report.ToolchainFacts{
			GoBin:   out.Toolchain.GoBin,
			Version: out.Toolchain.Version.String(),
		},
		ResolvedCommand: out.ResolvedTestCommand,
	})
	endBuild(err)
	if err != nil {
		return err
	}

	endHistory := s.stage("history", opts.HistoryRoot)
	runPath, latestPath, err := report.History{Root: opts.HistoryRoot}.Write(rep)
	endHistory(err)
	if err != nil {
		return err
	}
	out.Report = rep
	out.RunPath = runPath
	out.LatestPath = latestPath
	s.trace.Artifact(trace.ArtifactReportRun, runPath)
	s.trace.Artifact(trace.ArtifactReportLatest, latestPath)

	// The project artefacts come after the history and never before it. The
	// history is where the run's own record lives and is the thing a later run,
	// a `report merge`, or a `report latest` reads; `reports/mutation/` is a
	// convenience for humans and for CI, built out of the document that is
	// already safely filed. Publishing them the other way round would mean a
	// crash between the two left a workspace with a mutation report for a run
	// that has no record.
	//
	// The event is emitted whatever happens here, because both history paths are
	// already real and a run that wrote a report without saying where would be
	// the worse failure. The artefact failure is then returned and stops the
	// run: a `--report json,html` that quietly produced neither file, exited 0,
	// and left last week's pair in place is exactly the kind of green this
	// project keeps refusing to print.
	endArtifacts := s.stage("artifacts", opts.Config.Report.Directory)
	artifacts, artifactErr := report.WriteArtifacts(report.ArtifactOptions{
		Report:        rep,
		WorkspaceRoot: out.WorkspaceRoot,
		Directory:     opts.Config.Report.Directory,
		Formats:       opts.Config.Report.Formats,
		High:          opts.Config.Report.High,
		Low:           opts.Config.Report.Low,
	})
	endArtifacts(artifactErr)
	out.Artifacts = artifacts
	// Only a path that is really there is recorded, which is the same rule
	// [ReportPublished] follows: `--report none` writes neither file and the
	// failure path writes neither, and an artifact event naming a file nothing
	// wrote would be the one line of a recording a reader could not act on.
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

	tally, err := rep.Tally()
	if err != nil {
		return err
	}
	out.Verdict = mutation.Decide(tally, opts.Config.Policy, mutation.Signals{
		ExpectationFailure: rep.ExpectationFailure(),
	})

	summary := s.compose(out, st, tally, rep)
	s.summary = &summary
	return nil
}

// reportTiming, reportValidation and reportSnapshot render three run facts, or
// nothing at all when the run never established one.
//
// Nothing at all is the point. [report.Options] takes a nil for every one of
// them and means "this run did not measure it", and a run that stopped early
// really did not: an interruption between cataloguing and the first `go build`
// publishes a report — the catalogue is worth having — and a `validation:
// {builds: 0}` in it would be that run claiming it established what compiles
// without compiling anything. Zero is the discriminator in each case because
// zero is not a measurement any of them can produce: validation is at least one
// build, a snapshot is at least one file, and a run that reached the report has
// closed at least one phase.
func reportTiming(timing Timing) *report.Timing {
	if len(timing.Phases) == 0 && len(timing.Stages) == 0 {
		return nil
	}
	return &report.Timing{Phases: reportPhases(timing), Stages: reportStages(timing)}
}

// reportValidation renders what proving the catalogue compiles cost this run.
func reportValidation(facts ValidationFacts) *report.Validation {
	if facts.Builds == 0 {
		return nil
	}
	return &report.Validation{Builds: facts.Builds}
}

// reportSnapshot renders what the copy of the workspace turned out to be.
func reportSnapshot(facts SnapshotFacts) *report.SnapshotFacts {
	if facts.Files == 0 {
		return nil
	}
	return &report.SnapshotFacts{StableDir: facts.StableDir, Files: facts.Files}
}

// reportPhases renders the phases the run has finished, in the order they
// closed.
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

// reportStages renders the stages the run has finished, in the order they
// closed.
//
// The result is the recording's own word for what became of the stage, carried
// through rather than re-derived: [session.stage] decided it from the step's
// error, and a second opinion here is how a report and a trace start
// disagreeing about a run they both watched.
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

// compose assembles the closing summary block.
func (s *session) compose(out *RunOutcome, st *state, tally mutation.Tally, rep *report.Report) RunSummary {
	summary := RunSummary{
		RunID:    out.RunID,
		ExitCode: out.Verdict.Code,
		Notable:  notable(st, rep),
		Counts: Counts{
			Total:        tally.Total(),
			Killed:       tally.Killed,
			Survived:     tally.Survived(),
			TimedOut:     tally.TimedOut,
			Inconclusive: tally.Inconclusive,
			Errored:      tally.Errored,
			NotRun:       tally.NotRun,
			Rejected:     len(rep.Rejected),
			// Read out of the document rather than counted beside it, exactly
			// as every other number in this block is.
			Uncovered: uncoveredOf(rep),
			Cached:    rep.Cache.Hits,
		},
		Coverage: st.coverage.Mode(),
		Cache:    cacheMode(rep.Cache.Mode),
		Score:    mutation.ScoreOf(tally),
		Warnings: len(s.warnings),
		Skips:    skipCounts(st.found.Skips),
	}
	if len(out.Verdict.Failures) > 0 {
		summary.Failure = out.Verdict.Failures[0]
	}
	for _, expectation := range rep.Expectations {
		switch expectation.State {
		case report.StateFulfilled:
			summary.Expectations.Fulfilled++
		case report.StateStale:
			summary.Expectations.Stale++
		default:
			summary.Expectations.Unfulfilled++
		}
	}
	return summary.clone()
}

// notableRank orders the outcomes a summary lists, worst first. Killed and
// not-run mutants are absent on purpose: a summary that listed every kill would
// bury the handful of lines that need acting on.
var notableRank = map[mutation.Outcome]int{
	mutation.OutcomeSurvived:     0,
	mutation.OutcomeTimedOut:     1,
	mutation.OutcomeInconclusive: 2,
	mutation.OutcomeErrored:      3,
}

// notable returns the mutants worth looking at, worst first.
//
// The order inside a group is (path, line, column, rule, id), which is a total
// order and not merely a tidy one: two rules can propose an edit on the same
// line, and a summary block that changed shape between two runs of one
// workspace would not be diffable.
//
// Survivors are split once more before that, covered ones first. Both are
// survivors and neither outranks the other as a finding, but they call for
// different work — sharpen an existing test, or write one for a line nothing
// runs — and a reader scanning the block gets the two kinds in two runs rather
// than interleaved. It is a sub-order within one rank rather than a rank of its
// own, so a covered survivor still comes before every timeout.
func notable(st *state, rep *report.Report) []MutantResult {
	out := make([]MutantResult, 0, len(rep.Mutants))
	for _, m := range rep.Mutants {
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
		// Read back out of the published document rather than off the run
		// beside it, exactly as the counts in the closing block are: the three
		// facts a `-v` result line adds are then the same three the report
		// states, by construction.
		if m.KilledBy != nil {
			shown.KilledBy = *m.KilledBy
		}
		shown.Attempts = m.Attempts
		shown.CoveringTestPackages = slices.Clone(m.CoveringTestPackages)
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

// boolRank orders false before true, so that a sort key can be written the same
// way as every other one in [notable].
func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// uncoveredOf counts the mutants the run reported as uncovered, read out of the
// published document rather than counted beside it — the same discipline every
// other number in the closing summary follows.
func uncoveredOf(rep *report.Report) int {
	count := 0
	for _, m := range rep.Mutants {
		if m.Uncovered {
			count++
		}
	}
	return count
}

// displayIndex joins the catalogue to what discovery found out about each
// mutant, so that an event can name one without anybody downstream holding both.
//
// It returns two indexes off one join rather than two, because the join is the
// expensive half and both readings are of the same row. The first is the render
// data a renderer needs; the second is each mutant's package, which the account
// of an execution carries so that a consumer joining a mutation run to a test
// run has an import path to join on. The package comes from discovery's own type
// checker and is deliberately not derived from the module path and the
// directory, which agree with it for the ordinary module and not for a nested
// one.
//
// A catalogued mutant with no candidate behind it is impossible — the catalogue
// is built from the candidates — and is left with zero coordinates and no
// package rather than reported, because this is display data: a report that has
// lost a mutant is report.Build's failure to raise, and raising it twice would
// stop a run over a line number.
func displayIndex(
	catalog *mutation.Catalog,
	candidates []discover.Located,
) (display map[string]MutantResult, packages map[string]string) {
	type key struct {
		path string
		span mutation.Span
		rule string
	}
	located := make(map[key]discover.Located, len(candidates))
	for _, candidate := range candidates {
		k := key{path: candidate.Path, span: candidate.Span, rule: candidate.Rule.Name}
		if _, seen := located[k]; !seen {
			located[k] = candidate
		}
	}

	display = make(map[string]MutantResult, catalog.Len())
	packages = make(map[string]string, catalog.Len())
	for _, m := range catalog.Mutants() {
		where := located[key{path: m.Path, span: m.Span, rule: m.Rule.Name}]
		display[m.ID] = MutantResult{
			ID:          m.ID,
			DisplayID:   m.DisplayID,
			Path:        m.Path,
			Line:        where.Line,
			Column:      where.Column,
			Rule:        m.Rule.Name,
			Original:    m.Original,
			Replacement: m.Replacement,
		}
		if where.Package != "" {
			packages[m.ID] = where.Package
		}
	}
	return display, packages
}

// changedRef is the ref a `--changed` run recorded, or "" for every other run.
//
// It is read back off the resolved diff rather than off the options, so that a
// bare `--changed` records the upstream branch's own name — `origin/main` — and
// not the `@{upstream}` notation that found it. A report should say what was
// compared, not how it was looked up.
func changedRef(st *state) string {
	if st.changed == nil {
		return ""
	}
	return st.changed.Ref
}

// rejectionsOf reduces validation's rejections to what the report keeps. The
// coordinates and the rule are recovered from the catalogue by report.Build, so
// carrying them twice would be two chances to disagree.
func rejectionsOf(rejected []validate.Rejection) []report.Rejection {
	out := make([]report.Rejection, 0, len(rejected))
	for _, rejection := range rejected {
		out = append(out, report.Rejection{ID: rejection.ID, Diagnostic: rejection.Diagnostic})
	}
	return out
}

// reportWarnings renders the run's warnings in the report's vocabulary,
// keeping publication order.
func reportWarnings(warnings []Warning) []report.Warning {
	out := make([]report.Warning, 0, len(warnings))
	for _, warning := range warnings {
		out = append(out, report.Warning{Code: warning.Code, Message: warning.Message})
	}
	return out
}

// reportMemorySource maps this package's spelling onto the document's, for the
// reason [reportTimeoutSource] exists: an event stream is not a published
// format, and one enum serving both would make a rename of a console label a
// breaking change to somebody's jq expression.
func reportMemorySource(source MemorySource) report.MemorySource {
	switch source {
	case MemorySourceExplicit:
		return report.MemoryExplicit
	case MemorySourceDerived:
		return report.MemoryDerived
	default:
		return report.MemoryUnavailable
	}
}

// reportTimeoutSource maps this package's spelling onto the document's. The two
// enums are deliberately separate types — the report is a published format and
// the event stream is not — and this is the one place they meet.
func reportTimeoutSource(source TimeoutSource) report.TimeoutSource {
	if source == TimeoutExplicit {
		return report.TimeoutExplicit
	}
	return report.TimeoutDerived
}

// skipTotal is how many candidate sites discovery suppressed in all.
func skipTotal(skips []discover.Skip) int {
	total := 0
	for _, skip := range skips {
		total += skip.Count
	}
	return total
}

// skipCounts aggregates the per-file skips into per-reason totals, sorted by
// reason.
//
// Per-file rows are what the report carries, because that is where a user goes
// to look; per-reason totals are what a summary shows, because a file at a time
// turns "this tree has four constant expressions in it" into forty lines nobody
// reads.
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

// unknownValue is what a report field says when the run genuinely does not
// know it. It is the same word internal/report uses for the same question, and
// it is preferred to an empty string, which would read as a fact rather than as
// an absence.
const unknownValue = "unknown"

// or returns value, or fallback when value is empty.
func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// goVersion picks what the report says the workspace's Go version is.
//
// The module's own `go` directive is the answer whenever there is one: it is
// what decides the language semantics the sources are read with. A module old
// enough to declare none falls back to the toolchain that loaded it, which is
// the next most honest statement available, and report.Build fills in "unknown"
// when even that is missing.
func goVersion(module, toolchain string) string {
	if module != "" {
		return module
	}
	return toolchain
}

// A session owns the event channel for the length of one run.
type session struct {
	events   chan<- Event
	warnings []Warning
	closed   bool
	// published is the fan-out of the recording onto this stream, or nil for a
	// run that did not ask for one. See [eventSink].
	published *eventSink
	// trace is where this run is recorded, or nil for a run nobody asked for a
	// recording of. Every call site records unconditionally: the nil recorder is
	// safe on every method, which is what keeps the traced and the untraced path
	// one path.
	trace *trace.Recorder
	// clock is the run's time source, or nil for [time.Now]. It is read through
	// [session.now] so that a session a test built by hand still tells the time.
	clock func() time.Time
	// timing is what the phases and stages cost, in the order they closed.
	timing Timing
	// openPhase, phaseEnd and phaseStarted are the phase the run is in. phaseEnd
	// is nil exactly when no phase is open, which is what makes [session.closePhase]
	// idempotent and therefore safe to call from a defer and from an early
	// return both, and it returns the span the recorder measured — which is the
	// span the report publishes. phaseStarted is the fallback for an untraced
	// run, whose closer measures nothing.
	openPhase    Phase
	phaseEnd     func() time.Duration
	phaseStarted time.Time
	// summary is the closing block, set by publish and read by Run. It is
	// written from the run's own goroutine and read from it, after every worker
	// has joined.
	summary *RunSummary
	// cache is the outcome store this run reads and writes, or nil when the
	// cache is off. It is opened and used from the run's own goroutine only: the
	// lookups happen before the workers start and the write-back after they have
	// joined, which is what keeps the whole stage free of locks.
	cache *cache.Cache
	// cacheCorruptWarned and cacheWriteWarned hold the once-per-run warnings.
	// One unreadable entry and one unwritable directory usually mean every other
	// one is too, and a warning per mutant would bury the run's findings.
	cacheCorruptWarned bool
	cacheWriteWarned   bool
}

// emit publishes one event. A nil channel is the documented "publish nothing"
// case; the send is otherwise blocking, which is what makes the caller's
// obligation to drain a real one rather than a suggestion.
func (s *session) emit(e Event) {
	if s.events == nil {
		return
	}
	s.events <- e
}

// now is the run's clock. A session with none — which is every session one of
// this package's own tests builds by hand — reads the wall clock.
func (s *session) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

// enterPhase closes the phase the run was in and opens the next one.
//
// It is the only place a phase is announced, which is what keeps the two
// statements about a phase — the engine's [PhaseChanged] and the recording's
// `phase-start` — from being made in two places and drifting apart. The
// previous phase is closed first, so that a reader of either stream sees a
// phase end before the next one begins rather than two open at once.
func (s *session) enterPhase(phase Phase, detail string) {
	s.closePhase()
	s.emit(PhaseChanged{Phase: phase, Detail: detail})
	s.openPhase = phase
	s.phaseStarted = s.now()
	s.phaseEnd = s.trace.PhaseStart(string(phase))
}

// closePhase ends the open phase, if there is one, and times it.
//
// It is idempotent: a phase closed by the next one and again by [Run] on the way
// out is one span. That is what lets the last phase of a run be closed on every
// path without any path having to know whether something else closed it first.
func (s *session) closePhase() {
	if s.phaseEnd == nil {
		return
	}
	// The recorder's own measurement, not a second one. The report publishes
	// this span and so does the recording, and two readings of one clock taken
	// a function call apart are not the same number often enough to be relied
	// on: see [trace.Recorder.PhaseStart]. An untraced run has no recorder to
	// borrow from and times itself.
	duration := s.phaseEnd()
	s.phaseEnd = nil
	if s.trace == nil {
		duration = s.now().Sub(s.phaseStarted)
	}
	s.timing.Phases = append(s.timing.Phases, PhaseDuration{Phase: s.openPhase, Duration: duration})
	s.emit(PhaseCompleted{Phase: s.openPhase, Duration: duration})
}

// stage opens one recorded step inside the open phase and returns the closer
// that finishes it with what became of it.
//
// The closer takes the step's own error rather than a result word, so that no
// call site can report a failed step as a successful one by writing the wrong
// string: whether a step succeeded is exactly whether it returned an error, and
// that is the one thing every caller already has in hand.
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
		// The recorder's measurement, for the reason [session.closePhase] takes
		// the phase's from it.
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

// sweepTemporary collects the snapshot and scratch directories of runs that
// died before they could remove their own.
//
// It warns rather than failing. A run that cannot tidy up after a previous one
// is still a run whose measurements are sound, and turning somebody else's
// leftover permission problem into this run's exit code would stop the work to
// report the housekeeping.
//
// What it collected stays out of the report and goes into the recording, which
// is the difference between the two documents. It is a fact about the machine
// rather than about this workspace, so a report carrying it would invite a
// reader to compare two runs by how much rubbish each of them found; but a run
// that paused to delete four gigabytes has an explanation for the pause, and the
// recording is where a run explains itself.
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

// recordSnapshot files what the copy of the workspace turned out to be.
//
// A failure is recorded too, and with the same event: "the tree could not be
// frozen" is a fact about the snapshot, and a phase whose only account is a
// missing event is one a reader has to guess about.
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

// warn records a warning and publishes it. Warnings are kept as well as sent so
// that [RunOutcome] carries them into the report, where a renderer that was not
// listening cannot lose them.
//
// It is called from the run's own goroutine only. The execution workers publish
// through [session.hooks], which does nothing but send.
func (s *session) warn(code Code, message string) {
	s.warnCode(string(code), message)
}

// warnCode is [session.warn] for a code from another package's block.
//
// The coverage warnings are the reason it exists. GOM7601 and GOM7602 name
// conditions about coverage, so internal/coverage defines them next to the
// rules they are about; publishing them through a [Code] conversion would put
// two identifiers on one condition and put a GOM76xx value in a type documented
// to hold GOM40xx ones. The event and the report carry a string either way.
func (s *session) warnCode(code, message string) {
	s.warnDetail(code, message, "")
}

// warnDetail is [session.warnCode] for the warning that has more to say than
// one line, and knows it at the moment it says it.
//
// The detail is carried on the event beside the message rather than only in the
// recording, so that the console showing it puts it under the line it explains;
// see [Warning.Detail]. The recording's own note stays the one-line message,
// because the recording already holds the whole reason in the note the caller
// wrote for it — `note{coverage-unavailable}` — and a second copy under
// `note{warning}` would be the same paragraph recorded twice.
func (s *session) warnDetail(code, message, detail string) {
	w := Warning{Code: code, Message: message, Detail: detail}
	s.warnings = append(s.warnings, w)
	// Into the recording as well as onto the stream, because a recording is
	// meant to be the whole account of a run: a warning a user scrolled past on
	// a console is exactly the line somebody reading the trace afterwards is
	// looking for.
	s.trace.Note(trace.NoteWarning, code, message)
	s.emit(w)
}

// close closes the event channel exactly once. The idempotence is not
// defensive tidiness: it is what lets the deferred close sit at the top of Run
// without any path having to reason about whether something else closed first.
func (s *session) close() {
	if s.events == nil || s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

// check turns one [runner.Result] into an error, or nil if the command
// succeeded.
//
// The order of the cases is the contract. A cancelled run comes back from the
// runner as exit code [runner.ExitCodeUnavailable] with no error and no
// timeout, which is indistinguishable from a failure unless the context is
// asked first — so it is asked before the exit status is judged, and after the
// two conditions that are definitely not cancellations.
//
// The spec is taken so that every failure can name the command it judged, and
// it is attached in all four branches — the interruption included. The snapshot
// these commands run in is deleted as the run unwinds, so a failure that did
// not say what it started is one nobody can reproduce afterwards; and what was
// still running is the one thing a reader of a Ctrl-C wants to know.
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
		// Before the interruption below, and reported as a failure rather than
		// as one: the run did not finish inside the time it was given, and that
		// is something to diagnose rather than something somebody decided. See
		// [Interrupted].
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

// interrupted reports whether err is the run being stopped rather than the run
// going wrong.
//
// The rule is [context.Canceled] in the chain and nothing else. A cancellation
// is somebody's decision — a Ctrl-C, a dashboard's quit key, an embedder calling
// cancel — taken at the moment it happened and needing no diagnosis; a deadline
// expiring is the run failing to finish in the time it was given, which is
// exactly the failure a kept tree and a diagnostics bundle exist for.
//
// The cause is asked for rather than the code, and that is what makes the answer
// one answer. This package raises [CodeInterrupted] when it notices the context
// itself, but the same context reaches internal/gocmd, internal/validate and
// internal/execute too, and each reports it under a code of its own — so a
// predicate that matched on [CodeInterrupted] gave a deadline two different
// answers depending on which command happened to be in flight when the clock ran
// out. Every one of those packages wraps the context's own cause, so asking for
// the cause is both narrower and complete.
func interrupted(err error) bool {
	return errors.Is(err, context.Canceled)
}

// Interrupted reports whether a failure from [Run] is the run being stopped
// rather than the run going wrong.
//
// It is true for [context.Canceled] in the error's chain and for nothing else. A
// deadline that expired is deliberately *not* an interruption: nobody decided it
// at the moment it happened, and "the run ran out of time" is a question about
// where the time went, which is the question a kept snapshot and a diagnostics
// bundle are there to answer. Such a run reports [StatusFailed].
//
// It is exported because three decisions have to agree about one run: what
// [RunOutcome.Status] says, whether [Options.KeepTemp] on-failure keeps the
// temporary directories, and whether internal/cli writes a bundle. A second
// implementation of "was this an interruption" is a second answer waiting to
// disagree with this one.
func Interrupted(err error) bool { return interrupted(err) }

// deriveTimeout resolves the per-mutant timeout.
//
// The rejection boundary is `<=`, not `<`, and it is frozen. A timeout exactly
// equal to the slowest baseline run is not a tight budget, it is a budget that
// the unmutated tests have already been observed to reach: every mutant would
// be reported as a timeout, and the run would measure the scheduler rather than
// the test suite.
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

// deriveMemory resolves the per-mutant memory bound from what the baseline runs
// were measured to cost.
//
// It is [deriveTimeout] with two differences, and both are about the same
// thing: a memory measurement can be absent in a way a duration never is.
//
// The first is that a peak of zero — the platform could not say — is answered
// with no bound rather than with the floor. A floor is a *derivation*, and there
// is nothing here to derive from: applying one anyway would be go-mutants
// inventing a budget and then enforcing it, on the one platform where nothing
// measured what the suite actually needs.
//
// The second is that there is no refusal. deriveTimeout rejects an explicit
// timeout at or below the slowest baseline run, because such a budget is one the
// unmutated tests have already been seen to exhaust and every mutant would time
// out. The same argument does not carry: an explicit memory bound below the
// baseline peak is a bound the user chose knowing their own machine — a CI
// container with a hard cgroup limit is exactly the case — and refusing it would
// leave them with no way to say the true thing about where the run has to fit.
// A bound that is genuinely too small announces itself immediately, as every
// mutant killed for its memory, which is louder than a run that would not start.
func deriveMemory(explicit, peak int64) (int64, MemorySource) {
	if explicit > 0 {
		return explicit, MemorySourceExplicit
	}
	if peak <= 0 {
		return 0, MemorySourceUnavailable
	}
	return max(int64(MinDerivedMemory), MemoryFactor*peak), MemorySourceDerived
}

// enforceableMemory drops a bound this platform cannot keep.
//
// It is separate from [deriveMemory] so that the arithmetic stays testable
// everywhere and only the one line that depends on the machine is guarded by
// it. A *derived* bound that cannot be enforced is reported as absent rather
// than as derived, because the two say different things to the user reading the
// warning: one is "your suite is small", and the other is "nothing here will
// stop a runaway mutant", and a number go-mutants worked out for itself and
// then did not apply is a promise the run never made.
//
// An *explicit* bound survives, and the asymmetry is the point. The user wrote
// that number down; it belongs in the report of what they asked for, whether or
// not this machine can hold anybody to it, and the warning beside it says which.
// What must not follow it is the cache — see the engine's enforcedMemory — which
// records the budget a measurement was actually made under and not the one that
// was written.
func enforceableMemory(limit int64, source MemorySource) (int64, MemorySource) {
	if limit > 0 && source == MemorySourceDerived && !runner.MemoryBoundSupported() {
		return 0, MemorySourceUnavailable
	}
	return limit, source
}

// RunIDPattern is the shape of a run identifier, as a regular expression
// anchored at both ends.
//
// It is exported because the id is a name that leaves this package: internal/cli
// reads it back off a directory listing to decide which recordings are
// go-mutants' own and which are somebody else's files, and a second spelling of
// the same rule is a second thing to keep in step. [NewRunID] mints it and
// [Options.RunID] is checked against it.
const RunIDPattern = `^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`

// runID is [RunIDPattern] compiled once.
var runIDPattern = regexp.MustCompile(RunIDPattern)

// resolveRunID is the identity a run is filed under: the caller's when they
// named one, and a fresh one otherwise.
//
// A refused id still yields one, so that the failure has a name and the
// recording opened for it has something to say it is about. The run does not
// proceed under it — the error stops the pipeline before anything is copied —
// which is the whole point of checking an invocation rather than repairing it.
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

// NewRunID mints the identifier for one run: a UTC timestamp to the second and
// four random hex digits.
//
// The timestamp makes a directory listing of past runs read in order; the
// random suffix keeps two runs started in the same second apart. It is not a
// security boundary and does not need to be — nothing authenticates a run id —
// so two bytes of randomness is the right amount of collision resistance for a
// name a human has to be able to retype.
func NewRunID(t time.Time) string {
	var suffix [2]byte
	// crypto/rand.Read is documented never to fail; it panics internally on a
	// system without a usable entropy source rather than returning an error.
	_, _ = rand.Read(suffix[:])
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix[:])
}

// workspaceRoot resolves the tree to mutate.
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

// temporaryParent is the directory the run's snapshot, the scratch directory
// beside it and the sweep that precedes both all agree on: [Options.TempDirectory],
// or the operating system's temporary directory when the caller named none.
//
// It resolves nothing. A relative path means the same thing to everything
// downstream — internal/snapshot's destination takes filepath.Abs of it, and
// internal/tempowner's sweep reads it from this process's working directory,
// which is the directory it was written against — so making it absolute here
// would buy nothing and cost the one error [filepath.Abs] can return, on a
// machine whose working directory has gone away, a diagnostic code of its own.
// This is what the root package's Open does with the same option.
func temporaryParent(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return os.TempDir()
	}
	return dir
}

// testCommand picks the argv the baseline is measured with: the `--`
// passthrough when the user wrote one, and `test.command` otherwise.
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

// resolveProgram substitutes the located toolchain for a bare `go`, and leaves
// every other program alone.
//
// The substitution is exactly one string deep on purpose. A project whose test
// command is `./scripts/test.sh` or `gotestsum` has chosen a program, and
// second-guessing it would be wrong; a project whose command starts with `go`
// has said "the Go toolchain", and this run has already found out which one
// that is.
func resolveProgram(command []string, toolchain gocmd.Toolchain) []string {
	argv := slices.Clone(command)
	if argv[0] == "go" && toolchain.GoBin != "" {
		argv[0] = toolchain.GoBin
	}
	return argv
}

// childEnv builds the environment every child process of this run receives:
// this process's environment, minus every GO_MUTANTS_ variable, with the three
// temporary-directory variables pointed at the run's own scratch directory.
//
// Inheriting the rest is deliberate. GOFLAGS, GOMODCACHE, GOPROXY, a private
// module's credentials, and the PATH that makes a project's test command work
// are all part of what "the tests pass here" means, and a run that stripped
// them would be measuring a different project.
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

// isTempKey reports whether key is one of the temporary-directory variables.
// The comparison is case-insensitive because Windows environment names are.
func isTempKey(key string) bool {
	return slices.ContainsFunc(tempKeys, func(k string) bool { return strings.EqualFold(key, k) })
}

// mean returns the average of the observations, or zero for none.
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

// countNoun renders "1 file" or "3 files".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// firstLine returns everything before the first newline, so that a multi-line
// error still yields a one-line summary.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
