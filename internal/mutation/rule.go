// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
)

type Tier uint8

const (
	TierBalanced Tier = iota
	TierStrong
	TierAll
)

var ErrUnknownTier = errors.New("mutation: unknown tier")

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

func (t Tier) Valid() bool { return t <= TierAll }

func (t Tier) Includes(other Tier) bool { return other <= t }

func ParseTier(s string) (Tier, error) {
	for _, t := range Tiers() {
		if t.String() == s {
			return t, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownTier, s)
}

func Tiers() []Tier { return []Tier{TierBalanced, TierStrong, TierAll} }

type Family string

const (
	FamilyBooleanLiteral    Family = "boolean-literal"
	FamilyConditionNegation Family = "condition-negation"
	FamilyBooleanConnective Family = "boolean-connective"
	FamilyComparison        Family = "comparison"
	FamilyIntegerArithmetic Family = "integer-arithmetic"
	FamilyFloatArithmetic   Family = "float-arithmetic"
	FamilyReturnReplacement Family = "return-replacement"
	FamilyErrorSwallowing   Family = "error-swallowing"
	FamilyNeutralValue      Family = "neutral-value"
	FamilyBranchReplacement Family = "branch-replacement"
	FamilyBitwise           Family = "bitwise"
	FamilyArithmeticAssign  Family = "arithmetic-assignment"
	FamilyLabeledBranch     Family = "labeled-branch"
	FamilyStatementDeletion Family = "statement-deletion"
)

type Rule struct {
	Family  Family
	Name    string
	Version int
	Tier    Tier
}

func (r Rule) String() string { return r.Name + "@" + strconv.Itoa(r.Version) }

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

const (
	CanonicalFamilyCount = 14
	CanonicalRuleCount   = 49
)

type familyDef struct {
	family Family
	tier   Tier
	rules  []string
}

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
	{FamilyNeutralValue, TierStrong, []string{
		"return-empty-slice",
		"return-empty-map",
	}},
	{FamilyBranchReplacement, TierStrong, []string{
		"condition-to-true",
		"condition-to-false",
		"loop-condition-to-false",
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
	{FamilyLabeledBranch, TierAll, []string{
		"drop-break-label",
		"drop-continue-label",
	}},
	{FamilyStatementDeletion, TierAll, []string{
		"delete-call-statement",
		"delete-assignment",
		"delete-incdec",
	}},
}

var (
	ErrDuplicateRule      = errors.New("mutation: duplicate rule name")
	ErrFamilyTierConflict = errors.New("mutation: family rules disagree on tier")
	ErrFamilySplit        = errors.New("mutation: family rules are not contiguous")
	ErrUnknownRule        = errors.New("mutation: unknown rule")
	ErrRuleMismatch       = errors.New("mutation: rule does not match the registered rule of that name")
)

type Registry struct {
	rules       []Rule
	families    []Family
	familyTier  map[Family]Tier
	ruleIndex   map[string]int
	familyIndex map[Family]int
	familyRules map[Family][]Rule
}

var canonical = mustRegistry(canonicalTable)

func CanonicalRegistry() *Registry { return canonical }

func CanonicalRules() []Rule { return canonical.Rules() }

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

func (r *Registry) Len() int { return len(r.rules) }

func (r *Registry) Rules() []Rule { return slices.Clone(r.rules) }

func (r *Registry) Families() []Family { return slices.Clone(r.families) }

func (r *Registry) Lookup(name string) (Rule, bool) {
	i, ok := r.ruleIndex[name]
	if !ok {
		return Rule{}, false
	}
	return r.rules[i], true
}

func (r *Registry) Position(name string) (int, bool) {
	i, ok := r.ruleIndex[name]
	return i, ok
}

func (r *Registry) FamilyPosition(f Family) (int, bool) {
	i, ok := r.familyIndex[f]
	return i, ok
}

func (r *Registry) FamilyRules(f Family) []Rule { return slices.Clone(r.familyRules[f]) }

func (r *Registry) SelectTier(t Tier) []Rule {
	out := make([]Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		if t.Includes(rule.Tier) {
			out = append(out, rule)
		}
	}
	return out
}

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
