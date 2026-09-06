// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// temporariesFixture is the pair of directories one run makes, built without a
// run.
//
// Everything [session.release] decides is decidable from the two directories
// and the run's error, and neither needs a toolchain: a snapshot is a copy of a
// tree and a scratch directory is an os.MkdirTemp with a lock in it. Driving a
// whole pipeline to reach the deferred cleanup would pay minutes of real
// building for a rule that is a single boolean.
//
// The parent is the test's own temporary directory, so the kept directories
// these tests deliberately leave behind are removed by the test's cleanup —
// which is the point of naming a parent rather than letting the snapshot land
// in the machine's shared temporary directory.
func temporariesFixture(t *testing.T) (*session, *temporaries, chan Event, *RunOutcome) {
	t.Helper()

	parent := t.TempDir()
	snap, err := snapshot.Create(testkit.Copy(t, "simple"), snapshot.Options{DestParent: parent})
	if err != nil {
		t.Fatalf("creating the snapshot: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(snap.Dir()) })

	scratch, err := os.MkdirTemp(snap.Parent(), scratchPrefix)
	if err != nil {
		t.Fatalf("creating the scratch directory: %v", err)
	}
	owner, err := tempowner.Claim(scratch, time.Now())
	if err != nil {
		t.Fatalf("claiming the scratch directory: %v", err)
	}
	t.Cleanup(func() { _ = owner.Release() })

	// Buffered well past the two events a keep publishes, because nothing is
	// draining: [session.emit] blocks on an unbuffered channel and the point of
	// these tests is what release decided, not how it was consumed.
	events := make(chan Event, 8)
	return &session{events: events}, &temporaries{
		snapshot:     snap,
		scratch:      scratch,
		scratchOwner: owner,
	}, events, &RunOutcome{}
}

// keptEvents drains what a release published, by kind and path.
func keptEvents(t *testing.T, events chan Event) []PreservedDir {
	t.Helper()
	close(events)
	var kept []PreservedDir
	for event := range events {
		if directory, ok := event.(DirectoryKept); ok {
			kept = append(kept, PreservedDir(directory))
		}
	}
	return kept
}

// requireKeptMarker fails unless the directory is still there and its owner
// marker says the keep was deliberate.
//
// Both halves are the claim. A directory left behind with a lock nobody holds
// is indistinguishable from one a killed process abandoned, and the next run's
// sweep collects exactly those — so a keep that did not record itself in a way
// the sweep obeys is a keep that lasts until somebody else runs go-mutants.
func requireKeptMarker(t *testing.T, directory string) {
	t.Helper()
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("%s was removed by a run that asked to keep it: %v", directory, err)
	}
	marker, err := tempowner.ReadMarker(directory)
	if err != nil {
		t.Fatalf("reading the owner marker in %s: %v", directory, err)
	}
	if !marker.Kept {
		t.Errorf("%s survived without its marker saying kept, so the next run's sweep will collect it", directory)
	}
}

// requireGone fails unless the directory was removed.
func requireGone(t *testing.T, directory string) {
	t.Helper()
	if _, err := os.Stat(directory); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s survived a run that kept nothing (%v)", directory, err)
	}
}

// TestKeepTempAlwaysLeavesTheSnapshotAndScratchMarkedKept is the whole of what
// the option promises: both directories are still there afterwards, both say
// they were preserved on purpose, and the run says which they are.
func TestKeepTempAlwaysLeavesTheSnapshotAndScratchMarkedKept(t *testing.T) {
	t.Parallel()

	s, temps, events, out := temporariesFixture(t)
	snapshotDir, scratch := temps.snapshot.Dir(), temps.scratch

	s.release(temps, KeepTempAlways, out, nil)

	requireKeptMarker(t, snapshotDir)
	requireKeptMarker(t, scratch)
	want := []PreservedDir{{Kind: KeptScratch, Path: scratch}, {Kind: KeptSnapshot, Path: snapshotDir}}
	if !slices.Equal(out.Preserved, want) {
		t.Errorf("Preserved = %+v, want %+v", out.Preserved, want)
	}
	if got := keptEvents(t, events); !slices.Equal(got, want) {
		t.Errorf("the run published %+v, want one DirectoryKept per preserved directory (%+v)", got, want)
	}
	if len(s.warnings) != 0 {
		t.Errorf("a keep that worked published %v", s.warnings)
	}
}

// TestKeepTempOnFailureKeepsOnlyWhenTheRunFails is the difference between the
// two modes, stated as the four combinations that exist.
//
// `on-failure` is the mode a CI job can afford to leave on: a run that went
// fine leaves nothing behind, so the disk only pays for the runs somebody has
// to diagnose. `always` is the one a person types once.
func TestKeepTempOnFailureKeepsOnlyWhenTheRunFails(t *testing.T) {
	t.Parallel()

	failure := &Error{Code: CodeBaselineTestFailed, Message: "baseline run 1 of 1 failed"}
	cases := []struct {
		name string
		keep KeepTemp
		err  error
		want bool
	}{
		{"never, and the run failed", KeepTempNever, failure, false},
		{"on failure, and the run went fine", KeepTempOnFailure, nil, false},
		{"on failure, and the run failed", KeepTempOnFailure, failure, true},
		{"always, and the run went fine", KeepTempAlways, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			s, temps, events, out := temporariesFixture(t)
			snapshotDir, scratch := temps.snapshot.Dir(), temps.scratch

			s.release(temps, c.keep, out, c.err)

			if !c.want {
				requireGone(t, snapshotDir)
				requireGone(t, scratch)
				if len(out.Preserved) != 0 {
					t.Errorf("Preserved = %+v, want nothing", out.Preserved)
				}
				if kept := keptEvents(t, events); len(kept) != 0 {
					t.Errorf("the run published %+v, want no DirectoryKept", kept)
				}
				return
			}
			requireKeptMarker(t, snapshotDir)
			requireKeptMarker(t, scratch)
			if len(out.Preserved) != 2 {
				t.Errorf("Preserved = %+v, want the snapshot and the scratch directory", out.Preserved)
			}
		})
	}
}

// TestAnInterruptedRunKeepsNothing holds `on-failure` to what it says.
//
// A Ctrl-C is not a failure: nothing went wrong, the user asked for the run to
// stop, and a mode that filled the disk every time somebody changed their mind
// would be a mode nobody could leave on.
//
// Every way an interruption reaches the run counts, and each of them carries the
// context's own cause rather than a code of its own — this package's
// [CodeInterrupted], internal/execute's and internal/validate's, and a bare
// context.Canceled from whichever package noticed first. That is what lets
// [interrupted] ask one question; see
// [TestInterruptedIsACancellationAndNeverADeadline].
func TestAnInterruptedRunKeepsNothing(t *testing.T) {
	t.Parallel()

	cases := map[string]error{
		"a cancelled context": fmt.Errorf("the run stopped: %w", context.Canceled),
		"the engine's own error": &Error{
			Code:    CodeInterrupted,
			Message: "the run was interrupted",
			Err:     context.Canceled,
		},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, temps, events, out := temporariesFixture(t)
			snapshotDir, scratch := temps.snapshot.Dir(), temps.scratch

			s.release(temps, KeepTempOnFailure, out, err)

			requireGone(t, snapshotDir)
			requireGone(t, scratch)
			if len(out.Preserved) != 0 {
				t.Errorf("an interrupted run preserved %+v", out.Preserved)
			}
			if kept := keptEvents(t, events); len(kept) != 0 {
				t.Errorf("an interrupted run published %+v", kept)
			}
		})
	}
}

// TestAKeptDirectoryIsNotCollectedByTheNextRunsSweep is the other half of a
// keep, and the half that decides whether it lasts.
//
// Every run sweeps the temporary parent before it copies anything, and what it
// collects is every go-mutants directory whose lock is free. A kept directory's
// lock is free — the process that held it has exited — so the only thing
// standing between the answer somebody kept and the next run's collector is the
// marker.
func TestAKeptDirectoryIsNotCollectedByTheNextRunsSweep(t *testing.T) {
	t.Parallel()

	s, temps, events, out := temporariesFixture(t)
	snapshotDir, scratch := temps.snapshot.Dir(), temps.scratch
	parent := filepath.Dir(snapshotDir)

	s.release(temps, KeepTempAlways, out, nil)
	keptEvents(t, events)

	next := &session{}
	result := next.sweepTemporary(parent)

	requireKeptMarker(t, snapshotDir)
	requireKeptMarker(t, scratch)
	if len(result.Removed) != 0 {
		t.Errorf("the next run's sweep removed %v", result.Removed)
	}
	if result.Kept != 2 {
		t.Errorf("the sweep counted %d kept directories, want the snapshot and the scratch directory", result.Kept)
	}
	if len(next.warnings) != 0 {
		t.Errorf("the next run's sweep published %v", next.warnings)
	}
}

// TestInterruptedIsACancellationAndNeverADeadline is the predicate two features
// now hang off, checked where the two causes are actually told apart.
//
// [check] asks the context after it has judged the command, and the answer it
// used to give was the same for both causes: any `ctx.Err()` became
// [CodeInterrupted], which the old predicate matched by code. That made a
// deadline an interruption *here* and a failure everywhere else — internal/gocmd,
// internal/validate and internal/execute each surface the same expiry under
// their own code — so one run kept its directories and wrote a bundle or did
// not, depending on which command happened to be in flight when the clock ran
// out. One cause, two answers.
//
// The rule is now the cause and nothing else. A cancellation is somebody
// stopping the run; a deadline is the run failing to finish in the time it was
// given, which is a failure worth the tree and the bundle.
func TestInterruptedIsACancellationAndNeverADeadline(t *testing.T) {
	t.Parallel()

	spec := runner.Spec{Argv: []string{"/usr/bin/go", "test", "./..."}, Dir: "/tmp/snap/tree"}
	cases := []struct {
		name        string
		ctx         func(t *testing.T) context.Context
		interrupted bool
		code        Code
	}{
		{
			name: "a cancelled context",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			interrupted: true,
			code:        CodeInterrupted,
		},
		{
			name: "a deadline that has passed",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
			interrupted: false,
			code:        CodeDeadlineExceeded,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			err := check(c.ctx(t), spec, runner.Result{}, CodeBaselineTestFailed, "baseline run 1 of 1 failed")
			if err == nil {
				t.Fatal("check accepted a command whose context was already done")
			}
			if got := CodeOf(err); got != c.code {
				t.Errorf("check reported %s, want %s: %v", got, c.code, err)
			}
			if got := Interrupted(err); got != c.interrupted {
				t.Errorf("Interrupted(%v) = %t, want %t", err, got, c.interrupted)
			}
			// And what the run does about it follows from that one answer, which
			// is the whole point of there being one answer.
			if got := keepsTemporaries(KeepTempOnFailure, err); got == c.interrupted {
				t.Errorf("keepsTemporaries(on-failure, %v) = %t, want %t", err, got, !c.interrupted)
			}
			// The command is named whichever cause it was: what was still
			// running is the first thing a reader of either asks for.
			var coded *Error
			if !errors.As(err, &coded) || coded.Invocation == nil {
				t.Errorf("check lost the command it judged: %v", err)
			}
		})
	}
}

// TestPreservedIsSortedAndMatchesTheKeptEvents keeps the two accounts of one
// decision in step.
//
// [RunOutcome.Preserved] is what a caller reads afterwards and [DirectoryKept]
// is what a renderer prints as it happens, and a reader comparing a console
// against a returned value must not find one of them holding a directory the
// other does not. The order is fixed for the same reason every other list this
// package publishes is: two runs of one workspace produce two lists that line
// up row for row.
func TestPreservedIsSortedAndMatchesTheKeptEvents(t *testing.T) {
	t.Parallel()

	s, temps, events, out := temporariesFixture(t)
	s.release(temps, KeepTempAlways, out, nil)

	if !slices.IsSortedFunc(out.Preserved, comparePreserved) {
		t.Errorf("Preserved is not sorted by kind and then path: %+v", out.Preserved)
	}
	if got := keptEvents(t, events); !slices.Equal(got, out.Preserved) {
		t.Errorf("the events say %+v and the outcome says %+v", got, out.Preserved)
	}
	for _, directory := range out.Preserved {
		switch directory.Kind {
		case KeptSnapshot, KeptScratch:
		default:
			t.Errorf("preserved kind %q is neither %q nor %q", directory.Kind, KeptSnapshot, KeptScratch)
		}
	}
}

// TestDirectoryKeptIsASealedEvent is a compile-time assertion: it travels on the
// engine's stream, so it has to be part of the sealed interface a renderer
// switches over.
func TestDirectoryKeptIsASealedEvent(t *testing.T) {
	t.Parallel()

	// The declaration is the assertion: the interface's marker method is
	// unexported, so a type that forgot it would not compile here.
	var kept Event = DirectoryKept{Kind: KeptSnapshot, Path: "/tmp/go-mutants-snap-0000"}

	if got := eventNames([]Event{kept}); !slices.Equal(got, []string{"DirectoryKept"}) {
		t.Errorf("events = %v, want the new one", got)
	}
}

// TestKeepTempIsARecordedArtifactAndNeverAWarning pins where a keep is written
// down in the run's own account.
//
// A kept directory is a path the run produced, so it is an `artifact` beside
// the report documents rather than a note about something that went wrong.
// Nothing went wrong: the user asked for the directory and got it.
func TestKeepTempIsARecordedArtifactAndNeverAWarning(t *testing.T) {
	t.Parallel()

	s, temps, events, out := temporariesFixture(t)
	sink := trace.NewMemorySink(0)
	s.trace = trace.New(sink, time.Now, trace.StartRecord{Kind: trace.StartKindRun, RunID: "20260907T120000Z-a1b2"})

	s.release(temps, KeepTempAlways, out, nil)
	keptEvents(t, events)

	kinds := map[string]string{}
	for _, event := range sink.Events() {
		if event.Type == trace.TypeArtifact {
			kinds[event.Artifact.Kind] = event.Artifact.Path
		}
		if event.Type == trace.TypeNote {
			t.Errorf("a keep recorded the note %+v; a directory somebody asked for is not a problem", event.Note)
		}
	}
	if kinds[trace.ArtifactKeptSnapshot] != temps.snapshot.Dir() {
		t.Errorf("%s = %q, want %q", trace.ArtifactKeptSnapshot, kinds[trace.ArtifactKeptSnapshot], temps.snapshot.Dir())
	}
	if kinds[trace.ArtifactKeptScratch] != temps.scratch {
		t.Errorf("%s = %q, want %q", trace.ArtifactKeptScratch, kinds[trace.ArtifactKeptScratch], temps.scratch)
	}
}

// TestKeepTempIsPrintedAsTheWordAUserTyped keeps the mode's spelling and the
// flag's spelling one string.
func TestKeepTempIsPrintedAsTheWordAUserTyped(t *testing.T) {
	t.Parallel()

	want := map[KeepTemp]string{
		KeepTempNever:     "never",
		KeepTempAlways:    "always",
		KeepTempOnFailure: "on-failure",
	}
	for mode, spelling := range want {
		if got := mode.String(); got != spelling {
			t.Errorf("KeepTemp(%d).String() = %q, want %q", int(mode), got, spelling)
		}
	}
	if KeepTempNever != 0 {
		t.Errorf("KeepTempNever = %d, want the zero value: every caller that never heard of the option keeps nothing",
			int(KeepTempNever))
	}
}
