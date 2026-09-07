// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// TestTraceDumpsTheRingTailWhenTheTestFails is what a recording is for in a
// test: the failure prints the last of what the run did, without anybody having
// to reproduce it.
//
// The tail is bounded because the ring is not. A recording of a mutation run
// holds thousands of events, and a failure that printed all of them would push
// the assertion that failed off the top of the log — which is the one thing
// worse than printing nothing.
func TestTraceDumpsTheRingTailWhenTheTestFails(t *testing.T) {
	// The policy is off, so this test is about the log alone: nothing is
	// written, and the real kept root is not touched.
	t.Setenv(testkit.KeepEnv, "")
	t.Setenv(testkit.KeepDirEnv, filepath.Join(t.TempDir(), "kept"))

	rec := &recorder{name: "TestSomethingTraced"}
	rec.run(func(tb testing.TB) {
		recorder := mutantkit.Trace(tb)
		for i := range 300 {
			recorder.Note("test", "GOM0000", fmt.Sprintf("note-%03d", i))
		}
	})
	rec.failed = true
	rec.finish()

	log := rec.log()
	lines := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "note-") {
			lines++
		}
	}
	switch {
	case lines == 0:
		t.Fatalf("a failing test was told nothing about its recording:\n%s", log)
	case lines > mutantkit.TraceTailLines:
		t.Errorf("the tail is %d lines, over the bound of %d", lines, mutantkit.TraceTailLines)
	}
	if !strings.Contains(log, "note-299") {
		t.Errorf("the tail does not end at the last event:\n%s", tailOf(log))
	}
	if strings.Contains(log, "note-000") {
		t.Errorf("the tail reaches back to the first event, so it is not bounded:\n%s", tailOf(log))
	}
}

// TestTraceWritesTheWholeRingIntoTheKeptDirectory is the other half: the log
// gets a tail, the kept directory gets the recording — in the encoding every
// reader of this project's traces already takes.
func TestTraceWritesTheWholeRingIntoTheKeptDirectory(t *testing.T) {
	t.Setenv(testkit.KeepEnv, "always")
	t.Setenv(testkit.KeepDirEnv, filepath.Join(t.TempDir(), "kept"))

	rec := &recorder{name: "TestTracedAndKept", failed: true}
	var kept string
	rec.run(func(tb testing.TB) {
		kept = testkit.Scratch(tb)
		recorder := mutantkit.Trace(tb)
		for i := range 5 {
			recorder.Note("test", "GOM0000", fmt.Sprintf("note-%03d", i))
		}
	})
	rec.finish()

	stream := filepath.Join(kept, trace.FileName)
	events, err := trace.Read(stream)
	if err != nil {
		t.Fatalf("reading the kept recording %s: %v", stream, err)
	}

	// Complete rather than merely readable. A recording with no run-end is what
	// an interrupted run leaves, and every reader of these files — `trace
	// summary`, `trace validate`, goatest — says so out loud; a harness that
	// closed none of its recordings would have every kept trace reporting the
	// test as having been killed. The run-end is also the only place the drop
	// tally is written, so without it a truncated ring cannot say it is one.
	summary, err := trace.ReadSummary(stream)
	if err != nil {
		t.Fatalf("summarising the kept recording %s: %v", stream, err)
	}
	if !summary.HasRunEnd {
		t.Errorf("the kept recording has no run-end, so every reader calls it incomplete: %+v", summary)
	}
	if summary.Verdict != "failed" {
		t.Errorf("verdict = %q, want the failed test's own verdict", summary.Verdict)
	}
	if summary.EventsDropped != 0 || summary.MissingSequences != 0 {
		t.Errorf("the recording says it lost events it did not lose: %+v", summary)
	}
	// One run-start, five notes and one run-end: the whole ring rather than the
	// tail the log was given.
	if len(events) != 7 {
		t.Fatalf("the kept recording holds %d events, want 7: %+v", len(events), events)
	}
	if events[0].Type != trace.TypeRunStart || events[0].Start == nil {
		t.Errorf("the recording does not open with a run-start: %+v", events[0])
	}
	if events[0].Start != nil && events[0].Start.Kind != trace.StartKindWorkspace {
		t.Errorf("start kind = %q, want %q", events[0].Start.Kind, trace.StartKindWorkspace)
	}
	if events[len(events)-1].Type != trace.TypeRunEnd {
		t.Errorf("the recording does not close with a run-end: %+v", events[len(events)-1])
	}
}

// tailOf keeps a failure message from being the wall of text the test is about.
func tailOf(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return "…" + s[len(s)-2000:]
}
