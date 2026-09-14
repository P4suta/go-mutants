// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"os"
	"strings"
	"testing"
)

// The value probe form is the boolean form for everything that is not a
// boolean, and it is where most of a catalogue lives: the arithmetic, the
// bitwise and the comparison families all edit an expression whose value can be
// compared.
//
// It costs what the boolean form does not — the type has to be written out —
// and it buys a site no statement rewrite could reach, because it stands where
// the expression stood rather than hoisting anything to a statement that may
// not exist.

// valueProbeOf returns the probe hint of the one candidate of a rule.
func valueProbeOf(t *testing.T, got scanned, rule string) *ProbeSite {
	t.Helper()

	for _, candidate := range got.candidates {
		if candidate.Rule.Name == rule {
			return candidate.Guard.Probe
		}
	}
	t.Fatalf("the scan found %v, and none of them is %s", got.rules(), rule)
	return nil
}

// TestAnArithmeticOperandIsMeasuredWhereItStands is the ordinary case.
func TestAnArithmeticOperandIsMeasuredWhereItStands(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Total adds two numbers and doubles the result.
func Total(a, b int) int {
	n := (a + b) * 2
	return n
}
`)
	site := valueProbeOf(t, got, "add-to-sub")
	if site == nil {
		t.Fatal("an addition of two parameters is not probed")
	}
	if site.Form != ProbeFormValue {
		t.Errorf("form = %q, want %q", site.Form, ProbeFormValue)
	}
	if len(site.Types) != 1 || site.Types[0] != "int" {
		t.Errorf("Types = %q, want the one type the closure writes", site.Types)
	}
}

// TestAValueSiteWithNoStatementToHoistTo is the shape the form exists for.
//
// A `switch` tag is evaluated where it is written and has no statement around
// it: a rewrite that declared a temporary would have nowhere to declare it, and
// one that hoisted the tag out would evaluate it before the `switch` rather
// than as part of it. Standing where the expression stood needs neither.
func TestAValueSiteWithNoStatementToHoistTo(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Kind names a bucket.
func Kind(a, b int) string {
	switch a + b {
	case 0:
		return "zero"
	}
	return "other"
}
`)
	site := valueProbeOf(t, got, "add-to-sub")
	if site == nil {
		t.Fatal("a switch tag is not probed, though the form needs no statement")
	}
	if site.Form != ProbeFormValue {
		t.Errorf("form = %q, want %q", site.Form, ProbeFormValue)
	}
}

// TestAFloatingSiteIsNotProbed is the return form's rule applied here, and for
// the return form's reason.
//
// IEEE 754 says `-0.0 == 0`, so a site holding negative zero would be recorded
// as never having differed from a mutant that returns zero — the answer that
// skips the test — while `math.Signbit` and `1/x` both tell the two apart.
func TestAFloatingSiteIsNotProbed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Half halves the sum.
func Half(a, b float64) float64 {
	n := (a + b) / 2
	return n
}
`)
	if site := valueProbeOf(t, got, "fadd-to-fsub"); site != nil {
		t.Errorf("probe site = %+v, and `-0.0 != 0` is false", site)
	}
}

// TestAnIncomparableSiteIsNotProbed keeps the measurement legal Go.
//
// `p != mutated` is the whole rewrite, and a slice is not comparable at all —
// so a site whose value is one has no measurement to make, whatever else is
// true of it.
func TestAnIncomparableSiteIsNotProbed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Window returns a window of the slice.
func Window(xs []int, lo int) []int {
	w := xs[lo+1:]
	return w
}
`)
	site := valueProbeOf(t, got, "add-to-sub")
	if site == nil {
		return // the slice expression is refused and the walk found nothing else
	}
	if site.Types[0] == "[]int" {
		t.Errorf("probe site = %+v, and a slice cannot be compared with `!=`", site)
	}
}

// TestAValueSiteBesideACallIsNotProbed is the hazard the ordering rule exists
// for, asked of the form that found it.
//
// The rewrite puts a *call* where an expression stood, and Go orders calls
// within a statement's operands while leaving a plain read among them
// unordered. So a site beside another call would be pulled into that order, and
// where the other call writes what this one reads the two programs differ.
func TestAValueSiteBesideACallIsNotProbed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// n is what the call below writes.
var n = 1

// bump writes n and returns.
func bump() int {
	n = 5
	return 1
}

// Pair returns a read and a call, in that order.
func Pair(a, b int) (int, int) {
	return a + b, bump()
}
`)
	if site := valueProbeOf(t, got, "add-to-sub"); site != nil {
		t.Errorf("probe site = %+v, and a call beside it orders the read the language leaves free", site)
	}
}

// TestTheOrderingRuleLooksAtTheStatementAndNotItsBodies keeps the condition
// from refusing everything.
//
// An `if`'s body is not evaluated with its condition, and a loop's body is not
// evaluated with its post statement, so an effect in either is not an effect
// the site is ordered against. A rule that walked into them would refuse every
// condition in every function that does anything.
func TestTheOrderingRuleLooksAtTheStatementAndNotItsBodies(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// record is the effect in the body.
func record(n int) {}

// Walk sums until the limit, calling out on every step.
func Walk(limit int) int {
	total := 0
	for i := 0; i < limit; i++ {
		record(i + 1)
	}
	return total
}
`)
	if site := valueProbeOf(t, got, "lt-to-le"); site == nil {
		t.Error("the loop condition is not probed, though the call is in the body rather than beside it")
	}
}

// everyStatementType is every concrete type implementing ast.Stmt, by the name
// go/ast gives it.
//
// It is the closed set [statementOperands] has to answer for. A statement kind
// it does not name contributes no operands, which reads as "nothing to be
// ordered against" — the unsafe direction — so the switch is exhaustive on
// purpose and this is what says it stayed that way.
//
// BadStmt is here where internal/instrument's own table leaves it out: that
// table is about what the two phases can rewrite, and this is about what a
// switch has a case for, which is a question a parse error can still reach.
var everyStatementType = []string{
	"*ast.AssignStmt", "*ast.BadStmt", "*ast.BlockStmt", "*ast.BranchStmt",
	"*ast.CaseClause", "*ast.CommClause", "*ast.DeclStmt", "*ast.DeferStmt",
	"*ast.EmptyStmt", "*ast.ExprStmt", "*ast.ForStmt", "*ast.GoStmt",
	"*ast.IfStmt", "*ast.IncDecStmt", "*ast.LabeledStmt", "*ast.RangeStmt",
	"*ast.ReturnStmt", "*ast.SelectStmt", "*ast.SendStmt", "*ast.SwitchStmt",
	"*ast.TypeSwitchStmt",
}

// TestTheOrderingRuleNamesEveryStatementKind keeps the exhaustive switch
// exhaustive.
//
// The source is read rather than the behaviour exercised, because the failure
// this guards against is a Go release adding a statement kind: no fixture in
// this repository would hold one, and the switch would answer "no operands" for
// it in silence. What a reader of statementOperands can check by eye is exactly
// what this checks by scanning.
func TestTheOrderingRuleNamesEveryStatementKind(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("effects.go")
	if err != nil {
		t.Fatalf("reading effects.go: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "func statementOperands(stmt ast.Stmt) []ast.Expr {")
	if start < 0 {
		t.Fatal("effects.go no longer declares statementOperands")
	}
	body = body[start:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	for _, name := range everyStatementType {
		if !strings.Contains(body, name) {
			t.Errorf("statementOperands has no case for %s, so it answers \"nothing to be ordered against\" "+
				"for a statement nobody decided about", name)
		}
	}
}

// TestADeletedStatementRecordsThatItRan is the weakest form and the one that
// completes the table.
//
// A deletion's mutant differs from the original by the *absence* of an effect,
// and a probe tree runs effects: there is no value to compare and no rewrite
// could make one appear. What there is is the fact that the statement ran, and
// a pass that never ran it cannot have observed its removal.
func TestADeletedStatementRecordsThatItRan(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// log is the call that gets deleted.
func log(n int) {}

// Walk calls out once per step.
func Walk(limit int) {
	for i := 0; i < limit; i++ {
		log(i)
	}
}
`)
	site := valueProbeOf(t, got, "delete-call-statement")
	if site == nil {
		t.Fatal("a deleted call statement is not probed, though reachability is evidence")
	}
	if site.Form != ProbeFormReach {
		t.Errorf("form = %q, want %q", site.Form, ProbeFormReach)
	}
	if len(site.Types) != 0 {
		t.Errorf("Types = %q, and a reachability probe writes no type", site.Types)
	}
}

// TestAStrongerFormWinsOverReachability keeps the fallback a fallback.
//
// Reachability licenses less than a comparison does: it says the statement ran,
// where the other forms say the mutant would have changed something. So it is
// only ever chosen where nothing else can be, and an edit that has a value is
// measured by its value.
func TestAStrongerFormWinsOverReachability(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Walk sums to the limit.
func Walk(limit int) int {
	total := 0
	for i := 0; i < limit; i++ {
		total = total + i
	}
	return total
}
`)
	for _, c := range []struct {
		rule string
		want ProbeForm
	}{
		{"add-to-sub", ProbeFormValue},
		{"lt-to-le", ProbeFormBool},
		{"delete-assignment", ProbeFormReach},
	} {
		site := valueProbeOf(t, got, c.rule)
		if site == nil {
			t.Errorf("%s is not probed at all", c.rule)
			continue
		}
		if site.Form != c.want {
			t.Errorf("%s uses %q, want %q", c.rule, site.Form, c.want)
		}
	}
}

// TestAnEditThatIntroducesADivisionIsNotProbed is a soundness rule the site's
// own bytes cannot supply.
//
// The in-place forms evaluate the *mutated* reading as well as the original, so
// what has to be free of panics is both — and `mul-to-div` puts a division
// where the user wrote a multiplication. A probe tree that divided by zero
// where the original multiplied is not the original program, and the mutant it
// stands in for would have been caught by running it.
func TestAnEditThatIntroducesADivisionIsNotProbed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Scale multiplies by a number that may be zero.
func Scale(a, b int) int {
	n := a * b
	return n
}
`)
	if site := valueProbeOf(t, got, "mul-to-div"); site != nil {
		t.Errorf("probe site = %+v, and the mutated reading divides by a value that may be zero", site)
	}
}

// TestADivisionByANonZeroConstantIsProbed is the other half, and it is the rule
// the phase already applies to a division the user wrote.
//
// A constant divisor the compiler evaluated and found non-zero cannot panic, so
// the mutated reading is exactly as safe as the original and the site is
// measured.
func TestADivisionByANonZeroConstantIsProbed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Double multiplies by two.
func Double(a int) int {
	n := a * 2
	return n
}
`)
	if site := valueProbeOf(t, got, "mul-to-div"); site == nil {
		t.Error("a multiplication by a non-zero constant is not probed, though dividing by it cannot panic")
	}
}

// TestAFloatMultiplicationIsProbedWhateverItsDivisor is the exception the rule
// needs, because Go's float division does not panic.
//
// Dividing a float by zero yields an infinity, which is a value like any other:
// the comparison sees it, the mutant would have produced it, and nothing
// diverges. Refusing these would cost every floating mutant its probe for a
// hazard that is not there -- and the floating rule above already refuses the
// ones whose *values* `!=` cannot separate.
func TestAFloatMultiplicationIsProbedWhateverItsDivisor(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Ratio multiplies two numbers and hands back an integer.
func Ratio(a, b float64) int {
	if a*b > 0 {
		return 1
	}
	return 0
}
`)
	if site := valueProbeOf(t, got, "fmul-to-fdiv"); site == nil {
		t.Error("a float multiplication is not probed, though float division yields an infinity")
	}
}
