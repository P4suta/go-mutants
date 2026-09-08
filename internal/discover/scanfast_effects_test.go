// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import "testing"

// probeHintFor scans a function returning result type typ whose body is
// `return <expr>` and reports whether the return-value candidate for that
// statement carries a probe hint. The hint is present only when every operand
// of the returned expression is both effect-free and panic-free, so the answer
// pins the effect and panic analyses in effects.go through the one output they
// gate. The result type selects which return-value rule fires (numeric, string,
// bool, or a nillable type), so the caller pairs typ with an expr of that type.
func probeHintFor(t *testing.T, typ, expr, rule string) bool {
	t.Helper()
	src := `package pkg

type P struct{ X, Y int }
type R struct{ n int }
type Q struct{ r R }

func g() int { return 0 }

func F(a, b int, sl []int, x int32, bs []byte, e error, q Q, pq *R, p *int, s string) ` + typ + ` {
	return ` + expr + `
}
`
	g, ok := scanSource(t, src).guard(rule, expr)
	if !ok {
		t.Fatalf("no %s candidate for %q", rule, expr)
	}
	return g.Return != nil
}

// TestAProbeHintNeedsEffectFreeAndPanicFreeOperands pins effectFree and
// panicFree through the return probe hint they gate. Every row is a distinct
// branch of the two analyses: an operator combination and an inert builtin and
// a conversion are safe, while an index, a dereference, and a call are not — so
// a mutation that widens either analysis lights up exactly one row that should
// be absent.
func TestAProbeHintNeedsEffectFreeAndPanicFreeOperands(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		typ, expr string
		rule      string
		want      bool
	}{
		{"arithmetic of parameters", "int", "a + b", "return-zero-numeric", true},
		{"a unary negation", "int", "-a", "return-zero-numeric", true},
		{"a parenthesised sum", "int", "(a + b)", "return-zero-numeric", true},
		{"a numeric conversion", "int", "int(x)", "return-zero-numeric", true},
		{"a string conversion", "string", "string(bs)", "return-empty-string", true},
		{"len is an inert builtin", "int", "cap(sl)", "return-zero-numeric", true},
		{"min is an inert builtin", "int", "min(a, b)", "return-zero-numeric", true},
		{"an index may panic", "int", "sl[a]", "return-zero-numeric", false},
		{"a dereference may panic", "int", "*p", "return-zero-numeric", false},
		{"a call may have effects", "int", "g() + a", "return-zero-numeric", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probeHintFor(t, tc.typ, tc.expr, tc.rule); got != tc.want {
				t.Errorf("probe hint present = %t for %q, want %t", got, tc.expr, tc.want)
			}
		})
	}
}

// TestAProbeHintReadsIntoCompositesAndSelectors pins compositeParts and
// panicFreeSelector: a composite literal is probe-safe exactly when every part
// is, and a selector is probe-safe exactly when it dereferences no pointer. A
// slice or map whose element or key may panic is refused, and a field read
// through a pointer is refused, while their value-only counterparts are
// accepted.
func TestAProbeHintReadsIntoCompositesAndSelectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		typ, expr string
		rule      string
		want      bool
	}{
		{"a slice of safe parts", "[]int", "[]int{a, b}", "return-nil", true},
		{"a slice with an indexed part", "[]int", "[]int{sl[a]}", "return-nil", false},
		{"a map of safe parts", "map[int]int", "map[int]int{a: b}", "return-nil", true},
		{"a map with an indexed key", "map[int]int", "map[int]int{sl[a]: b}", "return-nil", false},
		{"a value selector chain", "int", "q.r.n", "return-zero-numeric", true},
		{"a pointer field read", "int", "pq.n", "return-zero-numeric", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probeHintFor(t, tc.typ, tc.expr, tc.rule); got != tc.want {
				t.Errorf("probe hint present = %t for %q, want %t", got, tc.expr, tc.want)
			}
		})
	}
}

// TestAProbeHintRefusesAComparisonThatMayPanic pins comparesWithoutPanic
// through the probe hint: comparing two integers cannot panic and is
// probe-safe, but a comparison that reaches interface dynamic types — an error
// against nil, a slice against nil — is refused because the runtime comparison
// can panic on an incomparable dynamic type.
func TestAProbeHintRefusesAComparisonThatMayPanic(t *testing.T) {
	t.Parallel()

	if !probeHintFor(t, "bool", "a == b", "return-true") {
		t.Error("an integer comparison should be probe-safe")
	}
	if probeHintFor(t, "bool", "e == nil", "return-true") {
		t.Error("an interface comparison should not be probe-safe")
	}
	if probeHintFor(t, "bool", "sl == nil", "return-true") {
		t.Error("a slice comparison should not be probe-safe")
	}
}

// TestABoolLiteralMapKeyIsAFormCSite pins the map arm of the KeyValueExpr case
// in wrappablePosition: a boolean literal used as a map key is an ordinary
// value that may be parenthesised, so it is a Form C site and carries a
// true-to-false candidate.
func TestABoolLiteralMapKeyIsAFormCSite(t *testing.T) {
	t.Parallel()

	got, ok := scanSource(t, `package pkg

func F() map[bool]int { return map[bool]int{true: 1} }
`).guard("true-to-false", "true")
	if !ok {
		t.Fatal("no true-to-false candidate for the bool map key")
	}
	if got.Form != GuardFormC {
		t.Errorf("form = %s, want C for a bool map key", got.Form)
	}
}
