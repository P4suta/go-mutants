// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"
)

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
