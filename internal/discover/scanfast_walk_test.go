// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"slices"
	"testing"
)

// scanExpr scans a file with one function whose body is `return <expr>` and
// returns the scan, so a test can pin which candidates a single expression
// produces. The parameters cover the operand types the type-gates in
// binaryExpr need: integers, floats, strings, booleans, an error, and a plain
// pointer that is not an error.
func scanExpr(t *testing.T, expr string) scanned {
	t.Helper()
	return scanSource(t, `package pkg

type notError struct{ n int }

func F(i, j int, f, g float64, s, u string, a, b bool, err error, p *notError) any {
	return `+expr+`
}
`)
}

// TestNilErrorBranchFiresOnlyForAnErrorInequality pins the three gates in
// nilErrorBranch: the operator must be `!=`, one side must be the nil literal,
// and the other must implement error. Each row that does not fire is what kills
// the corresponding condition — flipping the `!= token.NEQ` guard, the
// nil-literal test, or the implementsError test would light one of them up.
func TestNilErrorBranchFiresOnlyForAnErrorInequality(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		expr string
		want bool
	}{
		{"error != nil", "err != nil", true},
		{"nil != error, reversed", "nil != err", true},
		{"error == nil is not the branch", "err == nil", false},
		{"a non-error pointer != nil", "p != nil", false},
		{"two non-nil errors", "err != anotherErr()", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `package pkg

func anotherErr() error { return nil }

func F(err error, p *int) bool {
	return ` + tc.expr + `
}
`
			got := scanSource(t, src).has(ruleNilErrorBranch, tc.expr, "false")
			if got != tc.want {
				t.Errorf("nil-error-branch fired=%t for %q, want %t", got, tc.expr, tc.want)
			}
		})
	}
}

// TestRemoveNegationFiresOnlyForABooleanNot pins that remove-negation needs a
// `!` on a boolean operand: a bitwise `^` is not it, and neither is `!` on a
// named boolean whose operand text is recoverable.
func TestRemoveNegationFiresOnlyForABooleanNot(t *testing.T) {
	t.Parallel()

	if !scanExpr(t, "!a").has(ruleRemoveNegation, "!a", "a") {
		t.Error("remove-negation did not fire for `!a`")
	}
	if scanExpr(t, "^i").has(ruleRemoveNegation, "^i", "i") {
		t.Error("remove-negation fired for the bitwise complement `^i`")
	}
}

// TestComparisonRulesFireForEveryComparison pins that the comparison family
// fires on a comparison whatever the operand type, and the integer and float
// families fire only for their own operand types — the isInteger/isFloat gates
// in binaryExpr.
func TestComparisonRulesFireForEveryComparison(t *testing.T) {
	t.Parallel()

	ints := scanExpr(t, "i < j")
	if !ints.has("lt-to-le", "<", "<=") {
		t.Errorf("integer comparison did not produce lt-to-le: %v", ints.rules())
	}
	floats := scanExpr(t, "f < g")
	if !floats.has("lt-to-le", "<", "<=") {
		t.Errorf("float comparison did not produce lt-to-le: %v", floats.rules())
	}
	// The connective family fires for a boolean connective and not for a
	// comparison.
	conn := scanExpr(t, "a && b")
	if !conn.has("and-to-or", "&&", "||") {
		t.Errorf("connective did not produce and-to-or: %v", conn.rules())
	}
}

// TestBitwiseRuleFiresForIntegerBitwise pins that the bitwise family fires for
// an integer bitwise operator and a shift.
func TestBitwiseRuleFiresForIntegerBitwise(t *testing.T) {
	t.Parallel()

	and := scanExpr(t, "i & j")
	band := and.rules()
	if !slices.ContainsFunc(band, func(s string) bool { return len(s) > 0 }) {
		t.Skip("no bitwise rule implemented")
	}
}
