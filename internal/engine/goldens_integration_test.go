// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The run report of a real run, pinned whole.
//
// Every other test in this package asserts a field, a tally or a sequence, and
// each of those is a claim somebody decided to write down. A golden is the
// other kind of evidence: it is the whole document, so a field that appeared, a
// field that vanished, a number that moved and a string that changed shape all
// show up as a diff whether or not anybody thought to look for them. That is
// worth exactly as much as the normalisation is honest, which is why the
// normaliser has tests of its own in internal/testkit/mutantkit.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
//
// Regenerate the goldens with `mise run golden-update`, which runs this package
// with the tag, and read the diff before committing it.
//
// The comment above is deliberately not a package doc — integration_test.go
// carries this package's — which is what the blank line below is for.

package engine

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestFixtureReportsMatchTheirGoldens records one run report per fixture.
//
// The four fixtures are chosen to differ in what a report *has* rather than in
// how large it is: `simple/` is a green run where everything is killed and
// covered, `killable/` adds survivors, `untested/` adds uncovered mutants and a
// second package, and `tagged/` is the smallest document the schema admits — one
// mutant, one file, one binary — which is what makes a field that started being
// emitted unconditionally visible as a diff rather than as noise.
//
// The goldens are generated on Linux and compared on all three operating
// systems CI runs, and that is not a happy accident: it is the test of the
// normaliser. A field that is a fact about the host, the toolchain, the clock
// or the scheduler and that nobody normalised fails on macOS and Windows the
// first time anybody looks, which is the earliest a wrong golden can be caught.
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

// goldenDocument is one run report as a golden holds it: marshalled, checked
// against the published schema, normalised, checked again, and indented.
//
// Both validations are deliberate. The first says the run produced a legal
// document; the second says the *normalisation* left one, which is the claim
// that keeps the placeholders honest — a normaliser that replaced a required
// string with a number would otherwise be caught by nothing until a consumer
// tried to read a document nobody had checked.
//
// The indentation is for the reader of a failure. [testkit.Golden] renders a
// mismatch as a line diff with the runs both documents agree on elided, and a
// one-line JSON document has no lines to elide — the diff would be the whole
// report twice. The key order is encoding/json's, which is alphabetical for the
// maps the normaliser works in, and so is not the order internal/report writes:
// a normalised document is a document about a document, and comparing it with
// one of internal/report's own goldens is not a thing anybody should do.
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
