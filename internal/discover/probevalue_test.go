// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"os"
	"strings"
	"testing"
)

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
		return
	}
	if site.Types[0] == "[]int" {
		t.Errorf("probe site = %+v, and a slice cannot be compared with `!=`", site)
	}
}

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

var everyStatementType = []string{
	"*ast.AssignStmt", "*ast.BadStmt", "*ast.BlockStmt", "*ast.BranchStmt",
	"*ast.CaseClause", "*ast.CommClause", "*ast.DeclStmt", "*ast.DeferStmt",
	"*ast.EmptyStmt", "*ast.ExprStmt", "*ast.ForStmt", "*ast.GoStmt",
	"*ast.IfStmt", "*ast.IncDecStmt", "*ast.LabeledStmt", "*ast.RangeStmt",
	"*ast.ReturnStmt", "*ast.SelectStmt", "*ast.SendStmt", "*ast.SwitchStmt",
	"*ast.TypeSwitchStmt",
}

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
