// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestFixtureReportsMatchTheirGoldens(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"killable", "simple", "tagged", "untested"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			outcome, _, err := collect(t, t.Context(), options(t, name))
			if err != nil {
				t.Fatalf("Run over %s: %v", name, err)
			}
			if outcome.Report == nil {
				t.Fatal("the run published no report")
			}
			testkit.Golden(t, name+".report.golden.json", goldenDocument(t, outcome.Report))
		})
	}
}

func goldenDocument(t *testing.T, r *report.Report) []byte {
	t.Helper()
	normalized := mutantkit.NormalizeRunReport(t, mutantkit.MustMarshal(t, r))
	validateDocument(t, normalized)

	var indented bytes.Buffer
	if err := json.Indent(&indented, normalized, "", "  "); err != nil {
		t.Fatalf("indenting the normalised report: %v", err)
	}
	indented.WriteByte('\n')
	return indented.Bytes()
}
