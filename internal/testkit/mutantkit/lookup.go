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

func onlyMutant(t testing.TB, found []mutation.Mutant, what, catalogue string) mutation.Mutant {
	t.Helper()
	if len(found) != 1 {
		t.Fatalf("the catalogue holds %d mutants of %s, want exactly 1. The catalogue is:\n\t%s",
			len(found), what, catalogue)
		return mutation.Mutant{}
	}
	return found[0]
}

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

func describeCatalog(catalog *mutation.Catalog) string {
	return strings.Join(CatalogLines(catalog), "\n\t")
}

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
