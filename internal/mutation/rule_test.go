// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// canonicalOrder is the frozen v1 rule order, transcribed from the operator
// table in the design plan and in docs/operators.md.
//
// This is a golden list, not a convenience: registry position is the
// deduplication tiebreak, so reordering these names silently changes which of
// two identical edits gets a mutant ID and which is recorded as a duplicate.
var canonicalOrder = []string{
	// boolean-literal
	"true-to-false",
	"false-to-true",
	// condition-negation
	"negate-condition",
	"negate-loop-condition",
	"remove-negation",
	// boolean-connective
	"and-to-or",
	"or-to-and",
	// comparison
	"eq-to-neq",
	"neq-to-eq",
	"lt-to-le",
	"le-to-lt",
	"gt-to-ge",
	"ge-to-gt",
	// integer-arithmetic
	"add-to-sub",
	"sub-to-add",
	"mul-to-div",
	"div-to-mul",
	"rem-to-mul",
	// float-arithmetic
	"fadd-to-fsub",
	"fsub-to-fadd",
	"fmul-to-fdiv",
	"fdiv-to-fmul",
	// return-replacement
	"return-zero-numeric",
	"return-empty-string",
	"return-true",
	"return-false",
	"return-nil",
	// error-swallowing
	"return-err-to-nil",
	"nil-error-branch",
	// bitwise
	"band-to-bor",
	"bor-to-band",
	"xor-to-band",
	"shl-to-shr",
	"shr-to-shl",
	"andnot-to-band",
	// arithmetic-assignment
	"add-assign-to-sub-assign",
	"sub-assign-to-add-assign",
	"incr-to-decr",
	"decr-to-incr",
	// statement-deletion
	"delete-call-statement",
	"delete-assignment",
	"delete-incdec",
}

var canonicalFamilyOrder = []Family{
	FamilyBooleanLiteral,
	FamilyConditionNegation,
	FamilyBooleanConnective,
	FamilyComparison,
	FamilyIntegerArithmetic,
	FamilyFloatArithmetic,
	FamilyReturnReplacement,
	FamilyErrorSwallowing,
	FamilyBitwise,
	FamilyArithmeticAssign,
	FamilyStatementDeletion,
}

func TestCanonicalRegistryShape(t *testing.T) {
	t.Parallel()

	r := CanonicalRegistry()
	if got := r.Len(); got != CanonicalRuleCount {
		t.Errorf("registry has %d rules, want %d", got, CanonicalRuleCount)
	}
	if got := len(r.Families()); got != CanonicalFamilyCount {
		t.Errorf("registry has %d families, want %d", got, CanonicalFamilyCount)
	}
	if CanonicalRuleCount != len(canonicalOrder) {
		t.Errorf("CanonicalRuleCount = %d, but the golden order lists %d rules",
			CanonicalRuleCount, len(canonicalOrder))
	}
	if CanonicalFamilyCount != len(canonicalFamilyOrder) {
		t.Errorf("CanonicalFamilyCount = %d, but the golden order lists %d families",
			CanonicalFamilyCount, len(canonicalFamilyOrder))
	}
}

func TestCanonicalRegistryOrderIsFrozen(t *testing.T) {
	t.Parallel()

	got := make([]string, 0, CanonicalRuleCount)
	for _, rule := range CanonicalRules() {
		got = append(got, rule.Name)
	}
	if diff := cmp.Diff(canonicalOrder, got); diff != "" {
		t.Fatalf("canonical rule order changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(canonicalFamilyOrder, CanonicalRegistry().Families()); diff != "" {
		t.Fatalf("canonical family order changed (-want +got):\n%s", diff)
	}
}

func TestCanonicalRulesAreWellFormed(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, CanonicalRuleCount)
	for i, rule := range CanonicalRules() {
		if err := rule.Validate(); err != nil {
			t.Errorf("rule %d (%s): %v", i, rule.Name, err)
		}
		if seen[rule.Name] {
			t.Errorf("rule %q appears twice", rule.Name)
		}
		seen[rule.Name] = true
		if rule.Version != 1 {
			t.Errorf("rule %q is at version %d; every v1 rule ships at version 1", rule.Name, rule.Version)
		}
		if got, ok := CanonicalRegistry().Position(rule.Name); !ok || got != i {
			t.Errorf("Position(%q) = %d, %v, want %d, true", rule.Name, got, ok, i)
		}
	}
}

func TestFamilyTiers(t *testing.T) {
	t.Parallel()

	want := map[Family]Tier{
		FamilyBooleanLiteral:    TierBalanced,
		FamilyConditionNegation: TierBalanced,
		FamilyBooleanConnective: TierBalanced,
		FamilyComparison:        TierBalanced,
		FamilyIntegerArithmetic: TierBalanced,
		FamilyFloatArithmetic:   TierBalanced,
		FamilyReturnReplacement: TierBalanced,
		FamilyErrorSwallowing:   TierBalanced,
		FamilyBitwise:           TierStrong,
		FamilyArithmeticAssign:  TierStrong,
		FamilyStatementDeletion: TierAll,
	}
	for _, rule := range CanonicalRules() {
		wantTier, ok := want[rule.Family]
		if !ok {
			t.Fatalf("rule %q belongs to unexpected family %q", rule.Name, rule.Family)
		}
		if rule.Tier != wantTier {
			t.Errorf("rule %q has tier %s, want %s", rule.Name, rule.Tier, wantTier)
		}
	}
}

func TestFamilyRulesAreContiguousAndComplete(t *testing.T) {
	t.Parallel()

	r := CanonicalRegistry()
	total := 0
	for position, family := range r.Families() {
		got, ok := r.FamilyPosition(family)
		if !ok || got != position {
			t.Errorf("FamilyPosition(%q) = %d, %v, want %d, true", family, got, ok, position)
		}
		rules := r.FamilyRules(family)
		if len(rules) == 0 {
			t.Errorf("family %q has no rules", family)
		}
		total += len(rules)
	}
	if total != CanonicalRuleCount {
		t.Errorf("families hold %d rules in total, want %d", total, CanonicalRuleCount)
	}
}

func TestTierMonotonicity(t *testing.T) {
	t.Parallel()

	r := CanonicalRegistry()
	balanced := r.SelectTier(TierBalanced)
	strong := r.SelectTier(TierStrong)
	all := r.SelectTier(TierAll)

	if len(balanced) != 29 {
		t.Errorf("balanced selects %d rules, want 29", len(balanced))
	}
	if len(strong) != 39 {
		t.Errorf("strong selects %d rules, want 39", len(strong))
	}
	if len(all) != CanonicalRuleCount {
		t.Errorf("all selects %d rules, want %d", len(all), CanonicalRuleCount)
	}

	// balanced ⊂ strong ⊂ all, as prefixes: because tiers rise with table
	// position in the canonical table, each selection is the previous one
	// plus new rules, never a reshuffle.
	if diff := cmp.Diff(balanced, strong[:len(balanced)]); diff != "" {
		t.Errorf("strong does not extend balanced (-balanced +strong):\n%s", diff)
	}
	if diff := cmp.Diff(strong, all[:len(strong)]); diff != "" {
		t.Errorf("all does not extend strong (-strong +all):\n%s", diff)
	}
}

func TestTierIncludes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		profile Tier
		rule    Tier
		want    bool
	}{
		{TierBalanced, TierBalanced, true},
		{TierBalanced, TierStrong, false},
		{TierBalanced, TierAll, false},
		{TierStrong, TierBalanced, true},
		{TierStrong, TierStrong, true},
		{TierStrong, TierAll, false},
		{TierAll, TierBalanced, true},
		{TierAll, TierStrong, true},
		{TierAll, TierAll, true},
	}
	for _, tc := range tests {
		if got := tc.profile.Includes(tc.rule); got != tc.want {
			t.Errorf("%s.Includes(%s) = %v, want %v", tc.profile, tc.rule, got, tc.want)
		}
	}
}

func TestTierNames(t *testing.T) {
	t.Parallel()

	for _, tier := range Tiers() {
		parsed, err := ParseTier(tier.String())
		if err != nil {
			t.Fatalf("ParseTier(%q) error = %v", tier.String(), err)
		}
		if parsed != tier {
			t.Errorf("ParseTier(%q) = %v, want %v", tier.String(), parsed, tier)
		}
		if !tier.Valid() {
			t.Errorf("%v should be valid", tier)
		}
	}
	if got := TierBalanced.String(); got != "balanced" {
		t.Errorf("TierBalanced.String() = %q, want %q", got, "balanced")
	}
	if _, err := ParseTier("aggressive"); !errors.Is(err, ErrUnknownTier) {
		t.Errorf("ParseTier(%q) error = %v, want ErrUnknownTier", "aggressive", err)
	}
	if Tier(9).Valid() {
		t.Error("Tier(9) should not be valid")
	}
	if got := Tier(9).String(); got != "tier(9)" {
		t.Errorf("Tier(9).String() = %q, want %q", got, "tier(9)")
	}
}

func TestRuleString(t *testing.T) {
	t.Parallel()

	rule := mustRule(t, "eq-to-neq")
	if got := rule.String(); got != "eq-to-neq@1" {
		t.Errorf("String() = %q, want %q", got, "eq-to-neq@1")
	}
}

func TestRegistryVerify(t *testing.T) {
	t.Parallel()

	r := CanonicalRegistry()
	rule := mustRule(t, "eq-to-neq")

	if err := r.Verify(rule); err != nil {
		t.Fatalf("Verify(%v) error = %v", rule, err)
	}

	unknown := Rule{Family: "invented", Name: "eq-to-nothing", Version: 1, Tier: TierBalanced}
	if err := r.Verify(unknown); !errors.Is(err, ErrUnknownRule) {
		t.Errorf("Verify(unknown) error = %v, want ErrUnknownRule", err)
	}

	wrongVersion := rule
	wrongVersion.Version = 2
	if err := r.Verify(wrongVersion); !errors.Is(err, ErrRuleMismatch) {
		t.Errorf("Verify(wrong version) error = %v, want ErrRuleMismatch", err)
	}

	wrongFamily := rule
	wrongFamily.Family = FamilyBitwise
	if err := r.Verify(wrongFamily); !errors.Is(err, ErrRuleMismatch) {
		t.Errorf("Verify(wrong family) error = %v, want ErrRuleMismatch", err)
	}
}

func TestNewRegistryRejectsInconsistentTables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rules   []Rule
		wantErr error
	}{
		{
			name: "duplicate rule name",
			rules: []Rule{
				{Family: FamilyComparison, Name: "eq-to-neq", Version: 1, Tier: TierBalanced},
				{Family: FamilyComparison, Name: "eq-to-neq", Version: 2, Tier: TierBalanced},
			},
			wantErr: ErrDuplicateRule,
		},
		{
			name: "family with two tiers",
			rules: []Rule{
				{Family: FamilyComparison, Name: "eq-to-neq", Version: 1, Tier: TierBalanced},
				{Family: FamilyComparison, Name: "neq-to-eq", Version: 1, Tier: TierStrong},
			},
			wantErr: ErrFamilyTierConflict,
		},
		{
			name: "split family",
			rules: []Rule{
				{Family: FamilyComparison, Name: "eq-to-neq", Version: 1, Tier: TierBalanced},
				{Family: FamilyBitwise, Name: "band-to-bor", Version: 1, Tier: TierStrong},
				{Family: FamilyComparison, Name: "neq-to-eq", Version: 1, Tier: TierBalanced},
			},
			wantErr: ErrFamilySplit,
		},
		{
			name:    "zero version",
			rules:   []Rule{{Family: FamilyComparison, Name: "eq-to-neq", Version: 0, Tier: TierBalanced}},
			wantErr: ErrInvalidRuleVersion,
		},
		{
			name:    "no family",
			rules:   []Rule{{Name: "eq-to-neq", Version: 1, Tier: TierBalanced}},
			wantErr: ErrInvalidRuleName,
		},
		{
			name:    "no name",
			rules:   []Rule{{Family: FamilyComparison, Version: 1, Tier: TierBalanced}},
			wantErr: ErrInvalidRuleName,
		},
		{
			name:    "unknown tier",
			rules:   []Rule{{Family: FamilyComparison, Name: "eq-to-neq", Version: 1, Tier: Tier(200)}},
			wantErr: ErrUnknownTier,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewRegistry(tc.rules); !errors.Is(err, tc.wantErr) {
				t.Fatalf("NewRegistry() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRegistryAccessorsCopy(t *testing.T) {
	t.Parallel()

	r := CanonicalRegistry()
	rules := r.Rules()
	rules[0].Name = "clobbered"
	if r.Rules()[0].Name == "clobbered" {
		t.Fatal("Rules() handed out the registry's own backing array")
	}

	families := r.Families()
	families[0] = "clobbered"
	if r.Families()[0] == "clobbered" {
		t.Fatal("Families() handed out the registry's own backing array")
	}
}

func mustRule(t *testing.T, name string) Rule {
	t.Helper()

	rule, ok := CanonicalRegistry().Lookup(name)
	if !ok {
		t.Fatalf("rule %q is not registered", name)
	}
	return rule
}
