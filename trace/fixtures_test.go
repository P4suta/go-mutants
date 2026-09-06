// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/trace"
)

// The scripted recordings every group of tests in this package shares.
//
// [scriptedEvents] records one event of every type on a clock that advances by
// a fixed step, and [scriptedFailureEvents] records the same contract's other
// half: the error, timeout, refusal and lossy-accounting shapes a healthy run
// never produces. Between them they are three things at once: the sequences the
// recorder tests assert over, the bytes the two goldens pin, and the corpus the
// schema tests validate — so a payload that drifts from the contract fails in
// all three places rather than in whichever one somebody remembered to extend.
var fixtureStart = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

const (
	// fixtureTick is how far the scripted clock moves between two readings. A
	// quarter of a second is large enough that every duration in the recording
	// is a whole number of milliseconds, which is what keeps the golden stable.
	fixtureTick = 250 * time.Millisecond

	fixtureRunID        = "20260906T120000Z-1a2b"
	fixtureToolVersion  = "0.1.0-dev"
	fixturePID          = 31337
	fixtureRoot         = "/home/dev/project"
	fixtureVerdict      = "ok"
	fixtureStageName    = "catalog"
	fixtureStageDetail  = "1024 candidates"
	fixtureArtifactPath = "reports/mutation/run.json"
	fixtureNoteCode     = "GOM4102"
	fixtureNoteDetail   = "the coverage profile was empty"
	fixtureMutantID     = "4b2f8c1d0e6a39571c84fb02de7a6519cf3b48d20a71e6c95f803b4d17ea92c6"
	fixtureDisplayID    = "4b2f8c1d0e6a39571c84"
	fixturePackage      = "github.com/P4suta/go-mutants/internal/mutation"
	fixtureBinary       = "/tmp/go-mutants-tmp-1/mutation.test"
	fixtureDigest       = "07c5a1e94b83d26f0a1b7c8de95243f60b8a17d3e42c9b058f1d6a723c40e9b8"
	// fixtureContextKey is a cache context key: sixteen hex characters, the
	// truncation `cache.ContextKeyLength` names an entry's directory by. It is
	// deliberately a value nothing could mistake for a real one, because a
	// field whose name ends in `key` holding a plausible high-entropy string is
	// what a secret scanner is built to find, and the repository runs one over
	// its own fixtures.
	fixtureContextKey = "0000000000000001"

	fixturePrepareTime = 875 * time.Millisecond
	fixtureOutputTail  = "--- FAIL: TestScore (0.00s)"

	// The sequence numbers the scripted recording's own events point at. They
	// are constants rather than values read back out of the recording, so that
	// inserting an event without fixing what refers to it fails the fixture
	// rather than producing a golden whose references resolve to nothing.
	fixtureExecSeq         = 7
	fixtureProbeExecSeq    = 9
	fixtureValidateExecSeq = 11

	fixtureScriptedCount = 20
	fixtureTypeCount     = 16

	// The failure recording's own counts.
	fixtureFailureCount        = 17
	fixtureFailureValidateSeq  = 6
	fixtureFailureProbeExecSeq = 13
	fixtureFailureDroppedSeq   = 17
)

// fixtureClock returns a clock that starts at [fixtureStart] and advances one
// [fixtureTick] per reading. It is not safe for concurrent use, which is
// deliberate: the tests that record from many goroutines want a clock that
// says so rather than one that quietly interleaves.
func fixtureClock() func() time.Time {
	moment := fixtureStart.Add(-fixtureTick)
	return func() time.Time {
		moment = moment.Add(fixtureTick)
		return moment
	}
}

func fixtureStartRecord() trace.StartRecord {
	return trace.StartRecord{
		Kind:        trace.StartKindRun,
		RunID:       fixtureRunID,
		ToolVersion: fixtureToolVersion,
		PID:         fixturePID,
		Root:        fixtureRoot,
		Args:        []string{"run", "--no-tui"},
	}
}

func fixtureExecRecord() trace.ExecRecord {
	return trace.ExecRecord{
		Kind:      trace.ExecKindMutantRun,
		Subject:   fixtureMutantID,
		Argv:      []string{fixtureBinary, "-test.timeout=30s"},
		Dir:       fixtureRoot,
		EnvNames:  []string{"PATH=/usr/bin", "GO_MUTANTS_ACTIVE=" + fixtureMutantID, "PATH=/bin"},
		TimeoutMS: 30000,
		ExitCode:  1,
		Output:    []byte(fixtureOutputTail + "\n"),
	}
}

func fixtureProbeExecRecord() trace.ExecRecord {
	return trace.ExecRecord{
		Kind:       trace.ExecKindProbeRun,
		Argv:       []string{fixtureBinary, "-test.timeout=30s"},
		Dir:        "/tmp/go-mutants-probe-1",
		EnvNames:   []string{"PATH=/usr/bin"},
		TimeoutMS:  30000,
		ExitCode:   0,
		DurationMS: 96,
	}
}

func fixtureValidateExecRecord() trace.ExecRecord {
	return trace.ExecRecord{
		Kind:       trace.ExecKindValidateBuild,
		Argv:       []string{"go", "build", "./..."},
		Dir:        "/tmp/go-mutants-snap-1",
		EnvNames:   []string{"GOFLAGS=-mod=mod", "PATH=/usr/bin"},
		ExitCode:   1,
		DurationMS: 2400,
	}
}

func fixtureMutantRecord() trace.MutantRecord {
	return trace.MutantRecord{
		ID:         fixtureMutantID,
		DisplayID:  fixtureDisplayID,
		Attempt:    1,
		Worker:     3,
		Package:    fixturePackage,
		Binaries:   []string{fixtureBinary},
		Args:       []string{"-test.timeout=30s"},
		TimeoutMS:  30000,
		Outcome:    trace.OutcomeKilled,
		KilledBy:   fixturePackage,
		DurationMS: 412,
		ExecSeqs:   []int64{fixtureExecSeq},
		OutputTail: fixtureOutputTail,
	}
}

func fixtureProbeRecord() trace.ProbeRecord {
	return trace.ProbeRecord{
		Package:    fixturePackage,
		Binaries:   []string{fixtureBinary},
		Args:       []string{"-test.timeout=30s"},
		TimeoutMS:  30000,
		Outcome:    trace.ProbeOutcomeMeasured,
		ExitCode:   0,
		DurationMS: 96,
		Infected:   []string{fixtureMutantID},
		ExecSeqs:   []int64{fixtureProbeExecSeq},
	}
}

func fixtureValidateRecord() trace.ValidateRecord {
	return trace.ValidateRecord{
		Tree:    trace.ValidateTreeMutant,
		Op:      trace.ValidateOpBuild,
		Build:   2,
		Failed:  true,
		Blamed:  []string{"internal/mutation/score.go"},
		Pending: 4,
		ExecSeq: fixtureValidateExecSeq,
	}
}

func fixtureCoverageRecord() trace.CoverageRecord {
	return trace.CoverageRecord{
		MutantID:  fixtureMutantID,
		Path:      "internal/mutation/score.go",
		StartLine: 88,
		EndLine:   91,
		Covering:  []string{fixturePackage},
	}
}

func fixtureCacheRecord() trace.CacheRecord {
	return trace.CacheRecord{
		Op:         trace.CacheOpLookup,
		MutantID:   fixtureMutantID,
		Result:     trace.CacheResultHit,
		Outcome:    trace.OutcomeKilled,
		ContextKey: fixtureContextKey,
	}
}

func fixtureSnapshotRecord() trace.SnapshotRecord {
	return trace.SnapshotRecord{
		Kind:       trace.SnapshotKindWorkspace,
		Source:     fixtureRoot,
		Dir:        "/tmp/go-mutants-snap-1",
		Stable:     true,
		Files:      1204,
		Digest:     fixtureDigest,
		DurationMS: 1500,
	}
}

func fixtureSweepRecord() trace.SweepRecord {
	return trace.SweepRecord{
		Parent:       "/tmp",
		Removed:      []string{"/tmp/go-mutants-snap-0"},
		RemovedBytes: 268435456,
		Live:         1,
		Kept:         2,
	}
}

// scriptedEvents records one event of every type and returns the recording.
//
// Every reference inside it resolves: the mutant attempt names the `exec` that
// ran its binary, the probe pass names the `probe-run` before it, and the
// validation step names the `validate-build` before it. A golden whose
// `exec_seqs` pointed at nothing would document a shape no run produces.
func scriptedEvents(t *testing.T) []trace.Event {
	t.Helper()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	if recorder == nil {
		t.Fatal("New returned no recorder for a usable sink")
	}
	endPhase := recorder.PhaseStart(trace.PhaseMutate)
	endStage := recorder.Stage(fixtureStageName, fixtureStageDetail)
	endStage(trace.ResultSucceeded)
	recorder.Prepare(trace.PreparePhaseDiscovery, trace.StateStarted, "", 0)
	recorder.Prepare(trace.PreparePhaseDiscovery, trace.StateFinished, trace.ResultSucceeded, fixturePrepareTime)
	requireSeq(t, fixtureExecSeq, recorder.Exec(fixtureExecRecord()), "mutant-run exec")
	recorder.MutantExec(fixtureMutantRecord())
	requireSeq(t, fixtureProbeExecSeq, recorder.Exec(fixtureProbeExecRecord()), "probe-run exec")
	recorder.ProbeExec(fixtureProbeRecord())
	requireSeq(t, fixtureValidateExecSeq, recorder.Exec(fixtureValidateExecRecord()), "validate-build exec")
	recorder.Validate(fixtureValidateRecord())
	recorder.Coverage(fixtureCoverageRecord())
	recorder.Cache(fixtureCacheRecord())
	recorder.Snapshot(fixtureSnapshotRecord())
	recorder.Sweep(fixtureSweepRecord())
	recorder.Artifact(trace.ArtifactReportJSON, fixtureArtifactPath)
	recorder.Note(trace.NoteWarning, fixtureNoteCode, fixtureNoteDetail)
	endPhase()
	recorder.RunEnd(fixtureVerdict, 0, nil)
	return sink.Events()
}

// scriptedFailureEvents records the shapes a healthy run never produces.
//
// It is a workspace recording rather than a run recording, it fails at nearly
// every step it takes, one of its events is refused by the sink, and it closes
// with a verdict and a non-zero exit code. Everything a reader has to handle
// and a happy path never shows is in here: an execution that timed out, a
// probe pass stopped by an error before it reached an outcome, a mutant no
// test binary reaches, the whole validation vocabulary including the gate and
// a rejection, a snapshot and a sweep that failed, and an accounting that
// admits to a loss.
func scriptedFailureEvents(t *testing.T) []trace.Event {
	t.Helper()
	ring := trace.NewMemorySink(0)
	sink := &refusingAt{inner: ring, seq: fixtureFailureDroppedSeq}
	recorder := trace.New(sink, fixtureClock(), trace.StartRecord{
		Kind:        trace.StartKindWorkspace,
		ToolVersion: fixtureToolVersion,
		PID:         fixturePID,
		Root:        fixtureRoot,
	})
	if recorder == nil {
		t.Fatal("New returned no recorder for a usable sink")
	}
	recorder.Cache(trace.CacheRecord{
		Op:        trace.CacheOpOpen,
		Result:    trace.CacheResultOpened,
		Directory: "/home/dev/.cache/go-mutants/outcomes",
	})
	recorder.Snapshot(trace.SnapshotRecord{
		Kind:   trace.SnapshotKindProbe,
		Source: fixtureRoot,
		Dir:    "/tmp/go-mutants-probe-2",
		Error:  "copy internal/mutation/score.go: no space left on device",
	})
	recorder.Sweep(trace.SweepRecord{
		Parent: "/tmp",
		Error:  "read /tmp: permission denied",
	})
	recorder.Validate(trace.ValidateRecord{Tree: trace.ValidateTreeMutant, Op: trace.ValidateOpInstrument})
	requireSeq(t, fixtureFailureValidateSeq, recorder.Exec(fixtureValidateExecRecord()), "validate-build exec")
	recorder.Validate(trace.ValidateRecord{
		Tree:    trace.ValidateTreeMutant,
		Op:      trace.ValidateOpBuild,
		Build:   1,
		Failed:  true,
		Blamed:  []string{"internal/mutation/score.go"},
		Pending: 2,
		ExecSeq: fixtureFailureValidateSeq,
	})
	recorder.Validate(trace.ValidateRecord{Tree: trace.ValidateTreeMutant, Op: trace.ValidateOpGate})
	recorder.Validate(trace.ValidateRecord{
		Tree:       trace.ValidateTreeMutant,
		Op:         trace.ValidateOpIsolate,
		Path:       "internal/mutation/score.go",
		Candidates: 3,
		Accepted:   2,
	})
	recorder.Validate(trace.ValidateRecord{
		Tree:       trace.ValidateTreeMutant,
		Op:         trace.ValidateOpReject,
		Path:       "internal/mutation/score.go",
		MutantID:   fixtureMutantID,
		Diagnostic: "internal/mutation/score.go:88:12: invalid operation: untyped nil",
	})
	recorder.Validate(trace.ValidateRecord{
		Tree:     trace.ValidateTreeMutant,
		Op:       trace.ValidateOpDone,
		Builds:   3,
		Accepted: 2,
		Rejected: 1,
	})
	recorder.Coverage(trace.CoverageRecord{
		MutantID:  fixtureMutantID,
		Path:      "internal/mutation/score.go",
		Uncovered: true,
	})
	timedOut := fixtureProbeExecRecord()
	timedOut.TimedOut = true
	timedOut.ExitCode = -1
	timedOut.DurationMS = 30000
	timedOut.Error = "the test binary was killed after 30s"
	requireSeq(t, fixtureFailureProbeExecSeq, recorder.Exec(timedOut), "timed-out probe exec")
	recorder.ProbeExec(trace.ProbeRecord{
		Package:    fixturePackage,
		Binaries:   []string{fixtureBinary},
		Args:       []string{"-test.timeout=30s"},
		TimeoutMS:  30000,
		ExitCode:   -1,
		DurationMS: 30000,
		ExecSeqs:   []int64{fixtureFailureProbeExecSeq},
		Error:      "the probe log could not be read back",
	})
	recorder.Cache(trace.CacheRecord{
		Op:         trace.CacheOpStore,
		MutantID:   fixtureMutantID,
		Result:     trace.CacheResultWritten,
		ContextKey: fixtureContextKey,
	})
	recorder.Note(trace.NoteWarning, "GOM4106", "the probe tree could not be prepared")
	// The event the sink refuses. The bundle was written; the line saying so is
	// the one the recording lost, which is exactly the loss the accounting
	// exists to admit to.
	recorder.Artifact(trace.ArtifactDiagnostics, "reports/mutation/diagnostics/"+fixtureRunID)
	recorder.RunEnd("failed", 2, errors.New("the probe tree could not be prepared"))
	return ring.Events()
}

// requireSeq fails the fixture when an event did not land where the recording
// says it did.
func requireSeq(t *testing.T, want, got int64, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("the %s landed at seq %d, want %d: fix the fixture constants", what, got, want)
	}
}

// refusingAt forwards every event but one, and does not count its own losses —
// which is what makes the recorder fall back to counting refusals itself.
type refusingAt struct {
	inner *trace.MemorySink
	seq   int64

	mutex sync.Mutex
}

func (sink *refusingAt) Emit(event trace.Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if event.Seq == sink.seq {
		return errHook
	}
	return sink.inner.Emit(event)
}

func (sink *refusingAt) Close() error { return sink.inner.Close() }
