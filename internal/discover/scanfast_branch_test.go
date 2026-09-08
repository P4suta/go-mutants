// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import "testing"

// ifCond scans a function whose body is `if <cond> { return 1 }` over the given
// preamble, so a test can pin whether the branch-proof phase attaches a proof
// to an edit inside that condition. The condition body is on its own lines so
// its brace span is stable.
func ifCond(t *testing.T, preamble, cond string) scanned {
	t.Helper()
	return scanSource(t, "package pkg\n"+preamble+`
func F(a, b int, x []int, err error) int {
	if `+cond+` {
		return 1
	}
	return 0
}
`)
}

// TestOnlyDecreasingEditsCarryABranchProof pins decreasingRules: an edit that
// narrows a condition (le-to-lt, ge-to-gt, or-to-and, nil-error-branch) carries
// a proof, and an edit that widens it (lt-to-le, gt-to-ge, and-to-or) does not.
// Adding or removing a row from decreasingRules flips exactly one case.
func TestOnlyDecreasingEditsCarryABranchProof(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		preamble string
		cond     string
		rule     string
		original string
		want     bool
	}{
		{"le narrows", "", "a <= b", "le-to-lt", "<=", true},
		{"ge narrows", "", "a >= b", "ge-to-gt", ">=", true},
		{"or narrows", "", "a < b || a > b", "or-to-and", "||", true},
		{"nil-error narrows", "", "err != nil", ruleNilErrorBranch, "err != nil", true},
		{"lt widens", "", "a < b", "lt-to-le", "<", false},
		{"gt widens", "", "a > b", "gt-to-ge", ">", false},
		{"and widens", "", "a < b && a > 0", "and-to-or", "&&", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ifCond(t, tc.preamble, tc.cond).branch(tc.rule, tc.original) != nil
			if got != tc.want {
				t.Errorf("branch proof present = %t for %q, want %t", got, tc.cond, tc.want)
			}
		})
	}
}

// TestABranchProofNeedsAnInertConditionAndABody pins the three refusals in
// branchProof past the decreasing test: the whole condition must be inert, the
// gated body must hold a statement, and the edit must sit in an `if` or `for`
// condition rather than anywhere else a decreasing edit can appear.
func TestABranchProofNeedsAnInertConditionAndABody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			"inert integer comparison",
			"package pkg\nfunc F(a, b int) int { if a <= b {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"len is an inert builtin",
			"package pkg\nfunc F(x []int, b int) int { if len(x) <= b {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a decreasing edit nested in a connective is walked to the if",
			"package pkg\nfunc F(a, b, c int) int { if a <= b && c > 0 {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a decreasing edit nested in a disjunction is walked too",
			"package pkg\nfunc F(a, b, c int) int { if a <= b || c > 0 {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a for condition is gated too",
			"package pkg\nfunc F(a, b int) int { s := 0\nfor a <= b {\ns++\na++\n}\nreturn s }\n",
			true,
		},
		{
			"an impure call is not inert",
			"package pkg\nfunc imp() int { return 0 }\nfunc F(b int) int { if imp() <= b {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
		{
			"an empty body proves nothing",
			"package pkg\nfunc F(a, b int) int { if a <= b {\n}\nreturn 0 }\n",
			false,
		},
		{
			"a field read of a call result is not inert",
			"package pkg\ntype S struct{ n int }\nfunc g() S { return S{} }\nfunc F(b int) int { if g().n <= b {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
		{
			"a comparison outside a condition is not gated",
			"package pkg\nfunc F(a, b int) bool { return a <= b }\n",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSource(t, tc.src).branch("le-to-lt", "<=") != nil
			if got != tc.want {
				t.Errorf("branch proof present = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestInertSelectorTellsAQualifierFromADereference pins inertSelector: a
// package-qualified constant reads a name and is inert, a field read of a value
// struct is inert, and a field read through a pointer may panic on nil and is
// not — so a decreasing edit beside each is proved only for the first two.
func TestInertSelectorTellsAQualifierFromADereference(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			"package-qualified constant is inert",
			"package pkg\nimport \"math\"\nfunc F(b float64) int { if math.Pi <= b {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a value field read is inert",
			"package pkg\ntype S struct{ n int }\nfunc F(s S, b int) int { if s.n <= b {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a pointer field read may panic",
			"package pkg\ntype S struct{ n int }\nfunc F(p *S, b int) int { if p.n <= b {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSource(t, tc.src).branch("le-to-lt", "<=") != nil
			if got != tc.want {
				t.Errorf("branch proof present = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestInertBinaryAdmitsOnlyProvablySafeOperators pins inertBinary and the
// analyses it defers to: a division or shift is inert only when its divisor or
// count is a constant the compiler has folded, and an equality is inert only
// when neither operand can reach a dynamic type. Each row is a condition around
// a decreasing edit, so the branch proof is present exactly when the whole
// condition is inert.
func TestInertBinaryAdmitsOnlyProvablySafeOperators(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			"a constant divisor cannot divide by zero",
			"package pkg\nfunc F(a, b int) int { if a/2 <= b {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a variable divisor may divide by zero",
			"package pkg\nfunc F(a, b, c int) int { if a/c <= b {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
		{
			"a constant shift count is safe",
			"package pkg\nfunc F(a, b int) int { if a>>2 <= b {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a variable shift count may be negative",
			"package pkg\nfunc F(a, b int, n uint) int { if a>>n <= b {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSource(t, tc.src).branch("le-to-lt", "<=") != nil
			if got != tc.want {
				t.Errorf("branch proof present = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestAnEqualityIsInertOnlyWhenItCannotPanic pins safelyComparable and
// comparableWithoutPanic through the branch proof: an integer equality reads no
// dynamic type and is inert, but an equality between interface values compiles
// and may panic on an incomparable dynamic type and is not — so an or-to-and
// edit whose condition holds one is proved and the other is not. A conversion
// beside the equality stays inert, which exercises inertConversion.
func TestAnEqualityIsInertOnlyWhenItCannotPanic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			"an integer equality cannot panic",
			"package pkg\nfunc F(a, b, c, d int) int { if a == b || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"an interface equality may panic",
			"package pkg\nfunc F(e, f error, c, d int) int { if e == f || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
		{
			"a numeric conversion stays inert",
			"package pkg\nfunc F(x int32, b, c, d int) int { if int(x) == b || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"an array of comparable elements is inert",
			"package pkg\nfunc F(a1, a2 [2]int, c, d int) int { if a1 == a2 || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"an array of interfaces may panic on comparison",
			"package pkg\nfunc F(a1, a2 [2]any, c, d int) int { if a1 == a2 || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
		{
			"a struct of comparable fields is inert",
			"package pkg\ntype P struct{ x, y int }\nfunc F(p1, p2 P, c, d int) int { if p1 == p2 || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			true,
		},
		{
			"a struct with an interface field may panic",
			"package pkg\ntype Q struct{ v any }\nfunc F(q1, q2 Q, c, d int) int { if q1 == q2 || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
		{
			"a slice-to-array conversion is not inert",
			"package pkg\nfunc F(s []int, b, c, d int) int { if [2]int(s) == [2]int{} || c <= d {\nreturn 1\n}\nreturn 0 }\n",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSource(t, tc.src).branch("or-to-and", "||") != nil
			if got != tc.want {
				t.Errorf("branch proof present = %t, want %t", got, tc.want)
			}
		})
	}
}
