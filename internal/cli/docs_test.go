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

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

var exitCodePages = []string{"README.md", commandLineDoc}

const exitCodeHeading = "## Exit codes"

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

type exitCodeRow struct {
	code    int
	meaning string
}

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

const commandLineDoc = "docs/command-line.md"

const commandsHeading = "## The commands"

var cobraCommands = []string{"completion", "help"}

func TestEveryCommandsHelpMatchesItsGolden(t *testing.T) {
	t.Parallel()

	for _, path := range firstPartyCommands(t) {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			args := append(strings.Fields(path)[1:], "--help")
			code, stdout, stderr := execute(t, args...)
			if code != int(mutation.ExitOK) {
				t.Fatalf("`%s --help` exited %d\n%s", path, code, stderr)
			}
			testkit.Golden(t, goldenName(path), []byte(stdout))
		})
	}
}

func TestTheCommandListAUserSeesIsTheOneThisRepositoryWrote(t *testing.T) {
	t.Parallel()

	_, stdout, _ := execute(t, "--help")
	section, ok := helpSection(stdout, "Available Commands:")
	if !ok {
		t.Fatalf("the root help lists no commands:\n%s", stdout)
	}
	var listed []string
	for _, line := range strings.Split(section, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		listed = append(listed, fields[0])
	}

	want := slices.Clone(cobraCommands)
	for _, path := range firstPartyCommands(t) {
		if name := strings.TrimPrefix(path, "go-mutants "); name != path && !strings.Contains(name, " ") {
			want = append(want, name)
		}
	}
	slices.Sort(listed)
	slices.Sort(want)
	if !slices.Equal(listed, want) {
		t.Errorf("the root help lists %v and the tree plus cobraCommands is %v;\n"+
			"\ta command nobody in this repository wrote is one nobody reviewed the help of",
			listed, want)
	}
}

func helpSection(help, heading string) (string, bool) {
	lines := strings.Split(help, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, " ") != heading {
			continue
		}
		var block []string
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				break
			}
			if !strings.HasPrefix(next, " ") {
				break
			}
			block = append(block, next)
		}
		return strings.Join(block, "\n"), true
	}
	return "", false
}

func TestCommandLineDocNamesEveryCommand(t *testing.T) {
	t.Parallel()

	commands := firstPartyCommands(t)
	if len(commands) < 20 {
		t.Fatalf("the walk found %d commands, which is too few to be this tree", len(commands))
	}
	body, err := os.ReadFile(filepath.Join(testkit.Root(t), filepath.FromSlash(commandLineDoc)))
	if err != nil {
		t.Fatalf("reading %s: %v", commandLineDoc, err)
	}
	section, ok := headingSection(string(body), commandsHeading)
	if !ok {
		t.Fatalf("%s has no `%s` section", commandLineDoc, commandsHeading)
	}
	var named []string
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		first := strings.Trim(strings.TrimSpace(cells[0]), "`")
		if strings.HasPrefix(first, "go-mutants") {
			named = append(named, first)
		}
	}
	slices.Sort(named)
	for _, path := range commands {
		if path == "go-mutants" {
			continue
		}
		if !slices.Contains(named, path) {
			t.Errorf("%s does not name `%s`", commandLineDoc, path)
		}
	}
	for _, path := range named {
		if !slices.Contains(commands, path) {
			t.Errorf("%s names `%s`, which the tree does not hold", commandLineDoc, path)
		}
	}
}

func TestEveryCommandsHelpCarriesTheExitCodeTable(t *testing.T) {
	t.Parallel()

	for _, path := range firstPartyCommands(t) {
		args := append(strings.Fields(path)[1:], "--help")
		_, stdout, _ := execute(t, args...)
		for _, needle := range []string{"Exit codes:", "  0 ", "  1 ", "  2 ", "  130 ", "  143 "} {
			if !strings.Contains(stdout, needle) {
				t.Errorf("`%s --help` does not document %q", path, needle)
			}
		}
	}
}

func TestNoCommandsHelpDependsOnTheMachine(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	machine := []string{testkit.Root(t), os.TempDir()}
	if home != "" {
		machine = append(machine, home)
	}
	for _, path := range firstPartyCommands(t) {
		args := append(strings.Fields(path)[1:], "--help")
		_, stdout, _ := execute(t, args...)
		for _, absolute := range machine {
			if absolute != "" && strings.Contains(stdout, absolute) {
				t.Errorf("`%s --help` names %s, so its golden is this machine's", path, absolute)
			}
		}
	}

	_, stdout, _ := execute(t, "run", "--help")
	if strings.Contains(stdout, "(default ") {
		t.Errorf("run --help prints a pflag default, and the worker count is derived"+
			" from NumCPU:\n%s", stdout)
	}
	if !strings.Contains(stdout, "min(CPUs, 8)") {
		t.Errorf("run --help does not describe the worker default:\n%s", stdout)
	}
}

func allCommands(t *testing.T) []string {
	t.Helper()

	var paths []string
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		paths = append(paths, command.CommandPath())
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(NewRootCommand())
	slices.Sort(paths)
	return paths
}

func firstPartyCommands(t *testing.T) []string {
	t.Helper()

	return allCommands(t)
}

func goldenName(path string) string {
	name := strings.TrimPrefix(path, "go-mutants")
	name = strings.TrimSpace(name)
	if name == "" {
		name = "root"
	}
	return "help-" + strings.ReplaceAll(name, " ", "-") + ".golden.txt"
}
