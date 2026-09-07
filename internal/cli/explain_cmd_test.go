// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The half of `explain` that needs no toolchain: joining a run report and a
// recording into the account of one mutant.
//
// The documents here are built rather than measured, which is the point. What
// is under test is the join — which facts the command reads out of which
// document, and what it says when the second one is not there — and a real run
// would prove the same thing far more slowly while making the assertions depend
// on a fixture's line numbers.
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

// The identities the documents below are built from. They are full 64-character
// identities because that is what a report carries and what activation takes:
// the display id is a prefix of it, so one literal is both.
var (
	killedID    = strings.Repeat("1f", 32)
	survivorID  = strings.Repeat("2a", 32)
	uncoveredID = strings.Repeat("3b", 32)
	rejectedID  = strings.Repeat("4c", 32)
	cachedID    = strings.Repeat("5d", 32)
	firstTwinID = "abcd" + strings.Repeat("11", 30)
	otherTwinID = "abcd" + strings.Repeat("22", 30)
)

// explainRunID is the run every document here describes.
const explainRunID = "20260907T120000Z-a1b2"

// displayOf is the display id a report carries for a full identity: its first
// twenty characters, exactly as internal/mutation mints it.
func displayOf(id string) string { return id[:20] }

// explainReport is the run report these tests explain: one killed mutant, one
// survivor, one mutant nothing covers, one adopted from the cache, two mutants
// sharing a prefix, and one the compiler refused.
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

// inExplainWorkspace puts the working directory in a temporary one holding the
// report, and returns that directory.
//
// The chdir is what makes the trace lookup a real one: `explain` resolves
// `report.directory` from the workspace it is run in, exactly as the `trace`
// commands do, so a test that stayed in the package directory would be reading
// this repository's own recordings.
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

// explain runs the command in process against the report in the working
// directory.
func explain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = ExecuteContext(t.Context(),
		append([]string{"explain", "--no-color", "--report", "mutation.json"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// explained runs the command and fails the test unless it exited 0.
func explained(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := explain(t, args...)
	if code != 0 {
		t.Fatalf("`go-mutants explain %s` exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// recordEvents writes a recording into `<report.directory>/trace/<run id>/`,
// which is where a traced run would have filed it, and returns the directory.
//
// The events are written by hand rather than recorded, so that a test can put
// exactly the sequence it is about into the stream. They still go through the
// reader that every other consumer uses, so a stream this helper writes and
// the reader disagreed about would fail here rather than in production.
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

// ms is a duration in the unit the recording carries them in.
func ms(v int64) *int64 { return &v }

// killedRecording is the recording of the killed mutant's two attempts: a
// timeout on worker 3 and the serial retry that caught it.
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

// TestExplainResolvesAPrefixAgainstMutantsAndRejected is the first thing the
// command has to do: find the one mutant a prefix names, wherever the document
// keeps it.
//
// A rejected mutant is in `rejected[]` and not in `mutants[]`, and it is
// exactly the one somebody types a prefix of after reading `run --explain`. A
// resolver that looked only at the measured mutants would answer "no such
// mutant" for a mutant the same document names three lines further down.
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

// TestExplainSaysAmbiguousAndListsMatches is the other half of resolution: a
// prefix that names two mutants is not a question this command can answer, and
// the remedy is in the list of what it matched rather than in a longer prefix
// the user would have to guess at.
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

// TestExplainUnknownPrefixIsRefused is the third answer: a well-formed prefix
// that matches nothing.
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

// TestExplainKilledShowsKilledByAttemptsAndTail is the question the command
// exists for, asked of a mutant the tests caught.
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

// wantSurvivorAccount is every byte `explain` writes about the survivor when
// there is no recording to read.
//
// It is a literal rather than a golden file for the reason
// [wantSkipDetail] is one: internal/cli is not in mise.toml's golden-update
// list, and a literal is the same pin — an exact comparison of the whole
// output — read in the file that asserts it.
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

// TestExplainSurvivorShowsCoveringBinariesAndReproduction pins the whole
// output, because the shape is what is under test as much as the facts: six
// titled blocks in one order, so that two accounts of two mutants can be
// diffed against each other.
func TestExplainSurvivorShowsCoveringBinariesAndReproduction(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	if out := explained(t, survivorID[:8]); out != wantSurvivorAccount {
		t.Errorf("the survivor's account is not the expected one\n--- got ---\n%s\n--- want ---\n%s",
			out, wantSurvivorAccount)
	}
}

// TestExplainUncoveredSaysWhichLineNoBinaryReaches is the difference between
// the two survivors, and it is the difference between two pieces of work:
// sharpen a test you have, or write one.
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

// TestExplainRejectedQuotesTheDiagnostic is the mutant that never ran: the
// compiler's own words are the whole of what there is to say, and they are the
// part that says whether the rejection is a limit of the guard forms or a
// mutant that could never have meant anything.
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

// TestExplainCachedSaysThisRunDidNotMeasureIt is the third mutant with no
// executions under it, and the third reason.
//
// A cached mutant carries the duration, the attempts and the killer of the run
// that did measure it, so an account that printed those under an `executions`
// heading with nothing in it would read as a measurement this run made.
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

// TestExplainWithoutATraceSaysSoInsteadOfGuessing is the promise the whole
// command rests on: everything it prints came out of a document, and the
// sections whose document is missing say so rather than inventing a plausible
// command.
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

// TestExplainWithATraceListsEachBinaryRunAndItsOutputFile is what the
// recording adds: the commands underneath the passes, and the file their
// output was preserved in.
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

// TestExplainTimelineNamesTheStagesTheMutantTookPartIn is the "why was this
// slow" half: a mutant's passes happened inside stages, and the stages are
// where a run's minutes went.
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

// TestExplainPositionListsSkipSitesAndMutantsOnThatLine is the other kind of
// target: a place in the source rather than an identity.
//
// It is asked of the renderer rather than of the command, because the command
// has to run a discovery pass to answer it and a discovery pass needs a
// toolchain; the integration test drives the whole sentence. What is pinned
// here is that both halves are printed — the candidates that became mutants and
// the ones discovery passed over — since a listing that showed only the first
// would read as "there is nothing else here".
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
	if err := explainPosition(&out, false, position{path: "clamp.go", line: 41},
		sites, mutants, outcomesOf(r)); err != nil {
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
	// The other line's mutant and the other file's skip site are both out of
	// scope: a position names a line, and a listing that widened to the file
	// would answer a question nobody asked.
	if strings.Contains(text, "clamp.go:47") {
		t.Errorf("a mutant on another line was listed:\n%s", text)
	}
	if strings.Contains(text, "ready.go") {
		t.Errorf("a skip site in another file was listed:\n%s", text)
	}
}

// TestExplainRefusesJSON keeps the command honest about what it is. Every fact
// it prints is already in the two documents it read, so a `--json` that
// re-encoded them would be a third spelling of the same facts for nobody.
func TestExplainRefusesJSON(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	code, stdout, stderr := explain(t, "--json", killedID[:8])
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(CodeConflictingFlags)) {
		t.Errorf("the refusal carries no %s:\n%s", CodeConflictingFlags, stderr)
	}
	if !strings.Contains(stderr, "v2") {
		t.Errorf("the refusal does not say when a document might exist:\n%s", stderr)
	}
}

// TestExplainHelpNamesEverySource is the contract the help page is: a reader
// has to be able to find out which document is being read without running the
// command against the wrong one.
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

// TestExplainIsInTheRootHelp keeps the command discoverable: a command nobody
// is told about is a command nobody uses.
func TestExplainIsInTheRootHelp(t *testing.T) {
	_, stdout, _ := execute(t, "--help")
	if !strings.Contains(stdout, "explain") {
		t.Errorf("the root help does not list `explain`:\n%s", stdout)
	}
}

// TestExplainDistinguishesNoRecordingFromNoProcess is the difference between
// two absences that look alike in the reproduce block.
//
// A run that recorded nothing may still have executed the mutant, and running
// it again with `--trace` produces the command. A run that recorded everything
// and holds no command for this mutant never started a process for it, and no
// amount of re-running will make one appear — so telling a user to add `--trace`
// as though that were the problem would send them round a loop.
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

// inExplainHistory files the report as a stored run of this module and puts the
// working directory in a module of that name, so that the two sources nobody
// names on the command line — the latest run, and one named by its id — are
// resolved out of a real store rather than out of a path.
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

// TestExplainReadsTheLatestRunAndOneNamedById covers the two sources a user
// reaches for without naming a path: the run they have just made, and one
// further back in the same module's history.
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

// TestExplainRefusesARunItHasNoRecordOf is the other half: a run id that is not
// in this module's history is a mistake in the command line, not an empty
// account.
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

// TestExplainRefusesReportWithRun keeps the two sources from being named at
// once: each is the whole of the document to explain, so a command line
// carrying both has no reading that is not a guess about which was meant.
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

// TestExplainSaysWhetherTheTemporariesSurvived is the difference between a
// command somebody can paste and one whose first word fails.
//
// The binary and the directory the reproduction names live in the run's own
// scratch, which a run removes on the way out unless it was asked to keep it —
// and the recording says which happened, as an `artifact` of kind
// `kept-scratch`. A hedge that covered both cases was a hedge the reader had to
// resolve by running the command and watching `cd` fail.
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
		// The same recording with the kept-scratch artifact taken out, which is
		// exactly what a run without --keep-temp records.
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

// foreignRecording is [killedRecording] filed under a different run.
//
// A run id is derived from a stamp and four hex characters, so two runs really
// can collide — and `--trace DIR` points at whatever directory somebody was
// sent. Joining the two silently would attribute one run's commands to
// another's report.
func foreignRecording() []trace.Event {
	events := killedRecording()
	events[0].Start.RunID = "20260101T000000Z-9999"
	return events
}

// TestExplainWarnsWhenTheRecordingIsOfAnotherRun keeps the join honest about
// what it joined.
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

// TestExplainDoesNotBlameTheMutantForAForeignRecording is the other half of the
// same warning: "this run started no process for it" is a statement about the
// run, and a recording of somebody else's run cannot support it.
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

// TestExplainPositionShowsAWholeFileSkip is the site with no coordinate.
//
// A generated, cgo or excluded file was never opened, so its suppression
// carries line 0 — which is not a line anybody can ask about, and a filter that
// compared it to the line the user typed hid the one answer that file has.
func TestExplainPositionShowsAWholeFileSkip(t *testing.T) {
	var out bytes.Buffer
	sites := []discover.SkipSite{{Path: "gen.go", Reason: discover.SkipGenerated}}
	if err := explainPosition(&out, false, position{path: "gen.go", line: 6}, sites, nil, nil); err != nil {
		t.Fatalf("explainPosition: %v", err)
	}
	if !strings.Contains(out.String(), "generated") {
		t.Errorf("the whole-file skip is not listed for a line of that file:\n%s", out.String())
	}
}

// TestExplainPositionMatchesAMutantThatSpansLines is `--changed`'s rule applied
// here: a mutant covers the interval its original bytes touch, not the line it
// starts on.
//
// A multi-line condition mutated at its first line is a mutant *on* every line
// of it, which is exactly what somebody asking about the third line wants to
// know.
func TestExplainPositionMatchesAMutantThatSpansLines(t *testing.T) {
	var out bytes.Buffer
	mutants := []catalogMutant{{
		ID: killedID, DisplayID: displayOf(killedID), Path: "clamp.go", Line: 41, Column: 7,
		Family: "comparison", Rule: "lt-to-le",
		Original: "v < hi &&\n\t\tv > lo", Replacement: "v <= hi &&\n\t\tv > lo",
	}}
	if err := explainPosition(&out, false, position{path: "clamp.go", line: 42}, nil, mutants, nil); err != nil {
		t.Fatalf("explainPosition: %v", err)
	}
	if !strings.Contains(out.String(), displayOf(killedID)) {
		t.Errorf("a mutant whose span reaches line 42 is not listed for it:\n%s", out.String())
	}
}

// TestParsePositionAcceptsTheSpellingExplainPrints closes the loop between the
// two halves of the command: the identity block prints `path:line:col`, and
// pasting that back has to name the same place rather than a malformed prefix.
//
// A Windows path survives, which is the reason the scan is right to left and
// bounded: `C:\src\clamp.go` ends in no digits, so the drive letter's colon is
// never mistaken for a coordinate's.
func TestParsePositionAcceptsTheSpellingExplainPrints(t *testing.T) {
	// A Windows path with a line on the end. The expectation is written through
	// filepath.ToSlash rather than spelled out, because a backslash is a
	// separator on one platform and an ordinary byte in a file name on the
	// other — and what is under test here is that the drive letter's colon
	// survives, which is true on both.
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

// TestExplainRefusesAPositionThatNamesNoFile is the answer an empty listing was
// giving wrongly.
//
// A path with a typo in it produced two empty sections and exit 0, which reads
// as "there is nothing here" — the one answer that is never true of a file that
// does not exist. It is also checked before the discovery pass, so a mistyped
// path costs a message rather than a workspace copy.
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

// TestExplainAPositionNeedsNoStoredRun is the one target that is a question
// about the workspace rather than about a run.
//
// Refusing it for want of a report would refuse the command in the one
// situation somebody most wants it — a fresh checkout, before any run — and the
// outcome column has an honest answer for it already.
//
// The proof is the order: with no run recorded at all, the failure reported is
// the path's, which is only reachable past the report lookup.
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

// libraryRecording is [killedRecording] as a library session records it: kind
// `workspace`, with the overlay manifest its instrumented tree was compiled
// through.
func libraryRecording() []trace.Event {
	events := killedRecording()
	events[0].Start.Kind = trace.StartKindWorkspace
	events[0].Start.RunID = ""
	// Spliced in ahead of the run-end, which is renumbered: a sequence number
	// is the position in the stream, and the reader refuses a line that does
	// not follow the one before it.
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

// TestExplainPutsTheOverlayRebuildOnItsOwnLine keeps the run line runnable.
//
// GOFLAGS is read by the `go` command and by nothing else: a prebuilt test
// binary ignores it entirely, so an overlay on the run line was a variable that
// did nothing, in front of the one command in this tool that has to be exactly
// right. The manifest belongs to the *rebuild*, which is what
// docs/library.md's recipe says.
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

// TestExplainTimelineNamesTheMutantsOwnShare is what makes the section worth
// printing per mutant.
//
// Every mutant of a run takes part in the same `mutate/execute` stage, so the
// stage's own duration is the same figure on every account and says nothing
// about the mutant beside it. The share is the part that distinguishes the
// mutant that took twenty seconds from the four hundred that took three
// milliseconds each.
func TestExplainTimelineNamesTheMutantsOwnShare(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), nil)

	out := explained(t, killedID[:8])
	if !strings.Contains(out, "(this mutant: 20.52s)") {
		t.Errorf("the timeline does not say how much of the stage was this mutant's:\n%s", out)
	}
}

// TestExplainTimelineSaysAStageIsStillOpen covers the recording that stops in
// the middle of a step: a ring that wrapped, a bundle written from a run that
// died, a Ctrl-C.
//
// That step is the one a reader most needs named — it is where the run was when
// it stopped — and a stage carried only from its closing event would drop it
// silently.
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

// TestExplainARejectionShowsTheValidationThatRefusedIt is the timeline of a
// mutant that was never executed.
//
// It has no passes, so the question "why was this slow" is answered somewhere
// else entirely: by the bisection that established which candidates compile,
// which is where a slow run's minutes often went and which the recording
// records against the candidate it refused.
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
	// It still has no coverage and no executions: nothing measured a mutant
	// that does not compile, and a heading with nothing under it is noise.
	for _, absent := range []string{"\ncoverage\n", "\nexecutions\n"} {
		if strings.Contains(out, absent) {
			t.Errorf("a rejection was given a %q block it cannot fill:\n%s", strings.TrimSpace(absent), out)
		}
	}
}

// TestExplainResolvesARunPrefix is `--run` read the way the target is read.
//
// A run id is a stamp and four hex characters. Nobody retypes one, everybody
// selects one out of `report list` or out of a CI log, and a selection that
// clipped the last character should not be a different question — so a prefix
// that names one run is that run, and one that names two is refused with both
// listed, exactly as an ambiguous mutant prefix is.
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

// TestExplainSaysWhichBudgetSettledAMemoryKill is the account of the one
// outcome that reads wrong without it.
//
// A mutant the memory bound stopped is reported as `killed` and names the suite
// that was running — and that suite's tests all pass. A reader who went and
// looked would find nothing, which is exactly the state `explain` exists to
// resolve, so the verdict says which budget settled it and against what. The
// per-attempt lines carry the peak whether or not a bound was involved, because
// "which of my mutants cost the machine most" is a question about a run in
// which nothing went wrong.
func TestExplainSaysWhichBudgetSettledAMemoryKill(t *testing.T) {
	doc := explainReport()
	// The second pass, which is the one that settled it, is turned into a
	// memory kill: the outcome does not change, and everything about how it
	// reads does. The mutant's own fields carry the same facts — a document
	// records them at both levels so that a cached mutant, which has no rows at
	// all, reads the same way; see [TestExplainSaysWhichBudgetSettledACachedMemoryKill].
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

// TestExplainShowsWhatEachPassCostWithoutABoundInSight is the other half: the
// peak is an ordinary fact about an ordinary pass, not a footnote to a kill.
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

// TestExplainSaysWhichBudgetSettledACachedMemoryKill is the case the mutant's
// own memory fields exist for.
//
// A cached mutant carries an attempt count and no execution rows, because this
// run started no process for it. An account that read the rows would therefore
// say "killed by <pkg>" and stop — the same sentence a passing suite gets, on a
// run where nothing can be looked at — which is precisely the state `explain`
// exists to resolve. The facts are on the mutant as well as on the rows, and
// this is the reader that needs them there.
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
