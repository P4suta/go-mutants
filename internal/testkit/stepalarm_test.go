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

var goTestTimeout = regexp.MustCompile(`-timeout ([0-9]+[smh])`)

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

func TestTheStepAlarmIsNotAPerformanceGate(t *testing.T) {
	t.Parallel()

	const floor = 2 * time.Minute
	if DefaultTimeout < floor {
		t.Errorf("the step alarm is %s, want at least %s: below that it stops being an alarm "+
			"for a hung child and becomes a verdict on how loaded the machine was",
			DefaultTimeout, floor)
	}
}

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
