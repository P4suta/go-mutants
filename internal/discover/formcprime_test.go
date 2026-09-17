// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"
)

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
	for _, candidate := range got.candidates {
		if candidate.Guard.SiteType == "Replacer" {
			t.Errorf("%s claims to spell a dot-imported type", candidate.Rule.Name)
		}
	}
}
