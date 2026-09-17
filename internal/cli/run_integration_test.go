// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

func TestBareChangedNamesTheUpstreamAndMeasuresUntrackedWork(t *testing.T) {
	root, upstream := branchedModule(t)
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
	if rep.Summary.Killed == 0 {
		t.Error("nothing in the untracked file was killed, so the run proved nothing about it")
	}
}

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

func traceDirectoryOf(root string, rep *report.Report) string {
	return filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory), "trace", rep.RunID)
}

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

	raw, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if err := schemas.Validate(schemas.TraceEventV1, []byte(line)); err != nil {
			t.Errorf("line %d does not satisfy %s: %v\n%s", i+1, schemas.TraceEventV1, err, line)
		}
	}

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

	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		t.Fatalf("the recording ends with a %s, want a run-end", last.Type)
	}
	if last.Run.EventsDropped != 0 {
		t.Errorf("the recording dropped %d events", last.Run.EventsDropped)
	}

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

func TestRunTraceLandsUnderTheReportDirectoryEvenWithNoReportFormats(t *testing.T) {
	root := inKillableFixture(t)
	rep, _ := runReport(t, "--trace", "--report", "none")

	directory := traceDirectoryOf(root, rep)
	if _, err := os.Stat(filepath.Join(directory, trace.FileName)); err != nil {
		t.Fatalf("the recording is not under the report directory: %v", err)
	}
	reports := filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory))
	for _, name := range []string{"mutation.json", "mutation.html"} {
		if _, err := os.Stat(filepath.Join(reports, name)); err == nil {
			t.Errorf("--report none still published %s", name)
		}
	}
}

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

func TestRunTraceDoesNotChangeTheSnapshotDigestOrDrift(t *testing.T) {
	inKillableFixture(t)

	untraced, _ := runReport(t)
	traced, _ := runReport(t, "--trace")
	again, _ := runReport(t, "--trace")

	if traced.Workspace.WorkspaceDigest != untraced.Workspace.WorkspaceDigest {
		t.Errorf("workspace_digest = %q with a recording and %q without one",
			traced.Workspace.WorkspaceDigest, untraced.Workspace.WorkspaceDigest)
	}
	if again.Workspace.WorkspaceDigest != untraced.Workspace.WorkspaceDigest {
		t.Errorf("workspace_digest = %q with a recording already on disk and %q without one",
			again.Workspace.WorkspaceDigest, untraced.Workspace.WorkspaceDigest)
	}
	if untraced.Summary.Killed == 0 || traced.Summary.Killed != untraced.Summary.Killed {
		t.Errorf("the traced run killed %d mutants and the untraced one killed %d",
			traced.Summary.Killed, untraced.Summary.Killed)
	}
}

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

func TestATraceThatCannotBeWrittenCostsTheEventsAndNotTheRun(t *testing.T) {
	root := inKillableFixture(t)

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
