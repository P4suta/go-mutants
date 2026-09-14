// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"
)

// Form E is the last form and the least demanding one: an expression, in a
// position where an expression of the same type is legal, whose type this file
// can spell. What it buys is every position that holds an expression and no
// statement a guard can stand in -- a `switch` tag, a `range` clause, a type
// switch guard, and the initialiser of a `:=` in a header slot. Between them
// those were every refusal this repository had left.

// TestASwitchTagIsNowASite is the shape with no statement around it at all.
//
// The nearest statement to a `switch` tag is the `switch` itself, and no form
// wraps one; walking further out would guard a statement that does not hold the
// edit. The tag is an expression, and an expression is what this form needs.
func TestASwitchTagIsNowASite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Kind(a, b int) string {
	switch a + b {
	case 0:
		return "zero"
	}
	return "other"
}
`)
	if !got.has("add-to-sub", "+", "-") {
		t.Errorf("scan found %v, want the tag's addition", got.rules())
	}
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "add-to-sub" {
			continue
		}
		if candidate.Guard.Form != GuardFormE {
			t.Errorf("the switch tag uses %q, want %q", candidate.Guard.Form, GuardFormE)
		}
		if candidate.Guard.SiteType != "int" {
			t.Errorf("SiteType = %q, want %q", candidate.Guard.SiteType, "int")
		}
	}
	if got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan still records %v for a switch tag", got.skips())
	}
}

// TestAShortDeclarationInAnInitialiserIsNowASite is the shape Form F could not
// reach, and the reason the two forms are both needed.
//
// Form F moves a *statement* into a closure, and a `:=` moved into a closure
// declares inside the closure -- the condition after it would name something
// that is not there. Form E moves the *initialiser expression* instead, which
// declares nothing and leaves the declaration exactly where it was.
func TestAShortDeclarationInAnInitialiserIsNowASite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Sum(values []int, start int) int {
	total := 0
	for i := start + 1; i < len(values); i++ {
		total += values[i]
	}
	return total
}
`)
	if !got.has("add-to-sub", "+", "-") {
		t.Errorf("scan found %v, want the initialiser's addition", got.rules())
	}
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "add-to-sub" {
			continue
		}
		if candidate.Guard.Form != GuardFormE {
			t.Errorf("the initialiser uses %q, want %q", candidate.Guard.Form, GuardFormE)
		}
	}
	if got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan still records %v for a := initialiser", got.skips())
	}
}

// TestATypeSwitchGuardIsNowASite is the third position with no statement a
// guard can stand in. `v := x.(type)` is its own production rather than a
// simple statement, so neither Form F nor Form D reaches it -- but the
// expression being asserted over is an ordinary expression.
func TestATypeSwitchGuardIsNowASite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Kind(values []any, i int) string {
	switch v := values[i+1].(type) {
	case int:
		_ = v
		return "int"
	}
	return ""
}
`)
	if !got.has("add-to-sub", "+", "-") {
		t.Errorf("scan found %v, want the asserted expression's addition", got.rules())
	}
	if got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan still records %v for a type switch guard", got.skips())
	}
}

// TestARangeExpressionIsNowASite is the fourth, and it is the one a reader is
// likeliest to have wondered about: `for _, v := range xs[n+1:]` has an
// expression that decides how many times the loop runs, and nothing was
// mutating it.
func TestARangeExpressionIsNowASite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Sum(values []int, from int) int {
	total := 0
	for _, v := range values[from+1:] {
		total += v
	}
	return total
}
`)
	if !got.has("add-to-sub", "+", "-") {
		t.Errorf("scan found %v, want the range expression's addition", got.rules())
	}
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "add-to-sub" {
			continue
		}
		if candidate.Guard.Form != GuardFormE {
			t.Errorf("the range expression uses %q, want %q", candidate.Guard.Form, GuardFormE)
		}
		if candidate.Guard.SiteType != "int" {
			t.Errorf("SiteType = %q, want %q: the edit is inside the index, not the slice",
				candidate.Guard.SiteType, "int")
		}
	}
}

// TestATypeIsNotAValueAndIsRefused is the condition that keeps this form from
// claiming everything an ast.Expr can be.
//
// `case int:` in a type switch records a *type*, `fmt` in `fmt.Println` records
// a package, and `len` records a builtin. All three are expressions to go/ast,
// none is something a function can return, and go/types is what tells them
// apart.
func TestATypeIsNotAValueAndIsRefused(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

import "strconv"

func Kind(values []any) string {
	for _, value := range values {
		switch value.(type) {
		case int:
			return strconv.Itoa(1)
		}
	}
	return ""
}
`)
	for _, candidate := range got.candidates {
		if candidate.Original == "int" || candidate.Original == "strconv" || candidate.Original == "len" {
			t.Errorf("%s proposed an edit at %q, which is not a value",
				candidate.Rule.Name, candidate.Original)
		}
	}
}

// TestAnExpressionWhoseTypeThisFileCannotSpellIsRefused is the one refusal Form
// E is left making, and it is the one `unnameable-decl-type` now names alone.
//
// A dot import binds a package's names without binding a name for the package,
// so there is no qualification the speller will claim for a type from it. The
// arithmetic is what makes the refusal reachable: a comparison would be `bool`
// and an ordinary Form C site, while `d + 1` has the dot-imported type itself.
func TestAnExpressionWhoseTypeThisFileCannotSpellIsRefused(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

import . "time"

func Pick(d Duration) string {
	switch d + 1 {
	default:
		return "later"
	}
}
`)
	for _, candidate := range got.candidates {
		if candidate.Rule.Name == "add-to-sub" {
			t.Errorf("scan produced %s at a site whose type it cannot spell", candidate.Rule.String())
		}
	}
	if !got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan recorded %v, want a %s site", got.skips(), SkipUnnameableDeclType)
	}
}

// TestTheSearchWalksPastATypeItCannotSpell is that refusal's other side, and
// the reason it is a `continue` rather than a stop.
//
// A type this file cannot name is not the end of the search: an expression
// around it may have a type the file can. Here the tag's own type is
// dot-imported and the edit sits inside an `int` the file spells perfectly
// well, so the site is that inner expression and there is nothing to refuse.
func TestTheSearchWalksPastATypeItCannotSpell(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

import . "time"

func Pick(durations []Duration, i int) string {
	switch durations[i+1] {
	default:
		return "later"
	}
}
`)
	if !got.has("add-to-sub", "+", "-") {
		t.Errorf("scan found %v, want the index's addition", got.rules())
	}
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "add-to-sub" {
			continue
		}
		if candidate.Guard.SiteType != "int" {
			t.Errorf("SiteType = %q, want %q -- the site is the index, not the tag",
				candidate.Guard.SiteType, "int")
		}
	}
}

// TestAnInitialiserThatNamesItsOwnDeclaredVariableIsASite is the case that
// makes Form E more than a convenience, and it is the one Form D has to refuse.
//
// Go begins a declared name's scope at the *end* of its specification. Form D
// hoists `var total int;` in front of the assignment, which puts the new name
// in scope first and reads a zero out of it -- a program that compiles and
// computes something else, which is why that site is refused. Form E changes
// nothing about where anything is: the closure sits inside the initialiser,
// which is before that end, so the `total` inside it resolves to the enclosing
// declaration exactly as the original did.
func TestAnInitialiserThatNamesItsOwnDeclaredVariableIsASite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Double(n int) int {
	total := n
	{
		total := total * 2
		n = total
	}
	return n
}
`)
	if !got.has("mul-to-div", "*", "/") {
		t.Errorf("scan found %v, want the shadowing initialiser's multiplication", got.rules())
	}
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "mul-to-div" {
			continue
		}
		if candidate.Guard.Form != GuardFormE {
			t.Errorf("the shadowing initialiser uses %q, want %q -- Form D would hoist the "+
				"declaration in front of the name it reads", candidate.Guard.Form, GuardFormE)
		}
	}
}

// TestAnOrdinaryExpressionStillUsesTheFormThatCoveredIt is the ordering
// promise, which every form after the first has to carry: Form E is last, so a
// site any earlier form covers is covered by exactly that form.
func TestAnOrdinaryExpressionStillUsesTheFormThatCoveredIt(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Pick(a, b int, ok bool) int {
	if ok {
		return a + b
	}
	total := a * b
	return total
}
`)
	forms := map[string]GuardForm{}
	for _, candidate := range got.candidates {
		forms[candidate.Rule.Name] = candidate.Guard.Form
	}
	for rule, want := range map[string]GuardForm{
		"negate-condition":    GuardFormC,
		"condition-to-true":   GuardFormC,
		"add-to-sub":          GuardFormS,
		"mul-to-div":          GuardFormD,
		"return-zero-numeric": GuardFormS,
	} {
		if got, ok := forms[rule]; !ok {
			t.Errorf("scan produced no %s candidate", rule)
		} else if got != want {
			t.Errorf("%s uses %q, want %q", rule, got, want)
		}
	}
}
