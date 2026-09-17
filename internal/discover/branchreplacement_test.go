// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

func TestAnIfConditionCanBeSettledBothWays(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Clamp(v, limit int) int {
	if v > limit {
		return limit
	}
	return v
}
`)
	if !got.has("condition-to-true", "v > limit", "true") {
		t.Errorf("scan found %v, want a condition-to-true candidate", got.rules())
	}
	if !got.has("condition-to-false", "v > limit", "false") {
		t.Errorf("scan found %v, want a condition-to-false candidate", got.rules())
	}
}

func TestALoopConditionIsOnlySettledFalse(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Sum(values []int, limit int) int {
	total := 0
	for i := 0; i < limit; i++ {
		total += values[i]
	}
	return total
}
`)
	if !got.has("loop-condition-to-false", "i < limit", "false") {
		t.Errorf("scan found %v, want a loop-condition-to-false candidate", got.rules())
	}
	for _, rule := range got.rules() {
		if strings.HasPrefix(rule, "loop-condition-to-true") {
			t.Errorf("scan produced %v: a loop settled true never ends", got.rules())
		}
	}
}

func TestAConditionAlreadySpelledAsItsReplacementProducesNothing(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Always() int {
	if true {
		return 1
	}
	return 0
}
`)
	if got.has("condition-to-true", "true", "true") {
		t.Errorf("scan produced a mutation identical to its source: %v", got.rules())
	}
	if !got.has("condition-to-false", "true", "false") && !got.has("true-to-false", "true", "false") {
		t.Errorf("scan found %v, want the literal settled false by one rule or the other", got.rules())
	}
}

func TestAConditionTheCompilerAlreadyFoldedIsRefusedInOneDirection(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

const enabled = 3 > 2

func Use(v int) int {
	if enabled {
		return v
	}
	return 0
}
`)
	if got.has("condition-to-true", "enabled", "true") {
		t.Errorf("scan produced %v: the constant is already true", got.rules())
	}
	if !got.has("condition-to-false", "enabled", "false") {
		t.Errorf("scan found %v, want the constantly-true guard settled false", got.rules())
	}
}

func TestAForWithNoConditionOffersNothing(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Drain(ch chan int) int {
	total := 0
	for {
		v, ok := <-ch
		if !ok {
			return total
		}
		total += v
	}
}
`)
	for _, rule := range got.rules() {
		if strings.HasPrefix(rule, "loop-condition-to-") {
			t.Errorf("scan produced %v for a loop with no condition", got.rules())
		}
	}
}

func TestARangeOffersNoLoopCondition(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Count(values []int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}
`)
	for _, rule := range got.rules() {
		if strings.HasPrefix(rule, "loop-condition-to-") {
			t.Errorf("scan produced %v for a range clause", got.rules())
		}
	}
}

func TestANarrowingSettlementCarriesABranchProof(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Clamp(v, limit int) int {
	if v > limit {
		return limit
	}
	return v
}
`)
	for _, candidate := range got.candidates {
		switch candidate.Rule.Name {
		case "condition-to-false":
			if candidate.Branch == nil {
				t.Errorf("condition-to-false carries no branch proof, and `false` implies every condition")
			}
		case "condition-to-true":
			if candidate.Branch != nil {
				t.Errorf("condition-to-true carries a branch proof %+v, and it widens rather than narrows",
					candidate.Branch)
			}
		}
	}
}

func TestSettlingALoopConditionFalseStopsTheLoop(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Sum(values []int, limit int) int {
	total := 0
	for i := 0; i < limit; i++ {
		total += values[i]
	}
	return total
}
`)
	found := false
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "loop-condition-to-false" {
			continue
		}
		found = true
		if candidate.Termination == nil {
			t.Fatalf("loop-condition-to-false carries no termination proof")
		}
		if candidate.Termination.Verdict != TerminationBounded {
			t.Errorf("loop-condition-to-false is %q, want %q: the loop runs zero times",
				candidate.Termination.Verdict, TerminationBounded)
		}
	}
	if !found {
		t.Fatalf("scan found %v, want a loop-condition-to-false candidate", got.rules())
	}
}

func TestTheBranchReplacementFamilyIsNotInTheBalancedProfile(t *testing.T) {
	t.Parallel()

	found := map[string]mutation.Rule{}
	for _, rule := range SupportedRules() {
		if rule.Family == mutation.FamilyBranchReplacement {
			found[rule.Name] = rule
		}
	}
	for _, name := range []string{"condition-to-true", "condition-to-false", "loop-condition-to-false"} {
		rule, ok := found[name]
		if !ok {
			t.Fatalf("SupportedRules() does not name %s", name)
		}
		if rule.Tier != mutation.TierStrong {
			t.Errorf("%s is tier %q, want %q", name, rule.Tier, mutation.TierStrong)
		}
	}
	if len(found) != 3 {
		t.Errorf("the family holds %d implemented rules, want exactly 3: %v", len(found), found)
	}

	balanced := scanWith(t, `package pkg

func Clamp(v, limit int) int {
	if v > limit {
		return limit
	}
	return v
}
`, func(rule mutation.Rule) bool { return rule.Tier == mutation.TierBalanced })
	for _, rule := range balanced.rules() {
		if strings.HasPrefix(rule, "condition-to-") {
			t.Errorf("a balanced selection produced %v", balanced.rules())
		}
	}
}

func TestTheMoreLocalRuleStillWinsATie(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		source     string
		original   string
		wantWinner string
	}{{
		name: "an error branch",
		source: `package pkg

func Handle(err error) int {
	if err != nil {
		return 1
	}
	return 0
}
`,
		original:   "err != nil",
		wantWinner: "nil-error-branch",
	}, {
		name: "a literal condition",
		source: `package pkg

func Always(v int) int {
	if true {
		return v
	}
	return 0
}
`,
		original:   "true",
		wantWinner: "true-to-false",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			scan := scanSource(t, c.source)
			catalog, err := BuildCatalog(Result{Candidates: scan.candidates})
			if err != nil {
				t.Fatalf("BuildCatalog: %v", err)
			}
			var winners []string
			for _, m := range catalog.Mutants() {
				if m.Original == c.original && m.Replacement == "false" {
					winners = append(winners, m.Rule.Name)
				}
			}
			if len(winners) != 1 || winners[0] != c.wantWinner {
				t.Errorf("the edit %q->false is catalogued as %v, want exactly [%s]",
					c.original, winners, c.wantWinner)
			}
			shadowed := false
			for _, d := range catalog.Duplicates() {
				if d.Dropped.Rule.Name == "condition-to-false" && d.WinnerRule.Name == c.wantWinner {
					shadowed = true
				}
			}
			if !shadowed {
				t.Errorf("condition-to-false was not recorded as shadowed by %s: %+v",
					c.wantWinner, catalog.Duplicates())
			}
		})
	}
}
