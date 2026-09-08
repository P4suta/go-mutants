// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import "testing"

// scanStmt scans a file with one function whose body is the given statements,
// so a test can pin the guard form discovery computes for a statement in
// isolation. The parameters cover the operand and receiver shapes the guard
// tests need.
func scanStmt(t *testing.T, body string) scanned {
	t.Helper()
	return scanSource(t, `package pkg

type flags struct{ ok, no bool }

func sink(int)               {}
func pred(int) bool          { return true }

func F(a, b, n int, ch chan int, s flags, m map[int]int) int {
	`+body+`
	return a
}
`)
}

// TestStatementFormsAreSVsD pins statementGuard: a statement that declares
// nothing is Form S, and a `:=` or `var` that declares a name is Form D. Every
// row exercises a distinct case of the type switch in statementGuard, so a
// mutation that folds two cases together — dropping the DEFINE test, treating a
// DeclStmt as Form S — changes exactly one row's form.
func TestStatementFormsAreSVsD(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		rule     string
		original string
		wantForm GuardForm
	}{
		{"return declares nothing", "return a + b", "add-to-sub", "+", GuardFormS},
		{"increment declares nothing", "a++", "incr-to-decr", "++", GuardFormS},
		{"send declares nothing", "ch <- a + b", "add-to-sub", "+", GuardFormS},
		{"plain assignment declares nothing", "a = a + b", "add-to-sub", "+", GuardFormS},
		{"compound assignment declares nothing", "a += b - n", "sub-to-add", "-", GuardFormS},
		{"short declaration declares", "c := a + b; sink(c)", "add-to-sub", "+", GuardFormD},
		{"var declaration declares", "var c = a + b; sink(c)", "add-to-sub", "+", GuardFormD},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, ok := scanStmt(t, tc.body).guard(tc.rule, tc.original)
			if !ok {
				t.Fatalf("no %s candidate for %q", tc.rule, tc.body)
			}
			if g.Form != tc.wantForm {
				t.Errorf("form = %s, want %s for %q", g.Form, tc.wantForm, tc.body)
			}
		})
	}
}

// TestFormDNamesEveryDeclaredIdentifier pins defineTypes and declTypes: a Form
// D site declares every non-blank name on its left, in source order, with the
// type each was inferred as, and passes over the blank identifier. Mutating the
// `_` test or the order would change the summary.
func TestFormDNamesEveryDeclaredIdentifier(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{"single short decl", "c := a + b; sink(c)", "c:int"},
		{"multi short decl", "c, d := a+b, a-b; sink(c); sink(d)", "c:int d:int"},
		{"blank is passed over", "_, d := a+b, a-b; sink(d)", "d:int"},
		{"var single", "var c = a + b; sink(c)", "c:int"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, ok := scanStmt(t, tc.body).guard("add-to-sub", "+")
			if !ok {
				t.Fatalf("no add-to-sub candidate for %q", tc.body)
			}
			if g.Form != GuardFormD {
				t.Fatalf("form = %s, want D for %q", g.Form, tc.body)
			}
			if got := declSummary(g); got != tc.want {
				t.Errorf("declared = %q, want %q for %q", got, tc.want, tc.body)
			}
		})
	}
}

// TestBoolExpressionsAreFormC pins formCSite: a bool-valued expression that may
// legally be parenthesised is a Form C site, whether it is a return value, an
// `if` condition, a `for` condition, or a bool struct field read.
func TestBoolExpressionsAreFormC(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		src      string
		rule     string
		original string
	}{
		{"return value", "package pkg\nfunc F(a, b int) bool { return a < b }\n", "lt-to-le", "<"},
		{"if condition", "package pkg\nfunc F(a, b int) int { if a < b { return 1 }\nreturn 0 }\n", "lt-to-le", "<"},
		{"for condition", "package pkg\nfunc F(a, b int) { for a < b { a++ } }\n", "lt-to-le", "<"},
		{"bool field read", "package pkg\ntype S struct{ ok bool }\nfunc F(s S) bool { return s.ok }\n", "return-true", "s.ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, ok := scanSource(t, tc.src).guard(tc.rule, tc.original)
			if !ok {
				t.Fatalf("no %s candidate", tc.rule)
			}
			if g.Form != GuardFormC {
				t.Errorf("form = %s, want C", g.Form)
			}
		})
	}
}

// TestAFieldNameIsNotItsOwnFormCSite pins the SelectorExpr case of
// wrappablePosition: `s.ok` reads a bool, so the whole selector is one Form C
// site, but the field name `ok` is not an expression that may be wrapped. The
// scan produces exactly one return-true candidate — on `s.ok` — and none on the
// bare field name. Flipping `parent.Sel != expr` to `==` would emit a second
// candidate on the field name.
func TestAFieldNameIsNotItsOwnFormCSite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

type S struct{ ok bool }

func F(s S) bool { return s.ok }
`)
	if n := got.count("return-true", "s.ok"); n != 1 {
		t.Errorf("return-true on s.ok fired %d times, want 1: %v", n, got.rules())
	}
	if n := got.count("return-true", "ok"); n != 0 {
		t.Errorf("return-true fired on the bare field name %d times, want 0: %v", n, got.rules())
	}
}

// TestSimpleStatementSlotsAreRefused pins blockIsLegalFor: a statement in the
// init or post slot of a `for`, the init of an `if`, or the tag of a `switch`
// cannot be replaced by an `if` block, so a candidate whose nearest statement
// is one of those slots is suppressed rather than emitted with a form the
// instrumenter cannot use.
func TestSimpleStatementSlotsAreRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{
		{"for post", "package pkg\nfunc F(n int) { for i := 0; i < n; i = i + 1 {\n_ = i\n} }\n"},
		{"for init", "package pkg\nfunc F(n int) { for i := n - 1; i > 0; i-- {\n_ = i\n} }\n"},
		{"if init", "package pkg\nfunc F(a, b int) int { if c := a + b; c > 0 {\nreturn c\n}\nreturn 0 }\n"},
		{"switch tag", "package pkg\nfunc F(a, b int) int { switch a + b {\ncase 1:\nreturn 1\n}\nreturn 0 }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSource(t, tc.src)
			// The arithmetic in the refused slot must not be emitted as a
			// candidate; it is suppressed as an unnameable-decl-type skip.
			if got.count("add-to-sub", "+")+got.count("sub-to-add", "-") > 0 {
				t.Errorf("a refused slot produced an arithmetic candidate: %v", got.rules())
			}
			if len(got.sites) == 0 {
				t.Errorf("a refused slot recorded no skip site: %v", got.rules())
			}
		})
	}
}

// TestARedeclaringShortDeclarationIsRefused pins defineTypes: a `:=` that
// rebinds a name already in scope cannot be expressed as Form D, which would
// have to declare some names and leave others alone, so the site is refused
// whole. The arithmetic in its right-hand side produces no candidate.
func TestARedeclaringShortDeclarationIsRefused(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func F(a, b int) (int, error) {
	err := error(nil)
	c, err := a+b, error(nil)
	_ = err
	return c, err
}
`)
	if got.count("add-to-sub", "+") > 0 {
		t.Errorf("a redeclaring short declaration produced an arithmetic candidate: %v", got.rules())
	}
}

// TestFormDSpellsNamedAndImportedTypes pins declTypeOf and typeString: a Form D
// site declares each name with the source spelling of its type, qualified
// against the file's own imports. A named type from the same package is spelled
// bare, and an imported type is spelled with its package qualifier, which is
// what the rewrite has to write back into the file verbatim.
func TestFormDSpellsNamedAndImportedTypes(t *testing.T) {
	t.Parallel()

	named, ok := scanSource(t, `package pkg

type Celsius float64

func F(a, b Celsius) Celsius { c := a + b; return c }
`).guard("fadd-to-fsub", "+")
	if !ok {
		t.Fatal("no fadd-to-fsub candidate for the named-type sum")
	}
	if got := declSummary(named); got != "c:Celsius" {
		t.Errorf("named-type declaration = %q, want %q", got, "c:Celsius")
	}

	imported, ok := scanSource(t, `package pkg

import "time"

func F(a, b int) time.Duration { c := time.Duration(a + b); return c }
`).guard("add-to-sub", "+")
	if !ok {
		t.Fatal("no add-to-sub candidate for the imported-type sum")
	}
	if got := declSummary(imported); got != "c:time.Duration" {
		t.Errorf("imported-type declaration = %q, want %q", got, "c:time.Duration")
	}
}
