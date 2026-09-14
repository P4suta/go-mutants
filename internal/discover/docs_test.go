// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/testkit"
)

const (
	// operatorsDoc is the page that lists the reasons a site is skipped.
	operatorsDoc = "docs/operators.md"
	// exclusionsHeading opens the table of those reasons.
	exclusionsHeading = "## Documented exclusions"
)

// TestEverySkipReasonIsOnTheOperatorsPage keeps the table equal to what
// discovery can emit, in both directions.
//
// The page claims it already: "The reason strings below are the exact
// identifiers `internal/discover` emits (they appear verbatim in `list` output
// and in catalog/report JSON)". Nothing compared the two. A reason added to the
// code and not to the page is one a user meets in their terminal and cannot
// look up; a reason on the page that no build emits is a heading in
// `--explain`'s output that never appears, and a reader who goes looking for it.
func TestEverySkipReasonIsOnTheOperatorsPage(t *testing.T) {
	t.Parallel()

	documented := exclusionRows(t)
	if len(documented) == 0 {
		t.Fatalf("%s lists no exclusions; the parser has stopped seeing the table", operatorsDoc)
	}

	var emitted []string
	for _, reason := range discover.AllSkipReasons() {
		emitted = append(emitted, string(reason))
	}
	var named []string
	for reason := range documented {
		named = append(named, reason)
	}
	slices.Sort(emitted)
	slices.Sort(named)

	for _, reason := range emitted {
		if !slices.Contains(named, reason) {
			t.Errorf("discovery emits %q and %s does not list it", reason, operatorsDoc)
		}
	}
	for _, reason := range named {
		if !slices.Contains(emitted, reason) {
			t.Errorf("%s lists %q and no build emits it", operatorsDoc, reason)
		}
	}
}

// TestEverySkipReasonRowSaysWhy keeps the second column from going blank.
//
// The reason string is what a user sees in their terminal; the Why cell is the
// only place that says what it means. A row that names a reason and explains
// nothing is a row that sends a reader back to the source.
func TestEverySkipReasonRowSaysWhy(t *testing.T) {
	t.Parallel()

	for reason, why := range exclusionRows(t) {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s lists %q with nothing beside it", operatorsDoc, reason)
		}
	}
}

// TestEverySkipReasonExplanationIsOneSentence keeps the sentence `--explain`
// prints and the page's own column describing the same set.
//
// Explanation is what the command shows under each heading, so a reason with no
// explanation is a heading with nothing under it. The two texts are deliberately
// not compared word for word -- the page has room and the terminal does not --
// but a reason that has one and not the other is a gap either way.
func TestEverySkipReasonExplanationIsOneSentence(t *testing.T) {
	t.Parallel()

	for _, reason := range discover.AllSkipReasons() {
		explanation := reason.Explanation()
		if strings.TrimSpace(explanation) == "" {
			t.Errorf("%q has no explanation, so `--explain` prints a heading with nothing under it", reason)
			continue
		}
		if strings.Contains(explanation, "\n") {
			t.Errorf("%q's explanation carries a line break, and it is printed as one line: %q", reason, explanation)
		}
	}
}

// exclusionRows is the exclusions table, as reason -> why.
func exclusionRows(t *testing.T) map[string]string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join(testkit.Root(t), filepath.FromSlash(operatorsDoc)))
	if err != nil {
		t.Fatalf("reading %s: %v", operatorsDoc, err)
	}
	rows := map[string]string{}
	inTable := false
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = strings.TrimRight(line, " ") == exclusionsHeading
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !inTable || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		reason := strings.Trim(strings.TrimSpace(cells[0]), "`")
		if reason == "" || reason == "Reason" || strings.HasPrefix(reason, "-") {
			continue
		}
		rows[reason] = strings.TrimSpace(cells[1])
	}
	return rows
}
