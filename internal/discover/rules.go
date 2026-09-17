// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type tokenSwap struct {
	rule string
	to   token.Token
}

var comparisonSwaps = map[token.Token]tokenSwap{
	token.EQL: {"eq-to-neq", token.NEQ},
	token.NEQ: {"neq-to-eq", token.EQL},
	token.LSS: {"lt-to-le", token.LEQ},
	token.LEQ: {"le-to-lt", token.LSS},
	token.GTR: {"gt-to-ge", token.GEQ},
	token.GEQ: {"ge-to-gt", token.GTR},
}

var connectiveSwaps = map[token.Token]tokenSwap{
	token.LAND: {"and-to-or", token.LOR},
	token.LOR:  {"or-to-and", token.LAND},
}

var integerSwaps = map[token.Token]tokenSwap{
	token.ADD: {"add-to-sub", token.SUB},
	token.SUB: {"sub-to-add", token.ADD},
	token.MUL: {"mul-to-div", token.QUO},
	token.QUO: {"div-to-mul", token.MUL},
	token.REM: {"rem-to-mul", token.MUL},
}

var floatSwaps = map[token.Token]tokenSwap{
	token.ADD: {"fadd-to-fsub", token.SUB},
	token.SUB: {"fsub-to-fadd", token.ADD},
	token.MUL: {"fmul-to-fdiv", token.QUO},
	token.QUO: {"fdiv-to-fmul", token.MUL},
}

var bitwiseSwaps = map[token.Token]tokenSwap{
	token.AND:     {"band-to-bor", token.OR},
	token.OR:      {"bor-to-band", token.AND},
	token.XOR:     {"xor-to-band", token.AND},
	token.SHL:     {"shl-to-shr", token.SHR},
	token.SHR:     {"shr-to-shl", token.SHL},
	token.AND_NOT: {"andnot-to-band", token.AND},
}

var assignSwaps = map[token.Token]tokenSwap{
	token.ADD_ASSIGN: {"add-assign-to-sub-assign", token.SUB_ASSIGN},
	token.SUB_ASSIGN: {"sub-assign-to-add-assign", token.ADD_ASSIGN},
}

var incDecSwaps = map[token.Token]tokenSwap{
	token.INC: {"incr-to-decr", token.DEC},
	token.DEC: {"decr-to-incr", token.INC},
}

var booleanSwaps = map[string]struct {
	rule string
	to   string
}{
	"true":  {"true-to-false", "false"},
	"false": {"false-to-true", "true"},
}

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

	ruleReturnEmptySlice = "return-empty-slice"
	ruleReturnEmptyMap   = "return-empty-map"

	ruleConditionToTrue      = "condition-to-true"
	ruleConditionToFalse     = "condition-to-false"
	ruleLoopConditionToFalse = "loop-condition-to-false"

	ruleDropBreakLabel    = "drop-break-label"
	ruleDropContinueLabel = "drop-continue-label"

	ruleDeleteCallStatement = "delete-call-statement"
	ruleDeleteAssignment    = "delete-assignment"
	ruleDeleteIncDec        = "delete-incdec"
)

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
	ruleReturnEmptySlice,
	ruleReturnEmptyMap,
	ruleConditionToTrue,
	ruleConditionToFalse,
	ruleLoopConditionToFalse,
	ruleDropBreakLabel,
	ruleDropContinueLabel,
	ruleDeleteCallStatement,
	ruleDeleteAssignment,
	ruleDeleteIncDec,
}

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

type tokenMatcher struct {
	rule        mutation.Rule
	original    string
	replacement string
}

type booleanMatcher struct {
	rule        mutation.Rule
	replacement string
}

type matchers struct {
	comparison map[token.Token]tokenMatcher
	connective map[token.Token]tokenMatcher
	integer    map[token.Token]tokenMatcher
	float      map[token.Token]tokenMatcher
	bitwise    map[token.Token]tokenMatcher
	assignOp   map[token.Token]tokenMatcher
	incDec     map[token.Token]tokenMatcher
	boolean    map[string]booleanMatcher
	positional map[string]mutation.Rule
	selected   int
}

func (m matchers) empty() bool { return m.selected == 0 }

func (m matchers) rule(name string) (mutation.Rule, bool) {
	rule, ok := m.positional[name]
	return rule, ok
}

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

func isPositional(name string) bool {
	for _, positional := range positionalRules {
		if positional == name {
			return true
		}
	}
	return false
}
