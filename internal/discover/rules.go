// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The operator tables.
//
// Every table below maps a token this phase can match to the rule that rewrites
// it and the token it becomes. Replacements are derived from a token rather
// than written as a string so that they cannot drift from the operator they
// claim to be, and because the token's own spelling is what the span has to
// cover: Go gives each of these operators exactly one spelling, which is what
// makes an operator-only span exact.
//
// The rule names here are the names in the canonical registry, and they are
// frozen: a rule's name and version both feed the stable mutant ID, so a
// renamed rule is a different mutant and every cached outcome for it is lost.
// Nothing in this file may invent a name the registry does not hold —
// [newMatchers] verifies each one against it.

// A tokenSwap is one entry of an operator table.
type tokenSwap struct {
	rule string
	to   token.Token
}

// comparisonSwaps is the comparison family.
var comparisonSwaps = map[token.Token]tokenSwap{
	token.EQL: {"eq-to-neq", token.NEQ},
	token.NEQ: {"neq-to-eq", token.EQL},
	token.LSS: {"lt-to-le", token.LEQ},
	token.LEQ: {"le-to-lt", token.LSS},
	token.GTR: {"gt-to-ge", token.GEQ},
	token.GEQ: {"ge-to-gt", token.GTR},
}

// connectiveSwaps is the boolean-connective family. Both operands of `&&` and
// `||` are boolean by construction, so this is the one binary family with no
// type gate of its own.
var connectiveSwaps = map[token.Token]tokenSwap{
	token.LAND: {"and-to-or", token.LOR},
	token.LOR:  {"or-to-and", token.LAND},
}

// integerSwaps is the integer-arithmetic family. `%` becomes `*` because there
// is no second remainder operator to swap it with, and multiplication is the
// neighbouring operator whose result differs from a remainder for almost every
// input.
var integerSwaps = map[token.Token]tokenSwap{
	token.ADD: {"add-to-sub", token.SUB},
	token.SUB: {"sub-to-add", token.ADD},
	token.MUL: {"mul-to-div", token.QUO},
	token.QUO: {"div-to-mul", token.MUL},
	token.REM: {"rem-to-mul", token.MUL},
}

// floatSwaps is the float-arithmetic family. It matches the same four tokens as
// the integer family and is told apart from it by the operand types alone;
// there is no `%` here because Go has no floating-point remainder operator.
var floatSwaps = map[token.Token]tokenSwap{
	token.ADD: {"fadd-to-fsub", token.SUB},
	token.SUB: {"fsub-to-fadd", token.ADD},
	token.MUL: {"fmul-to-fdiv", token.QUO},
	token.QUO: {"fdiv-to-fmul", token.MUL},
}

// bitwiseSwaps is the bitwise family. `^` and `&^` both become `&` rather than
// each other: the catalogue names one rule per source operator, and `&` is the
// operator whose result differs from both for the most inputs.
var bitwiseSwaps = map[token.Token]tokenSwap{
	token.AND:     {"band-to-bor", token.OR},
	token.OR:      {"bor-to-band", token.AND},
	token.XOR:     {"xor-to-band", token.AND},
	token.SHL:     {"shl-to-shr", token.SHR},
	token.SHR:     {"shr-to-shl", token.SHL},
	token.AND_NOT: {"andnot-to-band", token.AND},
}

// assignSwaps is the compound-assignment half of the arithmetic-assignment
// family.
var assignSwaps = map[token.Token]tokenSwap{
	token.ADD_ASSIGN: {"add-assign-to-sub-assign", token.SUB_ASSIGN},
	token.SUB_ASSIGN: {"sub-assign-to-add-assign", token.ADD_ASSIGN},
}

// incDecSwaps is the `++`/`--` half of the same family.
var incDecSwaps = map[token.Token]tokenSwap{
	token.INC: {"incr-to-decr", token.DEC},
	token.DEC: {"decr-to-incr", token.INC},
}

// booleanSwaps is the boolean-literal family, keyed by the identifier that has
// to resolve to the universe constant of that name.
var booleanSwaps = map[string]struct {
	rule string
	to   string
}{
	"true":  {"true-to-false", "false"},
	"false": {"false-to-true", "true"},
}

// The rules that no token table can key, because what they match is a syntactic
// position rather than an operator: a condition, a return value, a whole
// statement. They are listed by name so that [SupportedRules] and
// [newMatchers] stay derived from one place.
const (
	ruleNegateCondition     = "negate-condition"
	ruleNegateLoopCondition = "negate-loop-condition"
	ruleRemoveNegation      = "remove-negation"

	ruleReturnZeroNumeric = "return-zero-numeric"
	ruleReturnEmptyString = "return-empty-string"
	ruleReturnTrue        = "return-true"
	ruleReturnFalse       = "return-false"
	ruleReturnNil         = "return-nil"

	ruleReturnErrToNil = "return-err-to-nil"
	ruleNilErrorBranch = "nil-error-branch"

	ruleDeleteCallStatement = "delete-call-statement"
	ruleDeleteAssignment    = "delete-assignment"
	ruleDeleteIncDec        = "delete-incdec"
)

// positionalRules is every rule above, in canonical registry order.
var positionalRules = []string{
	ruleNegateCondition,
	ruleNegateLoopCondition,
	ruleRemoveNegation,
	ruleReturnZeroNumeric,
	ruleReturnEmptyString,
	ruleReturnTrue,
	ruleReturnFalse,
	ruleReturnNil,
	ruleReturnErrToNil,
	ruleNilErrorBranch,
	ruleDeleteCallStatement,
	ruleDeleteAssignment,
	ruleDeleteIncDec,
}

// tokenTables is every operator table, so that the set of implemented rule
// names can be derived from the tables instead of retyped beside them.
func tokenTables() []map[token.Token]tokenSwap {
	return []map[token.Token]tokenSwap{
		comparisonSwaps,
		connectiveSwaps,
		integerSwaps,
		floatSwaps,
		bitwiseSwaps,
		assignSwaps,
		incDecSwaps,
	}
}

// implementedNames is the set of rule names this phase matches, collected from
// the tables above so that adding a table entry is the whole change.
var implementedNames = func() map[string]bool {
	names := make(map[string]bool)
	for _, table := range tokenTables() {
		for _, swap := range table {
			names[swap.rule] = true
		}
	}
	for _, swap := range booleanSwaps {
		names[swap.rule] = true
	}
	for _, name := range positionalRules {
		names[name] = true
	}
	return names
}()

// SupportedRules returns the rules this phase implements, in canonical
// registry order.
//
// It is derived from the operator tables rather than listed, and it is the one
// answer to "which rules can be discovered today". A caller may hand [Discover]
// a whole profile's selection without consulting it: a registered rule this
// phase does not implement is ignored rather than refused.
func SupportedRules() []mutation.Rule {
	registry := mutation.CanonicalRegistry()
	var out []mutation.Rule
	for _, rule := range registry.Rules() {
		if implementedNames[rule.Name] {
			out = append(out, rule)
		}
	}
	return out
}

// A tokenMatcher is one selected operator rule, resolved to the exact bytes it
// replaces and the exact bytes it writes.
type tokenMatcher struct {
	rule        mutation.Rule
	original    string
	replacement string
}

// A booleanMatcher is one selected boolean-literal rule.
type booleanMatcher struct {
	rule        mutation.Rule
	replacement string
}

// matchers is the selection, indexed the way the walk asks for it.
//
// One map per family rather than one map per token, because two families match
// the same tokens and are told apart by the operand types: `a + b` is an
// [integer] candidate or a [float] one, and the walk has to be able to ask each
// question separately.
type matchers struct {
	comparison map[token.Token]tokenMatcher
	connective map[token.Token]tokenMatcher
	integer    map[token.Token]tokenMatcher
	float      map[token.Token]tokenMatcher
	bitwise    map[token.Token]tokenMatcher
	assignOp   map[token.Token]tokenMatcher
	incDec     map[token.Token]tokenMatcher
	boolean    map[string]booleanMatcher
	// positional holds the rules that match a syntactic position, by name.
	positional map[string]mutation.Rule
	// selected counts every matcher, so that a selection which chose nothing
	// this phase implements can be recognised without summing seven maps.
	selected int
}

// empty reports whether no rule was selected, in which case the walk has
// nothing to look for and every suppression is moot.
func (m matchers) empty() bool { return m.selected == 0 }

// rule returns the selected rule of a positional name.
func (m matchers) rule(name string) (mutation.Rule, bool) {
	rule, ok := m.positional[name]
	return rule, ok
}

// newMatchers resolves a rule selection into matchers.
//
// A rule the canonical registry does not know, or knows with a different
// version or family, is an error: rule name and version both feed the mutant
// ID, so accepting a near miss would mint identities that no other run can
// reproduce. A registered rule this phase has not implemented is simply not
// matched.
func newMatchers(rules []mutation.Rule) (matchers, error) {
	if len(rules) == 0 {
		rules = SupportedRules()
	}
	registry := mutation.CanonicalRegistry()
	m := matchers{
		comparison: make(map[token.Token]tokenMatcher),
		connective: make(map[token.Token]tokenMatcher),
		integer:    make(map[token.Token]tokenMatcher),
		float:      make(map[token.Token]tokenMatcher),
		bitwise:    make(map[token.Token]tokenMatcher),
		assignOp:   make(map[token.Token]tokenMatcher),
		incDec:     make(map[token.Token]tokenMatcher),
		boolean:    make(map[string]booleanMatcher),
		positional: make(map[string]mutation.Rule),
	}
	tables := []struct {
		table map[token.Token]tokenSwap
		into  map[token.Token]tokenMatcher
	}{
		{comparisonSwaps, m.comparison},
		{connectiveSwaps, m.connective},
		{integerSwaps, m.integer},
		{floatSwaps, m.float},
		{bitwiseSwaps, m.bitwise},
		{assignSwaps, m.assignOp},
		{incDecSwaps, m.incDec},
	}
	for _, rule := range rules {
		if err := registry.Verify(rule); err != nil {
			return matchers{}, &Error{
				Code:    CodeUnknownRule,
				Message: "cannot discover with rule " + strconv.Quote(rule.String()),
				Err:     err,
			}
		}
		for _, entry := range tables {
			for tok, swap := range entry.table {
				if swap.rule != rule.Name {
					continue
				}
				entry.into[tok] = tokenMatcher{
					rule:        rule,
					original:    tok.String(),
					replacement: swap.to.String(),
				}
				m.selected++
			}
		}
		for literal, swap := range booleanSwaps {
			if swap.rule != rule.Name {
				continue
			}
			m.boolean[literal] = booleanMatcher{rule: rule, replacement: swap.to}
			m.selected++
		}
		if implementedNames[rule.Name] && isPositional(rule.Name) {
			m.positional[rule.Name] = rule
			m.selected++
		}
	}
	return m, nil
}

// isPositional reports whether a rule name is one of the positional rules.
func isPositional(name string) bool {
	for _, positional := range positionalRules {
		if positional == name {
			return true
		}
	}
	return false
}
