// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"fmt"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// MutantAt returns the one catalogued mutant a rule produced in a file.
//
// Naming a mutant this way — by the rule and the file rather than by an identity
// or a catalogue position — is what makes the assertions in the integration
// suites readable, and it works because the corpus modules are written for it:
// one function per file and no repeated operator. The uniqueness is asserted
// rather than assumed, and that assertion is the contract. A fixture that grew a
// second `==` would otherwise silently re-point half a suite at a mutant nobody
// meant, and the failure would arrive as a wrong replacement string in a test
// that never mentions the fixture.
func MutantAt(t testing.TB, catalog *mutation.Catalog, path, rule string) mutation.Mutant {
	t.Helper()
	var found []mutation.Mutant
	for _, m := range catalog.Mutants() {
		if m.Path == path && m.Rule.Name == rule {
			found = append(found, m)
		}
	}
	return onlyMutant(t, found, fmt.Sprintf("%s in %s", rule, path), describeCatalog(catalog))
}

// ByRule returns the one catalogued mutant a rule produced anywhere in the
// snapshot, for the fixtures small enough that the rule alone names it.
func ByRule(t testing.TB, catalog *mutation.Catalog, rule string) mutation.Mutant {
	t.Helper()
	var found []mutation.Mutant
	for _, m := range catalog.Mutants() {
		if m.Rule.Name == rule {
			found = append(found, m)
		}
	}
	return onlyMutant(t, found, rule, describeCatalog(catalog))
}

// APIMutantAt is [MutantAt] over the public catalogue, and it asserts one thing
// more: that the mutant was accepted.
//
// [gomutants.Catalog] lists rejected mutants too — an edit that did not compile
// is published so that a user can be told why there is no mutant there — and
// every test that looks one up is about to activate it and assert on what the
// suite did. A rejected mutant can never be activated, so returning one produces
// a run that fails for a reason nothing in the test mentions.
func APIMutantAt(t testing.TB, catalog gomutants.Catalog, path, rule string) gomutants.Mutant {
	t.Helper()
	var found []gomutants.Mutant
	for _, m := range catalog.Mutants {
		if m.Path == path && m.Rule == rule {
			found = append(found, m)
		}
	}
	return onlyAPIMutant(t, found, fmt.Sprintf("%s in %s", rule, path), describeAPICatalog(catalog))
}

// APIByRule is [ByRule] over the public catalogue, with the same acceptance
// assertion as [APIMutantAt].
func APIByRule(t testing.TB, catalog gomutants.Catalog, rule string) gomutants.Mutant {
	t.Helper()
	var found []gomutants.Mutant
	for _, m := range catalog.Mutants {
		if m.Rule == rule {
			found = append(found, m)
		}
	}
	return onlyAPIMutant(t, found, rule, describeAPICatalog(catalog))
}

// onlyMutant ends the test unless exactly one mutant matched, quoting the whole
// catalogue either way.
//
// The catalogue is quoted because both failures have the same remedy — look at
// what is actually in the fixture — and re-running a discovery pass to find out
// costs a toolchain and a minute.
func onlyMutant(t testing.TB, found []mutation.Mutant, what, catalogue string) mutation.Mutant {
	t.Helper()
	if len(found) != 1 {
		t.Fatalf("the catalogue holds %d mutants of %s, want exactly 1. The catalogue is:\n\t%s",
			len(found), what, catalogue)
		return mutation.Mutant{}
	}
	return found[0]
}

// onlyAPIMutant is [onlyMutant] plus the acceptance check the public catalogue
// makes possible.
func onlyAPIMutant(t testing.TB, found []gomutants.Mutant, what, catalogue string) gomutants.Mutant {
	t.Helper()
	if len(found) != 1 {
		t.Fatalf("the catalogue holds %d mutants of %s, want exactly 1. The catalogue is:\n\t%s",
			len(found), what, catalogue)
		return gomutants.Mutant{}
	}
	if !found[0].Accepted {
		t.Fatalf("the mutant %s of %s was rejected during validation, so it can never be activated. "+
			"The catalogue is:\n\t%s", found[0].DisplayID, what, catalogue)
		return gomutants.Mutant{}
	}
	return found[0]
}

// describeCatalog is [CatalogLines] as one indented block for a failure message.
func describeCatalog(catalog *mutation.Catalog) string {
	return strings.Join(CatalogLines(catalog), "\n\t")
}

// describeAPICatalog is the same for the public catalogue, and it prints the
// acceptance flag because that is half of what a lookup here asserts.
func describeAPICatalog(catalog gomutants.Catalog) string {
	lines := make([]string, 0, len(catalog.Mutants))
	for _, m := range catalog.Mutants {
		state := "accepted"
		if !m.Accepted {
			state = "rejected"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s -> %s (%s)",
			m.DisplayID, m.Path, m.Rule, m.Original, m.Replacement, state))
	}
	return strings.Join(lines, "\n\t")
}
