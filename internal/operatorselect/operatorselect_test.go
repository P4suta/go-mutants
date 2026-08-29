// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package operatorselect_test

import (
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/operatorselect"
)

func TestSelectExpandsDeduplicatesAndCanonicallyOrdersNames(t *testing.T) {
	rules, unknown := operatorselect.Select(mutation.TierAll, []string{
		"return-zero-numeric",
		"comparison",
		"eq-to-neq",
	})
	if unknown != "" {
		t.Fatalf("unknown = %q", unknown)
	}
	names := make([]string, len(rules))
	for i, rule := range rules {
		names[i] = rule.Name
	}
	want := []string{
		"eq-to-neq",
		"neq-to-eq",
		"lt-to-le",
		"le-to-lt",
		"gt-to-ge",
		"ge-to-gt",
		"return-zero-numeric",
	}
	if !slices.Equal(names, want) {
		t.Errorf("selected names = %v, want %v", names, want)
	}
}

func TestSelectUsesTheTierOnlyWhenNoNamesAreGiven(t *testing.T) {
	got, unknown := operatorselect.Select(mutation.TierStrong, nil)
	if unknown != "" {
		t.Fatalf("unknown = %q", unknown)
	}
	want := mutation.CanonicalRegistry().SelectTier(mutation.TierStrong)
	if !slices.Equal(got, want) {
		t.Errorf("strong selection differs from the canonical registry")
	}
}

func TestSelectReportsTheFirstUnknownNameWithoutAPartialSelection(t *testing.T) {
	rules, unknown := operatorselect.Select(mutation.TierBalanced, []string{
		"comparison",
		"invented",
		"also-invented",
	})
	if rules != nil {
		t.Errorf("rules = %v, want nil", rules)
	}
	if unknown != "invented" {
		t.Errorf("unknown = %q, want invented", unknown)
	}
}
