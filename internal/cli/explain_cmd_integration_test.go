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
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
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
func explainAfterARun(t *testing.T, args ...string) (string, *report.Report) {
	t.Helper()
	root := inKillableFixture(t)
	rep, _ := runReport(t, append([]string{"--trace"}, args...)...)
	return root, rep
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
// It is found by its shape — the one line that changes directory and activates
// a mutant — rather than by counting lines from the heading, so a section that
// gains a note above or below it, or the rebuild line a library session's
// account also carries, does not silently make this return the wrong string.
func reproduceCommand(t *testing.T, account string) string {
	t.Helper()
	for _, line := range strings.Split(account, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "cd ") && strings.Contains(trimmed, "GO_MUTANTS_ACTIVE=") {
			return trimmed
		}
	}
	t.Fatalf("the account carries no reproduction command:\n%s", account)
	return ""
}

// killingCommand is the child process the account's reproduce line describes,
// read out of the recording rather than out of the line.
//
// This is the structured half of the test and the reason it has two halves at
// all. What has to be executed is a program and its arguments, which the
// recording holds as data; what the account prints is one *line*, quoted for a
// shell. Splitting that line on spaces to get the program back was the bug this
// test was meant to catch and instead reproduced: a path with a space in it —
// or a Windows path, which is quoted because of its separators — comes back as
// two words with a stray quote on the front.
func killingCommand(t *testing.T, root string, rep *report.Report, id string) (dir string, argv []string) {
	t.Helper()
	stream := filepath.Join(traceDirectoryOf(root, rep), trace.FileName)
	events, err := trace.Read(stream)
	if err != nil {
		t.Fatalf("reading the recording the account read: %v", err)
	}

	// The last command of the mutant's last pass, which is the one whose output
	// is the evidence and the one the account prints. See explain's lastExecSeq.
	var seq int64
	for _, event := range events {
		if event.Type == trace.TypeMutantExec && event.Mutant.ID == id && len(event.Mutant.ExecSeqs) > 0 {
			seq = event.Mutant.ExecSeqs[len(event.Mutant.ExecSeqs)-1]
		}
	}
	for _, event := range events {
		if event.Seq == seq && event.Type == trace.TypeExec {
			return event.Exec.Dir, event.Exec.Argv
		}
	}
	t.Fatalf("the recording holds no command for %s", id)
	return "", nil
}

// TestExplainAfterATracedRunOfKillableReproducesTheKillingCommand is the
// command's central promise, checked by keeping it.
//
// It is two claims and they are checked separately, because they are about two
// different things and conflating them is what made an earlier version of this
// test wrong on Windows. The *command* is a program, a directory and an
// argument vector, which the recording holds as data; the *line* is that
// command rendered for a POSIX shell. Splitting the line on spaces to recover
// the program was a third thing — a shell parser — written by accident, and it
// failed on the first path that needed quoting.
//
// So: the command comes out of the recording and is run directly, on every
// platform, and a non-zero exit is the whole assertion — a Go test binary exits
// non-zero when a test fails, that failure is what "killed" means, and a zero
// would mean the mutant the report calls killed is not caught by the command
// the account says caught it. Then the printed line is checked against that
// same command: on a POSIX machine by running the line itself through `sh`,
// which is the only proof that a line meant to be pasted can be, and elsewhere
// by decoding it with the reader that lives beside the quoter.
func TestExplainAfterATracedRunOfKillableReproducesTheKillingCommand(t *testing.T) {
	root, rep := explainAfterARun(t, "--keep-temp")
	killed := mutantWith(t, rep, report.OutcomeKilled, false)

	account := explainOutput(t, killed.DisplayID[:8])
	if !strings.Contains(account, "killed by ") {
		t.Errorf("the account does not name the binary that caught it:\n%s", account)
	}
	dir, argv := killingCommand(t, root, rep, killed.ID)
	activation := "GO_MUTANTS_ACTIVE=" + killed.ID

	// The command, run as a command.
	if _, err := os.Stat(argv[0]); err != nil {
		t.Fatalf("the kept run's test binary is not there: %v", err)
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

	// The line, as a rendering of that command.
	command := reproduceCommand(t, account)
	want := "cd " + console.QuoteArgv([]string{dir}) + " && " + activation + " " + console.QuoteArgv(argv)
	if command != want {
		t.Fatalf("the printed reproduction is not the recorded command\n got: %s\nwant: %s", command, want)
	}

	// The line, as a line. It is decoded on every platform, because a decoder
	// that had drifted from the quoter would otherwise only be caught on the
	// one platform that cannot also run the line — which is the platform whose
	// failures are hardest to reproduce. Where there is a shell to paste into,
	// the paste itself is the stronger proof and is made as well.
	checkPrintedReproduction(t, command, dir, activation, argv)
	if runtime.GOOS != "windows" {
		runPrintedReproduction(t, command)
	}
}

// checkPrintedReproduction decodes the printed line and compares it with the
// command it was rendered from.
//
// It is what a platform whose shell cannot run the line gets instead of running
// it, and what every other platform gets as well. The decoder is
// [console.UnquoteArgv], which lives beside the quoter and is held to it by a
// round-trip test, so this is a comparison against the quoting rules rather
// than against a second guess at them.
func checkPrintedReproduction(t *testing.T, command, dir, activation string, argv []string) {
	t.Helper()
	fields, err := console.UnquoteArgv(command)
	if err != nil {
		t.Fatalf("the printed reproduction does not decode: %v\n%s", err, command)
	}
	// `cd <dir> && <activation> <argv...>`: the operator is a word of its own,
	// because the line is joined with spaces.
	if len(fields) < 4 || fields[0] != "cd" || fields[2] != "&&" || fields[3] != activation {
		t.Fatalf("the printed reproduction is not `cd <dir> && %s <argv...>`: %q", activation, fields)
	}
	if fields[1] != dir {
		t.Errorf("the reproduction changes to %q, want the recorded directory %q", fields[1], dir)
	}
	if got := fields[4:]; !slices.Equal(got, argv) {
		t.Errorf("the reproduction runs %q, want the recorded command %q", got, argv)
	}
}

// runPrintedReproduction runs the printed line through a POSIX shell, which is
// the only proof that a line meant to be pasted can be.
//
// Nothing is parsed here: the shell does the quoting, the `cd`, the environment
// assignment and the exec, exactly as the reader who selected the line would.
func runPrintedReproduction(t *testing.T, command string) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX shell to paste the reproduction into: %v", err)
	}
	output, err := exec.Command(shell, "-c", command).CombinedOutput()
	if err == nil {
		t.Errorf("the pasted reproduction passed, so it did not reproduce the kill:\n%s", output)
	}
	if !strings.Contains(string(output), "FAIL") {
		t.Errorf("the pasted reproduction did not fail as a Go test:\n%s\n%s", command, output)
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
	_, rep := explainAfterARun(t)
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
	_, rep := explainAfterARun(t)
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
