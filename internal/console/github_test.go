// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

// Both halves of the GitHub output are goldens.
//
// They are read by a machine — GitHub parses the workflow commands and renders
// the Markdown — so the exact spelling of `::warning file=` and the exact shape
// of a table row are the whole of what makes them work, and a test that
// asserted "contains the word survived" would pass through every way of getting
// them wrong.

// githubFixture is a run with one killed mutant, two survivors, one of which
// the expectations ledger predicted, and a not-run mutant that must not be
// annotated at all.
func githubFixture() *report.Report {
	score := 50.0
	return &report.Report{
		DocumentType:  report.DocumentType,
		SchemaVersion: report.SchemaVersion,
		Summary: report.Summary{
			Total: 4, Killed: 1, Survived: 2, TimedOut: 0,
			Inconclusive: 0, Errored: 0, NotRun: 1,
			ScorePercent: &score,
		},
		Mutants: []report.Mutant{
			{
				ID: strings.Repeat("11", 32), DisplayID: "11223344",
				Path: "internal/alpha/alpha.go", Line: 10, Column: 7,
				Family: "comparison", Rule: "eq-to-neq",
				Original: "==", Replacement: "!=", Outcome: report.OutcomeKilled,
			},
			{
				ID: strings.Repeat("22", 32), DisplayID: "22334455",
				Path: "internal/alpha/alpha.go", Line: 12, Column: 9,
				Family: "boolean-connective", Rule: "or-to-and",
				Original: "||", Replacement: "&&", Outcome: report.OutcomeSurvived,
			},
			{
				// Declared equivalent, with a reason, and therefore not
				// annotated: a warning on this line would teach a reviewer to
				// scroll past all of them.
				ID: strings.Repeat("33", 32), DisplayID: "33445566",
				Path: "internal/beta/beta.go", Line: 4, Column: 2,
				Family: "statement-deletion", Rule: "delete-call-statement",
				Original: "log.Print(\"x\")", Replacement: "", Outcome: report.OutcomeSurvived,
			},
			{
				ID: strings.Repeat("44", 32), DisplayID: "44556677",
				Path: "internal/beta/beta.go", Line: 9, Column: 5,
				Family: "comparison", Rule: "lt-to-le",
				Original: "<", Replacement: "<=", Outcome: report.OutcomeNotRun,
			},
		},
		Expectations: []report.Expectation{{
			ID:     strings.Repeat("33", 32),
			Reason: "Equivalent: the log line has no observable effect.",
			State:  report.StateFulfilled,
		}},
	}
}

// TestGitHubAnnotationsGolden pins the workflow commands, byte for byte.
func TestGitHubAnnotationsGolden(t *testing.T) {
	t.Parallel()

	want := "::warning file=internal/alpha/alpha.go,line=12,col=9::mutant 22334455 survived (or-to-and || -> &&)\n"
	if got := GitHubAnnotations(githubFixture()); got != want {
		t.Errorf("GitHubAnnotations() =\n%q\nwant\n%q", got, want)
	}
}

// TestGitHubAnnotationsSkipTheThreeThatAreNotUnexpectedSurvivors states, one
// mutant at a time, which rows produce a marker in a reviewer's diff.
func TestGitHubAnnotationsSkipTheThreeThatAreNotUnexpectedSurvivors(t *testing.T) {
	t.Parallel()

	got := GitHubAnnotations(githubFixture())
	for id, why := range map[string]string{
		"11223344": "it was killed",
		"33445566": "the expectations ledger predicted it",
		"44556677": "it was never run",
	} {
		if strings.Contains(got, id) {
			t.Errorf("mutant %s was annotated, and %s", id, why)
		}
	}
}

// TestGitHubAnnotationsEscape covers the characters that would otherwise end a
// workflow command early or start a property nobody wrote.
func TestGitHubAnnotationsEscape(t *testing.T) {
	t.Parallel()

	fixture := githubFixture()
	fixture.Mutants[1].Original = "a := 50% of\nthe, thing"
	fixture.Mutants[1].Path = "internal/od,d:name/x.go"

	got := GitHubAnnotations(fixture)
	if strings.Count(got, "\n") != 1 {
		t.Errorf("the annotation spans more than one line:\n%q", got)
	}
	for _, want := range []string{
		"file=internal/od%2Cd%3Aname/x.go", // the separators of the property list
		"%25",                              // the per cent sign
		`\n`,                               // the newline, already quoted by FormatText
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the annotation does not contain %q:\n%s", want, got)
		}
	}
}

// TestEscapeDataAndProperty states the two escapes on their own.
//
// A mutant's text reaches the message through [FormatText], which quotes
// anything with a newline in it, so the raw-newline case cannot arrive from a
// run go-mutants performed. It is escaped anyway and tested here: a report is a
// file, a file can be edited, and one raw newline in the wrong place turns the
// rest of a command into a line GitHub tries to interpret.
func TestEscapeDataAndProperty(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in       string
		data     string
		property string
	}{
		"nothing to do": {in: "plain", data: "plain", property: "plain"},
		"per cent":      {in: "50%", data: "50%25", property: "50%25"},
		"newline":       {in: "a\nb", data: "a%0Ab", property: "a%0Ab"},
		"carriage":      {in: "a\r\nb", data: "a%0D%0Ab", property: "a%0D%0Ab"},
		"separators":    {in: "a,b:c", data: "a,b:c", property: "a%2Cb%3Ac"},
		// The per cent is escaped first, so its own escape is not escaped again.
		"an escape in the input": {in: "%0A", data: "%250A", property: "%250A"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := escapeData(tc.in); got != tc.data {
				t.Errorf("escapeData(%q) = %q, want %q", tc.in, got, tc.data)
			}
			if got := escapeProperty(tc.in); got != tc.property {
				t.Errorf("escapeProperty(%q) = %q, want %q", tc.in, got, tc.property)
			}
		})
	}
}

// TestGitHubStepSummaryGolden pins the Markdown.
func TestGitHubStepSummaryGolden(t *testing.T) {
	t.Parallel()

	want := strings.Join([]string{
		"## go-mutants",
		"",
		"**Score 50.00%** — 1 of 3 detected.",
		"",
		"| Mutants | Killed | Survived | Timeout | Inconclusive | Errored | Not run |",
		"| ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
		"| 4 | 1 | 2 | 0 | 0 | 0 | 1 |",
		"",
		"### Survivors",
		"",
		"| Mutant | Location | Mutation |",
		"| --- | --- | --- |",
		"| `22334455` | `internal/alpha/alpha.go:12:9` | `or-to-and \\|\\| -> &&` |",
		"",
	}, "\n")
	if got := GitHubStepSummary(githubFixture()); got != want {
		t.Errorf("GitHubStepSummary() =\n%s\n--- want ---\n%s", got, want)
	}
}

// TestGitHubStepSummaryWithNoScore proves the summary says so plainly rather
// than printing a sentinel percentage. Both plausible sentinels are lies.
func TestGitHubStepSummaryWithNoScore(t *testing.T) {
	t.Parallel()

	fixture := githubFixture()
	fixture.Summary.ScorePercent = nil
	got := GitHubStepSummary(fixture)
	if !strings.Contains(got, "**Score N/A**") {
		t.Errorf("the summary does not say the score is undefined:\n%s", got)
	}
	if strings.Contains(got, "0.00%") || strings.Contains(got, "100.00%") {
		t.Errorf("the summary invented a score:\n%s", got)
	}
}

// TestGitHubStepSummaryWithNothingToAct on states the good news in a sentence
// rather than as an empty table.
func TestGitHubStepSummaryWithNothingToActOn(t *testing.T) {
	t.Parallel()

	fixture := githubFixture()
	for i := range fixture.Mutants {
		fixture.Mutants[i].Outcome = report.OutcomeKilled
	}
	got := GitHubStepSummary(fixture)
	if !strings.Contains(got, "No mutant survived unexpectedly.") {
		t.Errorf("the summary does not say the run was clean:\n%s", got)
	}
	if strings.Contains(got, "### Survivors") {
		t.Errorf("the summary has an empty survivors table:\n%s", got)
	}
}

// TestGitHubStepSummaryCapsTheTable keeps a job page readable when a run finds
// hundreds of survivors, and says how many were left out rather than trailing
// off.
func TestGitHubStepSummaryCapsTheTable(t *testing.T) {
	t.Parallel()

	fixture := githubFixture()
	template := fixture.Mutants[1]
	for i := 0; i < 25; i++ {
		extra := template
		extra.ID = strings.Repeat("ab", 31) + string(rune('a'+i%26)) + "0"
		extra.DisplayID = "extra" + string(rune('a'+i%26)) + "00"
		extra.Line = 100 + i
		fixture.Mutants = append(fixture.Mutants, extra)
	}
	got := GitHubStepSummary(fixture)
	rows := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "| `") {
			rows++
		}
	}
	if rows != maxAnnotatedSurvivors {
		t.Errorf("the table has %d rows, want %d", rows, maxAnnotatedSurvivors)
	}
	if !strings.Contains(got, "16 more in the full report.") {
		t.Errorf("the summary does not say how many were left out:\n%s", got)
	}
	// Every one of them still gets an annotation: those are attached to the
	// lines they belong to, and a reviewer only sees the ones in the file they
	// are looking at.
	if lines := strings.Count(GitHubAnnotations(fixture), "\n"); lines != 26 {
		t.Errorf("%d annotations were emitted, want 26", lines)
	}
}

// TestEmitGitHubAppends is the file half, against a real file with something
// already in it: several steps of one job write to the same summary, and a
// mutation run that truncated it would be a bad neighbour.
func TestEmitGitHubAppends(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "summary.md")
	const existing = "## build\n\nEverything compiled.\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("writing the existing summary: %v", err)
	}

	var out bytes.Buffer
	if err := EmitGitHub(&out, path, githubFixture()); err != nil {
		t.Fatalf("EmitGitHub: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the summary: %v", err)
	}
	if !strings.HasPrefix(string(got), existing) {
		t.Errorf("the earlier step's summary was overwritten:\n%s", got)
	}
	if !strings.Contains(string(got), "## go-mutants") {
		t.Errorf("the run's summary was not appended:\n%s", got)
	}
	if out.String() != GitHubAnnotations(githubFixture()) {
		t.Errorf("the annotations written to the stream are not the ones rendered:\n%q", out.String())
	}
}

// TestEmitGitHubCreatesAMissingSummaryFile covers running the same workflow
// outside a runner, where nothing has made the file.
func TestEmitGitHubCreatesAMissingSummaryFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "summary.md")
	if err := EmitGitHub(&bytes.Buffer{}, path, githubFixture()); err != nil {
		t.Fatalf("EmitGitHub: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the summary file was not created: %v", err)
	}
}

// TestEmitGitHubWithoutASummaryPathStillAnnotates keeps the two halves
// independent: the markers are the half a reviewer actually sees.
func TestEmitGitHubWithoutASummaryPathStillAnnotates(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := EmitGitHub(&out, "", githubFixture()); err != nil {
		t.Fatalf("EmitGitHub: %v", err)
	}
	if !strings.Contains(out.String(), "::warning ") {
		t.Errorf("no annotation was emitted:\n%q", out.String())
	}
}

// TestEmitGitHubWithNoReport is the caller's slip, which writes nothing rather
// than dereferencing nothing.
func TestEmitGitHubWithNoReport(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := EmitGitHub(&out, filepath.Join(t.TempDir(), "summary.md"), nil); err != nil {
		t.Fatalf("EmitGitHub(nil) = %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Errorf("EmitGitHub(nil) wrote %q", out.String())
	}
}
