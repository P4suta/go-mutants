// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

type familyTally struct{ killed, survived int }

var familiesTable = map[string]familyTally{
	string(mutation.FamilyBooleanLiteral):    {killed: 2, survived: 2},
	string(mutation.FamilyConditionNegation): {killed: 8, survived: 1},
	string(mutation.FamilyBooleanConnective): {killed: 3, survived: 0},
	string(mutation.FamilyComparison):        {killed: 11, survived: 0},
	string(mutation.FamilyIntegerArithmetic): {killed: 9, survived: 2},
	string(mutation.FamilyFloatArithmetic):   {killed: 4, survived: 0},
	string(mutation.FamilyReturnReplacement): {killed: 18, survived: 3},
	string(mutation.FamilyErrorSwallowing):   {killed: 4, survived: 0},
	string(mutation.FamilyNeutralValue):      {killed: 2, survived: 1},
	string(mutation.FamilyBranchReplacement): {killed: 12, survived: 2},
	string(mutation.FamilyBitwise):           {killed: 6, survived: 2},
	string(mutation.FamilyArithmeticAssign):  {killed: 7, survived: 1},
	string(mutation.FamilyLabeledBranch):     {killed: 2, survived: 0},
	string(mutation.FamilyStatementDeletion): {killed: 7, survived: 0},
}

const (
	familiesMutants   = 111
	familiesKilled    = 95
	familiesSurvived  = 16
	familiesUncovered = 2

	familiesBalanced = 69
	familiesStrong   = 102
)

func TestFamiliesRunReachesEveryOperatorFamily(t *testing.T) {
	t.Parallel()
	opts := options(t, "families")
	opts.Config.Mutation.Profile = mutation.TierAll
	opts.Config.Execution.Jobs = 4

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}
	if profile := outcome.Report.Selection.Profile; profile != mutation.TierAll.String() {
		t.Errorf("the report records profile %q, want %q", profile, mutation.TierAll)
	}

	if len(outcome.Report.Rejected) != 0 {
		t.Errorf("rejected = %+v, want none: every family in this fixture instruments", outcome.Report.Rejected)
	}

	summary := outcome.Report.Summary
	if summary.Total != familiesMutants || summary.Killed != familiesKilled || summary.Survived != familiesSurvived {
		t.Errorf("summary = %+v, want %d mutants, %d killed, %d survived",
			summary, familiesMutants, familiesKilled, familiesSurvived)
	}
	if summary.NotRun != 0 || summary.Errored != 0 || summary.Inconclusive != 0 {
		t.Errorf("summary = %+v, want every mutant settled as killed or survived", summary)
	}
	if summary.TimedOut != 0 {
		t.Errorf("%d mutants timed out: a loop in the fixture no longer terminates under every mutant of it",
			summary.TimedOut)
	}
	if want := float64(familiesKilled) / float64(familiesMutants) * 100; summary.ScorePercent == nil ||
		*summary.ScorePercent != want {
		t.Errorf("score = %v, want %v (%d of %d)", summary.ScorePercent, want, familiesKilled, familiesMutants)
	}

	got := make(map[string]familyTally, len(familiesTable))
	for _, m := range outcome.Report.Mutants {
		if m.Uncovered {
			continue
		}
		row := got[m.Family]
		switch m.Outcome {
		case report.OutcomeKilled:
			row.killed++
		case report.OutcomeSurvived:
			row.survived++
		default:
			t.Errorf("mutant %s (%s) settled as %s, want killed or survived", m.DisplayID, m.Rule, m.Outcome)
		}
		got[m.Family] = row
	}
	if !maps.Equal(got, familiesTable) {
		t.Errorf("per-family results =\n\t%s\nwant\n\t%s", renderTally(got), renderTally(familiesTable))
	}

	for _, family := range mutation.CanonicalRegistry().Families() {
		if got[string(family)].killed == 0 {
			t.Errorf("the %s family contributed no executed, killed mutant: %+v", family, got[string(family)])
		}
	}

	fired := make(map[string]bool, mutation.CanonicalRuleCount)
	for _, m := range outcome.Report.Mutants {
		fired[m.Rule] = true
	}
	var missing []string
	for _, rule := range mutation.CanonicalRules() {
		if !fired[rule.Name] {
			missing = append(missing, rule.Name)
		}
	}
	if len(missing) != 0 {
		t.Errorf("no mutant was produced for %d of the %d catalogued rules: %s",
			len(missing), mutation.CanonicalRuleCount, strings.Join(missing, ", "))
	}
	if len(fired) != mutation.CanonicalRuleCount {
		t.Errorf("the run produced mutants for %d rules, want the whole catalogue of %d",
			len(fired), mutation.CanonicalRuleCount)
	}

	assertFamiliesSurvivors(t, events)

	wantSkips := []report.Skip(nil)
	if !slices.Equal(outcome.Report.Skips, wantSkips) {
		t.Errorf("skips = %+v, want %+v", outcome.Report.Skips, wantSkips)
	}

	assertFamiliesCoverage(t, outcome, events)

	document, err := os.ReadFile(published(t, events).RunPath)
	if err != nil {
		t.Fatalf("reading the filed report: %v", err)
	}
	validateDocument(t, document)
}

func assertFamiliesSurvivors(t *testing.T, events []Event) {
	t.Helper()
	want := []string{
		"survived bits.go:44 return-zero-numeric",
		"survived bits.go:44 shr-to-shl",
		"survived bits.go:44 xor-to-band",
		"survived booleans.go:60 condition-to-false",
		"survived booleans.go:60 condition-to-true",
		"survived booleans.go:60 negate-condition",
		"survived booleans.go:61 false-to-true",
		"survived booleans.go:63 true-to-false",
		"survived loops.go:85 add-assign-to-sub-assign",
		"survived loops.go:87 return-zero-numeric",
		"survived numbers.go:35 add-to-sub",
		"survived numbers.go:35 mul-to-div",
		"survived numbers.go:35 return-zero-numeric",
		"survived numbers.go:46 add-to-sub",
		"survived numbers.go:46 return-zero-numeric",
		"survived results.go:90 return-empty-slice",
	}
	var got []string
	for _, line := range results(events) {
		if strings.HasPrefix(line, "survived ") {
			got = append(got, line)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("survivors =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

func assertFamiliesCoverage(t *testing.T, outcome RunOutcome, events []Event) {
	t.Helper()
	block := outcome.Report.Coverage
	if block.Mode != report.CoverageTest {
		t.Fatalf("coverage mode = %q, want %q with the built-in test command", block.Mode, report.CoverageTest)
	}
	if block.Binaries == nil || *block.Binaries != 1 {
		t.Errorf("coverage.binaries = %v, want the fixture's 1", block.Binaries)
	}
	if block.Tests == nil || *block.Tests < 1 {
		t.Errorf("coverage.tests = %v, want the tests the pass profiled", block.Tests)
	}
	if block.MutantsUncovered == nil || *block.MutantsUncovered != familiesUncovered {
		t.Errorf("coverage.mutants_uncovered = %v, want %d", block.MutantsUncovered, familiesUncovered)
	}

	started := make(map[string]bool, len(outcome.Report.Mutants))
	for _, e := range events {
		if begun, ok := e.(MutantStarted); ok {
			started[begun.ID] = true
		}
	}
	uncovered := 0
	for _, m := range outcome.Report.Mutants {
		if !m.Uncovered {
			if m.Attempts == 0 {
				t.Errorf("the covered mutant %s (%s) was never executed", m.DisplayID, m.Rule)
			}
			continue
		}
		uncovered++
		if m.Path != "numbers.go" {
			t.Errorf("the uncovered mutant %s is in %s, want it in Orphan's numbers.go", m.DisplayID, m.Path)
		}
		if m.Outcome != report.OutcomeSurvived {
			t.Errorf("the uncovered mutant %s settled as %s, want survived", m.DisplayID, m.Outcome)
		}
		if m.Attempts != 0 || m.DurationMS != 0 {
			t.Errorf("the uncovered mutant %s reports %d attempts in %dms, want none of either",
				m.DisplayID, m.Attempts, m.DurationMS)
		}
		if started[m.ID] {
			t.Errorf("the uncovered mutant %s was started anyway", m.DisplayID)
		}
	}
	if uncovered != familiesUncovered {
		t.Errorf("the report holds %d uncovered mutants, want %d", uncovered, familiesUncovered)
	}
}

func renderTally(table map[string]familyTally) string {
	families := slices.Sorted(maps.Keys(table))
	lines := make([]string, 0, len(families))
	for _, family := range families {
		row := table[family]
		lines = append(lines, fmt.Sprintf("%-24s killed %2d  survived %2d", family, row.killed, row.survived))
	}
	return strings.Join(lines, "\n\t")
}

type tierSelection struct {
	total    int
	ids      map[string]bool
	families map[string]bool
}

func TestProfileTiersSelectMonotonicallyOverTheWholeCatalogue(t *testing.T) {
	t.Parallel()

	seen := make(map[mutation.Tier]tierSelection, len(mutation.Tiers()))
	for _, tier := range mutation.Tiers() {
		opts := options(t, "families")
		opts.Config.Mutation.Profile = tier
		opts.Config.Execution.Jobs = 4

		outcome, _, err := collect(t, t.Context(), opts)
		if err != nil {
			t.Fatalf("Run --profile %s: %v", tier, err)
		}
		if outcome.Status != StatusOK {
			t.Fatalf("--profile %s: status = %s, want %s", tier, outcome.Status, StatusOK)
		}
		if len(outcome.Report.Rejected) != 0 {
			t.Errorf("--profile %s rejected %+v, want none", tier, outcome.Report.Rejected)
		}
		if notRun := outcome.Report.Summary.NotRun; notRun != 0 {
			t.Errorf("--profile %s left %d mutants not run", tier, notRun)
		}
		if profile := outcome.Report.Selection.Profile; profile != tier.String() {
			t.Errorf("the report records profile %q, want %q", profile, tier)
		}

		chosen := tierSelection{
			total:    len(outcome.Report.Mutants),
			ids:      make(map[string]bool, len(outcome.Report.Mutants)),
			families: make(map[string]bool),
		}
		for _, m := range outcome.Report.Mutants {
			chosen.ids[m.ID] = true
			chosen.families[m.Family] = true
		}
		seen[tier] = chosen
	}

	balanced, strong, all := seen[mutation.TierBalanced], seen[mutation.TierStrong], seen[mutation.TierAll]
	wantTotals := map[mutation.Tier]int{
		mutation.TierBalanced: familiesBalanced,
		mutation.TierStrong:   familiesStrong,
		mutation.TierAll:      familiesMutants,
	}
	for _, tier := range mutation.Tiers() {
		if got := seen[tier].total; got != wantTotals[tier] {
			t.Errorf("--profile %s catalogued %d mutants, want %d", tier, got, wantTotals[tier])
		}
	}
	if balanced.total >= strong.total || strong.total >= all.total {
		t.Errorf("the tiers catalogued %d, %d and %d mutants, want each tier to add to the one below it",
			balanced.total, strong.total, all.total)
	}

	for _, pair := range []struct{ narrow, wide mutation.Tier }{
		{mutation.TierBalanced, mutation.TierStrong},
		{mutation.TierStrong, mutation.TierAll},
	} {
		for id := range seen[pair.narrow].ids {
			if !seen[pair.wide].ids[id] {
				t.Errorf("%s selected mutant %s, which %s did not: %s ⊄ %s",
					pair.narrow, id[:mutation.DisplayIDLength], pair.wide, pair.narrow, pair.wide)
				break
			}
		}
	}

	wantStrongAdds := []string{
		string(mutation.FamilyArithmeticAssign),
		string(mutation.FamilyBitwise),
		string(mutation.FamilyBranchReplacement),
		string(mutation.FamilyNeutralValue),
	}
	if got := addedFamilies(balanced, strong); !slices.Equal(got, wantStrongAdds) {
		t.Errorf("strong adds the families %v to balanced, want %v", got, wantStrongAdds)
	}
	wantAllAdds := []string{
		string(mutation.FamilyLabeledBranch),
		string(mutation.FamilyStatementDeletion),
	}
	if got := addedFamilies(strong, all); !slices.Equal(got, wantAllAdds) {
		t.Errorf("all adds the families %v to strong, want %v", got, wantAllAdds)
	}
	if got, want := len(balanced.families), mutation.CanonicalFamilyCount-len(wantStrongAdds)-len(wantAllAdds); got != want {
		t.Errorf("balanced selected %d families, want %d", got, want)
	}
	if got := len(all.families); got != mutation.CanonicalFamilyCount {
		t.Errorf("all selected %d families, want the whole catalogue of %d", got, mutation.CanonicalFamilyCount)
	}
}

func addedFamilies(narrow, wide tierSelection) []string {
	var out []string
	for family := range wide.families {
		if !narrow.families[family] {
			out = append(out, family)
		}
	}
	slices.Sort(out)
	return out
}
