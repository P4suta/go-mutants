// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import "testing"

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

func TestASimpleStatementSlotIsReachedByAClosureForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		rule string
		orig string
		form GuardForm
	}{
		{"for post", "package pkg\nfunc F(n int) { for i := 0; i < n; i = i + 1 {\n_ = i\n} }\n", "add-to-sub", "+", GuardFormF},
		{"for init", "package pkg\nfunc F(n int) { for i := n - 1; i > 0; i-- {\n_ = i\n} }\n", "sub-to-add", "-", GuardFormE},
		{"if init", "package pkg\nfunc F(a, b int) int { if c := a + b; c > 0 {\nreturn c\n}\nreturn 0 }\n", "add-to-sub", "+", GuardFormE},
		{"switch tag", "package pkg\nfunc F(a, b int) int { switch a + b {\ncase 1:\nreturn 1\n}\nreturn 0 }\n", "add-to-sub", "+", GuardFormE},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSource(t, tc.src)
			g, ok := got.guard(tc.rule, tc.orig)
			if !ok {
				t.Fatalf("no %s candidate in this slot: %v", tc.rule, got.rules())
			}
			if g.Form != tc.form {
				t.Errorf("guard form = %q, want %q", g.Form, tc.form)
			}
			if len(got.sites) != 0 {
				t.Errorf("a slot a form reaches recorded %d skip site(s)", len(got.sites))
			}
		})
	}
}

func TestARedeclaringShortDeclarationIsReachedByTheExpressionForm(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func F(a, b int) (int, error) {
	err := error(nil)
	c, err := a+b, error(nil)
	_ = err
	return c, err
}
`)
	g, ok := got.guard("add-to-sub", "+")
	if !ok {
		t.Fatalf("no add-to-sub candidate in a redeclaring short declaration: %v", got.rules())
	}
	if g.Form != GuardFormE {
		t.Errorf("guard form = %q, want %q -- Form D cannot declare a name that is already declared", g.Form, GuardFormE)
	}
}

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
