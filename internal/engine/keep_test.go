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

	events := make(chan Event, 8)
	return &session{events: events}, &temporaries{
		snapshot:     snap,
		scratch:      scratch,
		scratchOwner: owner,
	}, events, &RunOutcome{}
}

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

func requireGone(t *testing.T, directory string) {
	t.Helper()
	if _, err := os.Stat(directory); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s survived a run that kept nothing (%v)", directory, err)
	}
}

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
			if got := keepsTemporaries(KeepTempOnFailure, err); got == c.interrupted {
				t.Errorf("keepsTemporaries(on-failure, %v) = %t, want %t", err, got, !c.interrupted)
			}
			var coded *Error
			if !errors.As(err, &coded) || coded.Invocation == nil {
				t.Errorf("check lost the command it judged: %v", err)
			}
		})
	}
}

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

func TestDirectoryKeptIsASealedEvent(t *testing.T) {
	t.Parallel()

	var kept Event = DirectoryKept{Kind: KeptSnapshot, Path: "/tmp/go-mutants-snap-0000"}

	if got := eventNames([]Event{kept}); !slices.Equal(got, []string{"DirectoryKept"}) {
		t.Errorf("events = %v, want the new one", got)
	}
}

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

func TestTheScratchGoesEvenAfterAKilledTestLeftADirectoryShut(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a removal fail")
	}

	root := filepath.Join(t.TempDir(), "scratch")
	deep := filepath.Join(root, "workers", "w3", "TestSomething", "001", "inner")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatalf("staging the scratch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deep, "eight"), []byte("12345678"), 0o600); err != nil {
		t.Fatalf("staging a file: %v", err)
	}
	if err := os.Chmod(deep, 0o600); err != nil {
		t.Fatalf("shutting the directory: %v", err)
	}
	if err := os.RemoveAll(root); err == nil {
		t.Skip("this filesystem removes a directory it cannot search, so there is nothing to force")
	}

	if err := forceRemoveAll(root); err != nil {
		t.Fatalf("forceRemoveAll: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch directory is still there: %v", err)
	}
}

func TestForcingARemovalStillReportsOneItCannotMake(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a removal fail")
	}

	parent := t.TempDir()
	root := filepath.Join(parent, "scratch")
	if err := os.MkdirAll(filepath.Join(root, "inner"), 0o700); err != nil {
		t.Fatalf("staging the scratch: %v", err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("closing the parent to writes: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if err := os.WriteFile(filepath.Join(parent, "probe"), nil, 0o600); err == nil {
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}

	if err := forceRemoveAll(root); err == nil {
		t.Error("forceRemoveAll reported success for a directory that is still there")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the directory was removed after all: %v", err)
	}
}

func TestWidenReachesADirectoryItCannotListUntilItHasWidenedIt(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses")
	}

	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatalf("staging: %v", err)
	}
	for _, dir := range []string{inner, outer} {
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatalf("shutting %s: %v", dir, err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(outer, 0o700)
		_ = os.Chmod(inner, 0o700)
	})
	if _, err := os.ReadDir(outer); err == nil {
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}

	widen(root)
	for _, dir := range []string{outer, inner} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if info.Mode().Perm()&0o700 != 0o700 {
			t.Errorf("%s is %v, want it listable, searchable and writable", dir, info.Mode().Perm())
		}
	}
}
