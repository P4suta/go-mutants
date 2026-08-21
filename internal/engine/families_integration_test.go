// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The whole operator catalogue, end to end.
//
// The rest of the engine's integration suite proves one mechanism at a time
// against a fixture built for it: the baseline gate, compile validation,
// coverage narrowing, the interruption path. This file proves the operators
// themselves. `fixtures/families` holds at least one live candidate for each of
// the forty-two rules the frozen registry names, and the run here has to
// discover, instrument, compile, execute and score every one of them.
//
// It lives in its own file rather than at the end of integration_test.go
// because it is the only part of the suite whose subject is the catalogue, and
// because its expectations are the families fixture's documented claims about
// itself — fixtures/README.md says the same things in prose.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
//
// The comment above is deliberately not a package doc: integration_test.go
// carries this package's, and a second one would leave `go doc` picking between
// them.

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

// A familyTally is one row of the families fixture's claim about itself: how
// many of an operator family's executed mutants die, and how many live.
type familyTally struct{ killed, survived int }

// familiesTable is that claim in full, one row per family, counted over the
// mutants the run actually executed.
//
// It is aggregated by family rather than written out mutant by mutant because
// what this fixture exists to prove is a statement about families: that every
// one of the eleven reaches execution, and that each one both kills and — where
// the fixture leaves a gap on purpose — survives. Seventy-six per-mutant rows
// would say the same thing in a form nobody reads, and would turn every
// reformatting of the fixture into a wall of diff. The survivors are then named
// individually below, because that is the half a count cannot pin.
//
// The uncovered pair in `Orphan` is deliberately outside this table. Coverage
// settles those two without executing either, so counting them here would sit a
// survivor no test could have caught beside survivors that eleven tests ran
// straight past, and would inflate `integer-arithmetic` and
// `return-replacement` with a fate that is not about the operator at all. They
// are asserted separately, as coverage's own finding.
var familiesTable = map[string]familyTally{
	string(mutation.FamilyBooleanLiteral):    {killed: 2, survived: 2},
	string(mutation.FamilyConditionNegation): {killed: 6, survived: 1},
	string(mutation.FamilyBooleanConnective): {killed: 2, survived: 0},
	string(mutation.FamilyComparison):        {killed: 8, survived: 0},
	string(mutation.FamilyIntegerArithmetic): {killed: 9, survived: 2},
	string(mutation.FamilyFloatArithmetic):   {killed: 4, survived: 0},
	string(mutation.FamilyReturnReplacement): {killed: 14, survived: 3},
	string(mutation.FamilyErrorSwallowing):   {killed: 4, survived: 0},
	string(mutation.FamilyBitwise):           {killed: 6, survived: 2},
	string(mutation.FamilyArithmeticAssign):  {killed: 4, survived: 1},
	string(mutation.FamilyStatementDeletion): {killed: 4, survived: 0},
}

// The families fixture's totals, stated once so that the assertions below read
// as claims about the fixture rather than as restatements of whatever the run
// happened to produce. familiesBalanced and familiesStrong are the same
// catalogue seen from the two narrower tiers.
const (
	familiesMutants   = 76
	familiesKilled    = 63
	familiesSurvived  = 13
	familiesUncovered = 2

	familiesBalanced = 59
	familiesStrong   = 72
)

// TestFamiliesRunReachesEveryOperatorFamily is the catalogue end to end.
//
// A family that stopped being discovered, stopped being instrumentable, or
// started being refused by the compiler shows up here as a missing row rather
// than as a number that quietly got smaller. The run is at profile `all`
// because that is the only tier that selects every family; the tiers themselves
// are the next test's subject.
func TestFamiliesRunReachesEveryOperatorFamily(t *testing.T) {
	privateTempDir(t)
	opts := options(t, "families")
	opts.Config.Mutation.Profile = mutation.TierAll
	// Four workers rather than the harness's one. Everything asserted here is a
	// tally or a set, so the event *order* is not part of the claim — and this
	// fixture starts seventy-four processes, which is where the time goes.
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

	// Nothing was refused. Every guard form the expanded catalogue needs — the
	// bool selector, the statement guard, and the declaration rewrite — composes
	// a compilable program over every family in this fixture, and a rejection
	// here would mean one of the three stopped being able to express one of the
	// eleven. fixtures/rejectable is where a rejection is the expected answer.
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
	// Called out on its own because the tally above would report a hang as a
	// missing kill and send a reader looking in the wrong place. A timeout here
	// means a loop in the fixture stopped terminating under every mutant of it,
	// which is the invariant the fixture's package documentation is most
	// concerned with.
	if summary.TimedOut != 0 {
		t.Errorf("%d mutants timed out: a loop in the fixture no longer terminates under every mutant of it",
			summary.TimedOut)
	}
	if want := float64(familiesKilled) / float64(familiesMutants) * 100; summary.ScorePercent == nil ||
		*summary.ScorePercent != want {
		t.Errorf("score = %v, want %v (%d of %d)", summary.ScorePercent, want, familiesKilled, familiesMutants)
	}

	// The table, over the mutants the run executed.
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

	// Stated against the registry rather than against the table above, which
	// would be circular: a family the registry names and this run never executed
	// is the failure the whole fixture exists to catch.
	for _, family := range mutation.CanonicalRegistry().Families() {
		if got[string(family)].killed == 0 {
			t.Errorf("the %s family contributed no executed, killed mutant: %+v", family, got[string(family)])
		}
	}

	// And every rule, not merely every family: a family whose six rules had
	// collapsed into one matcher would still fill in its row above.
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

	// The two skips are the `i++` post statement of the fixture's counted loop:
	// a block is not legal Go there, so both rules that match it are recorded
	// with a reason instead of being catalogued. They are asserted because a
	// guard form that silently began swallowing sites would otherwise look like
	// progress.
	wantSkips := []report.Skip{{Path: "loops.go", Reason: "unnameable-decl-type", Count: 2}}
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

// assertFamiliesSurvivors names the thirteen survivors.
//
// The per-family table says how many there are; this says which they are,
// because a survivor that moved from an under-tested function into a pinned one
// would leave every count intact. Each line here is a function the fixture's
// README lists as deliberately under-tested — `Salt`, `Toggle`, `Drift`,
// `Weigh` — or the one nothing calls at all, `Orphan`.
//
// The line numbers are the fixture's, and a mutant's coordinates come from a
// byte offset into the file — so editing a *doc comment* in
// fixtures/families moves them exactly as editing code does. The cheapest way to
// refresh this list after any edit there is to ask the tool rather than to count:
//
//	cd fixtures/families && go-mutants list --profile all
func assertFamiliesSurvivors(t *testing.T, events []Event) {
	t.Helper()
	want := []string{
		"survived bits.go:44 return-zero-numeric",
		"survived bits.go:44 shr-to-shl",
		"survived bits.go:44 xor-to-band",
		"survived booleans.go:54 negate-condition",
		"survived booleans.go:55 false-to-true",
		"survived booleans.go:57 true-to-false",
		"survived loops.go:67 add-assign-to-sub-assign",
		"survived loops.go:69 return-zero-numeric",
		"survived numbers.go:35 add-to-sub",
		"survived numbers.go:35 mul-to-div",
		"survived numbers.go:35 return-zero-numeric",
		"survived numbers.go:46 add-to-sub",
		"survived numbers.go:46 return-zero-numeric",
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

// assertFamiliesCoverage is coverage-guided selection judged against the
// families fixture.
//
// The binary count on its own would pass with the narrowing broken, so what is
// asserted is the consequence: the two mutants in the function nothing calls
// were never started, carry no attempts, and are still published as survivors —
// while every other mutant in the fixture really ran.
func assertFamiliesCoverage(t *testing.T, outcome RunOutcome, events []Event) {
	t.Helper()
	block := outcome.Report.Coverage
	if block.Mode != report.CoveragePackage {
		t.Fatalf("coverage mode = %q, want %q with the built-in test command", block.Mode, report.CoveragePackage)
	}
	if block.Binaries == nil || *block.Binaries != 1 {
		t.Errorf("coverage.binaries = %v, want the fixture's 1", block.Binaries)
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
		// Orphan's, and only Orphan's. The file is named because a mutant that
		// became uncovered somewhere else would mean a test stopped reaching a
		// line, which is a different fault from the narrowing being wrong.
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

// renderTally prints a per-family table as sorted lines, so that a failure
// names the family that moved instead of printing two map literals in whatever
// order the runtime felt like.
func renderTally(table map[string]familyTally) string {
	families := slices.Sorted(maps.Keys(table))
	lines := make([]string, 0, len(families))
	for _, family := range families {
		row := table[family]
		lines = append(lines, fmt.Sprintf("%-24s killed %2d  survived %2d", family, row.killed, row.survived))
	}
	return strings.Join(lines, "\n\t")
}

// A tierSelection is what one profile's run catalogued.
type tierSelection struct {
	total    int
	ids      map[string]bool
	families map[string]bool
}

// TestProfileTiersSelectMonotonicallyOverTheWholeCatalogue is the profile
// contract measured through the run pipeline rather than asserted about the
// rule table.
//
// `balanced ⊂ strong ⊂ all` is a property of that table and is already unit
// tested there. What this adds is that the property survives every phase between
// the table and the report: three real runs over one fixture, each discovering,
// instrumenting, compiling and executing its own tier's selection.
//
// The counts are the readable half. The load-bearing half is which *families*
// each tier adds, because a count that moved could have moved for any reason —
// a fixture edit, a deduplication change, a rule that stopped firing — while the
// family sets name the three operators a profile is actually about.
func TestProfileTiersSelectMonotonicallyOverTheWholeCatalogue(t *testing.T) {
	privateTempDir(t)

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
		// Each tier is a whole, healthy run and not merely a catalogue: nothing
		// is refused and nothing is left unmeasured, so the sets compared below
		// are sets of mutants that really executed.
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

	// Inclusion by identity, not by count. A mutant's id is a digest over its
	// path, rule, span and bytes and has nothing to do with the profile that
	// selected it, so a tier that swapped one mutant for another would keep the
	// arithmetic and fail here.
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

	// What each tier adds, by family. These three names are the whole meaning of
	// the profile setting, and they are the reason `balanced` is the default:
	// bit manipulation and statement deletion are where an equivalent mutant is
	// likeliest, so they are opt-in.
	wantStrongAdds := []string{
		string(mutation.FamilyArithmeticAssign),
		string(mutation.FamilyBitwise),
	}
	if got := addedFamilies(balanced, strong); !slices.Equal(got, wantStrongAdds) {
		t.Errorf("strong adds the families %v to balanced, want %v", got, wantStrongAdds)
	}
	wantAllAdds := []string{string(mutation.FamilyStatementDeletion)}
	if got := addedFamilies(strong, all); !slices.Equal(got, wantAllAdds) {
		t.Errorf("all adds the families %v to strong, want %v", got, wantAllAdds)
	}
	// And balanced really is the other eight, whole: a tier that dropped a
	// family it is supposed to hold would leave both differences above looking
	// exactly right.
	if got, want := len(balanced.families), mutation.CanonicalFamilyCount-len(wantStrongAdds)-len(wantAllAdds); got != want {
		t.Errorf("balanced selected %d families, want %d", got, want)
	}
	if got := len(all.families); got != mutation.CanonicalFamilyCount {
		t.Errorf("all selected %d families, want the whole catalogue of %d", got, mutation.CanonicalFamilyCount)
	}
}

// addedFamilies returns the families the wider selection holds and the narrower
// one does not, sorted.
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
