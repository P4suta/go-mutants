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
	// operatorsDoc is the page that lists the reasons a site is skipped, beside
	// the rules that would otherwise have mutated it.
	operatorsDoc = "docs/operators.md"
	// exclusionsHeading opens the table of those reasons.
	exclusionsHeading = "## Documented exclusions"

	// limitationsDoc lists the same reasons from the other end: what go-mutants
	// will not do, and what it does instead.
	limitationsDoc = "docs/limitations.md"
	// skipsHeading opens its table of them.
	skipsHeading = "## Recorded skips"
)

// reasonTables are the pages that enumerate the skip reasons, and the heading
// each one keeps them under.
//
// Two pages rather than one because they answer different questions -- the
// operators page says which rule was declined and the limitations page says
// what a user gets instead -- and both are wrong in the same way if a reason is
// added to the code and to neither.
var reasonTables = map[string]string{
	operatorsDoc:   exclusionsHeading,
	limitationsDoc: skipsHeading,
}

// TestEverySkipReasonIsOnBothPages keeps the tables equal to what discovery can
// emit, in both directions and on both pages.
//
// The operators page claims it already: "The reason strings below are the exact
// identifiers `internal/discover` emits (they appear verbatim in `list` output
// and in catalog/report JSON)". Nothing compared the two. A reason added to the
// code and not to a page is one a user meets in their terminal and cannot look
// up; a reason on a page that no build emits is a heading in `--explain`'s
// output that never appears, and a reader who goes looking for it.
func TestEverySkipReasonIsOnBothPages(t *testing.T) {
	t.Parallel()

	var emitted []string
	for _, reason := range discover.AllSkipReasons() {
		emitted = append(emitted, string(reason))
	}
	slices.Sort(emitted)

	for page, heading := range reasonTables {
		documented := reasonRows(t, page, heading)
		if len(documented) == 0 {
			t.Errorf("%s lists no skip reasons under %q; the parser has stopped seeing the table", page, heading)
			continue
		}
		var named []string
		for reason := range documented {
			named = append(named, reason)
		}
		slices.Sort(named)
		for _, reason := range emitted {
			if !slices.Contains(named, reason) {
				t.Errorf("discovery emits %q and %s does not list it", reason, page)
			}
		}
		for _, reason := range named {
			if !slices.Contains(emitted, reason) {
				t.Errorf("%s lists %q and no build emits it", page, reason)
			}
		}
	}
}

// TestEverySkipReasonRowSaysWhy keeps the second column from going blank.
//
// The reason string is what a user sees in their terminal; the second cell is
// the only place that says what it means. A row that names a reason and
// explains nothing is a row that sends a reader back to the source.
func TestEverySkipReasonRowSaysWhy(t *testing.T) {
	t.Parallel()

	for page, heading := range reasonTables {
		for reason, why := range reasonRows(t, page, heading) {
			if strings.TrimSpace(why) == "" {
				t.Errorf("%s lists %q with nothing beside it", page, reason)
			}
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

// reasonRows is one page's skip-reason table, as reason -> why.
func reasonRows(t *testing.T, page, heading string) map[string]string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join(testkit.Root(t), filepath.FromSlash(page)))
	if err != nil {
		t.Fatalf("reading %s: %v", page, err)
	}
	rows := map[string]string{}
	inTable := false
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = strings.TrimRight(line, " ") == heading
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
		if reason == "" || reason == "Reason" || reason == "What go-mutants does" ||
			strings.HasPrefix(reason, "-") || strings.Contains(reason, " ") {
			continue
		}
		rows[reason] = strings.TrimSpace(cells[1])
	}
	return rows
}
