// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"slices"
	"testing"
)

// Import completion is the last thing standing between a guard that knows what
// it wants to write and one that can write it.
//
// Every form but Form C spells a type, and a type is spelled with the name its
// package has *in the file being rewritten*. A file can hold an expression of a
// type it has no name for — a helper in a sibling file returns one — and that
// was a refusal until a completion could supply the name. imports.go argues why
// a sibling's import and no wider set is the safe thing to draw on.

// completionFixture is the shape every test here is about: one file holding an
// expression whose type belongs to a package only its sibling imports.
//
// A `switch` tag is what closes every other escape, exactly as in package
// unnameable: there is no statement around it for Form S, Form D or Form F to
// stand in, and its type is not boolean, so Form C and Form C' have nothing to
// select. What is left is Form E, which has to write the type out.
const completionFixture = `package pkg

func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	default:
		return a
	}
}
`

// completionSibling supplies the type and the import.
const completionSibling = `package pkg

import "time"

func scaled(n int) time.Duration { return time.Duration(n) }
`

// siteOf returns the guard of the one candidate of a rule, failing when the
// scan found none.
func siteOf(t *testing.T, got scanned, rule string) Guard {
	t.Helper()

	for _, candidate := range got.candidates {
		if candidate.Rule.Name == rule {
			return candidate.Guard
		}
	}
	t.Fatalf("the scan found %v, and none of them is %s", got.rules(), rule)
	return Guard{}
}

// TestASiblingsImportMakesATypeSpellable is the whole feature in one assertion
// pair: the site exists, and it says which import it needs to exist.
func TestASiblingsImportMakesATypeSpellable(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, completionFixture, completionSibling)
	if got.hasSkip(SkipUnnameableDeclType) {
		t.Fatalf("the tag is still a refusal: %v", got.skips())
	}
	guard := siteOf(t, got, "add-to-sub")
	if guard.Form != GuardFormE {
		t.Errorf("form = %q, want %q", guard.Form, GuardFormE)
	}
	if guard.SiteType != "time.Duration" {
		t.Errorf("SiteType = %q, want it spelled against the completed import", guard.SiteType)
	}
	want := []Completion{{Path: "time", Local: "time"}}
	if !slices.Equal(guard.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", guard.Imports, want)
	}
}

// TestAFileThatAlreadyImportsThePackageIsCompletedWithNothing keeps the
// completion list a statement about what is *missing*.
//
// A guard that declared an import the file already has would make the rewritten
// file import one package twice, which is a redeclaration rather than a
// redundancy. So the ordinary case — a file that can already spell the type —
// has to come back with an empty list rather than with the import it has.
func TestAFileThatAlreadyImportsThePackageIsCompletedWithNothing(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

import "time"

func Widest(a, b int) time.Duration {
	switch scaled(a) + scaled(b) {
	default:
		return 0
	}
}
`, completionSibling)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType != "time.Duration" {
		t.Errorf("SiteType = %q, want the type this file could already spell", guard.SiteType)
	}
	if len(guard.Imports) != 0 {
		t.Errorf("Imports = %+v, and the file imports that package already", guard.Imports)
	}
}

// TestACompletionDodgesANameTheFileAlreadyBinds is the collision the preferred
// name can walk into.
//
// The name a completion would like is the package's own, and a file is entitled
// to a local variable of that name. Binding the import to it anyway would
// shadow the import for exactly the statements a guard sits in, and the failure
// — "time.Duration undefined (type int has no field Duration)" — would name the
// generated import rather than the collision. So the name is bumped, which is
// what internal/instrument already does for the runtime alias.
func TestACompletionDodgesANameTheFileAlreadyBinds(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

func Widest(a, b int) int {
	time := a
	switch scaled(time) + scaled(b) {
	default:
		return time
	}
}
`, completionSibling)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType == "time.Duration" {
		t.Fatalf("SiteType = %q, which the local variable `time` shadows", guard.SiteType)
	}
	if guard.SiteType != "time2.Duration" {
		t.Errorf("SiteType = %q, want the bumped name", guard.SiteType)
	}
	want := []Completion{{Path: "time", Local: "time2"}}
	if !slices.Equal(guard.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", guard.Imports, want)
	}
}

// TestABlankImportOfTheFilesOwnIsCompleted is the form that looks like an
// exception and is the rule.
//
// A blank import imports the package and binds nothing, so the file has the
// edge and no name for it — which is exactly the condition a completion exists
// for, and exactly why [guardResolver.indexImports] skips those two forms while
// [importsOf] does not.
func TestABlankImportOfTheFilesOwnIsCompleted(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

import _ "time"

func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	default:
		return a
	}
}
`, completionSibling)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType != "time.Duration" {
		t.Errorf("SiteType = %q, want the blank import completed to a usable name", guard.SiteType)
	}
	want := []Completion{{Path: "time", Local: "time"}}
	if !slices.Equal(guard.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", guard.Imports, want)
	}
}

// TestASiblingsAliasIsThePreferredName keeps a package's own habits.
//
// A file that renames an import has a reason, and a completion that ignored it
// would put two names for one package in front of a reader of one package's
// source. The alias is only *preferred*: a name this file binds still bumps it.
func TestASiblingsAliasIsThePreferredName(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, completionFixture, `package pkg

import clock "time"

func scaled(n int) clock.Duration { return clock.Duration(n) }
`)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType != "clock.Duration" {
		t.Errorf("SiteType = %q, want the sibling's own name for the package", guard.SiteType)
	}
}

// TestEveryRewriteOfOneFileAgreesAboutWhatAPackageIsCalled is what makes a
// completion a fact about the file rather than about the candidate.
//
// Two sites needing one package must name it once. Two names would be two
// imports of one path, and the second would not compile; and because the
// rewriter unions what the guards declare, one name arrived at twice is what
// that union has to be able to assume.
func TestEveryRewriteOfOneFileAgreesAboutWhatAPackageIsCalled(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	default:
		return a
	}
}

func Narrowest(a, b int) int {
	switch scaled(a) * scaled(b) {
	default:
		return b
	}
}
`, completionSibling)
	first := siteOf(t, got, "add-to-sub")
	second := siteOf(t, got, "mul-to-div")
	if first.SiteType != second.SiteType {
		t.Errorf("one file spells the package two ways: %q and %q", first.SiteType, second.SiteType)
	}
	if !slices.Equal(first.Imports, second.Imports) {
		t.Errorf("one file asks for two imports of one package: %+v and %+v", first.Imports, second.Imports)
	}
}

// TestImportsOfPrefersAnExplicitAliasWhateverTheFileOrder pins the index's own
// rule, which the tests above can only see through a spelling.
func TestImportsOfPrefersAnExplicitAliasWhateverTheFileOrder(t *testing.T) {
	t.Parallel()

	for _, order := range []struct {
		name  string
		files []string
	}{
		{"the plain import first", []string{
			"package pkg\n\nimport \"time\"\n",
			"package pkg\n\nimport clock \"time\"\n",
		}},
		{"the alias first", []string{
			"package pkg\n\nimport clock \"time\"\n",
			"package pkg\n\nimport \"time\"\n",
		}},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()

			index := importsOf(parseAll(t, order.files))
			if got := index["time"]; got != "clock" {
				t.Errorf("importsOf = %q for \"time\", want the explicit alias %q", got, "clock")
			}
		})
	}
}
