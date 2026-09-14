// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The branch-replacement family replaces a whole condition with a constant,
// which the catalogue could not do before it. `negate-condition` writes `!(C)`,
// which is a different condition rather than a settled one; `true-to-false`
// fires only where the condition *is* a literal; `nil-error-branch` is the one
// special case of "this branch stops firing", written for `err != nil` alone.
// Between them they never answer "what if this branch always ran" or "what if
// it never did", which is the commonest thing a reader wants to know about a
// guard.

// TestAnIfConditionCanBeSettledBothWays is the shape the family exists for.
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

// TestALoopConditionIsOnlySettledFalse is the family's one deliberate absence,
// and it is a decision about cost rather than about expressiveness.
//
// `for i := 0; i < n; i++` with its condition settled *true* is a loop that
// never ends. Every counted loop in a tree would become one, each costing a
// whole per-mutant timeout -- twice, since a timeout is measured again before
// it is believed -- to teach a reader nothing they could not have worked out
// from the source. `false` is the safe direction: the loop runs zero times,
// which is a real and cheap mutant.
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

// TestAConditionAlreadySpelledAsItsReplacementProducesNothing is the same
// silent refusal the return rules make: the mutation and the source would be
// the same program, which is not a place go-mutants declined to mutate.
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
	// The other direction is a real edit, and it is byte-identical to what
	// `true-to-false` writes at the same span. Both are emitted and
	// deduplication picks one -- the more local rule, which is the earlier
	// family, which is `boolean-literal`.
	if !got.has("condition-to-false", "true", "false") && !got.has("true-to-false", "true", "false") {
		t.Errorf("scan found %v, want the literal settled false by one rule or the other", got.rules())
	}
}

// TestAConditionTheCompilerAlreadyFoldedIsRefusedInOneDirection is the same
// refusal one level down, and it is the one that earns its keep.
//
// A named constant used as a condition is spelled `limit` and *is* `true`, so
// settling it true writes different bytes for the same program -- a mutant that
// no test could ever kill and every suite would carry forever. go/types has
// already folded the constant, so noticing costs a map lookup.
//
// Only the matching direction goes. Settling a constantly-true condition
// *false* is a branch that stops firing, which is exactly the mutant somebody
// wants when a build tag or a platform constant has quietly made a guard
// unconditional.
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

// TestAForWithNoConditionOffersNothing pins the absence that makes the rule
// safe to write at all. `for { }` has no condition to settle, and inventing one
// would be a different edit than this family describes.
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

// TestARangeOffersNoLoopCondition is the other loop with nothing to settle:
// `for _, v := range xs` has a range clause where a condition would be, and
// go/ast does not give it a Cond at all.
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

// TestANarrowingSettlementCarriesABranchProof is what makes this family more
// than two more mutants per guard.
//
// Settling a condition false is a *narrowing* edit -- `false` implies the
// original condition, whatever it is -- so it belongs to the same lemma the
// four narrowing operators already carry, and a consumer can discharge it
// without running anything: a test during which no statement of the body
// executed cannot tell the mutant from the original. Settling it *true* widens,
// so it carries no proof, and the absence has to be as deliberate as the
// presence.
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

// TestSettlingALoopConditionFalseStopsTheLoop is the termination proof's side
// of the same fact, and it is worth pinning because this is the one rule in the
// catalogue that can only ever make a loop *shorter*.
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

// TestTheBranchReplacementFamilyIsNotInTheBalancedProfile is the tier, and the
// tier is the whole reason this is a family of its own.
//
// Two independent arguments put it at `strong`. Equivalence: a defensive check
// that cannot actually fail survives `condition-to-false` in every suite, and
// there are a great many of those. Subsumption: a test that kills
// `condition-to-true` almost always kills `negate-condition` at the same span,
// so in `balanced` the family would mostly inflate the denominator with near
// duplicates of a rule that is already there.
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

// TestANamedBooleanConditionIsRefusedTheWayNegationIsNot draws the line this
// family shares with its guard rather than with its own type gate.
//
// `negate-condition` accepts any boolean type, because `!` does. This family
// writes the *literal* `true`, which is an untyped constant and assignable to
// any boolean type -- so the edit itself is fine, and what refuses a named
// boolean is the guard: Form C requires a site of exactly the universe `bool`.
// The refusal is therefore recorded as unnameable-decl-type, the same answer a
// negation at the same site gets, rather than as a silence this family invented.
func TestANamedBooleanConditionIsRefusedTheWayNegationIsNot(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

type Flag bool

func Use(f Flag) int {
	if f {
		return 1
	}
	return 0
}
`)
	for _, rule := range got.rules() {
		if strings.HasPrefix(rule, "condition-to-") {
			t.Errorf("scan produced %v at a site no guard form can express", got.rules())
		}
	}
	if !got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan recorded %v, want a %s site", got.skips(), SkipUnnameableDeclType)
	}
}

// TestTheMoreLocalRuleStillWinsATie is the reason this family was inserted
// where it was, and it is the one property a new family can silently break.
//
// `condition-to-false` writes `false` over a whole condition, and two rules
// already do that at particular conditions: `nil-error-branch` at `err != nil`,
// and `true-to-false` at the literal `true`. Same file, same span, same
// replacement bytes -- which is exactly what the catalogue calls one edit. The
// winner is the candidate whose rule comes first in the registry table, because
// that table runs from the most local edit to the least, and both of those are
// more local than "settle the whole condition". Inserting `branch-replacement`
// after every family that could tie with it is what keeps that true, and a
// family added in the wrong place would show up here rather than as a survivor
// somebody notices months later.
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
