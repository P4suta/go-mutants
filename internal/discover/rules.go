// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// comparisonSwaps is the comparison family: which operator token each rule
// matches, and which token it becomes.
//
// The replacement is derived from a token rather than written as a string so
// that it cannot drift from the operator it claims to be, and because the
// token's own spelling is what the span has to cover: Go gives each of these
// operators exactly one spelling, which is what makes an operator-only span
// exact.
var comparisonSwaps = map[token.Token]struct {
	rule string
	to   token.Token
}{
	token.EQL: {"eq-to-neq", token.NEQ},
	token.NEQ: {"neq-to-eq", token.EQL},
	token.LSS: {"lt-to-le", token.LEQ},
	token.LEQ: {"le-to-lt", token.LSS},
	token.GTR: {"gt-to-ge", token.GEQ},
	token.GEQ: {"ge-to-gt", token.GTR},
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

// SupportedRules returns the rules this phase implements, in canonical
// registry order.
//
// It is smaller than the registry on purpose: the operator catalogue is
// complete and frozen, while the phases that implement it land one family at a
// time. A caller may hand [Discover] a whole profile's selection without
// tracking which families exist yet — the ones that do not are ignored rather
// than refused, and this function is how a caller can say so out loud.
func SupportedRules() []mutation.Rule {
	registry := mutation.CanonicalRegistry()
	var out []mutation.Rule
	for _, rule := range registry.Rules() {
		if supportedName(rule.Name) {
			out = append(out, rule)
		}
	}
	return out
}

// supportedName reports whether a rule name is one this phase implements.
func supportedName(name string) bool {
	for _, swap := range comparisonSwaps {
		if swap.rule == name {
			return true
		}
	}
	for _, swap := range booleanSwaps {
		if swap.rule == name {
			return true
		}
	}
	return false
}

// A comparisonMatcher is one selected comparison rule, resolved to the exact
// bytes it replaces and the exact bytes it writes.
type comparisonMatcher struct {
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
type matchers struct {
	comparison map[token.Token]comparisonMatcher
	boolean    map[string]booleanMatcher
}

// empty reports whether no rule was selected, in which case the walk has
// nothing to look for and every suppression is moot.
func (m matchers) empty() bool { return len(m.comparison) == 0 && len(m.boolean) == 0 }

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
		comparison: make(map[token.Token]comparisonMatcher),
		boolean:    make(map[string]booleanMatcher),
	}
	for _, rule := range rules {
		if err := registry.Verify(rule); err != nil {
			return matchers{}, &Error{
				Code:    CodeUnknownRule,
				Message: "cannot discover with rule " + strconv.Quote(rule.String()),
				Err:     err,
			}
		}
		for tok, swap := range comparisonSwaps {
			if swap.rule != rule.Name {
				continue
			}
			m.comparison[tok] = comparisonMatcher{
				rule:        rule,
				original:    tok.String(),
				replacement: swap.to.String(),
			}
		}
		for literal, swap := range booleanSwaps {
			if swap.rule != rule.Name {
				continue
			}
			m.boolean[literal] = booleanMatcher{rule: rule, replacement: swap.to}
		}
	}
	return m, nil
}
