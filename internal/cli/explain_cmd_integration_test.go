// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of `explain`: a real run, its real report, its real
// recording, and — for the one test that is the whole point of the command —
// the printed reproduction actually run.
//
// A synthetic report and a hand-written recording prove the join; only a run
// proves the join is true. The reproduce block is the claim with the sharpest
// edge in the tool — paste this and see the mutant caught — and the only way to
// check a claim like that is to paste it.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

// explainAfterARun runs killable with a recording and returns the report it
// wrote.
//
// `--keep-temp` is what makes the reproduction runnable at all: the test binary
// the recording names lives in the run's scratch directory, and a run that
// removed it would leave a command pointing at a path that is gone. Every
// directory it keeps is under the TMPDIR [inKillableFixture] redirected into
// the test's own temporary directory, so the keep is undone by the test's
// cleanup rather than left on the developer's disk.
func explainAfterARun(t *testing.T, args ...string) *report.Report {
	t.Helper()
	inKillableFixture(t)
	rep, _ := runReport(t, append([]string{"--trace"}, args...)...)
	return rep
}

// explainOutput drives `explain` in process and fails unless it exited 0.
func explainOutput(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := execute(t, append([]string{"explain", "--no-color"}, args...)...)
	if code != 0 {
		t.Fatalf("`go-mutants explain %s` exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// mutantWith returns the first mutant of the run with a given outcome, and
// fails the test when the fixture has none.
func mutantWith(t *testing.T, rep *report.Report, outcome report.Outcome, uncovered bool) report.Mutant {
	t.Helper()
	for _, m := range rep.Mutants {
		if m.Outcome == outcome && m.Uncovered == uncovered {
			return m
		}
	}
	t.Fatalf("the run has no %s mutant with uncovered=%v; the fixture's fates have changed", outcome, uncovered)
	return report.Mutant{}
}

// reproduceCommand lifts the pasteable line out of an account.
//
// It is found by its shape — the one line that starts with `cd ` — rather than
// by counting lines from the heading, so a section that gains a note above or
// below it does not silently start returning the wrong string.
func reproduceCommand(t *testing.T, account string) string {
	t.Helper()
	for _, line := range strings.Split(account, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "cd ") {
			return trimmed
		}
	}
	t.Fatalf("the account carries no reproduction command:\n%s", account)
	return ""
}

// TestExplainAfterATracedRunOfKillableReproducesTheKillingCommand is the
// command's central promise, checked by keeping it.
//
// The line is parsed rather than handed to a shell, and that is not
// squeamishness about `sh`: this repository's tests run on Windows too, and a
// test that could only pass on a POSIX machine would be a test of the
// reproduction on half the platforms it is printed on. What is parsed is
// exactly the three parts the line is composed of — the directory, the
// activation, and the argument vector — so a change to any of them fails here.
//
// A non-zero exit is the assertion, and it is the whole assertion: a Go test
// binary exits non-zero when a test fails, that failure is what "killed" means,
// and a zero would mean the mutant the report calls killed is not caught by the
// command the account says caught it.
func TestExplainAfterATracedRunOfKillableReproducesTheKillingCommand(t *testing.T) {
	rep := explainAfterARun(t, "--keep-temp")
	killed := mutantWith(t, rep, report.OutcomeKilled, false)

	account := explainOutput(t, killed.DisplayID[:8])
	if !strings.Contains(account, "killed by ") {
		t.Errorf("the account does not name the binary that caught it:\n%s", account)
	}
	command := reproduceCommand(t, account)

	dir, rest, ok := strings.Cut(strings.TrimPrefix(command, "cd "), " && ")
	if !ok {
		t.Fatalf("the reproduction is not `cd <dir> && <command>`: %q", command)
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		t.Fatalf("the reproduction has no command after the activation: %q", command)
	}
	activation, argv := fields[0], fields[1:]
	if activation != "GO_MUTANTS_ACTIVE="+killed.ID {
		t.Fatalf("the reproduction activates %q, want the mutant's own identity %q", activation, killed.ID)
	}
	if _, err := os.Stat(argv[0]); err != nil {
		t.Fatalf("the reproduction names a binary that is not there: %v", err)
	}

	child := exec.Command(argv[0], argv[1:]...)
	child.Dir = dir
	child.Env = append(os.Environ(), activation)
	output, err := child.CombinedOutput()
	if err == nil {
		t.Errorf("the reproduction of a killed mutant passed, so it did not reproduce the kill:\n%s", output)
	}
	if !strings.Contains(string(output), "FAIL") {
		t.Errorf("the reproduction did not fail as a Go test:\n%s", output)
	}
}

// TestExplainASurvivorAfterARun is the other verdict, and the one somebody
// actually types the command for.
//
// killable's survivor is its uncovered one — nothing in the module calls
// Untested — so the account has to say which line no binary reaches rather than
// list packages that cover it, and its reproduce block has to say that this run
// started no process for it rather than blame a recording that is right there.
func TestExplainASurvivorAfterARun(t *testing.T) {
	rep := explainAfterARun(t)
	survivor := mutantWith(t, rep, report.OutcomeSurvived, true)

	account := explainOutput(t, survivor.DisplayID[:8])
	for _, want := range []string{
		"survived without being executed",
		"no test binary reaches line " + strconv.Itoa(survivor.Line) + " of " + survivor.Path,
		"this run started no process for it",
		"go-mutants run --mutant " + survivor.DisplayID[:8],
	} {
		if !strings.Contains(account, want) {
			t.Errorf("the survivor's account does not carry %q:\n%s", want, account)
		}
	}
	// The recording really was found, which is what makes the two sentences
	// above statements about the mutant rather than about a missing file.
	if strings.Contains(account, "no trace recorded") {
		t.Errorf("the traced run's recording was not found:\n%s", account)
	}
}

// TestExplainAPositionAfterARun is the target that is a place rather than an
// identity: the question asked from the source instead of from the report.
func TestExplainAPositionAfterARun(t *testing.T) {
	rep := explainAfterARun(t)
	killed := mutantWith(t, rep, report.OutcomeKilled, false)
	target := killed.Path + ":" + strconv.Itoa(killed.Line)

	account := explainOutput(t, target)
	for _, want := range []string{
		"position " + target,
		"skip sites",
		"mutants",
		killed.DisplayID,
		target + ":" + strconv.Itoa(killed.Column),
		"killed",
	} {
		if !strings.Contains(account, want) {
			t.Errorf("the position account does not carry %q:\n%s", want, account)
		}
	}
	// The report was read out of the history rather than named, which is the
	// default source and the one nobody types a flag for.
	if !strings.Contains(account, "run "+rep.RunID) {
		t.Errorf("the account does not say which run it read:\n%s", account)
	}
}

// TestExplainAPositionHonoursTheRunsSelection is what makes the outcome column
// mean what it says.
//
// The discovery pass this form runs is a *second* pass over the workspace, and
// a pass configured differently from the run would catalogue mutants the run
// never had — reported as "not in this run", which reads as a mutant the run
// skipped rather than one the reader's own flags excluded. The report states
// the selection the run resolved, so the pass is configured from it.
func TestExplainAPositionHonoursTheRunsSelection(t *testing.T) {
	inKillableFixture(t)
	rep, _ := runReport(t, "--operator", "comparison")

	var target string
	for _, m := range rep.Mutants {
		if m.Family == "comparison" {
			target = m.Path + ":" + strconv.Itoa(m.Line)
			break
		}
	}
	if target == "" {
		t.Fatal("the narrowed run catalogued no comparison mutant")
	}

	account := explainOutput(t, target)
	if !strings.Contains(account, "comparison/") {
		t.Errorf("the position account lists none of the run's own mutants:\n%s", account)
	}
	if strings.Contains(account, "not in this run") {
		t.Errorf("the discovery pass catalogued mutants the run never selected:\n%s", account)
	}
	if strings.Contains(account, "condition-negation/") {
		t.Errorf("the pass ignored the run's --operator and widened the catalogue:\n%s", account)
	}
}

// TestExplainAPositionWithNoStoredRun is the question asked before any run:
// what would go-mutants make of this line, and why is there nothing here.
//
// It exits 0 with the outcome column saying there is no report, because that is
// the true answer — and refusing it for want of a run would refuse the command
// in the one situation somebody most wants it.
func TestExplainAPositionWithNoStoredRun(t *testing.T) {
	inKillableFixture(t)

	account := explainOutput(t, "clamp.go:41")
	for _, want := range []string{"position clamp.go:41", "skip sites", "mutants", "no report"} {
		if !strings.Contains(account, want) {
			t.Errorf("the account of an unmeasured position does not carry %q:\n%s", want, account)
		}
	}
	if strings.Contains(account, "run 2026") {
		t.Errorf("an account with no run behind it claims one:\n%s", account)
	}
}
