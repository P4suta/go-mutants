// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// Tier is a profile level. Tiers are monotonically inclusive: a profile at
// one tier selects every rule at that tier and at every lower tier, so
//
//	balanced ⊂ strong ⊂ all
//
// holds by construction rather than by a hand-maintained list. The numeric
// order is part of that contract; new tiers may only be appended.
type Tier uint8

// The v1 tiers, in inclusion order.
const (
	// TierBalanced is the default profile: operators whose survivors almost
	// always point at a real gap in the tests.
	TierBalanced Tier = iota
	// TierStrong adds operators that are valuable but noisier in code that
	// manipulates bits for performance rather than for meaning.
	TierStrong
	// TierAll adds statement deletion, the classic source of equivalent
	// mutants in logging and metrics code.
	TierAll
)

// ErrUnknownTier reports a profile name that is not a tier.
var ErrUnknownTier = errors.New("mutation: unknown tier")

// String returns the tier's canonical name, which is also its profile name in
// configuration and on the command line.
func (t Tier) String() string {
	switch t {
	case TierBalanced:
		return "balanced"
	case TierStrong:
		return "strong"
	case TierAll:
		return "all"
	default:
		return "tier(" + strconv.Itoa(int(t)) + ")"
	}
}

// Valid reports whether t is one of the defined tiers.
func (t Tier) Valid() bool { return t <= TierAll }

// Includes reports whether a profile at tier t selects rules at tier other.
func (t Tier) Includes(other Tier) bool { return other <= t }

// ParseTier resolves a profile name to its tier.
func ParseTier(s string) (Tier, error) {
	for _, t := range Tiers() {
		if t.String() == s {
			return t, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownTier, s)
}

// Tiers returns every tier in inclusion order.
func Tiers() []Tier { return []Tier{TierBalanced, TierStrong, TierAll} }

// Family is an operator family name. Families are the unit of selection for
// `--operator` and `mutation.operators`, and their order in the canonical
// table is the deduplication tiebreak: an earlier family is the more local
// edit.
type Family string

// The eleven v1 operator families, in canonical table order.
const (
	FamilyBooleanLiteral    Family = "boolean-literal"
	FamilyConditionNegation Family = "condition-negation"
	FamilyBooleanConnective Family = "boolean-connective"
	FamilyComparison        Family = "comparison"
	FamilyIntegerArithmetic Family = "integer-arithmetic"
	FamilyFloatArithmetic   Family = "float-arithmetic"
	FamilyReturnReplacement Family = "return-replacement"
	FamilyErrorSwallowing   Family = "error-swallowing"
	FamilyBitwise           Family = "bitwise"
	FamilyArithmeticAssign  Family = "arithmetic-assignment"
	FamilyStatementDeletion Family = "statement-deletion"
)

// Rule is one mutation operator: metadata only.
//
// This package knows a rule's name, version, family, and tier, and nothing at
// all about how to match or rewrite Go syntax. Matching lives in
// internal/discover and rewriting in internal/instrument. The separation is
// what lets the identity recipe and the catalogue be tested without a Go
// toolchain in the loop.
type Rule struct {
	// Family groups the rule for selection and for the dedup tiebreak.
	Family Family
	// Name is the rule's globally unique name, for example "eq-to-neq".
	Name string
	// Version is the rule's behaviour version, starting at 1. It feeds the
	// mutant ID, so a rule that changes what it emits bumps this instead of
	// silently reusing identities.
	Version int
	// Tier is the lowest profile that selects the rule. It always equals the
	// family's tier; the registry enforces that.
	Tier Tier
}

// String renders the rule as it appears in reports and console output:
// "name@version".
func (r Rule) String() string { return r.Name + "@" + strconv.Itoa(r.Version) }

// Validate reports whether the rule is well formed.
func (r Rule) Validate() error {
	if r.Family == "" {
		return fmt.Errorf("%w: rule %q has no family", ErrInvalidRuleName, r.Name)
	}
	if r.Name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidRuleName)
	}
	if r.Version < 1 {
		return fmt.Errorf("%w: %s has version %d", ErrInvalidRuleVersion, r.Name, r.Version)
	}
	if !r.Tier.Valid() {
		return fmt.Errorf("%w: rule %q has tier %d", ErrUnknownTier, r.Name, r.Tier)
	}
	return nil
}

// Counts of the canonical v1 catalogue, asserted by the registry tests.
//
// The design plan's headline says "11 family / 43 rule" while the table in
// the same plan enumerates 42 named rules. The enumeration is authoritative
// here: a rule name is part of a mutant ID, so no rule may exist without a
// deliberately chosen name. See docs/operators.md, which records the same
// discrepancy and hands the reconciliation to this registry.
const (
	CanonicalFamilyCount = 11
	CanonicalRuleCount   = 42
)

// familyDef is one row of the canonical operator table.
type familyDef struct {
	family Family
	tier   Tier
	rules  []string
}

// canonicalTable is the v1 operator catalogue, in the exact order of the
// table in the design plan. Order is a contract: registry position breaks
// deduplication ties, and both `list` output and the catalogue schema are
// generated from this slice.
//
// Every v1 rule is at version 1. When a rule's emission changes, its version
// is bumped in place; rows are never reordered, because reordering would
// change which of two identical edits wins deduplication.
var canonicalTable = []familyDef{
	{FamilyBooleanLiteral, TierBalanced, []string{
		"true-to-false",
		"false-to-true",
	}},
	{FamilyConditionNegation, TierBalanced, []string{
		"negate-condition",
		"negate-loop-condition",
		"remove-negation",
	}},
	{FamilyBooleanConnective, TierBalanced, []string{
		"and-to-or",
		"or-to-and",
	}},
	{FamilyComparison, TierBalanced, []string{
		"eq-to-neq",
		"neq-to-eq",
		"lt-to-le",
		"le-to-lt",
		"gt-to-ge",
		"ge-to-gt",
	}},
	{FamilyIntegerArithmetic, TierBalanced, []string{
		"add-to-sub",
		"sub-to-add",
		"mul-to-div",
		"div-to-mul",
		"rem-to-mul",
	}},
	{FamilyFloatArithmetic, TierBalanced, []string{
		"fadd-to-fsub",
		"fsub-to-fadd",
		"fmul-to-fdiv",
		"fdiv-to-fmul",
	}},
	{FamilyReturnReplacement, TierBalanced, []string{
		"return-zero-numeric",
		"return-empty-string",
		"return-true",
		"return-false",
		"return-nil",
	}},
	{FamilyErrorSwallowing, TierBalanced, []string{
		"return-err-to-nil",
		"nil-error-branch",
	}},
	{FamilyBitwise, TierStrong, []string{
		"band-to-bor",
		"bor-to-band",
		"xor-to-band",
		"shl-to-shr",
		"shr-to-shl",
		"andnot-to-band",
	}},
	{FamilyArithmeticAssign, TierStrong, []string{
		"add-assign-to-sub-assign",
		"sub-assign-to-add-assign",
		"incr-to-decr",
		"decr-to-incr",
	}},
	{FamilyStatementDeletion, TierAll, []string{
		"delete-call-statement",
		"delete-assignment",
		"delete-incdec",
	}},
}

// Errors reported when a registry is built from an inconsistent table.
var (
	// ErrDuplicateRule reports two rules sharing one name.
	ErrDuplicateRule = errors.New("mutation: duplicate rule name")
	// ErrFamilyTierConflict reports a family whose rules disagree on tier.
	ErrFamilyTierConflict = errors.New("mutation: family rules disagree on tier")
	// ErrFamilySplit reports a family whose rules are not contiguous in
	// table order.
	ErrFamilySplit = errors.New("mutation: family rules are not contiguous")
	// ErrUnknownRule reports a rule that is not in the registry.
	ErrUnknownRule = errors.New("mutation: unknown rule")
	// ErrRuleMismatch reports a rule whose metadata disagrees with the
	// registered rule of the same name.
	ErrRuleMismatch = errors.New("mutation: rule does not match the registered rule of that name")
)

// Registry is an ordered, immutable set of rules. Position in the registry is
// meaningful: it is table order, family-major, and it is the deterministic
// tiebreak the catalogue uses when two families produce the same byte edit.
type Registry struct {
	rules       []Rule
	families    []Family
	familyTier  map[Family]Tier
	ruleIndex   map[string]int
	familyIndex map[Family]int
	familyRules map[Family][]Rule
}

// canonical is the shared v1 registry. It is built once and never mutated;
// every accessor returns copies of its slices.
var canonical = mustRegistry(canonicalTable)

// CanonicalRegistry returns the frozen v1 operator registry: 11 families and
// 42 rules in the order of the design plan's table.
func CanonicalRegistry() *Registry { return canonical }

// CanonicalRules returns the v1 rules in table order.
func CanonicalRules() []Rule { return canonical.Rules() }

// NewRegistry builds a registry from rules in table order, validating the
// invariants the catalogue relies on: unique names, valid metadata, one tier
// per family, and contiguous families.
func NewRegistry(rules []Rule) (*Registry, error) {
	r := &Registry{
		rules:       slices.Clone(rules),
		familyTier:  make(map[Family]Tier, len(rules)),
		ruleIndex:   make(map[string]int, len(rules)),
		familyIndex: make(map[Family]int),
		familyRules: make(map[Family][]Rule),
	}
	for i, rule := range r.rules {
		if err := rule.Validate(); err != nil {
			return nil, err
		}
		if prev, ok := r.ruleIndex[rule.Name]; ok {
			return nil, fmt.Errorf("%w: %q at positions %d and %d", ErrDuplicateRule, rule.Name, prev, i)
		}
		r.ruleIndex[rule.Name] = i

		if tier, ok := r.familyTier[rule.Family]; ok {
			if tier != rule.Tier {
				return nil, fmt.Errorf("%w: %q has both %s and %s", ErrFamilyTierConflict, rule.Family, tier, rule.Tier)
			}
			// The family must be contiguous: the previous rule has to
			// belong to it, otherwise table position no longer identifies a
			// family block and the dedup tiebreak stops being explainable.
			if i == 0 || r.rules[i-1].Family != rule.Family {
				return nil, fmt.Errorf("%w: %q resumes at position %d", ErrFamilySplit, rule.Family, i)
			}
		} else {
			r.familyTier[rule.Family] = rule.Tier
			r.familyIndex[rule.Family] = len(r.families)
			r.families = append(r.families, rule.Family)
		}
		r.familyRules[rule.Family] = append(r.familyRules[rule.Family], rule)
	}
	return r, nil
}

// mustRegistry builds a registry from a family table and panics on an
// inconsistency. It is only ever called on the compiled-in canonical table,
// where a failure is a programming error caught by the package tests before
// any binary ships.
func mustRegistry(table []familyDef) *Registry {
	var rules []Rule
	for _, def := range table {
		for _, name := range def.rules {
			rules = append(rules, Rule{
				Family:  def.family,
				Name:    name,
				Version: 1,
				Tier:    def.tier,
			})
		}
	}
	r, err := NewRegistry(rules)
	if err != nil {
		panic("mutation: canonical rule table is inconsistent: " + err.Error())
	}
	return r
}

// Len returns the number of registered rules.
func (r *Registry) Len() int { return len(r.rules) }

// Rules returns every rule in table order.
func (r *Registry) Rules() []Rule { return slices.Clone(r.rules) }

// Families returns every family in table order.
func (r *Registry) Families() []Family { return slices.Clone(r.families) }

// Lookup returns the registered rule with the given name.
func (r *Registry) Lookup(name string) (Rule, bool) {
	i, ok := r.ruleIndex[name]
	if !ok {
		return Rule{}, false
	}
	return r.rules[i], true
}

// Position returns the rule's index in table order. It is the deduplication
// tiebreak: the lower position is the more local rule.
func (r *Registry) Position(name string) (int, bool) {
	i, ok := r.ruleIndex[name]
	return i, ok
}

// FamilyPosition returns the family's index in table order.
func (r *Registry) FamilyPosition(f Family) (int, bool) {
	i, ok := r.familyIndex[f]
	return i, ok
}

// FamilyRules returns the rules of one family in table order.
func (r *Registry) FamilyRules(f Family) []Rule { return slices.Clone(r.familyRules[f]) }

// SelectTier returns every rule a profile at tier t selects, in table order.
func (r *Registry) SelectTier(t Tier) []Rule {
	out := make([]Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		if t.Includes(rule.Tier) {
			out = append(out, rule)
		}
	}
	return out
}

// Verify reports whether rule is registered with exactly this metadata. A
// name match with a different version or family is an error rather than a
// near miss: version and family both change how a mutant is identified and
// selected.
func (r *Registry) Verify(rule Rule) error {
	registered, ok := r.Lookup(rule.Name)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRule, rule.Name)
	}
	if registered != rule {
		return fmt.Errorf("%w: %+v is registered as %+v", ErrRuleMismatch, rule, registered)
	}
	return nil
}
