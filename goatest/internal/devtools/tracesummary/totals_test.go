// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const (
	firstBucketBoundary  = 2
	secondBucketBoundary = 4
	thirdBucketBoundary  = 8
	fourthBucketBoundary = 16
	fifthBucketBoundary  = 32
	sixthBucketBoundary  = 64
	beyondEveryBucket    = 4096

	deltaCallCount  = 3
	gammaDurationMS = 5
)

func TestAFanOutBucketHoldsThePowerOfTwoItsCountFallsIn(t *testing.T) {
	t.Parallel()
	labels := fanOutBucketLabels()
	for _, test := range []struct {
		reaching int
		want     string
	}{
		{reaching: -1, want: "0"},
		{reaching: 0, want: "0"},
		{reaching: 1, want: "1"},
		{reaching: firstBucketBoundary, want: "2-3"},
		{reaching: firstBucketBoundary + 1, want: "2-3"},
		{reaching: secondBucketBoundary, want: "4-7"},
		{reaching: thirdBucketBoundary, want: "8-15"},
		{reaching: fourthBucketBoundary, want: "16-31"},
		{reaching: fifthBucketBoundary, want: "32-63"},
		{reaching: sixthBucketBoundary, want: "64+"},
		{reaching: beyondEveryBucket, want: "64+"},
	} {
		t.Run(test.want+" holds "+strconv.Itoa(test.reaching), func(t *testing.T) {
			t.Parallel()
			if got := labels[fanOutBucketIndex(test.reaching)]; got != test.want {
				t.Fatalf("a route reaching %d targets fell in %q, want %q", test.reaching, got, test.want)
			}
		})
	}
}

func TestPrepareTotalsOrderByDurationThenFinishedThenStartedThenPhase(t *testing.T) {
	t.Parallel()
	prepare := func(seq int, phase, state, result string, duration string) string {
		payload := `{"phase":"` + phase + `","state":"` + state + `"`
		if result != "" {
			payload += `,"result":"` + result + `"`
		}
		if duration != "" {
			payload += `,"duration_ms":` + duration
		}
		return `{"seq":` + strconv.Itoa(seq) + `,"type":"prepare","timestamp":"2026-01-01T00:00:0` +
			strconv.Itoa(seq) + `Z","elapsed_ms":` + strconv.Itoa(seq) + `,"prepare":` + payload + `}}`
	}
	events, err := readEvents(strings.NewReader(stream(runStart,
		prepare(2, "binary_build", "started", "", ""),
		prepare(3, "binary_build", "finished", "succeeded", "0"),
		prepare(4, "discovery", "started", "", ""),
		prepare(5, "discovery", "finished", "succeeded", "0"),
		prepare(6, "verification", "started", "", ""),
		prepare(7, "verification", "started", "", ""),
		prepare(8, "verification", "finished", "failed", "0"),
		prepare(9, "probe_snapshot", "finished", "skipped", "5"),
	)))
	if err != nil {
		t.Fatal(err)
	}
	totals := prepareTotals(events)
	got := make([]string, 0, len(totals))
	for _, total := range totals {
		got = append(got, total.phase)
	}
	want := []string{"probe_snapshot", "verification", "binary_build", "discovery"}
	if !slices.Equal(got, want) {
		t.Fatalf("prepare totals read %q, want %q: the longest first, then the most finished, "+
			"then the most started, then by name", got, want)
	}
	for _, total := range totals {
		switch total.phase {
		case "verification":
			if total.started != firstBucketBoundary || total.failed != 1 {
				t.Errorf("gamma counted %d started and %d failed, want 2 and 1", total.started, total.failed)
			}
		case "probe_snapshot":
			if total.skipped != 1 || total.duration == 0 {
				t.Errorf("delta counted %d skipped over %dms, want one and a duration", total.skipped, total.duration)
			}
		case "discovery", "binary_build":
			if total.succeeded != 1 {
				t.Errorf("%s counted %d succeeded, want one", total.phase, total.succeeded)
			}
		}
	}
}

func TestExecTotalsOrderByDurationThenCallsThenClass(t *testing.T) {
	t.Parallel()
	exec := func(seq int, argv []string, duration int64) trace.Event {
		return trace.Event{
			Seq: int64(seq), Type: trace.TypeExec, ElapsedMS: int64(seq),
			Exec: &trace.ExecRecord{Argv: argv, DurationMS: duration},
		}
	}
	totals := execTotals([]trace.Event{
		exec(2, []string{"beta"}, 1),
		exec(3, []string{"alpha"}, 1),
		exec(4, []string{"alpha"}, 0),
		exec(5, []string{"gamma"}, gammaDurationMS),
		exec(6, []string{"delta"}, 1),
		exec(7, []string{"delta"}, 0),
		exec(8, []string{"delta"}, 0),
	})
	got := make([]string, 0, len(totals))
	for _, total := range totals {
		got = append(got, total.class)
	}
	want := []string{"gamma", "delta", "alpha", "beta"}
	if !slices.Equal(got, want) {
		t.Fatalf("exec totals read %q, want %q: the longest first, then the most called, then by name",
			got, want)
	}
	for _, total := range totals {
		switch total.class {
		case "delta":
			if total.calls != deltaCallCount {
				t.Errorf("delta counted %d calls, want three", total.calls)
			}
		case "gamma":
			if total.duration != gammaDurationMS {
				t.Errorf("gamma took %dms, want five", total.duration)
			}
		}
	}
	if totals := execTotals(nil); len(totals) != 0 {
		t.Fatalf("a recording with no execution totalled %+v, want none", totals)
	}
}
