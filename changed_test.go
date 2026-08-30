// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gitdiff"
)

func TestResolvePrepareOptionsRejectsChangedRefWithoutChanged(t *testing.T) {
	t.Parallel()

	_, err := resolvePrepareOptions(PrepareOptions{ChangedRef: "HEAD"})
	if err == nil {
		t.Fatal("resolvePrepareOptions accepted ChangedRef without Changed")
	}
	if !strings.Contains(err.Error(), "changed ref") {
		t.Fatalf("error = %q, want changed-ref context", err)
	}
}

func TestNarrowAcceptedToChangedLines(t *testing.T) {
	t.Parallel()

	accepted := map[string]bool{
		"single":   true,
		"multi":    true,
		"outside":  true,
		"unknown":  true,
		"rejected": false,
	}
	mutants := []Mutant{
		{ID: "single", Path: "single.go", Line: 10, Original: "=="},
		{ID: "multi", Path: "multi.go", Line: 20, Original: "left &&\nright"},
		{ID: "outside", Path: "outside.go", Line: 30, Original: "!="},
		{ID: "unknown"},
		{ID: "rejected", Path: "single.go", Line: 10, Original: "=="},
	}
	changed := gitdiff.Changed{Files: map[string][]gitdiff.Range{
		"single.go": {{First: 10, Last: 10}},
		"multi.go":  {{First: 21, Last: 21}},
	}}

	got := narrowAcceptedToChanged(accepted, mutants, changed)
	for _, id := range []string{"single", "multi", "unknown"} {
		if !got[id] {
			t.Errorf("%s was removed", id)
		}
	}
	for _, id := range []string{"outside", "rejected"} {
		if got[id] {
			t.Errorf("%s was selected", id)
		}
	}
	if !accepted["outside"] {
		t.Error("narrowAcceptedToChanged mutated its input")
	}
}
