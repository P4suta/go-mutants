// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"testing"

	gomutants "github.com/P4suta/go-mutants"
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
	// The zero case is asserted by TestTheDefaultWorkerCountIsTheEnginesPublishedOne
	// instead, against the engine's number. Bounding it by the runner's own
	// derivation here would compare a function with itself and pass for any
	// value it ever returned.
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

// TestTheDefaultWorkerCountIsTheEnginesPublishedOne pins the two products to a
// single answer to a single question: how much of a machine a mutation run is
// allowed to take.
//
// They answered it twice. The engine's answer is [gomutants.DefaultJobs], and
// it carries its argument -- a mutation run is a background chore that should
// leave a laptop usable, so the derived count is the machine's clamped to a
// ceiling. The runner's was a bare 4 with nothing beside it, and because the
// runner always passes Jobs explicitly, the engine's number never applied: on
// an eighteen-core machine the runner ran four workers and the engine's ceiling
// was never reached, let alone consulted.
//
// This is the test two repositories could not hold. Neither half is wrong on
// its own; what is wrong is that there are two of them, and nothing either side
// could import would have said so.
func TestTheDefaultWorkerCountIsTheEnginesPublishedOne(t *testing.T) {
	want := gomutants.DefaultJobs()
	if got := mutationJobLimit(Options{}, config.Config{}); got != want {
		t.Fatalf("derived mutation jobs = %d, want the engine's published default %d", got, want)
	}
}
