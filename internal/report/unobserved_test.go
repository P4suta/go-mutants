// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

func unobservedOptions(t *testing.T) (report.Options, int) {
	t.Helper()

	opts := coverageOptions(t)
	for i := range opts.Results {
		result := &opts.Results[i]
		if result.Uncovered || result.Outcome != mutation.OutcomeSurvived || result.Attempts != 0 {
			continue
		}
		result.Unobserved = true
		return opts, i
	}
	for i := range opts.Results {
		result := &opts.Results[i]
		if !result.Uncovered {
			continue
		}
		result.Uncovered = false
		result.Unobserved = true
		return opts, i
	}
	t.Fatal("the coverage fixture holds no survivor to settle")
	return report.Options{}, 0
}

func TestAnUnobservedSurvivorIsWrittenAndIsNotUncovered(t *testing.T) {
	t.Parallel()

	opts, settled := unobservedOptions(t)
	id := opts.Results[settled].ID

	built, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for _, m := range built.Mutants {
		if m.ID != id {
			continue
		}
		found = true
		if !m.Unobserved {
			t.Errorf("mutant %s is not marked unobserved", m.DisplayID)
		}
		if m.Uncovered {
			t.Errorf("mutant %s is marked uncovered as well", m.DisplayID)
		}
		if m.Outcome != report.OutcomeSurvived || m.Attempts != 0 {
			t.Errorf("mutant %s is %s after %d attempts", m.DisplayID, m.Outcome, m.Attempts)
		}
	}
	if !found {
		t.Fatalf("the document holds no mutant %s", id[:12])
	}
}

func TestBuildRefusesAnUnobservedMutantThatCannotBeOne(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		edit func(*report.MutantResult)
		want string
	}{
		"both uncovered and unobserved": {
			edit: func(r *report.MutantResult) { r.Uncovered = true },
			want: "is marked both uncovered and unobserved",
		},
		"unobserved and killed": {
			edit: func(r *report.MutantResult) {
				r.Outcome = mutation.OutcomeKilled
				r.Attempts = 0
			},
			want: "is marked unobserved but is killed after 0 attempts",
		},
		"unobserved with attempts": {
			edit: func(r *report.MutantResult) { r.Attempts = 2 },
			want: "is marked unobserved but is survived after 2 attempts",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts, settled := unobservedOptions(t)
			tc.edit(&opts.Results[settled])

			_, err := report.Build(opts)
			if got := report.CodeOf(err); got != report.CodeInvalidCoverage {
				t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidCoverage)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say what was wrong: %v", err)
			}
		})
	}
}

func TestBuildRefusesAnUnobservedMutantWithExecutions(t *testing.T) {
	t.Parallel()

	opts, settled := unobservedOptions(t)
	opts.Results[settled].Attempts = 1
	opts.Results[settled].Executions = []report.Execution{{
		Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 0,
	}}

	_, err := report.Build(opts)
	if got := report.CodeOf(err); got != report.CodeInvalidExecutions {
		t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidExecutions)
	}
	if !strings.Contains(err.Error(), "is marked unobserved and carries 1 execution") {
		t.Errorf("the refusal does not say what was wrong: %v", err)
	}
}
