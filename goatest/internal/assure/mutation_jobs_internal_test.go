// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"runtime"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/config"
)

const (
	explicitMutationJobs       = 3
	uncappedMutationJobs       = 12
	mutationProgressTotal      = 250
	maximumMutationProgressLog = 102
)

func TestMutationJobLimitParallelizesLocalWorkAndSerializesExclusiveResources(t *testing.T) {
	if got := mutationJobLimit(Options{MutationJobs: explicitMutationJobs}, config.Config{}); got != explicitMutationJobs {
		t.Fatalf("local mutation jobs = %d, want 3", got)
	}
	shared := config.Config{Resources: map[string]config.Resource{
		"postgres": {Shared: true},
	}}
	if got := mutationJobLimit(Options{MutationJobs: explicitMutationJobs}, shared); got != explicitMutationJobs {
		t.Fatalf("shared-resource mutation jobs = %d, want 3", got)
	}
	exclusive := config.Config{Resources: map[string]config.Resource{
		"postgres": {Exclusive: true},
	}}
	if got := mutationJobLimit(Options{MutationJobs: 3}, exclusive); got != 1 {
		t.Fatalf("exclusive-resource mutation jobs = %d, want 1", got)
	}
	// The zero case is asserted by TestTheDerivedWorkerCountIsTheCapAndTheMachine
	// instead. Bounding it by the runner's own derivation here would compare a
	// function with itself and pass for any value it ever returned.
	if got := mutationJobLimit(Options{MutationJobs: uncappedMutationJobs}, config.Config{}); got != uncappedMutationJobs {
		t.Fatalf("explicit mutation jobs = %d, want 12: an operator's explicit choice is respected, only the default is capped", got)
	}
}

func TestMutationProgressReportsFirstPercentMilestonesAndLast(t *testing.T) {
	var events []Event
	progress := mutationProgress(Options{Progress: func(event Event) {
		events = append(events, event)
	}})
	for completed := 1; completed <= mutationProgressTotal; completed++ {
		progress(completed, mutationProgressTotal)
	}
	if len(events) == 0 || events[0].Kind != "mutation-progress" || events[0].Detail != "1/250" {
		t.Fatalf("first progress = %+v", events)
	}
	if events[len(events)-1].Detail != "250/250" {
		t.Fatalf("last progress = %+v", events[len(events)-1])
	}
	if len(events) > maximumMutationProgressLog {
		t.Fatalf("progress emitted %d events, want at most 102", len(events))
	}
}

// TestTheDerivedWorkerCountIsTheCapAndTheMachine pins what a run takes when
// nothing asked: the ceiling, the machine, whichever is smaller, never zero.
//
// That the ceiling is the *engine's* ceiling is a separate claim, and it is
// held by internal/devgates, which reads both trees. It cannot be held here:
// a run is built with GOWORK=off against the engine version go.mod pins, so
// naming a symbol the engine gained after that pin would fail to compile in
// exactly the build this package's own gate uses.
func TestTheDerivedWorkerCountIsTheCapAndTheMachine(t *testing.T) {
	want := max(1, min(runtime.GOMAXPROCS(0), defaultMutationJobCap))
	if got := mutationJobLimit(Options{}, config.Config{}); got != want {
		t.Fatalf("derived mutation jobs = %d, want %d", got, want)
	}
	if got := mutationJobLimit(Options{}, config.Config{}); got < 1 {
		t.Fatalf("derived mutation jobs = %d, want at least one worker", got)
	}
}
