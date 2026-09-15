// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// ceilingCorpus are the modules whose work is counted.
//
// Small ones, deliberately. The point of a ceiling is that it moves when the
// engine changes what it does, and a fixture large enough to be interesting on
// its own would move it for reasons about the fixture instead. `simple` is a
// green run end to end; `killable` has survivors, which cost a whole-binary
// confirmation each under ADR 0010; `coverage` has an uncovered mutant, which
// costs nothing at all and is the one a regression would start executing; and
// `runaway` holds a mutant that does not leave its loop, which is the only
// fixture where the difference between measuring a timeout once and measuring
// it twice is a number.
var ceilingCorpus = []string{"simple", "killable", "coverage", "runaway"}

// TestTheWorkAWholeRunDoesIsCounted is the ratchet on how much a run does.
//
// Every subprocess go-mutants starts goes through one choke point and is
// recorded there with a kind, which ADR 0002 exists to guarantee. Counting them
// is therefore free, and this is the thing worth counting: **how many children
// a run starts is a property of the engine, and how long they take is a
// property of the machine.** A duration cannot be ratcheted -- it moves with
// load, with the disk, with whatever else the runner is doing -- and a count
// does not move at all unless the engine's behaviour does.
//
// So the golden holds counts and nothing else. No durations, no sizes, no
// wall clock. A change to it is a change to what a run does, and the diff is
// the review: an optimisation that skips work shows up as smaller numbers, and
// a regression that starts running something twice shows up as larger ones --
// on a laptop, on a loaded runner, and on a machine nobody has built yet.
func TestTheWorkAWholeRunDoesIsCounted(t *testing.T) {
	t.Parallel()

	var rendered strings.Builder
	for _, name := range ceilingCorpus {
		counts := execCounts(t, name)
		fmt.Fprintf(&rendered, "%s\n", name)
		kinds := make([]string, 0, len(counts))
		for kind := range counts {
			kinds = append(kinds, kind)
		}
		slices.Sort(kinds)
		for _, kind := range kinds {
			fmt.Fprintf(&rendered, "\t%s %d\n", kind, counts[kind])
		}
	}
	testkit.Golden(t, "work-ceiling.golden.txt", []byte(rendered.String()))
}

// TestEveryCountedKindIsOneTheContractNames keeps the ceiling from recording a
// label the schema would refuse.
//
// A kind is the one thing internal/runner cannot work out for itself, so it is
// the one thing every call site has to supply. An unlabelled spec is accepted
// by the runner and refused by the schema -- a missing diagnostic must not
// become a failed run -- which means a call site that forgot could show up here
// as a blank column rather than as an error anywhere.
func TestEveryCountedKindIsOneTheContractNames(t *testing.T) {
	t.Parallel()

	known := trace.ExecKinds()
	for _, name := range ceilingCorpus {
		for kind := range execCounts(t, name) {
			if !slices.Contains(known, kind) {
				t.Errorf("%s started a command of kind %q, which trace.ExecKinds does not name: %v",
					name, kind, known)
			}
		}
	}
}

// execCounts runs one corpus module with its recording on disk and returns how
// many commands of each kind it started.
//
// The count alone. ExecTally also carries a duration, and taking it would make
// this a test about the machine.
func execCounts(t *testing.T, name string) map[string]int {
	t.Helper()

	root := testkit.Copy(t, name)
	opts := optionsAt(t, root)

	traceRoot := filepath.Join(testkit.Scratch(t), "trace", name)
	sink, err := trace.NewDirSink(traceRoot, "run", trace.Filesystem{})
	if err != nil {
		t.Fatalf("opening a recording for %s: %v", name, err)
	}
	opts.TraceSink = sink

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("running %s: %v", name, err)
	}
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("closing the recording for %s: %v", name, closeErr)
	}
	if outcome.Report == nil {
		t.Fatalf("%s published no report", name)
	}

	summary, err := trace.ReadSummary(filepath.Join(traceRoot, "run", trace.FileName))
	if err != nil {
		t.Fatalf("reading the recording of %s: %v", name, err)
	}
	if summary.Missing {
		t.Fatalf("the recording of %s is not there", name)
	}
	// A recording that lost events would under-count, and an under-count is
	// the one direction this ratchet must never move silently.
	if !summary.HasRunEnd {
		t.Fatalf("the recording of %s has no run-end, so it is not the whole run", name)
	}
	if summary.EventsDropped != 0 || summary.MissingSequences != 0 {
		t.Fatalf("the recording of %s is lossy: %d dropped, %d missing",
			name, summary.EventsDropped, summary.MissingSequences)
	}

	counts := make(map[string]int, len(summary.ExecByKind))
	for kind, tally := range summary.ExecByKind {
		counts[kind] = tally.Count
	}
	if len(counts) == 0 {
		t.Fatalf("%s started no commands at all, so this counts nothing", name)
	}
	return counts
}
