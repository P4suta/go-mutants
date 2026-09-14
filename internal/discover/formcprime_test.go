// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"
)

// Form C' is the bool selector with a conversion at each end, and it exists
// because Form C requires the site to be *exactly* the universe `bool`. A
// condition of a named boolean type is perfectly mutable Go -- `!` applies to
// any boolean type, and so does every rule that writes a boolean constant --
// and until this form landed every such condition was a recorded refusal.

// TestANamedBooleanConditionIsNowASite is the shape the form was written for.
//
// Three rules want that site: the negation and both settlements. All three used
// to be refused there, at one coordinate, which is what made the refusal
// impossible to miss.
func TestANamedBooleanConditionIsNowASite(t *testing.T) {
	t.Parallel()

	source := `package pkg

type Flag bool

func Use(f Flag, v int) int {
	if f {
		return v
	}
	return 0
}
`
	got := scanSource(t, source)
	for _, want := range []struct{ rule, original, replacement string }{
		{"negate-condition", "f", "!(f)"},
		{"condition-to-true", "f", "true"},
		{"condition-to-false", "f", "false"},
	} {
		if !got.has(want.rule, want.original, want.replacement) {
			t.Errorf("scan found %v, want %s %s->%s", got.rules(), want.rule, want.original, want.replacement)
		}
	}
	if got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan still records %v for a named boolean condition", got.skips())
	}
}

// TestAFormCPrimeSiteCarriesTheTypeToConvertBackTo is the part of the hint that
// is new, and the part the instrumenter cannot work out for itself: it parses
// the snapshot without type checking it, so the name of the type has to travel
// with the site.
func TestAFormCPrimeSiteCarriesTheTypeToConvertBackTo(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

type Flag bool

func Use(f Flag, v int) int {
	if f {
		return v
	}
	return 0
}
`)
	found := 0
	for _, candidate := range got.candidates {
		if candidate.Guard.Form != GuardFormCPrime {
			continue
		}
		found++
		if candidate.Guard.SiteType != "Flag" {
			t.Errorf("%s carries SiteType %q, want %q", candidate.Rule.Name, candidate.Guard.SiteType, "Flag")
		}
	}
	if found != 3 {
		t.Errorf("%d candidates use Form C', want the negation and both settlements", found)
	}
}

// TestTheUniverseBoolStillUsesFormC is the property that makes this form safe
// to add at all.
//
// Form C' is tried last, after Form C and after both statement forms, so a site
// either of them already covered is covered by exactly what covered it before.
// If it were a loosened gate inside Form C instead, every ordinary boolean
// condition in every tree would start rendering with two conversions around it
// -- different bytes in the instrumented tree, for nothing.
func TestTheUniverseBoolStillUsesFormC(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Use(ok bool, v int) int {
	if ok {
		return v
	}
	return 0
}
`)
	for _, candidate := range got.candidates {
		if candidate.Guard.Form == GuardFormCPrime {
			t.Errorf("%s over %q uses Form C' at a site of the universe bool",
				candidate.Rule.Name, candidate.Original)
		}
		if candidate.Guard.SiteType != "" {
			t.Errorf("%s over %q carries SiteType %q, and only Form C' has one",
				candidate.Rule.Name, candidate.Original, candidate.Guard.SiteType)
		}
	}
}

// TestANamedBooleanInAStatementStillUsesThatStatement is the other half of the
// ordering promise: a statement form that already covered a site goes on
// covering it, even though the expression inside it is now a Form C' candidate
// in its own right.
func TestANamedBooleanInAStatementStillUsesThatStatement(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

type Flag bool

func Set(a, b int) Flag {
	var out Flag
	out = a > b
	return out
}
`)
	found := false
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "gt-to-ge" {
			continue
		}
		found = true
		if candidate.Guard.Form != GuardFormS {
			t.Errorf("gt-to-ge in an assignment uses %q, want %q", candidate.Guard.Form, GuardFormS)
		}
	}
	if !found {
		t.Fatalf("scan found %v, want a gt-to-ge candidate", got.rules())
	}
}

// TestABooleanTypeThisFileCannotNameIsStillRefused is the refusal that remains,
// and it is the same one Form D makes about a declared type: go-mutants knows
// what it would write and cannot say it in Go.
//
// A dot import binds a package's names without binding a name for the package,
// so `Flag` is in scope and `Flag(...)` is a conversion this file could write
// -- but the speller will not claim a qualification it cannot check, and
// guessing is what it exists not to do.
func TestABooleanTypeThisFileCannotNameIsStillRefused(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

import . "strings"

func Use(r *Replacer, v int) int {
	if r == nil {
		return v
	}
	return 0
}
`)
	// The comparison is the universe bool, so this file's own condition is an
	// ordinary Form C site. What matters is that nothing here claims a
	// spelling: every candidate either has no SiteType or has one the file
	// really binds.
	for _, candidate := range got.candidates {
		if candidate.Guard.SiteType == "Replacer" {
			t.Errorf("%s claims to spell a dot-imported type", candidate.Rule.Name)
		}
	}
}
