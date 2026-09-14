// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// exitCodePages are the pages that print the exit-code table.
//
// The release checklist asks that the README's table and exitCodeHelp be equal
// verbatim, and until this test existed the only way to know was to diff two
// differently-shaped documents by eye. They are equal by construction now: the
// Meaning cell of each row is the help's own words for that code.
var exitCodePages = []string{"README.md"}

// exitCodeHeading opens the table on each of them.
const exitCodeHeading = "## Exit codes"

// TestEveryExitCodeTableSaysWhatTheHelpSays keeps four statements of one
// contract equal: the constants, the help every command prints, and the table
// on each page that documents it.
//
// The first direction is the one nothing checked at all. exitCodeHelp is a
// string constant, so a sixth ExitCode could be added, returned, and branched
// on by a CI configuration without the help ever mentioning it.
func TestEveryExitCodeTableSaysWhatTheHelpSays(t *testing.T) {
	t.Parallel()

	fromHelp := exitCodeRows(t, exitCodeHelp)
	if len(fromHelp) == 0 {
		t.Fatalf("the parser found no rows in exitCodeHelp:\n%s", exitCodeHelp)
	}

	declared := []mutation.ExitCode{
		mutation.ExitOK,
		mutation.ExitPolicyFailure,
		mutation.ExitInfrastructure,
		mutation.ExitInterrupted,
		mutation.ExitTerminated,
	}
	var helped []int
	for _, row := range fromHelp {
		helped = append(helped, row.code)
	}
	var want []int
	for _, code := range declared {
		want = append(want, int(code))
	}
	slices.Sort(helped)
	slices.Sort(want)
	if !slices.Equal(helped, want) {
		t.Errorf("exitCodeHelp documents %v and internal/mutation declares %v;\n"+
			"\tthe table is part of the command line contract, so a code it does not\n"+
			"\tname is one a CI configuration cannot branch on", helped, want)
	}

	root := testkit.Root(t)
	for _, page := range exitCodePages {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		section, ok := headingSection(string(body), exitCodeHeading)
		if !ok {
			t.Errorf("%s has no `%s` section", page, exitCodeHeading)
			continue
		}
		fromPage := exitCodeTableRows(section)
		if !slices.Equal(fromPage, fromHelp) {
			t.Errorf("%s's exit-code table and exitCodeHelp differ:\n\tpage: %v\n\thelp: %v",
				page, fromPage, fromHelp)
		}
	}
}

// An exitCodeRow is one code and the words beside it.
type exitCodeRow struct {
	code    int
	meaning string
}

// exitCodeRows reads the rows of exitCodeHelp: the lines after the heading,
// each opening with a number.
func exitCodeRows(t *testing.T, help string) []exitCodeRow {
	t.Helper()

	var rows []exitCodeRow
	seen := false
	for _, line := range strings.Split(help, "\n") {
		if strings.TrimSpace(line) == strings.TrimSuffix(exitCodeHeading, " ")[3:]+":" {
			seen = true
			continue
		}
		if !seen {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		code, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		rows = append(rows, exitCodeRow{code: code, meaning: strings.Join(fields[1:], " ")})
	}
	return rows
}

// exitCodeTableRows reads the rows of a page's exit-code table, stripping the
// backticks a Markdown table puts round the number and round any flag in the
// meaning -- so that what is compared is the words and not their markup.
func exitCodeTableRows(section string) []exitCodeRow {
	var rows []exitCodeRow
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		first := strings.Trim(strings.TrimSpace(cells[0]), "`")
		code, err := strconv.Atoi(first)
		if err != nil {
			continue
		}
		meaning := strings.ReplaceAll(strings.TrimSpace(cells[1]), "`", "")
		rows = append(rows, exitCodeRow{code: code, meaning: strings.Join(strings.Fields(meaning), " ")})
	}
	return rows
}

// headingSection is the body under one Markdown heading, up to the next of the
// same level or shallower.
func headingSection(body, heading string) (string, bool) {
	lines := strings.Split(body, "\n")
	depth := len(heading) - len(strings.TrimLeft(heading, "#"))
	for i, line := range lines {
		if strings.TrimRight(line, " ") != heading {
			continue
		}
		var section []string
		for _, next := range lines[i+1:] {
			if strings.HasPrefix(next, "#") {
				if d := len(next) - len(strings.TrimLeft(next, "#")); d <= depth {
					break
				}
			}
			section = append(section, next)
		}
		return strings.Join(section, "\n"), true
	}
	return "", false
}
