// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	reportDocumentation = "../../docs/report-v1.md"

	readmePath = "../../README.md"

	exitCodeTableHeading = "| Code | Meaning |"

	commandSurfaceHeading = "The command surface is:"

	exitCodeTableCells = 3
)

var backquotedValue = regexp.MustCompile("`([^`]+)`")

var verdicts = []report.Verdict{
	report.VerdictAssured,
	report.VerdictChangeAssured,
	report.VerdictScopeAssured,
	report.VerdictDefect,
	report.VerdictInsufficient,
	report.VerdictError,
	report.VerdictReproduced,
	report.VerdictResolved,
	report.VerdictCompleted,
}

func TestEveryVerdictHasADocumentedExitCode(t *testing.T) {
	t.Parallel()
	documented := documentedExitCodes(t)
	for _, verdict := range verdicts {
		code := exitCode(verdict)
		want, ok := documented[string(verdict)]
		if !ok {
			t.Errorf("verdict %s exits %d and is named nowhere in %s", verdict, code, reportDocumentation)
			continue
		}
		if want != code {
			t.Errorf("verdict %s exits %d, and %s says %d", verdict, code, reportDocumentation, want)
		}
	}
	for verdict := range documented {
		if !slices.Contains(verdicts, report.Verdict(verdict)) {
			t.Errorf("%s names verdict %s, which is declared nowhere", reportDocumentation, verdict)
		}
	}
}

func TestEveryExitCodeConstantIsDocumented(t *testing.T) {
	t.Parallel()
	codes := documentedExitCodeNumbers(t)
	for _, code := range []int{
		ExitAssured, ExitDefect, ExitInsufficient, ExitError, ExitInterrupted, ExitTerminated,
	} {
		if !codes[code] {
			t.Errorf("exit code %d is declared in Go and absent from %s", code, reportDocumentation)
		}
	}
	for code := range codes {
		if !slices.Contains([]int{
			ExitAssured, ExitDefect, ExitInsufficient, ExitError, ExitInterrupted, ExitTerminated,
		}, code) {
			t.Errorf("%s documents exit code %d, which no constant declares", reportDocumentation, code)
		}
	}
}

func TestTheHelpTextNamesEveryExitCode(t *testing.T) {
	t.Parallel()
	for _, code := range []int{
		ExitAssured, ExitDefect, ExitInsufficient, ExitError, ExitInterrupted, ExitTerminated,
	} {
		if !strings.Contains(help, " "+strconv.Itoa(code)+" ") {
			t.Errorf("the help text does not name exit code %d", code)
		}
	}
}

func TestEveryCommandIsDispatchedHelpedAndDocumented(t *testing.T) {
	t.Parallel()
	commands := []Command{
		CommandVerify, CommandInit, CommandExplain, CommandReplay, CommandAccept,
		CommandReport, CommandPlan, CommandDoctor, CommandFix, CommandCache, CommandTrace,
	}
	documented := documentedCommands(t)
	for _, command := range commands {
		if _, ok := commandHelp(command); !ok {
			t.Errorf("command %s has no help text", command)
		}
		if !helpNamesCommand(command) {
			t.Errorf("command %s is absent from the usage lines of the help text", command)
		}
		if !slices.Contains(documented, string(command)) {
			t.Errorf("command %s is absent from the command surface in %s", command, readmePath)
		}
	}
	for _, command := range documented {
		if command == "help" {
			continue
		}
		if !slices.Contains(commands, Command(command)) {
			t.Errorf("%s documents command %s, which is declared nowhere", readmePath, command)
		}
	}
}

func helpNamesCommand(command Command) bool {
	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= usageLineFields && fields[0] == "goatest" && fields[1] == string(command) {
			return true
		}
	}
	return false
}

const usageLineFields = 2

func TestTheExitCodeLedgerSeesAVerdictTheTableDoesNotHold(t *testing.T) {
	t.Parallel()
	documented := documentedExitCodes(t)
	if _, ok := documented["A_VERDICT_THIS_TOOL_DOES_NOT_HAVE"]; ok {
		t.Fatal("the table holds the fixture verdict, so this test proves nothing")
	}
	if len(documented) == 0 {
		t.Fatal("the table was read as empty, so the ledger would agree with anything")
	}
}

func documentedExitCodeNumbers(t *testing.T) map[int]bool {
	t.Helper()
	codes := make(map[int]bool)
	for _, row := range exitCodeRows(t) {
		codes[row.code] = true
	}
	return codes
}

type exitCodeRow struct {
	code    int
	meaning string
}

func exitCodeRows(t *testing.T) []exitCodeRow {
	t.Helper()
	lines := documentationLines(t, reportDocumentation)
	heading := slices.Index(lines, exitCodeTableHeading)
	if heading < 0 {
		t.Fatalf("%s no longer holds the exit-code table heading %q", reportDocumentation, exitCodeTableHeading)
	}
	var rows []exitCodeRow
	for _, line := range lines[heading+2:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < exitCodeTableCells {
			break
		}
		code, err := strconv.Atoi(strings.TrimSpace(cells[1]))
		if err != nil {
			t.Fatalf("%s: exit-code row %q does not open with a number", reportDocumentation, line)
		}
		rows = append(rows, exitCodeRow{code: code, meaning: cells[2]})
	}
	return rows
}

func documentedExitCodes(t *testing.T) map[string]int {
	t.Helper()
	documented := make(map[string]int)
	for _, row := range exitCodeRows(t) {
		for _, match := range backquotedValue.FindAllStringSubmatch(row.meaning, -1) {
			documented[match[1]] = row.code
		}
	}
	return documented
}

func documentedCommands(t *testing.T) []string {
	t.Helper()
	lines := documentationLines(t, readmePath)
	start := slices.Index(lines, commandSurfaceHeading)
	if start < 0 {
		t.Fatalf("%s no longer holds %q", readmePath, commandSurfaceHeading)
	}
	var documented []string
	inside := false
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "```") {
			if inside {
				break
			}
			inside = true
			continue
		}
		if !inside {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "goatest" {
			continue
		}
		if !slices.Contains(documented, fields[1]) {
			documented = append(documented, fields[1])
		}
	}
	return documented
}

func documentationLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(string(data), "\n")
}
