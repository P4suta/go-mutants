// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const (
	firstElapsedMS  = 5
	secondElapsedMS = 9
	routeCallCount  = 2
	projectedLabels = 3
)

func TestAHeaderNamesTheSchemaOnlyWhenTheRecordingCarriesOne(t *testing.T) {
	t.Parallel()
	with := headerBlock("trace.jsonl", []trace.Event{
		{Type: trace.TypeRunStart, Schema: "goatest-trace-v1", ElapsedMS: firstElapsedMS},
		{Type: trace.TypeRunEnd, ElapsedMS: secondElapsedMS},
	})
	if !strings.Contains(strings.Join(with, "\n"), "schema: goatest-trace-v1") {
		t.Fatalf("a recording that names its schema rendered %q", with)
	}
	without := headerBlock("trace.jsonl", []trace.Event{{Type: trace.TypeRunStart}})
	if strings.Contains(strings.Join(without, "\n"), "schema:") {
		t.Fatalf("a recording that names no schema rendered %q", without)
	}
}

func TestProbedLinesAreWrittenOnlyForRoutesThereWere(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		routes int
		want   string
	}{
		{name: "no route at all", routes: 0},
		{name: "a route below zero", routes: -1},
		{name: "one route", routes: 1, want: "probed: 1 route"},
		{name: "two routes", routes: routeCallCount, want: "probed: 2 routes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := probedLines(test.routes)
			if test.want == "" {
				if lines != nil {
					t.Fatalf("probedLines(%d) = %q, want nothing", test.routes, lines)
				}
				return
			}
			if len(lines) != 1 || lines[0] != test.want {
				t.Fatalf("probedLines(%d) = %q, want %q", test.routes, lines, test.want)
			}
		})
	}
}

func TestAnExecClassKeepsTheFirstWordsAndSaysThereWereMore(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		argv []string
		want string
	}{
		{name: "no command at all", want: noCommand},
		{name: "one word", argv: []string{"go"}, want: "go"},
		{
			name: "exactly the words it keeps",
			argv: []string{"go", "test", "-run", "TestOne", "-count", "1"},
			want: "go test -run TestOne -count 1",
		},
		{
			name: "one word more than it keeps",
			argv: []string{"go", "test", "-run", "TestOne", "-count", "1", "./..."},
			want: "go test -run TestOne -count 1 " + ellipsis,
		},
		{
			name: "an absolute path among them",
			argv: []string{"/usr/local/bin/go", "test"}, want: "<path> test",
		},
		{
			name: "a Windows path among them",
			argv: []string{`C:\tools\go.exe`, "test"}, want: "<path> test",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := execClass(test.argv); got != test.want {
				t.Fatalf("execClass(%q) = %q, want %q", test.argv, got, test.want)
			}
		})
	}
}

func TestATallyProjectsEveryLabelItWasAskedForInThatOrder(t *testing.T) {
	t.Parallel()
	counts := map[string]int{"measured": 2, "unavailable": 1, "invented": 9}
	projected := tally(counts, "measured", "timed-out", "unavailable")
	if len(projected) != projectedLabels {
		t.Fatalf("tally projected %d labels, want the three it was asked for: %+v", len(projected), projected)
	}
	if projected[0].label != "measured" || projected[0].count != routeCallCount {
		t.Errorf("the first label is %+v, want measured 2", projected[0])
	}
	if projected[1].label != "timed-out" || projected[1].count != 0 {
		t.Errorf("a label nothing counted is %+v, want it named with no count", projected[1])
	}
	if rendered := formatLabelCounts(projected); rendered != "measured 2, timed-out 0, unavailable 1" {
		t.Fatalf("formatLabelCounts = %q, want every label it was given", rendered)
	}
	if rendered := formatLabelCounts(nil); rendered != "" {
		t.Fatalf("formatLabelCounts of nothing = %q, want nothing", rendered)
	}
}

func TestAControlBlockCountsAnOutcomeOrAnErrorAndNeverBoth(t *testing.T) {
	t.Parallel()
	control := func(outcome, failure string) trace.Event {
		return trace.Event{
			Type: trace.TypeProbeExec,
			Probe: &trace.ProbeRecord{
				Target: "exact-original:example.com/app", Package: "example.com/app",
				Control: true, Outcome: outcome, Error: failure, DurationMS: 1,
			},
		}
	}
	lines := controlBlock([]trace.Event{
		control(trace.ProbeOutcomeMeasured, ""),
		control("", "the process would not start"),
		{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{Target: "TestOne", Outcome: trace.ProbeOutcomeMeasured}},
		{Type: trace.TypeRunEnd},
	})
	rendered := strings.Join(lines, "\n")
	if !strings.Contains(rendered, "2 executions") {
		t.Fatalf("the control block counted something other than the two controls:\n%s", rendered)
	}
	if !strings.Contains(rendered, "measured 1") || !strings.Contains(rendered, probeError+" 1") {
		t.Fatalf("the control block rendered %q, want one measured and one error", rendered)
	}
	if controlBlock([]trace.Event{{Type: trace.TypeRunEnd}}) != nil {
		t.Fatal("a recording with no control rendered a control block")
	}
}
