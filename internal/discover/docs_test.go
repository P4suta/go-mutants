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
	operatorsDoc      = "docs/operators.md"
	exclusionsHeading = "## Documented exclusions"

	limitationsDoc = "docs/limitations.md"
	skipsHeading   = "## Recorded skips"
)

var reasonTables = map[string]string{
	operatorsDoc:   exclusionsHeading,
	limitationsDoc: skipsHeading,
}

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
		if strings.HasPrefix(line, "### ") {
			inTable = false
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
