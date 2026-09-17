// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package cli

import (
	"encoding/json"
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
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/trace"
)

func explainAfterARun(t *testing.T, args ...string) (string, *report.Report) {
	t.Helper()
	root := inKillableFixture(t)
	rep, _ := runReport(t, append([]string{"--trace"}, args...)...)
	return root, rep
}

func explainOutput(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := execute(t, append([]string{"explain", "--no-color"}, args...)...)
	if code != 0 {
		t.Fatalf("`go-mutants explain %s` exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

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

func killingCommand(t *testing.T, root string, rep *report.Report, id string) (dir string, argv []string) {
	t.Helper()
	stream := filepath.Join(traceDirectoryOf(root, rep), trace.FileName)
	events, err := trace.Read(stream)
	if err != nil {
		t.Fatalf("reading the recording the account read: %v", err)
	}

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

func TestExplainAfterATracedRunOfKillableReproducesTheKillingCommand(t *testing.T) {
	root, rep := explainAfterARun(t, "--keep-temp")
	killed := mutantWith(t, rep, report.OutcomeKilled, false)

	account := explainOutput(t, killed.DisplayID[:8])
	if !strings.Contains(account, "killed by ") {
		t.Errorf("the account does not name the binary that caught it:\n%s", account)
	}
	dir, argv := killingCommand(t, root, rep, killed.ID)
	activation := "GO_MUTANTS_ACTIVE=" + killed.ID

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

	command := reproduceCommand(t, account)
	want := "cd " + console.QuoteArgv([]string{dir}) + " && " + activation + " " + console.QuoteArgv(argv)
	if command != want {
		t.Fatalf("the printed reproduction is not the recorded command\n got: %s\nwant: %s", command, want)
	}

	checkPrintedReproduction(t, command, dir, activation, argv)
	if runtime.GOOS != "windows" {
		runPrintedReproduction(t, command)
	}
}

func checkPrintedReproduction(t *testing.T, command, dir, activation string, argv []string) {
	t.Helper()
	fields, err := console.UnquoteArgv(command)
	if err != nil {
		t.Fatalf("the printed reproduction does not decode: %v\n%s", err, command)
	}
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
	if strings.Contains(account, "no trace recorded") {
		t.Errorf("the traced run's recording was not found:\n%s", account)
	}
}

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
	if !strings.Contains(account, "run "+rep.RunID) {
		t.Errorf("the account does not say which run it read:\n%s", account)
	}
}

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

func explainDocumentOf(t *testing.T, args ...string) map[string]any {
	t.Helper()
	stdout := explainOutput(t, append([]string{"--json"}, args...)...)
	if err := schemas.Validate(schemas.ExplainV1, []byte(stdout)); err != nil {
		t.Fatalf("`go-mutants explain --json %s` wrote a document %s rejects: %v\n%s",
			strings.Join(args, " "), schemas.ExplainV1, err, stdout)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decoding the document: %v\n%s", err, stdout)
	}
	return document
}

func reproduceOf(t *testing.T, document map[string]any, field string) any {
	t.Helper()
	reproduce, ok := document["reproduce"].(map[string]any)
	if !ok {
		t.Fatalf("the document holds no reproduce block: %v", document)
	}
	value, held := reproduce[field]
	if !held {
		t.Fatalf("the reproduce block holds no %q: %v", field, reproduce)
	}
	return value
}

func TestExplainJSONReproducesTheKillingCommand(t *testing.T) {
	root, rep := explainAfterARun(t, "--keep-temp")
	killed := mutantWith(t, rep, report.OutcomeKilled, false)

	document := explainDocumentOf(t, killed.DisplayID[:8])
	if available := reproduceOf(t, document, "available"); available != true {
		t.Fatalf("reproduce.available = %v after a run that kept its temporaries", available)
	}

	dir, argv := killingCommand(t, root, rep, killed.ID)
	activation := "GO_MUTANTS_ACTIVE=" + killed.ID

	if got := reproduceOf(t, document, "dir"); got != dir {
		t.Errorf("reproduce.dir = %v, want the recorded directory %q", got, dir)
	}
	published := stringsOf(t, reproduceOf(t, document, "argv"))
	if !slices.Equal(published, argv) {
		t.Errorf("reproduce.argv = %q, want the recorded command %q", published, argv)
	}
	activationOf(t, document, killed.ID)
	if _, err := os.Stat(published[0]); err != nil {
		t.Fatalf("the kept run's test binary is not there: %v", err)
	}
	child := exec.Command(published[0], published[1:]...)
	child.Dir = dir
	child.Env = append(os.Environ(), activation)
	output, err := child.CombinedOutput()
	if err == nil {
		t.Errorf("running reproduce.argv passed, so the document did not reproduce the kill:\n%s", output)
	}
	if !strings.Contains(string(output), "FAIL") {
		t.Errorf("running reproduce.argv did not fail as a Go test:\n%s", output)
	}

	command, ok := reproduceOf(t, document, "command").(string)
	if !ok || command == "" {
		t.Fatalf("reproduce.command is not a line: %v", reproduceOf(t, document, "command"))
	}
	if printed := reproduceCommand(t, explainOutput(t, killed.DisplayID[:8])); command != printed {
		t.Errorf("the document and the prose print different reproductions\n json: %s\nprose: %s", command, printed)
	}
	checkPrintedReproduction(t, command, dir, activation, argv)
	if runtime.GOOS != "windows" {
		runPrintedReproduction(t, command)
	}
}

func activationOf(t *testing.T, document map[string]any, id string) {
	t.Helper()
	activation, ok := reproduceOf(t, document, "activation").(map[string]any)
	if !ok {
		t.Fatalf("reproduce.activation is not an object: %v", reproduceOf(t, document, "activation"))
	}
	if activation["variable"] != "GO_MUTANTS_ACTIVE" {
		t.Errorf("reproduce.activation.variable = %v, want the variable the runtime reads", activation["variable"])
	}
	if activation["value"] != id {
		t.Errorf("reproduce.activation.value = %v, want the full identity %q", activation["value"], id)
	}
}

func stringsOf(t *testing.T, value any) []string {
	t.Helper()
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("%v is not a list", value)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("%v is not a string", item)
		}
		out = append(out, text)
	}
	return out
}

func TestExplainJSONOfAPositionAfterARun(t *testing.T) {
	_, rep := explainAfterARun(t)
	killed := mutantWith(t, rep, report.OutcomeKilled, false)

	document := explainDocumentOf(t, killed.Path+":"+strconv.Itoa(killed.Line))
	subject, ok := document["subject"].(map[string]any)
	if !ok || subject["kind"] != "position" {
		t.Fatalf("subject = %v, want a position", document["subject"])
	}
	if subject["path"] != killed.Path {
		t.Errorf("subject.path = %v, want %q", subject["path"], killed.Path)
	}
	mutants, ok := document["mutants"].([]any)
	if !ok || len(mutants) == 0 {
		t.Fatalf("mutants = %v; the line the run caught a mutant on has none", document["mutants"])
	}
	found := false
	for _, entry := range mutants {
		row, ok := entry.(map[string]any)
		if !ok || row["id"] != killed.ID {
			continue
		}
		found = true
		if row["outcome"] != string(report.OutcomeKilled) {
			t.Errorf("mutants[].outcome = %v for a mutant the run killed", row["outcome"])
		}
	}
	if !found {
		t.Errorf("the listing at %s:%d does not hold the mutant the run killed there", killed.Path, killed.Line)
	}
	if _, ok := document["skip_sites"].([]any); !ok {
		t.Errorf("skip_sites = %v, want a list even when it is empty", document["skip_sites"])
	}
	for _, absent := range []string{"verdict", "executions", "timeline", "reproduce"} {
		if _, held := document[absent]; held {
			t.Errorf("a position account carries %q, which only a mutant's account has", absent)
		}
	}
}
