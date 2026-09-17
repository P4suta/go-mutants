// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestTheArchitecturePageQuotesTheCountsTheGoldenFixes(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	golden := readFile(t, filepath.Join(root, "internal", "engine", "testdata", "work-ceiling.golden.txt"))
	page := readFile(t, filepath.Join(root, "docs", "architecture.md"))

	for _, module := range []string{"simple", "killable"} {
		compiles := countedKind(t, golden, module, "go-test-c")
		mutants := countedKind(t, golden, module, "mutant-run")

		if compiles >= mutants {
			t.Errorf("%s compiles %d test binaries for %d mutants;\n"+
				"\tthe page claims the build happens once rather than once per mutant, and this "+
				"corpus module is no longer evidence for it", module, compiles, mutants)
		}

		quoted := quotedCounts(t, page, module)
		if len(quoted) != 2 || quoted[0] != compiles || quoted[1] != mutants {
			t.Errorf("docs/architecture.md says %s starts %v, and the golden fixes %d go-test-c "+
				"and %d mutant-run;\n\tthe page's numbers are the claim's evidence, so a golden "+
				"that moved without the page is a claim standing on a number nobody looked at",
				module, quoted, compiles, mutants)
		}
	}
}

func countedKind(t *testing.T, golden, module, kind string) int {
	t.Helper()

	block, found := strings.CutPrefix(golden[indexOfModule(t, golden, module):], module+"\n")
	if !found {
		t.Fatalf("the golden's %s block does not start where it was found", module)
	}
	for _, line := range strings.Split(block, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != kind {
			continue
		}
		count, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("the golden's %s %s count is %q: %v", module, kind, fields[1], err)
		}
		return count
	}
	t.Fatalf("the golden records no %s for %s, so the page cannot be quoting one", kind, module)
	return 0
}

func indexOfModule(t *testing.T, golden, module string) int {
	t.Helper()

	at := strings.Index(golden, "\n"+module+"\n")
	if at >= 0 {
		return at + 1
	}
	if strings.HasPrefix(golden, module+"\n") {
		return 0
	}
	t.Fatalf("the golden has no %s block; the corpus it counts has changed", module)
	return 0
}

var boldNumber = regexp.MustCompile(`\*\*(\d+)\*\*`)

func quotedCounts(t *testing.T, page, module string) []int {
	t.Helper()

	at := strings.Index(page, "`"+module+"` corpus module starts")
	if at < 0 {
		at = strings.Index(page, "A run of `"+module+"` starts")
	}
	if at < 0 {
		t.Fatalf("docs/architecture.md no longer says what a run of %s starts", module)
	}
	sentence := page[at:]
	if end := strings.Index(sentence, "\n\n"); end >= 0 {
		sentence = sentence[:end]
	}
	if end := strings.Index(sentence[1:], "A run of "); end >= 0 {
		sentence = sentence[:end+1]
	}

	var counts []int
	for _, match := range boldNumber.FindAllStringSubmatch(sentence, -1) {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("the page states %q as a count: %v", match[1], err)
		}
		counts = append(counts, value)
	}
	return counts
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func TestTheCountReadersReadNumbersTheyWereGiven(t *testing.T) {
	t.Parallel()

	const golden = "example\n\tgo-test-c 3\n\tmutant-run 40\n\nother\n\tgo-test-c 9\n"
	if got := countedKind(t, golden, "example", "go-test-c"); got != 3 {
		t.Errorf("the golden reader found %d go-test-c where the text says 3", got)
	}
	if got := countedKind(t, golden, "example", "mutant-run"); got != 40 {
		t.Errorf("the golden reader found %d mutant-run where the text says 40", got)
	}
	if got := countedKind(t, golden, "other", "go-test-c"); got != 9 {
		t.Errorf("the golden reader read %d for the second block, which is the first block's number", got)
	}

	const page = "A run of the `example` corpus module starts **3** `go-test-c` and **40** " +
		"`mutant-run`.\nA run of `other` starts **9** and **11**.\n\n"
	if got := quotedCounts(t, page, "example"); len(got) != 2 || got[0] != 3 || got[1] != 40 {
		t.Errorf("the page reader found %v where the text says 3 and 40", got)
	}
	if got := quotedCounts(t, page, "other"); len(got) != 2 || got[0] != 9 || got[1] != 11 {
		t.Errorf("the page reader found %v for the second sentence, which is not what it says", got)
	}
}
