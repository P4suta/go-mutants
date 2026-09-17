// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// goTestTimeout finds the `-timeout <duration>` of a `go test` command line.
var goTestTimeout = regexp.MustCompile(`-timeout ([0-9]+[smh])`)

// TestTheStepAlarmFiresBeforeEveryTaskBudget is the half of
// [DefaultTimeout]'s promise that is not about a number at all.
//
// The alarm exists so that a child that hangs is reported as a named step with
// its output quoted. That only happens if it fires *first*: once `go test`'s own
// per-binary alarm goes off, the test is dead and what a reader gets instead is
// a panic with every goroutine in the process dumped after it. So the alarm has
// to be shorter than every budget any task in this repository gives a `go test`
// -- and that is a relationship between two configured numbers, which is
// something a test can hold, rather than a claim about how fast a machine is,
// which is not.
//
// Reading the budgets out of mise.toml rather than listing them here is the
// point: a task added with a tighter budget than the alarm is exactly the change
// this has to catch, and a copy of the list would not catch it.
func TestTheStepAlarmFiresBeforeEveryTaskBudget(t *testing.T) {
	t.Parallel()

	source := ReadFile(t, filepath.Join(Root(t), "mise.toml"))
	var budgets []time.Duration
	for _, match := range goTestTimeout.FindAllStringSubmatch(string(source), -1) {
		budget, err := time.ParseDuration(match[1])
		if err != nil {
			t.Fatalf("mise.toml sets -timeout %q, which is not a duration: %v", match[1], err)
		}
		budgets = append(budgets, budget)
	}
	if len(budgets) == 0 {
		t.Fatalf("mise.toml sets no `go test -timeout`; the scan has stopped seeing them")
	}
	for _, budget := range budgets {
		if DefaultTimeout >= budget {
			t.Errorf("the step alarm is %s and a task budgets a whole `go test` at %s: "+
				"a hung step would be reported as a goroutine dump rather than as a step",
				DefaultTimeout, budget)
		}
	}
}

// TestTheStepAlarmIsNotAPerformanceGate states the doctrine the number obeys,
// in the only form a test can state it: an alarm that a slow machine can reach
// is a gate on the machine, so the alarm has to be far above any step this
// suite drives rather than near the top of one.
//
// The bound is deliberately crude -- a step here is a build or a run of a
// fixture module of a few dozen lines, and minutes are not what those cost on
// any machine. What the number must never be is *tuned*, because tuning it is
// measuring the machine that did the tuning. This fails if someone tightens it
// back towards the region where load decides the verdict.
func TestTheStepAlarmIsNotAPerformanceGate(t *testing.T) {
	t.Parallel()

	const floor = 2 * time.Minute
	if DefaultTimeout < floor {
		t.Errorf("the step alarm is %s, want at least %s: below that it stops being an alarm "+
			"for a hung child and becomes a verdict on how loaded the machine was",
			DefaultTimeout, floor)
	}
}

// TestEveryTaskBudgetIsWrittenInOneOfTheUnitsThisScanReads is the scan's own
// guard, and it is here because the test above passes vacuously if the regexp
// stops matching.
//
// `go test -timeout` accepts any duration Go parses, including `90s` and `1h30m`
// and `0`. The scan reads a single integer and one unit, which covers every
// budget in this repository; a budget written some other way would be skipped
// silently and the alarm would go unchecked against it.
func TestEveryTaskBudgetIsWrittenInOneOfTheUnitsThisScanReads(t *testing.T) {
	t.Parallel()

	source := string(ReadFile(t, filepath.Join(Root(t), "mise.toml")))
	for _, line := range strings.Split(source, "\n") {
		index := strings.Index(line, "-timeout ")
		if index < 0 {
			continue
		}
		if !goTestTimeout.MatchString(line[index:]) {
			t.Errorf("mise.toml line %q sets a -timeout this scan cannot read, so the step "+
				"alarm is not checked against it", strings.TrimSpace(line))
		}
	}
}
