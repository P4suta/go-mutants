// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import "testing"

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
	return g.Probe != nil && g.Probe.Form == ProbeFormReturn
}

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
