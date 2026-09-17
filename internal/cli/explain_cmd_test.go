// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testsupport"
	"github.com/P4suta/go-mutants/trace"
)

var (
	killedID    = strings.Repeat("1f", 32)
	survivorID  = strings.Repeat("2a", 32)
	uncoveredID = strings.Repeat("3b", 32)
	rejectedID  = strings.Repeat("4c", 32)
	cachedID    = strings.Repeat("5d", 32)
	firstTwinID = "abcd" + strings.Repeat("11", 30)
	otherTwinID = "abcd" + strings.Repeat("22", 30)
)

const explainRunID = "20260907T120000Z-a1b2"

func displayOf(id string) string { return id[:20] }

func explainReport() *report.Report {
	killedBy := "example.com/killable"
	notRun := string(report.NotRunOutOfSelection)
	tail := "--- FAIL: TestClamp (0.00s)\n    clamp_test.go:41: Clamp(10, 0, 10) = 10, want 9\nFAIL\n"
	binaries := 1
	uncovered := 1
	return &report.Report{
		DocumentType:  report.DocumentType,
		SchemaVersion: report.SchemaVersion,
		ToolVersion:   "0.1.0-dev",
		RunID:         explainRunID,
		Status:        report.StatusCompleted,
		StartedAt:     "2026-09-07T12:00:00Z",
		FinishedAt:    "2026-09-07T12:01:00Z",
		DurationMS:    60000,
		Workspace: report.Workspace{
			ModulePath: "example.com/killable",
			GoVersion:  "1.26",
		},
		Test: report.Test{
			Command:         []string{"go", "test", "./..."},
			ResolvedCommand: []string{"/usr/lib/go/bin/go", "test", "./..."},
			Toolchain: &report.ToolchainFacts{
				GoBin:   "/usr/lib/go/bin/go",
				Version: "go version go1.26.0 linux/amd64",
			},
			TimeoutMS:     20000,
			TimeoutSource: report.TimeoutExplicit,
			MemoryBytes:   1 << 30,
			MemorySource:  report.MemoryDerived,
		},
		Coverage: report.Coverage{
			Mode:             report.CoveragePackage,
			Binaries:         &binaries,
			MutantsUncovered: &uncovered,
		},
		Cache: report.Cache{Mode: report.CacheOn, Hits: 1},
		Mutants: []report.Mutant{
			{
				ID: killedID, DisplayID: displayOf(killedID),
				Path: "clamp.go", Package: "example.com/killable",
				Family: "comparison", Rule: "lt-to-le", RuleVersion: 1,
				Line: 41, Column: 7,
				Original: "v < hi", Replacement: "v <= hi",
				Outcome: report.OutcomeKilled, DurationMS: 520,
				KilledBy: &killedBy, Attempts: 2,
				Executions: []report.Execution{
					{Attempt: 1, Worker: 3, Outcome: report.OutcomeTimedOut, DurationMS: 20000,
						KilledBy: "example.com/killable", Binaries: []string{"example.com/killable"},
						PeakMemoryBytes: 314572800},
					{Attempt: 2, Worker: 0, Outcome: report.OutcomeKilled, DurationMS: 520,
						KilledBy: "example.com/killable", Binaries: []string{"example.com/killable"},
						PeakMemoryBytes: 419430400},
				},
				OutputTail:           &tail,
				CoveringTestPackages: []string{"example.com/killable"},
			},
			{
				ID: survivorID, DisplayID: displayOf(survivorID),
				Path: "ready.go", Package: "example.com/killable",
				Family: "boolean", Rule: "true-to-false", RuleVersion: 1,
				Line: 14, Column: 9,
				Original: "true", Replacement: "false",
				Outcome: report.OutcomeSurvived, DurationMS: 310, Attempts: 1,
				Executions: []report.Execution{
					{Attempt: 1, Worker: 1, Outcome: report.OutcomeSurvived, DurationMS: 310,
						Binaries: []string{"example.com/killable"}, PeakMemoryBytes: 209715200},
				},
				CoveringTestPackages: []string{"example.com/killable"},
			},
			{
				ID: uncoveredID, DisplayID: displayOf(uncoveredID),
				Path: "untested.go", Package: "example.com/killable",
				Family: "comparison", Rule: "neq-to-eq", RuleVersion: 1,
				Line: 14, Column: 11,
				Original: "!=", Replacement: "==",
				Outcome:              report.OutcomeSurvived,
				Executions:           []report.Execution{},
				CoveringTestPackages: []string{},
				Uncovered:            true,
			},
			{
				ID: cachedID, DisplayID: displayOf(cachedID),
				Path: "clamp.go", Package: "example.com/killable",
				Family: "comparison", Rule: "gt-to-ge", RuleVersion: 1,
				Line: 42, Column: 8,
				Original: "v > lo", Replacement: "v >= lo",
				Outcome: report.OutcomeKilled, DurationMS: 480,
				KilledBy: &killedBy, Attempts: 1,
				Executions:           []report.Execution{},
				CoveringTestPackages: []string{"example.com/killable"},
				Cached:               true,
			},
			{
				ID: firstTwinID, DisplayID: displayOf(firstTwinID),
				Path: "clamp.go", Package: "example.com/killable",
				Family: "arithmetic", Rule: "add-to-sub", RuleVersion: 1,
				Line: 45, Column: 10,
				Original: "lo + 1", Replacement: "lo - 1",
				Outcome: report.OutcomeKilled, DurationMS: 100,
				KilledBy: &killedBy, Attempts: 1,
				Executions:           []report.Execution{},
				CoveringTestPackages: []string{"example.com/killable"},
			},
			{
				ID: otherTwinID, DisplayID: displayOf(otherTwinID),
				Path: "clamp.go", Package: "example.com/killable",
				Family: "arithmetic", Rule: "sub-to-add", RuleVersion: 1,
				Line: 47, Column: 9,
				Original: "hi - 1", Replacement: "hi + 1",
				Outcome:              report.OutcomeNotRun,
				NotRunReason:         &notRun,
				Executions:           []report.Execution{},
				CoveringTestPackages: []string{},
			},
		},
		Rejected: []report.Rejected{{
			ID: rejectedID, DisplayID: displayOf(rejectedID),
			Path: "clamp.go", Line: 43, Column: 12, Rule: "swap-operands",
			Diagnostic: "./clamp.go:43:12: invalid operation: v + hi (mismatched types int and string)\n" +
				"./clamp.go:43:12: cannot use v + hi (value of type int) as string value in return statement",
		}},
		Skips:        []report.Skip{},
		Expectations: []report.Expectation{},
		Warnings:     []report.Warning{},
	}
}

func positionAccount(
	r *report.Report, where position, sites []discover.SkipSite, mutants []catalogMutant,
) explainPositionDocument {
	return gatherPosition(r, "mutation.json", where, sites, mutants, outcomesOf(r))
}

func inExplainWorkspace(t *testing.T, r *report.Report) string {
	t.Helper()
	dir := t.TempDir()
	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("encoding the report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mutation.json"), data, 0o600); err != nil {
		t.Fatalf("writing the report: %v", err)
	}
	t.Chdir(dir)
	return dir
}

func explain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = ExecuteContext(t.Context(),
		append([]string{"explain", "--no-color", "--report", "mutation.json"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func explained(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := explain(t, args...)
	if code != 0 {
		t.Fatalf("`go-mutants explain %s` exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

func recordEvents(t *testing.T, workspace string, events []trace.Event, outputs map[int64]string) string {
	t.Helper()
	dir := filepath.Join(workspace, "reports", "mutation", "trace", explainRunID)
	if err := os.MkdirAll(filepath.Join(dir, trace.OutputDirectoryName), 0o755); err != nil {
		t.Fatalf("creating the recording directory: %v", err)
	}
	var b bytes.Buffer
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding a trace event: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, trace.FileName), b.Bytes(), 0o600); err != nil {
		t.Fatalf("writing the recording: %v", err)
	}
	for seq, text := range outputs {
		name := filepath.Join(dir, trace.OutputDirectoryName, strconv.FormatInt(seq, 10)+".txt")
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			t.Fatalf("writing the preserved output: %v", err)
		}
	}
	return dir
}

func ms(v int64) *int64 { return &v }

func killedRecording() []trace.Event {
	return []trace.Event{
		{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1,
			Timestamp: "2026-09-07T12:00:00Z", ElapsedMS: 0,
			Start: &trace.StartRecord{
				Kind: trace.StartKindRun, RunID: explainRunID, ToolVersion: "0.1.0-dev",
				PID: 4242, Root: "/tmp/killable",
				Args: []string{"run", "--trace", "--keep-temp"},
			}},
		{Seq: 2, Type: trace.TypeStage, Timestamp: "2026-09-07T12:00:01Z", ElapsedMS: 1000,
			Stage: &trace.StageRecord{Phase: trace.PhaseMutate, Name: "execute", State: trace.StateStarted}},
		{Seq: 3, Type: trace.TypeExec, Timestamp: "2026-09-07T12:00:02Z", ElapsedMS: 2000,
			Exec: &trace.ExecRecord{
				Kind: trace.ExecKindMutantRun, Subject: killedID,
				Argv:      []string{"/tmp/scratch/bin/killable.test", "-test.timeout=40s"},
				Dir:       "/tmp/snapshot",
				EnvNames:  []string{"GO_MUTANTS_ACTIVE", "PATH", "TMPDIR"},
				TimeoutMS: 20000, ExitCode: -1, TimedOut: true, DurationMS: 20000,
			}},
		{Seq: 4, Type: trace.TypeMutantExec, Timestamp: "2026-09-07T12:00:22Z", ElapsedMS: 22000,
			Mutant: &trace.MutantRecord{
				ID: killedID, DisplayID: displayOf(killedID), Attempt: 1, Worker: 3,
				Package: "example.com/killable", Binaries: []string{"example.com/killable"},
				TimeoutMS: 20000, Outcome: trace.OutcomeTimedOut,
				KilledBy: "example.com/killable", DurationMS: 20000, ExecSeqs: []int64{3},
			}},
		{Seq: 5, Type: trace.TypeExec, Timestamp: "2026-09-07T12:00:23Z", ElapsedMS: 23000,
			Exec: &trace.ExecRecord{
				Kind: trace.ExecKindMutantRun, Subject: killedID,
				Argv:      []string{"/tmp/scratch/bin/killable.test", "-test.timeout=40s"},
				Dir:       "/tmp/snapshot",
				EnvNames:  []string{"GO_MUTANTS_ACTIVE", "PATH", "TMPDIR"},
				TimeoutMS: 20000, ExitCode: 1, DurationMS: 520,
				OutputBytes: 96, OutputSHA256: strings.Repeat("ab", 32),
				OutputPath: "output/5.txt",
			}},
		{Seq: 6, Type: trace.TypeMutantExec, Timestamp: "2026-09-07T12:00:24Z", ElapsedMS: 24000,
			Mutant: &trace.MutantRecord{
				ID: killedID, DisplayID: displayOf(killedID), Attempt: 2, Worker: 0,
				Package: "example.com/killable", Binaries: []string{"example.com/killable"},
				TimeoutMS: 20000, Outcome: trace.OutcomeKilled,
				KilledBy: "example.com/killable", DurationMS: 520, ExecSeqs: []int64{5},
				OutputTail: "--- FAIL: TestClamp (0.00s)\nFAIL\n",
			}},
		{Seq: 7, Type: trace.TypeStage, Timestamp: "2026-09-07T12:00:25Z", ElapsedMS: 25000,
			Stage: &trace.StageRecord{Phase: trace.PhaseMutate, Name: "execute",
				State: trace.StateFinished, Result: trace.ResultSucceeded, DurationMS: ms(24000)}},
		{Seq: 8, Type: trace.TypeArtifact, Timestamp: "2026-09-07T12:00:26Z", ElapsedMS: 26000,
			Artifact: &trace.ArtifactRecord{Kind: trace.ArtifactKeptScratch, Path: "/tmp/scratch"}},
		{Seq: 9, Type: trace.TypeRunEnd, Timestamp: "2026-09-07T12:00:27Z", ElapsedMS: 27000,
			Run: &trace.RunRecord{Verdict: "completed", ExitCode: 0, EventsEmitted: 8}},
	}
}

func TestExplainResolvesAPrefixAgainstMutantsAndRejected(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	for _, want := range []struct {
		prefix string
		needle string
	}{
		{killedID[:8], "lt-to-le"},
		{rejectedID[:8], "swap-operands"},
	} {
		out := explained(t, want.prefix)
		if !strings.Contains(out, want.needle) {
			t.Errorf("explain %s did not resolve to the %s mutant:\n%s", want.prefix, want.needle, out)
		}
	}
}

func TestExplainSaysAmbiguousAndListsMatches(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	code, stdout, stderr := explain(t, "abcd")
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	for _, want := range []string{
		displayOf(firstTwinID), "clamp.go:45", "killed",
		displayOf(otherTwinID), "clamp.go:47", "not-run",
	} {
		if !strings.Contains(stdout+stderr, want) {
			t.Errorf("the ambiguity does not name %q:\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}
}

func TestExplainUnknownPrefixIsRefused(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	code, stdout, stderr := explain(t, "beef")
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(CodeMutantUnresolved)) {
		t.Errorf("the refusal carries no %s:\n%s", CodeMutantUnresolved, stderr)
	}
}

func TestExplainKilledShowsKilledByAttemptsAndTail(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(),
		map[int64]string{5: "--- FAIL: TestClamp (0.00s)\n    clamp_test.go:41: want 9\nFAIL\n"})

	out := explained(t, killedID[:8])
	for _, want := range []string{
		"killed by example.com/killable",
		"2 attempts",
		"attempt 1  worker 3",
		"attempt 2  worker 0",
		"--- FAIL: TestClamp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the account of a killed mutant does not carry %q:\n%s", want, out)
		}
	}
}

const wantSurvivorAccount = `run 20260907T120000Z-a1b2  completed
report mutation.json
no trace recorded for run 20260907T120000Z-a1b2; re-run with --trace

mutant
  display id  2a2a2a2a2a2a2a2a2a2a
  id          2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a
  rule        boolean/true-to-false
  position    ready.go:14:9
  change      true -> false
  package     example.com/killable

outcome
  survived after 1 attempt

coverage
  covered by: example.com/killable

executions
  attempt 1  worker 1  survived  310ms  peak 200.0 MiB
    binaries: example.com/killable
  no recording, so the commands these passes started are not in this account

timeline
  no recording, so the stages this mutant took part in are not in this account

reproduce
  no recording, so there is no argument vector to paste
  the run's tests were: /usr/lib/go/bin/go test ./...
  go-mutants run --mutant 2a2a2a2a --keep-temp -vv --trace
`

func TestExplainSurvivorShowsCoveringBinariesAndReproduction(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	if out := explained(t, survivorID[:8]); out != wantSurvivorAccount {
		t.Errorf("the survivor's account is not the expected one\n--- got ---\n%s\n--- want ---\n%s",
			out, wantSurvivorAccount)
	}
}

func TestExplainUncoveredSaysWhichLineNoBinaryReaches(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	out := explained(t, uncoveredID[:8])
	if !strings.Contains(out, "no test binary reaches line 14 of untested.go") {
		t.Errorf("the uncovered survivor does not name the line nothing reaches:\n%s", out)
	}
	if !strings.Contains(out, "survived without being executed") {
		t.Errorf("the uncovered survivor does not say it was never executed:\n%s", out)
	}
}

func TestExplainRejectedQuotesTheDiagnostic(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	out := explained(t, rejectedID[:8])
	for _, want := range []string{
		"rejected",
		"invalid operation: v + hi (mismatched types int and string)",
		"cannot use v + hi (value of type int) as string value in return statement",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rejection does not carry %q:\n%s", want, out)
		}
	}
}

func TestExplainCachedSaysThisRunDidNotMeasureIt(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	out := explained(t, cachedID[:8])
	if !strings.Contains(out, "reused from the outcome cache") {
		t.Errorf("the cached mutant does not say where its outcome came from:\n%s", out)
	}
	if !strings.Contains(out, "this run started no process for it") {
		t.Errorf("the cached mutant does not say this run measured nothing:\n%s", out)
	}
}

func TestExplainWithoutATraceSaysSoInsteadOfGuessing(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	out := explained(t, killedID[:8])
	if !strings.Contains(out, "no trace recorded for run "+explainRunID+"; re-run with --trace") {
		t.Errorf("the absence of a recording is not reported:\n%s", out)
	}
	if strings.Contains(out, "GO_MUTANTS_ACTIVE=") {
		t.Errorf("a reproduction command was invented with no recording to derive it from:\n%s", out)
	}
	if !strings.Contains(out, "go-mutants run --mutant "+killedID[:8]+" --keep-temp -vv --trace") {
		t.Errorf("the account does not say how to get a recording:\n%s", out)
	}
}

func TestExplainWithATraceListsEachBinaryRunAndItsOutputFile(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	dir := recordEvents(t, workspace, killedRecording(),
		map[int64]string{5: "one\ntwo\nthree\n--- FAIL: TestClamp (0.00s)\nFAIL\n"})

	out := explained(t, killedID[:8])
	if !strings.Contains(out, filepath.Join(dir, trace.FileName)) {
		t.Errorf("the account does not name the recording it read:\n%s", out)
	}
	if !strings.Contains(out, "/tmp/scratch/bin/killable.test -test.timeout=40s") {
		t.Errorf("the account does not quote the argument vector:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(dir, "output", "5.txt")) {
		t.Errorf("the account does not name the preserved output file:\n%s", out)
	}
	if !strings.Contains(out, "--- FAIL: TestClamp (0.00s)") {
		t.Errorf("the account does not show the tail of the preserved output:\n%s", out)
	}
	if !strings.Contains(out, "cd /tmp/snapshot && GO_MUTANTS_ACTIVE="+killedID) {
		t.Errorf("the account does not derive a reproduction from the recording:\n%s", out)
	}
}

func TestExplainTimelineNamesTheStagesTheMutantTookPartIn(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), nil)

	out := explained(t, killedID[:8])
	if !strings.Contains(out, "mutate/execute") {
		t.Errorf("the timeline does not name the stage the mutant ran inside:\n%s", out)
	}
	if !strings.Contains(out, "24s") {
		t.Errorf("the timeline does not carry the stage's duration:\n%s", out)
	}
}

func TestExplainPositionListsSkipSitesAndMutantsOnThatLine(t *testing.T) {
	var out bytes.Buffer
	r := explainReport()
	sites := []discover.SkipSite{
		{Path: "clamp.go", Reason: discover.SkipConstDecl, Line: 41, Column: 9},
		{Path: "ready.go", Reason: discover.SkipConstDecl, Line: 3, Column: 1},
	}
	mutants := []catalogMutant{
		{ID: killedID, DisplayID: displayOf(killedID), Path: "clamp.go", Line: 41, Column: 7,
			Family: "comparison", Rule: "lt-to-le", Original: "v < hi", Replacement: "v <= hi"},
		{ID: otherTwinID, DisplayID: displayOf(otherTwinID), Path: "clamp.go", Line: 47, Column: 9,
			Family: "arithmetic", Rule: "sub-to-add", Original: "hi - 1", Replacement: "hi + 1"},
	}
	if err := explainPosition(&out, false,
		positionAccount(r, position{path: "clamp.go", line: 41}, sites, mutants)); err != nil {
		t.Fatalf("explainPosition: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"position clamp.go:41",
		"clamp.go:41:9  const-decl",
		displayOf(killedID),
		"clamp.go:41:7",
		"killed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the position account does not carry %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "clamp.go:47") {
		t.Errorf("a mutant on another line was listed:\n%s", text)
	}
	if strings.Contains(text, "ready.go") {
		t.Errorf("a skip site in another file was listed:\n%s", text)
	}
}

func TestExplainHelpNamesEverySource(t *testing.T) {
	code, stdout, _ := execute(t, "explain", "--help")
	if code != 0 {
		t.Fatalf("`go-mutants explain --help` exited %d", code)
	}
	for _, want := range []string{"--report", "--run", "--trace", "latest"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the help does not name %q:\n%s", want, stdout)
		}
	}
}

func TestExplainIsInTheRootHelp(t *testing.T) {
	_, stdout, _ := execute(t, "--help")
	if !strings.Contains(stdout, "explain") {
		t.Errorf("the root help does not list `explain`:\n%s", stdout)
	}
}

func TestExplainDistinguishesNoRecordingFromNoProcess(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), nil)

	out := explained(t, uncoveredID[:8])
	if !strings.Contains(out, "this run started no process for it, so its recording holds no argument vector") {
		t.Errorf("an uncovered mutant's reproduce block blames the recording:\n%s", out)
	}
	if strings.Contains(out, "no recording, so there is no argument vector") {
		t.Errorf("a recording that is there is reported as missing:\n%s", out)
	}
}

func inExplainHistory(t *testing.T, r *report.Report) {
	t.Helper()
	base := testsupport.CacheDir(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module "+r.Workspace.ModulePath+"\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	t.Chdir(dir)

	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("encoding the report: %v", err)
	}
	seedWorkspace(t, filepath.Join(base, report.DirName), historyDigest, map[string]string{
		r.RunID + ".json":     string(data),
		report.LatestFileName: string(data),
	})
}

func TestExplainReadsTheLatestRunAndOneNamedById(t *testing.T) {
	inExplainHistory(t, explainReport())

	for _, args := range [][]string{
		{"explain", "--no-color", killedID[:8]},
		{"explain", "--no-color", "--run", explainRunID, killedID[:8]},
	} {
		code, stdout, stderr := execute(t, args...)
		if code != 0 {
			t.Fatalf("`go-mutants %s` exited %d\nstdout:\n%s\nstderr:\n%s",
				strings.Join(args, " "), code, stdout, stderr)
		}
		if !strings.Contains(stdout, "run "+explainRunID) {
			t.Errorf("`%s` did not read the stored run:\n%s", strings.Join(args, " "), stdout)
		}
		if !strings.Contains(stdout, "killed by example.com/killable") {
			t.Errorf("`%s` did not explain the mutant:\n%s", strings.Join(args, " "), stdout)
		}
	}
}

func TestExplainRefusesARunItHasNoRecordOf(t *testing.T) {
	inExplainHistory(t, explainReport())

	code, _, stderr := execute(t, "explain", "--no-color", "--run", "20260101T000000Z-9999", killedID[:8])
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, string(CodeNoStoredRun)) {
		t.Errorf("the refusal carries no %s:\n%s", CodeNoStoredRun, stderr)
	}
}

func TestExplainRefusesReportWithRun(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	code, _, stderr := execute(t, "explain", "--no-color",
		"--report", "mutation.json", "--run", explainRunID, killedID[:8])
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, string(CodeConflictingFlags)) {
		t.Errorf("the refusal carries no %s:\n%s", CodeConflictingFlags, stderr)
	}
}

func TestExplainSaysWhetherTheTemporariesSurvived(t *testing.T) {
	const gone = "this run did not keep its temporaries"
	const kept = "the run kept its temporaries"

	t.Run("kept", func(t *testing.T) {
		workspace := inExplainWorkspace(t, explainReport())
		recordEvents(t, workspace, killedRecording(), nil)
		out := explained(t, killedID[:8])
		if !strings.Contains(out, kept) {
			t.Errorf("a kept run's account does not say the temporaries are there:\n%s", out)
		}
		if strings.Contains(out, gone) {
			t.Errorf("a kept run's account says its temporaries are gone:\n%s", out)
		}
	})

	t.Run("removed", func(t *testing.T) {
		workspace := inExplainWorkspace(t, explainReport())
		events := killedRecording()
		var without []trace.Event
		for _, event := range events {
			if event.Type == trace.TypeArtifact {
				continue
			}
			without = append(without, event)
		}
		recordEvents(t, workspace, without, nil)
		out := explained(t, killedID[:8])
		if !strings.Contains(out, gone) {
			t.Errorf("an unkept run's account does not say the temporaries are gone:\n%s", out)
		}
		if !strings.Contains(out, "--trace --keep-temp") {
			t.Errorf("an unkept run's account does not say how to get them:\n%s", out)
		}
	})
}

func foreignRecording() []trace.Event {
	events := killedRecording()
	events[0].Start.RunID = "20260101T000000Z-9999"
	return events
}

func TestExplainWarnsWhenTheRecordingIsOfAnotherRun(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	dir := recordEvents(t, workspace, foreignRecording(), nil)

	out := explained(t, "--trace", dir, killedID[:8])
	if !strings.Contains(out, "20260101T000000Z-9999") {
		t.Errorf("the account does not name the run the recording is of:\n%s", out)
	}
	if !strings.Contains(out, "not "+explainRunID) {
		t.Errorf("the account does not say the recording is of another run:\n%s", out)
	}
}

func TestExplainDoesNotBlameTheMutantForAForeignRecording(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	dir := recordEvents(t, workspace, foreignRecording(), nil)

	out := explained(t, "--trace", dir, uncoveredID[:8])
	if strings.Contains(out, "this run started no process for it, so its recording") {
		t.Errorf("a foreign recording was read as evidence about this run:\n%s", out)
	}
	if !strings.Contains(out, "is of another run") {
		t.Errorf("the reproduce block does not say why it has no command:\n%s", out)
	}
}

func TestExplainPositionShowsAWholeFileSkip(t *testing.T) {
	var out bytes.Buffer
	sites := []discover.SkipSite{{Path: "gen.go", Reason: discover.SkipGenerated}}
	if err := explainPosition(&out, false,
		positionAccount(nil, position{path: "gen.go", line: 6}, sites, nil)); err != nil {
		t.Fatalf("explainPosition: %v", err)
	}
	if !strings.Contains(out.String(), "generated") {
		t.Errorf("the whole-file skip is not listed for a line of that file:\n%s", out.String())
	}
}

func TestExplainPositionMatchesAMutantThatSpansLines(t *testing.T) {
	var out bytes.Buffer
	mutants := []catalogMutant{{
		ID: killedID, DisplayID: displayOf(killedID), Path: "clamp.go", Line: 41, Column: 7,
		Family: "comparison", Rule: "lt-to-le",
		Original: "v < hi &&\n\t\tv > lo", Replacement: "v <= hi &&\n\t\tv > lo",
	}}
	if err := explainPosition(&out, false,
		positionAccount(nil, position{path: "clamp.go", line: 42}, nil, mutants)); err != nil {
		t.Fatalf("explainPosition: %v", err)
	}
	if !strings.Contains(out.String(), displayOf(killedID)) {
		t.Errorf("a mutant whose span reaches line 42 is not listed for it:\n%s", out.String())
	}
}

func TestParsePositionAcceptsTheSpellingExplainPrints(t *testing.T) {
	const drivePath = `C:\src\clamp.go:41`
	for _, c := range []struct {
		target string
		want   position
		ok     bool
	}{
		{"clamp.go", position{path: "clamp.go"}, true},
		{"clamp.go:41", position{path: "clamp.go", line: 41}, true},
		{"clamp.go:41:7", position{path: "clamp.go", line: 41}, true},
		{"./clamp.go:41", position{path: "clamp.go", line: 41}, true},
		{"internal/./x/../clamp.go", position{path: "internal/clamp.go"}, true},
		{drivePath, position{path: path.Clean(filepath.ToSlash(drivePath[:len(drivePath)-3])), line: 41}, true},
		{"abcd1234", position{}, false},
		{"clamp.go:0", position{}, false},
	} {
		got, ok := parsePosition(c.target)
		if ok != c.ok || got != c.want {
			t.Errorf("parsePosition(%q) = %+v, %v; want %+v, %v", c.target, got, ok, c.want, c.ok)
		}
	}
}

func TestExplainRefusesAPositionThatNamesNoFile(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	code, stdout, stderr := explain(t, "nosuch/clamp.go:41")
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "nosuch/clamp.go") {
		t.Errorf("the refusal does not name the path:\n%s", stderr)
	}
}

func TestExplainAPositionNeedsNoStoredRun(t *testing.T) {
	testsupport.CacheDir(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/empty\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	t.Chdir(dir)

	code, stdout, stderr := execute(t, "explain", "--no-color", "nosuch/clamp.go:41")
	if strings.Contains(stderr, string(CodeNoStoredRun)) {
		t.Errorf("a position query was refused for want of a run:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if code != 2 || !strings.Contains(stderr, "nosuch/clamp.go") {
		t.Errorf("exit = %d, want a refusal naming the path\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

func libraryRecording() []trace.Event {
	events := killedRecording()
	events[0].Start.Kind = trace.StartKindWorkspace
	events[0].Start.RunID = ""
	end := events[len(events)-1]
	manifest := trace.Event{
		Seq: end.Seq, Type: trace.TypeArtifact, Timestamp: "2026-09-07T12:00:26Z", ElapsedMS: 26000,
		Artifact: &trace.ArtifactRecord{
			Kind: trace.ArtifactOverlayManifest, Path: "/tmp/session/overlay.json",
		},
	}
	end.Seq++
	return append(events[:len(events)-1], manifest, end)
}

func TestExplainPutsTheOverlayRebuildOnItsOwnLine(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	dir := recordEvents(t, workspace, libraryRecording(), nil)

	out := explained(t, "--trace", dir, killedID[:8])
	var runLine, rebuildLine string
	for _, line := range strings.Split(out, "\n") {
		switch trimmed := strings.TrimSpace(line); {
		case strings.HasPrefix(trimmed, "cd ") && strings.Contains(trimmed, "GO_MUTANTS_ACTIVE="):
			runLine = trimmed
		case strings.Contains(trimmed, "GOFLAGS=-overlay="):
			rebuildLine = trimmed
		}
	}
	if runLine == "" {
		t.Fatalf("the account carries no run line:\n%s", out)
	}
	if strings.Contains(runLine, "GOFLAGS") {
		t.Errorf("the run line still carries an inert GOFLAGS:\n%s", runLine)
	}
	if rebuildLine == "" {
		t.Fatalf("the account does not say how to rebuild the binary:\n%s", out)
	}
	if !strings.Contains(rebuildLine, "/tmp/session/overlay.json") {
		t.Errorf("the rebuild line does not name the manifest:\n%s", rebuildLine)
	}
	if !strings.Contains(rebuildLine, "go test -c") {
		t.Errorf("the rebuild line does not rebuild anything:\n%s", rebuildLine)
	}
}

func TestExplainTimelineNamesTheMutantsOwnShare(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), nil)

	out := explained(t, killedID[:8])
	if !strings.Contains(out, "(this mutant: 20.52s)") {
		t.Errorf("the timeline does not say how much of the stage was this mutant's:\n%s", out)
	}
}

func TestExplainTimelineSaysAStageIsStillOpen(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	var interrupted []trace.Event
	for _, event := range killedRecording() {
		if event.Type == trace.TypeStage && event.Stage.State == trace.StateFinished {
			continue
		}
		interrupted = append(interrupted, event)
	}
	recordEvents(t, workspace, interrupted, nil)

	out := explained(t, killedID[:8])
	if !strings.Contains(out, "mutate/execute") {
		t.Errorf("an unfinished stage was dropped from the timeline:\n%s", out)
	}
	if !strings.Contains(out, "still open at the end of the recording") {
		t.Errorf("the timeline does not say the stage never closed:\n%s", out)
	}
}

func TestExplainARejectionShowsTheValidationThatRefusedIt(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	events := []trace.Event{
		{Seq: 1, Type: trace.TypeRunStart, Schema: trace.SchemaV1,
			Timestamp: "2026-09-07T12:00:00Z",
			Start: &trace.StartRecord{
				Kind: trace.StartKindRun, RunID: explainRunID, ToolVersion: "0.1.0-dev",
				PID: 4242, Root: "/tmp/killable",
			}},
		{Seq: 2, Type: trace.TypeStage, Timestamp: "2026-09-07T12:00:01Z", ElapsedMS: 1000,
			Stage: &trace.StageRecord{Phase: trace.PhaseMutate, Name: "validate", State: trace.StateStarted}},
		{Seq: 3, Type: trace.TypeValidate, Timestamp: "2026-09-07T12:00:02Z", ElapsedMS: 2000,
			Validate: &trace.ValidateRecord{
				Tree: trace.ValidateTreeMutant, Op: trace.ValidateOpReject,
				Path: "clamp.go", MutantID: rejectedID,
				Diagnostic: "./clamp.go:43:12: invalid operation",
			}},
		{Seq: 4, Type: trace.TypeStage, Timestamp: "2026-09-07T12:00:09Z", ElapsedMS: 9000,
			Stage: &trace.StageRecord{Phase: trace.PhaseMutate, Name: "validate",
				State: trace.StateFinished, Result: trace.ResultSucceeded, DurationMS: ms(8000)}},
		{Seq: 5, Type: trace.TypeRunEnd, Timestamp: "2026-09-07T12:00:10Z", ElapsedMS: 10000,
			Run: &trace.RunRecord{Verdict: "completed", ExitCode: 0, EventsEmitted: 4}},
	}
	recordEvents(t, workspace, events, nil)

	out := explained(t, rejectedID[:8])
	if !strings.Contains(out, "mutate/validate") {
		t.Errorf("a rejection has no timeline for the search that refused it:\n%s", out)
	}
	if !strings.Contains(out, "8s") {
		t.Errorf("the validation step carries no duration:\n%s", out)
	}
	for _, absent := range []string{"\ncoverage\n", "\nexecutions\n"} {
		if strings.Contains(out, absent) {
			t.Errorf("a rejection was given a %q block it cannot fill:\n%s", strings.TrimSpace(absent), out)
		}
	}
}

func TestExplainResolvesARunPrefix(t *testing.T) {
	first := explainReport()
	second := explainReport()
	second.RunID = "20260907T120000Z-a1c3"

	testsupport.CacheDir(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module "+first.Workspace.ModulePath+"\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	t.Chdir(dir)
	documents := map[string]string{}
	for _, r := range []*report.Report{first, second} {
		data, err := r.Marshal()
		if err != nil {
			t.Fatalf("encoding the report: %v", err)
		}
		documents[r.RunID+".json"] = string(data)
	}
	seedWorkspace(t, filepath.Join(testsupport.CacheDir(t), report.DirName), historyDigest, documents)

	t.Run("unique", func(t *testing.T) {
		code, stdout, stderr := execute(t, "explain", "--no-color", "--run", "20260907T120000Z-a1b", killedID[:8])
		if code != 0 {
			t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "run "+explainRunID) {
			t.Errorf("the prefix did not resolve to the run it names:\n%s", stdout)
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		code, stdout, stderr := execute(t, "explain", "--no-color", "--run", "20260907T120000Z-a1", killedID[:8])
		if code != 2 {
			t.Errorf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		for _, want := range []string{explainRunID, "20260907T120000Z-a1c3"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("the ambiguity does not list %q:\n%s", want, stdout)
			}
		}
		if !strings.Contains(stderr, string(CodeNoStoredRun)) {
			t.Errorf("the refusal carries no %s:\n%s", CodeNoStoredRun, stderr)
		}
	})
}

func TestExplainSaysWhichBudgetSettledAMemoryKill(t *testing.T) {
	doc := explainReport()
	rows := doc.Mutants[0].Executions
	rows[1].MemoryExceeded = true
	rows[1].PeakMemoryBytes = 3435973836
	doc.Mutants[0].MemoryExceeded = true
	doc.Mutants[0].PeakMemoryBytes = 3435973836
	inExplainWorkspace(t, doc)

	out := explained(t, killedID[:8])
	for _, want := range []string{
		"killed by example.com/killable (memory: 3.2 GiB > 1.0 GiB) after 2 attempts",
		"attempt 2  worker 0  killed",
		"peak 3.2 GiB",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the account of a memory kill does not carry %q:\n%s", want, out)
		}
	}
}

func TestExplainShowsWhatEachPassCostWithoutABoundInSight(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	out := explained(t, survivorID[:8])
	if !strings.Contains(out, "peak 200.0 MiB") {
		t.Errorf("the account of a survivor does not say what its pass cost:\n%s", out)
	}
	if strings.Contains(out, "memory:") {
		t.Errorf("a pass no bound stopped mentions the bound:\n%s", out)
	}
}

func TestExplainSaysWhichBudgetSettledACachedMemoryKill(t *testing.T) {
	doc := explainReport()
	var cached *report.Mutant
	for i := range doc.Mutants {
		if doc.Mutants[i].Cached {
			cached = &doc.Mutants[i]
			break
		}
	}
	if cached == nil {
		t.Fatal("the explain fixture has no cached mutant")
	}
	if len(cached.Executions) != 0 {
		t.Fatalf("the cached mutant carries %d rows, so this proves nothing", len(cached.Executions))
	}
	cached.Outcome = report.OutcomeKilled
	cached.MemoryExceeded = true
	cached.PeakMemoryBytes = 3435973836
	inExplainWorkspace(t, doc)

	out := explained(t, cached.ID[:8])
	if !strings.Contains(out, "(memory: 3.2 GiB > 1.0 GiB)") {
		t.Errorf("the account of a cached memory kill does not say which budget settled it:\n%s", out)
	}
	if !strings.Contains(out, "reused from the outcome cache") {
		t.Errorf("the account does not say the outcome was adopted:\n%s", out)
	}
}
