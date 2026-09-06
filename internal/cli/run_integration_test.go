// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of `run`: a real repository, a real module, and the
// documents the command actually writes.
//
// It lives here rather than in internal/engine because what is under test is
// the whole sentence a user types. The engine can be handed any ref a test
// likes; only the command line can produce the one the bare flag carries, and
// the two `--changed` failures this file pins were both invisible from
// underneath — a report that documented the lookup instead of the comparison,
// and a selection that could not see a file git had never been told about. The
// same is true of `--trace`: the engine can be handed a sink, and only the
// command line decides where that sink writes, whether the directory it wrote
// into survives the run, and whether writing it changed the tree.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testsupport"
	"github.com/P4suta/go-mutants/trace"
)

// The work this run is meant to notice: a file that has never been added, and
// the test beside it. A whole new file rather than an edit, because that is the
// case `git diff` cannot report at all — it has no index entry to be compared
// against — so every mutant in it is either selected by the untracked scan or
// by nothing.
const (
	freshFile = "fresh.go"
	freshTest = "fresh_test.go"

	freshSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

// Fresh is uncommitted, unstaged work: written, saved, and never handed to git.
func Fresh(a, b int) int {
	if a > b {
		return a - b
	}
	return a + b
}
`

	freshTestSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

import "testing"

func TestFresh(t *testing.T) {
	if got := Fresh(7, 2); got != 5 {
		t.Errorf("Fresh(7, 2) = %d, want 5", got)
	}
	if got := Fresh(2, 7); got != 9 {
		t.Errorf("Fresh(2, 7) = %d, want 9", got)
	}
}
`
)

// branchedModule copies the killable fixture into a temporary directory, commits
// it, and cuts a branch that tracks the trunk — which is what makes a bare
// `--changed` a question with an answer. It returns the workspace root and the
// upstream branch's own name.
//
// The environment is redirected first so that nothing here — the snapshot, the
// report, the history store — reaches the developer's own directories.
func branchedModule(t *testing.T) (root, upstream string) {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "killable"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}

	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	testsupport.CacheDir(t)
	neutralGitEnvironment(t)

	root = filepath.Join(t.TempDir(), "killable")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatalf("copying the killable fixture: %v", err)
	}

	gitCommand(t, root, "init", "--quiet")
	gitCommand(t, root, "add", "--all")
	gitCommand(t, root, "commit", "--quiet", "--message", "the fixture as it was")
	upstream = gitCommand(t, root, "rev-parse", "--abbrev-ref", "HEAD")
	gitCommand(t, root, "checkout", "--quiet", "-b", "feature")
	gitCommand(t, root, "branch", "--quiet", "--set-upstream-to="+upstream, "feature")
	return root, upstream
}

// TestBareChangedNamesTheUpstreamAndMeasuresUntrackedWork is both halves of what
// a bare `--changed` promises, read off the document the command wrote.
//
// The two used to fail together and for related reasons — the feature stopping
// one step short of the tree it claims to read. `changed_ref` recorded
// `@{upstream}`, which documents a lookup rather than a comparison and makes
// two shards that diffed different upstreams look congruent; and the selection
// was taken from `git diff` alone, so an afternoon's work in a file that had
// never been `git add`ed came back as `0 of N mutants selected`, `score N/A`,
// and exit 0 — the green that proves nothing, which every other part of this
// feature fails closed to avoid.
func TestBareChangedNamesTheUpstreamAndMeasuresUntrackedWork(t *testing.T) {
	root, upstream := branchedModule(t)
	// Written into the working tree and left there: never added, never staged,
	// never committed.
	for name, source := range map[string]string{freshFile: freshSource, freshTest: freshTestSource} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	t.Chdir(root)

	code, stdout, stderr := execute(t, "run", "--changed", "--json", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --changed --json` exited %d\nstderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, gitdiff.UpstreamRef) {
		t.Errorf("the document records the notation the base was looked up with rather than the branch it found:\n%s", stdout)
	}

	var rep report.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("the run did not write a document: %v\n%s", err, stdout)
	}
	if rep.Selection.Mode != report.ModeChanged {
		t.Errorf("selection.mode = %q, want %q", rep.Selection.Mode, report.ModeChanged)
	}
	if rep.Selection.ChangedRef == nil || *rep.Selection.ChangedRef != upstream {
		t.Errorf("selection.changed_ref = %v, want the upstream branch's own name (%q)",
			rep.Selection.ChangedRef, upstream)
	}

	var measured, elsewhere int
	for _, m := range rep.Mutants {
		if m.Path != freshFile {
			elsewhere++
			if m.Outcome != report.OutcomeNotRun {
				t.Errorf("mutant %s at %s:%d was measured and is not on new work",
					m.DisplayID, m.Path, m.Line)
			}
			continue
		}
		if m.Outcome == report.OutcomeNotRun {
			t.Errorf("mutant %s is in a file git has never seen and was not run: %v",
				m.DisplayID, m.NotRunReason)
			continue
		}
		measured++
	}
	if measured == 0 {
		t.Fatal("nothing in the untracked file was measured, so --changed cannot see work that was never added")
	}
	if elsewhere == 0 {
		t.Fatal("the catalogue holds nothing outside the untracked file, so nothing was narrowed away")
	}
	if rep.Selection.Selected != measured {
		t.Errorf("selection.selected = %d and %d mutants were measured", rep.Selection.Selected, measured)
	}
	// The tests beside the new file catch some of what it carries, so the run is
	// a measurement rather than only a selection.
	if rep.Summary.Killed == 0 {
		t.Error("nothing in the untracked file was killed, so the run proved nothing about it")
	}
}

// inKillableFixture copies the killable fixture into a directory of the test's
// own and makes it the working directory, with the temporary parent and the
// cache root redirected so that nothing here reaches the developer's own.
//
// A copy, never the corpus module: a run writes `reports/mutation/` into the
// directory it is started in, and a recording is one more thing it would leave
// in a checked-in fixture.
func inKillableFixture(t *testing.T) string {
	t.Helper()
	root := testkit.Copy(t, "killable")
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	testsupport.CacheDir(t)
	t.Chdir(root)
	return root
}

// runReport drives one run and decodes the document it wrote to standard
// output. --json is what puts the document there; the console lines and the
// warnings go to standard error beside it.
func runReport(t *testing.T, args ...string) (*report.Report, string) {
	t.Helper()
	code, stdout, stderr := execute(t, append([]string{"run", "--json", "--no-color", "--no-tui"}, args...)...)
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run %s` exited %d\nstderr:\n%s", strings.Join(args, " "), code, stderr)
	}
	var rep report.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("the run did not write a document: %v\n%s", err, stdout)
	}
	return &rep, stderr
}

// traceDirectoryOf is where a run of this workspace filed the recording of the
// run the document describes.
func traceDirectoryOf(root string, rep *report.Report) string {
	return filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory), "trace", rep.RunID)
}

// TestRunTraceWritesAValidStreamNamedByTheRunIdUnderTheReportDirectory is the
// whole of what `--trace` promises, checked against the run it recorded.
//
// It is one test rather than five because the claim is a single one: the file
// on disk is the account of the run whose document is on standard output. The
// interesting failures are the joins — a recording nobody can pair with a
// report, an event naming an output file that is not there, a stream that
// stops without saying it stopped — and each of those is invisible from either
// side alone.
func TestRunTraceWritesAValidStreamNamedByTheRunIdUnderTheReportDirectory(t *testing.T) {
	root := inKillableFixture(t)
	rep, stderr := runReport(t, "--trace")

	directory := traceDirectoryOf(root, rep)
	if !strings.Contains(stderr, "trace: "+directory) {
		t.Errorf("the run did not say where it recorded:\n%s", stderr)
	}
	stream := filepath.Join(directory, trace.FileName)
	events, err := trace.Read(stream)
	if err != nil {
		t.Fatalf("the recording does not read back: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("the run recorded nothing")
	}

	// Every line of it is a document of the published contract, which is what
	// keeps a field added in a hurry from producing a recording no consumer can
	// read.
	raw, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if err := schemas.Validate(schemas.TraceEventV1, []byte(line)); err != nil {
			t.Errorf("line %d does not satisfy %s: %v\n%s", i+1, schemas.TraceEventV1, err, line)
		}
	}

	// The recording and the report are one run, said twice. Without this the
	// two documents a failed run leaves behind cannot be put beside each other.
	first := events[0]
	if first.Type != trace.TypeRunStart || first.Start == nil {
		t.Fatalf("the recording opens with a %s, want a run-start", first.Type)
	}
	if first.Start.RunID != rep.RunID {
		t.Errorf("run-start.run_id = %q, want the report's %q", first.Start.RunID, rep.RunID)
	}
	if first.Start.Kind != trace.StartKindRun {
		t.Errorf("run-start.kind = %q, want %q", first.Start.Kind, trace.StartKindRun)
	}

	// The last line is the run-end, which is what tells a reader they have read
	// the whole run rather than a prefix of one.
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		t.Fatalf("the recording ends with a %s, want a run-end", last.Type)
	}
	if last.Run.EventsDropped != 0 {
		t.Errorf("the recording dropped %d events", last.Run.EventsDropped)
	}

	// A mutant execution's output is preserved beside the stream rather than
	// serialised into it, and the digest in the event is of the whole capture.
	// Checking the file against it is what makes the two halves one record.
	var preserved int
	for _, event := range events {
		if event.Type != trace.TypeExec || event.Exec.Kind != trace.ExecKindMutantRun {
			continue
		}
		if event.Exec.OutputPath == "" {
			continue
		}
		preserved++
		output, readErr := os.ReadFile(filepath.Join(directory, filepath.FromSlash(event.Exec.OutputPath)))
		if readErr != nil {
			t.Errorf("event %d names %s, which is not there: %v", event.Seq, event.Exec.OutputPath, readErr)
			continue
		}
		if !event.Exec.OutputTruncated {
			digest := sha256.Sum256(output)
			if got := hex.EncodeToString(digest[:]); got != event.Exec.OutputSHA256 {
				t.Errorf("event %d preserved output digesting %s, and the event says %s",
					event.Seq, got, event.Exec.OutputSHA256)
			}
		}
	}
	if preserved == 0 {
		t.Error("no mutant execution preserved its output, so the killing binary's words are nowhere")
	}
}

// TestRunTraceLandsUnderTheReportDirectoryEvenWithNoReportFormats keeps the two
// meanings of `report.directory` apart.
//
// `--report none` turns off the two documents a run publishes into a workspace;
// it does not move where a recording goes. The directory is where a trace may
// live because it is the one place internal/snapshot already excludes from the
// tree it digests — a fact about the workspace, not about which formats were
// asked for.
func TestRunTraceLandsUnderTheReportDirectoryEvenWithNoReportFormats(t *testing.T) {
	root := inKillableFixture(t)
	rep, _ := runReport(t, "--trace", "--report", "none")

	directory := traceDirectoryOf(root, rep)
	if _, err := os.Stat(filepath.Join(directory, trace.FileName)); err != nil {
		t.Fatalf("the recording is not under the report directory: %v", err)
	}
	// And the project documents really were suppressed, so this is a run with a
	// recording and no report rather than one that ignored the flag.
	reports := filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory))
	for _, name := range []string{"mutation.json", "mutation.html"} {
		if _, err := os.Stat(filepath.Join(reports, name)); err == nil {
			t.Errorf("--report none still published %s", name)
		}
	}
}

// TestAnUntracedRunLeavesNoTraceDirectory is goat cleanliness: a run that was
// not asked for a recording writes no file for one.
//
// The ring an untraced run records into is the whole point — the failure nobody
// expected is the one nobody passed --trace for — and it is only affordable
// because it costs a bounded amount of memory and not a directory that grows on
// every run of every project on the machine.
func TestAnUntracedRunLeavesNoTraceDirectory(t *testing.T) {
	root := inKillableFixture(t)
	_, stderr := runReport(t)

	traceRoot := filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory), "trace")
	if _, err := os.Stat(traceRoot); !os.IsNotExist(err) {
		t.Errorf("an untraced run left %s behind (%v)", traceRoot, err)
	}
	if strings.Contains(stderr, "trace: ") {
		t.Errorf("an untraced run said where it recorded:\n%s", stderr)
	}
}

// TestRunTraceDoesNotChangeTheSnapshotDigestOrDrift is the invariant the
// refusal rule exists to protect, checked the only way it can be: by tracing
// into the workspace and seeing that the workspace did not move.
//
// A recording grows while the run measures. If it were written anywhere the
// snapshot reads, the run would digest a tree that changed under it and report
// drift it caused itself — a diagnostic that fails the run it is a diagnostic
// of. Under `report.directory`, which the snapshot excludes, the traced run and
// the untraced one describe the same workspace.
func TestRunTraceDoesNotChangeTheSnapshotDigestOrDrift(t *testing.T) {
	inKillableFixture(t)

	untraced, _ := runReport(t)
	traced, _ := runReport(t, "--trace")
	again, _ := runReport(t, "--trace")

	if traced.Workspace.WorkspaceDigest != untraced.Workspace.WorkspaceDigest {
		t.Errorf("workspace_digest = %q with a recording and %q without one",
			traced.Workspace.WorkspaceDigest, untraced.Workspace.WorkspaceDigest)
	}
	// And a second traced run, which is the one that finds a recording already
	// sitting in the tree rather than putting the first one there.
	if again.Workspace.WorkspaceDigest != untraced.Workspace.WorkspaceDigest {
		t.Errorf("workspace_digest = %q with a recording already on disk and %q without one",
			again.Workspace.WorkspaceDigest, untraced.Workspace.WorkspaceDigest)
	}
	if untraced.Summary.Killed == 0 || traced.Summary.Killed != untraced.Summary.Killed {
		t.Errorf("the traced run killed %d mutants and the untraced one killed %d",
			traced.Summary.Killed, untraced.Summary.Killed)
	}
}

// refusingWrites is a stream that refuses its first writes and then behaves.
//
// It is how "a disk that will not take a write costs the events and not the
// run" is produced, and it refuses only the first few rather than all of them
// so that the recording still reaches its run-end — which is the event that has
// to say how much of itself is missing. A sink that lost everything could not
// tell anybody that it had.
type refusingWrites struct {
	trace.File
	remaining int
}

func (f *refusingWrites) Write(data []byte) (int, error) {
	if f.remaining > 0 {
		f.remaining--
		return 0, errors.New("no space left on device")
	}
	return f.File.Write(data)
}

// refuseTraceWrites points the recording at a disk that refuses its first n
// writes, for the length of one test.
func refuseTraceWrites(t *testing.T, n int) {
	t.Helper()
	t.Cleanup(func() { traceFilesystem = trace.Filesystem{} })
	traceFilesystem = trace.Filesystem{
		OpenAppend: func(name string, perm fs.FileMode) (trace.File, error) {
			file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perm)
			if err != nil {
				return nil, err
			}
			return &refusingWrites{File: file, remaining: n}, nil
		},
	}
}

// TestATraceThatCannotBeWrittenCostsTheEventsAndNotTheRun is the invariant the
// whole feature lives or dies by, checked against a disk that will not take a
// write.
//
// A diagnostic that can fail the run it is a diagnostic of inverts the point of
// having one. So a sink that refuses events costs exactly those events: the same
// mutants, the same verdicts, the same exit status — and a recording that says
// in its own accounting how much of itself is missing, because a lossy recording
// that looked complete would be worse than no recording at all.
func TestATraceThatCannotBeWrittenCostsTheEventsAndNotTheRun(t *testing.T) {
	root := inKillableFixture(t)

	// The cache is off on both runs so that the second measures what the first
	// measured rather than reusing it, which is what makes the comparison a
	// comparison of two runs.
	clean, _ := runReport(t, "--cache", "off")

	const refused = 5
	refuseTraceWrites(t, refused)
	traced, stderr := runReport(t, "--cache", "off", "--trace")

	if traced.Summary.Killed != clean.Summary.Killed ||
		traced.Summary.Survived != clean.Summary.Survived ||
		traced.Summary.Errored != clean.Summary.Errored {
		t.Errorf("the run with a failing recording measured killed %d/survived %d/errored %d, "+
			"and the one without a recording measured killed %d/survived %d/errored %d",
			traced.Summary.Killed, traced.Summary.Survived, traced.Summary.Errored,
			clean.Summary.Killed, clean.Summary.Survived, clean.Summary.Errored)
	}
	if !strings.Contains(stderr, "trace: ") {
		t.Errorf("the run did not say where it recorded:\n%s", stderr)
	}

	// And the recording is honest about what it lost. The first events are gone
	// — a reader sees the run-start missing and a gap before the first sequence
	// number it has — and the run-end counts them.
	summary, err := trace.ReadSummary(traceDirectoryOf(root, traced))
	if err != nil {
		t.Fatalf("the recording does not read back: %v", err)
	}
	if !summary.HasRunEnd {
		t.Fatal("the recording has no run-end, so it cannot report what it lost")
	}
	if summary.EventsDropped != refused {
		t.Errorf("run-end.events_dropped = %d, want the %d writes the disk refused", summary.EventsDropped, refused)
	}
	if summary.MissingSequences != refused {
		t.Errorf("the recording reports %d missing sequence numbers, want %d", summary.MissingSequences, refused)
	}
}

// TestTraceIsNamedInTheReportBlockAndObeysQuiet pins where the path is printed
// from.
//
// It is one more path the run produced, so it is one more labelled line in the
// block that names the others — printed by the renderer, in the renderer's own
// layout, and kept by --quiet on exactly the terms `report json:` is kept. A
// line the command printed for itself after the run would have needed a second
// rule about --quiet, would have been lost when the dashboard closed the
// alternate screen, and would not have been where a reader looks for paths.
func TestTraceIsNamedInTheReportBlockAndObeysQuiet(t *testing.T) {
	inKillableFixture(t)

	code, stdout, stderr := execute(t, "run", "--trace", "--quiet", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --trace --quiet` exited %d\nstderr:\n%s", code, stderr)
	}
	lines := strings.Split(stdout, "\n")
	var traceLine, lastReport int
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "trace: "):
			traceLine = i + 1
		case strings.HasPrefix(line, "report "):
			lastReport = i + 1
		}
	}
	if traceLine == 0 {
		t.Fatalf("--quiet dropped where the run recorded:\n%s", stdout)
	}
	if lastReport == 0 || traceLine != lastReport+1 {
		t.Errorf("the recording is on line %d and the last report path on line %d; "+
			"it belongs in the same block:\n%s", traceLine, lastReport, stdout)
	}
}
