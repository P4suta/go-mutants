// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func emptyComparisonReport() report.Report {
	return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictInsufficient}
}

func TestEveryBlockSaysSoWhenItHasNothingToShow(t *testing.T) {
	t.Parallel()
	nothing := compare(emptyComparisonReport(), emptyComparisonReport())
	if got := statusBlock(nothing); !slices.Equal(got, []string{"status transitions", "no mutant is in both reports"}) {
		t.Fatalf("status block of two empty reports = %q", got)
	}
	if got := regressionBlock(nothing); !slices.Equal(got, []string{"regressions", "no mutant that was killed stopped being killed"}) {
		t.Fatalf("regression block of two empty reports = %q", got)
	}
	if got := kindBlock(nothing); !slices.Equal(got, []string{
		"finding kinds per mutant", "no mutant is in both reports",
		"", "finding kinds by count", "neither report carries a finding",
	}) {
		t.Fatalf("kind block of two empty reports = %q", got)
	}
}

func TestEveryBlockCarriesItsHeadingHoweverEmptyTheComparison(t *testing.T) {
	t.Parallel()
	nothing := compare(emptyComparisonReport(), emptyComparisonReport())
	for name, block := range map[string][]string{
		"header":     headerBlock("before.json", "after.json", nothing),
		"accounting": accountingBlock(nothing),
		"inventory":  inventoryBlock(nothing),
		"status":     statusBlock(nothing),
		"kind":       kindBlock(nothing),
		"regression": regressionBlock(nothing),
	} {
		if len(block) == 0 {
			t.Fatalf("the %s block is empty, so the renderer would join two separators", name)
		}
	}
}

func TestRenderComparisonSaysNothingOfTheKindWhenThereIsSomethingToCompare(t *testing.T) {
	t.Parallel()
	before, after := sampleReports()
	rendered := renderComparison("before.json", "after.json", compare(before, after))
	for _, sentence := range []string{
		"no mutant is in both reports",
		"neither report carries a finding",
		"no mutant that was killed stopped being killed",
	} {
		if strings.Contains(rendered, sentence) {
			t.Fatalf("rendered comparison of two populated reports says %q:\n%s", sentence, rendered)
		}
	}
}

func TestRenderTableHoldsAHeadingWithNoRowsBeneathIt(t *testing.T) {
	t.Parallel()
	lines := renderTable([]column{{"kind", false}, {"count", true}}, nil)
	if !slices.Equal(lines, []string{"kind  count"}) {
		t.Fatalf("table with no rows = %q", lines)
	}
}

func TestOrNoValueNamesTheAbsenceOfAValue(t *testing.T) {
	t.Parallel()
	if got := orNoValue(""); got != noValue {
		t.Fatalf("orNoValue(\"\") = %q, want %q", got, noValue)
	}
	if got := orNoValue("rule"); got != "rule" {
		t.Fatalf("orNoValue(%q) = %q", "rule", got)
	}
}

func TestCompareCountsAMutantOnceHoweverOftenAReportNamesIt(t *testing.T) {
	t.Parallel()
	before := emptyComparisonReport()
	before.Mutants = []report.MutantDisposition{
		mutantAt("m-01", report.MutantKilled, "value.go", 1),
		mutantAt("m-01", report.MutantSurvived, "value.go", 1),
	}
	after := emptyComparisonReport()
	after.Mutants = []report.MutantDisposition{
		mutantAt("m-01", report.MutantSurvived, "value.go", 1),
		mutantAt("m-01", report.MutantKilled, "value.go", 1),
	}
	result := compare(before, after)
	if result.commonMutants != 1 || result.onlyBefore != 0 || result.onlyAfter != 0 {
		t.Fatalf("comparison = %+v, want the repeated mutant counted once", result)
	}
	if len(result.statuses) != 1 ||
		result.statuses[0].before != report.MutantKilled || result.statuses[0].after != report.MutantSurvived {
		t.Fatalf("status transitions = %+v, want the first disposition of each report kept", result.statuses)
	}
}

func TestCompareIgnoresAFindingThatNamesNoMutant(t *testing.T) {
	t.Parallel()
	before := emptyComparisonReport()
	before.Mutants = []report.MutantDisposition{mutantAt("m-01", report.MutantSurvived, "value.go", 1)}
	before.Findings = []report.Finding{findingOf("surviving-mutant", ""), findingOf("surviving-mutant", "m-01")}
	after := emptyComparisonReport()
	after.Mutants = before.Mutants
	after.Findings = []report.Finding{findingOf("surviving-mutant", "m-01")}
	result := compare(before, after)
	if len(result.kinds) != 1 || result.kinds[0].before != "surviving-mutant" || result.kinds[0].mutants != 1 {
		t.Fatalf("kind transitions = %+v, want only the finding that names a mutant", result.kinds)
	}
}

func TestRegressionsAreOrderedByPathThenLineThenIdentity(t *testing.T) {
	t.Parallel()
	before := emptyComparisonReport()
	after := emptyComparisonReport()
	for _, mutant := range []struct {
		id   string
		path string
		line int
	}{
		{id: "m-b", path: "b.go", line: 1},
		{id: "m-a0", path: "a.go", line: 9},
		{id: "m-a1b", path: "a.go", line: 1},
		{id: "m-a1a", path: "a.go", line: 1},
	} {
		before.Mutants = append(before.Mutants, mutantAt(mutant.id, report.MutantKilled, mutant.path, mutant.line))
		after.Mutants = append(after.Mutants, mutantAt(mutant.id, report.MutantSurvived, mutant.path, mutant.line))
	}
	result := compare(before, after)
	order := make([]string, 0, len(result.regressions))
	for _, regression := range result.regressions {
		order = append(order, regression.id)
	}
	if !slices.Equal(order, []string{"m-a1a", "m-a1b", "m-a0", "m-b"}) {
		t.Fatalf("regression order = %v", order)
	}
}

func TestLoadReportDistinguishesADocumentItCannotDecodeFromOneWithMoreThanOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		document string
		message  string
	}{
		{name: "not an object at all", document: `{`, message: "decode "},
		{name: "a second document", document: `{"schema":"goatest-report-v1"}{"schema":"goatest-report-v1"}`, message: "has trailing data"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "report.json")
			if err := os.WriteFile(path, []byte(test.document), filemode.ReadableFile); err != nil {
				t.Fatal(err)
			}
			_, err := loadReport(path)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("loadReport = %v, want %q", err, test.message)
			}
		})
	}
}
