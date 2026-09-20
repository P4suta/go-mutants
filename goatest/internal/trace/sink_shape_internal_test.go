// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

const (
	clonedDurationMS  int64 = 7
	changedDurationMS int64 = 9
	refusingSinks     int64 = 2
)

type refusingSink struct{ err error }

func (sink refusingSink) Emit(Event) error { return sink.err }
func (sink refusingSink) Close() error     { return nil }

func TestCloneEventCopiesEveryPayloadItHolds(t *testing.T) {
	t.Parallel()
	duration := clonedDurationMS
	original := Event{
		Phase:    &PhaseRecord{Name: "baseline"},
		Prepare:  &PrepareRecord{Phase: "discovery", State: PrepareStateFinished, DurationMS: &duration},
		Exec:     &ExecRecord{Argv: []string{"go"}, EnvNames: []string{"PATH"}, Output: []byte("out")},
		Mutant:   &MutantRecord{ID: "m-1", Args: []string{"-test.run=X"}},
		Route:    &RouteRecord{Path: "a.go", ReachingTargets: []string{"TestOne"}, Plan: []string{"mutant"}},
		Probe:    &ProbeRecord{Target: "TestOne", Args: []string{"-test.run=X"}, Infected: []string{"m-1"}},
		Progress: &ProgressRecord{Kind: "note"},
		Artifact: &ArtifactRecord{Kind: "report", Path: "p"},
		Run:      &RunRecord{Verdict: "assured"},
	}
	clone := cloneEvent(original)
	for name, shared := range map[string]bool{
		"phase":    clone.Phase == original.Phase,
		"prepare":  clone.Prepare == original.Prepare,
		"duration": clone.Prepare.DurationMS == original.Prepare.DurationMS,
		"exec":     clone.Exec == original.Exec,
		"mutant":   clone.Mutant == original.Mutant,
		"route":    clone.Route == original.Route,
		"probe":    clone.Probe == original.Probe,
		"progress": clone.Progress == original.Progress,
		"artifact": clone.Artifact == original.Artifact,
		"run":      clone.Run == original.Run,
	} {
		if shared {
			t.Fatalf("the clone shares its %s payload with the original", name)
		}
	}
	original.Phase.Name = "race"
	original.Exec.Argv[0] = "git"
	original.Mutant.Args[0] = "-test.run=Y"
	original.Route.ReachingTargets[0] = "TestTwo"
	original.Probe.Infected[0] = "m-2"
	*original.Prepare.DurationMS = changedDurationMS
	if clone.Phase.Name != "baseline" || clone.Exec.Argv[0] != "go" || clone.Mutant.Args[0] != "-test.run=X" ||
		clone.Route.ReachingTargets[0] != "TestOne" || clone.Probe.Infected[0] != "m-1" || *clone.Prepare.DurationMS != clonedDurationMS {
		t.Fatalf("changing the original changed the clone: %+v", clone)
	}
}

func TestCloneEventLeavesAnEventWithNoPayloadAlone(t *testing.T) {
	t.Parallel()
	clone := cloneEvent(Event{Seq: 1, Type: TypeRunStart})
	if clone.Phase != nil || clone.Prepare != nil || clone.Exec != nil || clone.Mutant != nil ||
		clone.Route != nil || clone.Probe != nil || clone.Progress != nil || clone.Artifact != nil || clone.Run != nil {
		t.Fatalf("clone of an event with no payload = %+v", clone)
	}
}

func TestATeeOfNoSinksAtAllAcceptsEverything(t *testing.T) {
	t.Parallel()
	tee := NewTeeSink(nil, nil)
	if err := tee.Emit(Event{Seq: 1, Type: TypeRunStart}); err != nil {
		t.Fatalf("Emit into a tee of nothing = %v", err)
	}
	if got := tee.Dropped(); got != 0 {
		t.Fatalf("a tee of nothing dropped %d", got)
	}
}

func TestATeeReportsTheFirstRefusalAndCountsEveryOne(t *testing.T) {
	t.Parallel()
	first := errors.New("the first refusal")
	second := errors.New("the second refusal")
	tee := NewTeeSink(refusingSink{err: first}, refusingSink{}, refusingSink{err: second})
	err := tee.Emit(Event{Seq: 1, Type: TypeRunStart})
	if !errors.Is(err, first) {
		t.Fatalf("Emit = %v, want the first refusal %v", err, first)
	}
	if got := tee.Dropped(); got != refusingSinks {
		t.Fatalf("a tee that two sinks refused dropped %d, want %d", got, refusingSinks)
	}
}

func TestAMemorySinkKeepsItsMostRecentEventsAndTheRunEndBeside(t *testing.T) {
	t.Parallel()
	const capacity = 3
	sink := NewMemorySink(capacity)
	for seq := range capacity + 1 {
		if err := sink.Emit(Event{Seq: int64(seq + 1), Type: TypeProgress}); err != nil {
			t.Fatal(err)
		}
	}
	events := sink.Events()
	if len(events) != capacity-1 || events[0].Seq != 3 {
		t.Fatalf("events = %+v, want the oldest dropped to leave room for a run-end", events)
	}
	if got := sink.Dropped(); got != int64(capacity-1) {
		t.Fatalf("dropped = %d", got)
	}
}

func TestEnvironmentNamesAnswersNothingForNothing(t *testing.T) {
	t.Parallel()
	for _, entries := range [][]string{nil, {}, {"=value"}, {"="}} {
		if got := environmentNames(entries); got != nil {
			t.Fatalf("environmentNames(%q) = %#v, want nothing at all", entries, got)
		}
	}
	got := environmentNames([]string{"B=2", "A=1", "A=3"})
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("environmentNames = %q, want the names sorted and said once", got)
	}
}

func TestARecorderEndsItsRunOnceHoweverOftenItIsAsked(t *testing.T) {
	t.Parallel()
	sink := NewMemorySink(0)
	recorder := New(sink, nil)
	recorder.RunEnd("assured", nil)
	recorder.RunEnd("insufficient", errors.New("later"))
	ends := 0
	for _, event := range sink.Events() {
		if event.Type == TypeRunEnd {
			ends++
			if event.Run.Verdict != "assured" || event.Run.Error != "" {
				t.Fatalf("run-end = %+v, want the first verdict kept", event.Run)
			}
		}
	}
	if ends != 1 {
		t.Fatalf("run-end events = %d, want one", ends)
	}
}

func TestARecorderCountsOnlyTheEventsASinkRefused(t *testing.T) {
	t.Parallel()
	kept := NewMemorySink(0)
	keeping := New(kept, nil)
	keeping.Progress("note", "detail")
	keeping.RunEnd("assured", nil)
	events := kept.Events()
	if last := events[len(events)-1]; last.Run.EventsDropped != 0 || last.Run.EventsEmitted <= 0 {
		t.Fatalf("run-end over a sink that kept everything = %+v, want nothing dropped", last.Run)
	}
}

func TestARecorderSaysWhatWentWrongWithTheRun(t *testing.T) {
	t.Parallel()
	sink := NewMemorySink(0)
	recorder := New(sink, nil)
	recorder.RunEnd("insufficient", errors.New("the run failed"))
	events := sink.Events()
	last := events[len(events)-1]
	if last.Type != TypeRunEnd || !strings.Contains(last.Run.Error, "the run failed") {
		t.Fatalf("run-end = %+v", last)
	}
}

func TestADirSinkMakesTheDirectoriesItWasGivenAWayToMake(t *testing.T) {
	t.Parallel()
	made := 0
	hooks := Filesystem{Mkdir: func(path string, perm fs.FileMode) error {
		made++
		return os.Mkdir(path, perm)
	}}
	sink, err := NewDirSink(t.TempDir(), "run-1", hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	if made == 0 {
		t.Fatal("the sink made its directory without the hook it was given")
	}
}
