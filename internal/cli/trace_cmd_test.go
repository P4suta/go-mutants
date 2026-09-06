// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

// tracedWorkspace is a directory with a trace root under the default report
// directory, and nothing else. It is what a workspace looks like after a run
// that recorded.
func tracedWorkspace(t *testing.T) string {
	t.Helper()
	root := resolvedTempDir(t)
	t.Chdir(root)
	return root
}

// traceRootOf is where a recording of a run in root lands.
func traceRootOf(root string) string {
	return filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory), traceDirectoryName)
}

// refusingSink is a sink that keeps nothing and counts nothing, which is what
// makes a recording lossy: the events it refused are missing from the account
// and only the run-end says so.
type refusingSink struct{}

func (refusingSink) Emit(trace.Event) error { return os.ErrClosed }
func (refusingSink) Close() error           { return nil }

// record writes one whole recording into a trace root and returns its
// directory. lossy adds a sink that refuses everything, so the run-end reports
// events the recording does not hold.
func record(t *testing.T, traceRoot, runID string, lossy bool, body func(*trace.Recorder)) string {
	t.Helper()
	dir, err := trace.NewDirSink(traceRoot, runID, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink(%s, %s): %v", traceRoot, runID, err)
	}
	var sink trace.Sink = dir
	if lossy {
		sink = trace.NewTeeSink(dir, refusingSink{})
	}
	recorder := trace.New(sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindRun,
		RunID:       runID,
		ToolVersion: Version,
		PID:         os.Getpid(),
		Root:        "workspace",
	})
	if body != nil {
		body(recorder)
	}
	recorder.RunEnd("ok", 0, nil)
	if err := sink.Close(); err != nil {
		t.Fatalf("closing the recording: %v", err)
	}
	return dir.Directory()
}

// oneCompile is the smallest recording that says something a summary can
// report: a phase, a stage inside it, and one subprocess.
func oneCompile(recorder *trace.Recorder) {
	closePhase := recorder.PhaseStart(trace.PhaseMutate)
	finish := recorder.Stage("build-binaries", "")
	recorder.Exec(trace.ExecRecord{
		Kind:       trace.ExecKindGoTestC,
		Subject:    "example.test/pkg",
		Argv:       []string{"go", "test", "-c"},
		Dir:        "/workspace",
		DurationMS: 40,
	})
	finish(trace.ResultSucceeded)
	closePhase()
}

// TestTraceSummaryReadsTheLatestRecording is the command's default: the run
// somebody has just made, without their having to copy its id out of the
// console first.
func TestTraceSummaryReadsTheLatestRecording(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	record(t, traceRoot, "20260901T120000Z-0001", false, nil)
	record(t, traceRoot, "20260907T120000Z-0002", false, oneCompile)

	code, stdout, stderr := execute(t, "trace", "summary")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "20260907T120000Z-0002") {
		t.Errorf("the summary is not of the newest recording:\n%s", stdout)
	}
	if strings.Contains(stdout, "20260901T120000Z-0001") {
		t.Errorf("the summary reports more than one recording:\n%s", stdout)
	}
	for _, want := range []string{trace.ExecKindGoTestC, trace.PhaseMutate, "build-binaries", "complete"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the summary does not report %q:\n%s", want, stdout)
		}
	}

	// And the same recording named by its id, which is what a bug report
	// quotes.
	code, byID, stderr := execute(t, "trace", "summary", "20260901T120000Z-0001")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(byID, "20260901T120000Z-0001") {
		t.Errorf("summary of a named run reports another one:\n%s", byID)
	}

	// A module with no recording at all is a refusal rather than an empty
	// summary: this command's whole output is one recording, and there is none.
	t.Chdir(resolvedTempDir(t))
	code, _, stderr = execute(t, "trace", "summary")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for a workspace with no recording, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeNoTraceRecorded)) {
		t.Errorf("stderr = %q, want %s", stderr, CodeNoTraceRecorded)
	}
}

// TestTraceValidateRejectsALineOutsideTheContract is the same check `report
// validate` makes, on the other document go-mutants publishes a schema for.
//
// Every line is checked rather than the first, because a recording is a stream:
// a consumer reads it to the end, and a run that wrote nine hundred good events
// and one that nobody can decode has published a file that breaks halfway
// through.
func TestTraceValidateRejectsALineOutsideTheContract(t *testing.T) {
	root := tracedWorkspace(t)
	directory := record(t, traceRootOf(root), "20260907T120000Z-0001", false, oneCompile)
	stream := filepath.Join(directory, trace.FileName)

	code, stdout, stderr := execute(t, "trace", "validate", stream)
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d for a recording go-mutants wrote, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, trace.SchemaV1) {
		t.Errorf("stdout = %q, want the schema it was checked against", stdout)
	}

	// One line outside the contract, appended after a whole valid recording.
	// The type is one the schema knows and the payload is another type's, which
	// is the mistake a hand-edited recording actually makes.
	broken := filepath.Join(t.TempDir(), trace.FileName)
	original, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("reading the recording: %v", err)
	}
	damaged := append(slices.Clone(original),
		[]byte(`{"seq":99,"type":"note","timestamp":"2026-09-07T12:00:00Z","elapsed_ms":1,"note":{"kind":"warning","surprise":true}}`+"\n")...)
	if err := os.WriteFile(broken, damaged, 0o600); err != nil {
		t.Fatalf("writing the damaged recording: %v", err)
	}

	code, _, stderr = execute(t, "trace", "validate", broken)
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for a recording outside the contract, want 2", code)
	}
	if !strings.Contains(stderr, "error GOM") {
		t.Errorf("stderr = %q, want a coded refusal", stderr)
	}
}

// TestTraceCleanRemovesRecordingsAndNothingElse is `cache clean`'s promise
// applied to the diagnostic exhaust: the command deletes, so what it will not
// touch matters more than what it will.
func TestTraceCleanRemovesRecordingsAndNothingElse(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	for _, id := range []string{"20260901T120000Z-0001", "20260902T120000Z-0002", "20260903T120000Z-0003"} {
		record(t, traceRoot, id, false, nil)
	}
	if err := os.WriteFile(filepath.Join(traceRoot, "NOTES"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("writing the stray file: %v", err)
	}

	code, stdout, stderr := execute(t, "trace", "clean", "--keep", "1")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "2 recordings") {
		t.Errorf("stdout = %q, want the two oldest reported as removed", stdout)
	}
	left := entriesOf(t, traceRoot)
	want := []string{"20260903T120000Z-0003", "NOTES"}
	slices.Sort(left)
	if !slices.Equal(left, want) {
		t.Errorf("the trace root holds %q, want %q", left, want)
	}

	// With no --keep the command removes every recording and still leaves what
	// is not one.
	code, _, stderr = execute(t, "trace", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if left = entriesOf(t, traceRoot); !slices.Equal(left, []string{"NOTES"}) {
		t.Errorf("the trace root holds %q, want only the file that is not a recording", left)
	}

	// A second clean is not a failure: an empty trace root is an answer.
	code, stdout, stderr = execute(t, "trace", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("stdout = %q, want it to say there was nothing to remove", stdout)
	}
}

// TestTraceCleanSaysWhatItKeptRatherThanClaimingThereIsNothing tells the two
// ways a clean removes nothing apart.
//
// "Nothing to remove" and "nothing here" are different answers, and printing
// the second for the first is the worst thing a command that deletes can say:
// somebody reading it concludes the recordings are gone and stops looking for
// the disk they are still sitting on. Two retentions reach it — a --keep that
// covers everything, and the ordinary one over a root where no recording ended
// with its run-end — and the second has a reason worth stating, since a plain
// `trace clean` that appears to have done nothing is otherwise a mystery.
func TestTraceCleanSaysWhatItKeptRatherThanClaimingThereIsNothing(t *testing.T) {
	t.Run("kept by --keep", func(t *testing.T) {
		root := tracedWorkspace(t)
		traceRoot := traceRootOf(root)
		for _, id := range []string{"20260901T120000Z-0001", "20260902T120000Z-0002"} {
			record(t, traceRoot, id, false, nil)
		}

		code, stdout, stderr := execute(t, "trace", "clean", "--keep", "5")
		if code != int(mutation.ExitOK) {
			t.Fatalf("exit = %d, want 0\n%s", code, stderr)
		}
		if strings.Contains(stdout, "no recording") {
			t.Errorf("the command says there is no recording while two are on disk:\n%s", stdout)
		}
		if !strings.Contains(stdout, "kept") {
			t.Errorf("stdout = %q, want it to say the recordings were kept", stdout)
		}
		if left := entriesOf(t, traceRoot); len(left) != 2 {
			t.Errorf("the trace root holds %q, want both recordings", left)
		}
	})

	t.Run("kept because no run finished", func(t *testing.T) {
		root := tracedWorkspace(t)
		traceRoot := traceRootOf(root)
		recordUnfinished(t, traceRoot, "20260901T120000Z-0001")

		code, stdout, stderr := execute(t, "trace", "clean")
		if code != int(mutation.ExitOK) {
			t.Fatalf("exit = %d, want 0\n%s", code, stderr)
		}
		// The line that would be false, whole: "no recording in <root>" with
		// nothing after it. The true message begins the same way and goes on to
		// say why the recording that is there was kept.
		if strings.Contains(stdout, "no recording in "+traceRoot+"\n") {
			t.Errorf("the command says the trace root is empty while a recording is in it:\n%s", stdout)
		}
		if !strings.Contains(stdout, "run-end") {
			t.Errorf("stdout = %q, want the reason the recording was kept", stdout)
		}
		if !strings.Contains(stdout, "--all") {
			t.Errorf("stdout = %q, want the flag that removes it anyway", stdout)
		}
		if left := entriesOf(t, traceRoot); len(left) != 1 {
			t.Errorf("the trace root holds %q, want the recording no run finished", left)
		}
	})

	t.Run("nothing here", func(t *testing.T) {
		root := tracedWorkspace(t)
		code, stdout, stderr := execute(t, "trace", "clean")
		if code != int(mutation.ExitOK) {
			t.Fatalf("exit = %d, want 0\n%s", code, stderr)
		}
		if !strings.Contains(stdout, "no recording in "+traceRootOf(root)+"\n") {
			t.Errorf("stdout = %q, want it to say the trace root holds nothing", stdout)
		}
	})
}

// TestTraceCleanRemovesTheEmptyTraceDirectory keeps a cleaned workspace the
// shape of one that was never traced.
//
// A `reports/mutation/trace/` with nothing in it is a directory somebody has to
// look inside to find out is empty, and the command that emptied it is the one
// thing that knows it is.
func TestTraceCleanRemovesTheEmptyTraceDirectory(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	record(t, traceRoot, "20260901T120000Z-0001", false, nil)

	code, stdout, stderr := execute(t, "trace", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "removed the empty trace directory") {
		t.Errorf("stdout = %q, want the empty directory reported as removed", stdout)
	}
	if _, err := os.Stat(traceRoot); !os.IsNotExist(err) {
		t.Errorf("the trace directory survived its last recording (%v)", err)
	}

	// And a root that still holds something is left where it is, so that
	// nothing somebody else keeps there is taken away with it.
	record(t, traceRoot, "20260902T120000Z-0002", false, nil)
	if err := os.WriteFile(filepath.Join(traceRoot, "NOTES"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("writing the stray file: %v", err)
	}
	if code, _, stderr = execute(t, "trace", "clean"); code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if _, err := os.Stat(traceRoot); err != nil {
		t.Errorf("the trace directory was removed with something still in it: %v", err)
	}
}

// TestTraceCleanKeepsAnUnfinishedRecordingUnlessAllIsAsked is the exception the
// retention rule exists for.
//
// A recording whose stream does not end with a run-end is a run still in
// progress or a run that died, and the second is the recording a reader most
// wants: a collector that removed the account of the crash and kept ten accounts
// of runs that went fine would be collecting exactly backwards. It is also what
// makes a live run safe from a concurrent collector, rather than only from being
// the newest name in the root.
func TestTraceCleanKeepsAnUnfinishedRecordingUnlessAllIsAsked(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	record(t, traceRoot, "20260901T120000Z-0001", false, oneCompile)
	unfinished := "20260902T120000Z-0002"
	recordUnfinished(t, traceRoot, unfinished)

	code, stdout, stderr := execute(t, "trace", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "removed 1 recording") {
		t.Errorf("stdout = %q, want exactly the finished recording removed", stdout)
	}
	if left := entriesOf(t, traceRoot); !slices.Equal(left, []string{unfinished}) {
		t.Errorf("the trace root holds %q, want the recording no run finished", left)
	}

	// --all is how somebody who has read them says so, and it takes no --keep:
	// the two contradict each other, and a command that deletes must not resolve
	// a contradiction on its own.
	code, _, stderr = execute(t, "trace", "clean", "--all", "--keep", "1")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for --all with --keep, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeConflictingFlags)) ||
		!strings.Contains(stderr, "--all") || !strings.Contains(stderr, "--keep") {
		t.Errorf("stderr = %q, want the two flags refused together with a reason", stderr)
	}

	code, stdout, stderr = execute(t, "trace", "clean", "--all")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "removed 1 recording") {
		t.Errorf("stdout = %q, want the unfinished recording removed", stdout)
	}
	if _, err := os.Stat(traceRoot); !os.IsNotExist(err) {
		t.Errorf("the trace directory survived its last recording (%v)", err)
	}
}

// TestTraceListSaysLossyForADroppedRecording is the column a reader has to see
// before they trust a listing of counts.
//
// A recording that lost events is still worth reading and is not the same
// document as one that did not: a question about "how many times did this run
// compile" has no answer in a recording missing an unknown number of them.
func TestTraceListSaysLossyForADroppedRecording(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	record(t, traceRoot, "20260901T120000Z-0001", false, oneCompile)
	record(t, traceRoot, "20260902T120000Z-0002", true, oneCompile)

	code, stdout, stderr := execute(t, "trace", "list")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		if id, _, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			rows[id] = line
		}
	}
	if row, ok := rows["20260901T120000Z-0001"]; !ok || !strings.Contains(row, "complete") {
		t.Errorf("the whole recording is listed as %q, want complete:\n%s", row, stdout)
	}
	if row, ok := rows["20260902T120000Z-0002"]; !ok || !strings.Contains(row, "lossy") {
		t.Errorf("the lossy recording is listed as %q, want lossy:\n%s", row, stdout)
	}
	// The newest first, which is the order somebody reading a listing wants and
	// the order `report list` already uses.
	if first, second := strings.Index(stdout, "20260902"), strings.Index(stdout, "20260901"); first > second {
		t.Errorf("the listing is oldest first:\n%s", stdout)
	}

	// A workspace that has never recorded is an empty listing and exits 0, the
	// same answer `report list` gives.
	t.Chdir(resolvedTempDir(t))
	code, stdout, stderr = execute(t, "trace", "list")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d for a workspace with no recording, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "no recording") {
		t.Errorf("stdout = %q, want it to say there is no recording", stdout)
	}
}

// TestTraceDiffReportsTheDelta is the comparison a performance question is
// asked of: two recordings of the same workspace, and what moved between them.
func TestTraceDiffReportsTheDelta(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	before := record(t, traceRoot, "20260901T120000Z-0001", false, oneCompile)
	after := record(t, traceRoot, "20260902T120000Z-0002", false, func(recorder *trace.Recorder) {
		oneCompile(recorder)
		recorder.Exec(trace.ExecRecord{
			Kind:       trace.ExecKindGoTestC,
			Subject:    "example.test/other",
			Argv:       []string{"go", "test", "-c"},
			Dir:        "/workspace",
			DurationMS: 60,
		})
	})

	code, stdout, stderr := execute(t, "trace", "diff", before, after)
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	for _, want := range []string{trace.ExecKindGoTestC, "+1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the diff does not report %q:\n%s", want, stdout)
		}
	}
	// Two run ids that name recordings in this workspace work as well as two
	// paths, because a diff is usually between two runs somebody has just made.
	code, byID, stderr := execute(t, "trace", "diff", "20260901T120000Z-0001", "20260902T120000Z-0002")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if byID != stdout {
		t.Errorf("naming the runs by id gave a different answer:\n%s\n---\n%s", byID, stdout)
	}

	// A recording that is not there is named rather than reported as an empty
	// diff.
	code, _, stderr = execute(t, "trace", "diff", "20260901T120000Z-0001", "20261231T235959Z-ffff")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for a recording that does not exist, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeNoTraceRecorded)) {
		t.Errorf("stderr = %q, want %s", stderr, CodeNoTraceRecorded)
	}
}

// TestAnUnreadableRootIsReportedAsUnreadableRatherThanAsUndeleted keeps a
// command that deletes from misdiagnosing the reason it did not.
//
// A root that cannot be read and a recording that will not go away are
// different problems with different remedies — a permission or a path that is
// not a directory, against a file somebody has open — and only one of them is
// about deleting. Reporting the first as "a recording could not be removed:
// … cannot be read" sends a reader looking for a locked file that does not
// exist.
//
// So an already-coded failure travels out with the code it was given, and only
// a removal that failed is wrapped as one.
func TestAnUnreadableRootIsReportedAsUnreadableRatherThanAsUndeleted(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	if err := os.MkdirAll(filepath.Dir(traceRoot), 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}
	// A file where the directory belongs: os.ReadDir refuses it, which is the
	// same shape of failure a permission would produce and needs no privilege
	// to arrange.
	if err := os.WriteFile(traceRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("occupying the trace root: %v", err)
	}

	code, _, stderr := execute(t, "trace", "clean")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}
	if !strings.Contains(stderr, string(CodeUnreadableTrace)) {
		t.Errorf("stderr = %q, want it coded %s", stderr, CodeUnreadableTrace)
	}
	if strings.Contains(stderr, string(CodeTraceNotRemoved)) {
		t.Errorf("a root that could not be read was reported as one that could not be deleted:\n%s", stderr)
	}
	if strings.Contains(stderr, "removed") {
		t.Errorf("the failure talks about removing something:\n%s", stderr)
	}
}

// withDirectoryListing points the collector's directory reads at a listing of
// the test's own, for the length of one test.
func withDirectoryListing(t *testing.T, list func(string) ([]os.DirEntry, error)) {
	t.Helper()
	t.Cleanup(func() { readDir = os.ReadDir })
	readDir = list
}

// TestARootThatIsNotADirectoryIsRefusedOnEveryPlatform pins the classification
// to something other than what the operating system happens to say.
//
// A file where the trace root belongs is a read failure on Unix, where
// os.ReadDir reports ENOTDIR, and *is not one* on Windows: Go's readdir there
// asks the handle for directory information, and on two of the error codes it
// can come back with it breaks out of its loop and returns `names, dirents,
// infos, nil` — the error it was holding is discarded, so the caller is handed
// an empty directory and no failure at all. `trace clean` then said "nothing to
// remove: no recording here" about a root it had not been able to read, which
// is the one thing a command that deletes must never say.
//
// So the rule is stated here rather than inherited: a root that exists and is
// not a directory is a failure, and the listing is never reached. The injected
// listing is Windows' own answer — no entries, no error — and the refusal has to
// survive it.
func TestARootThatIsNotADirectoryIsRefusedOnEveryPlatform(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	if err := os.MkdirAll(filepath.Dir(traceRoot), 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}
	if err := os.WriteFile(traceRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("occupying the trace root: %v", err)
	}
	withDirectoryListing(t, func(string) ([]os.DirEntry, error) { return nil, nil })

	code, stdout, stderr := execute(t, "trace", "clean")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(CodeUnreadableTrace)) {
		t.Errorf("stderr = %q, want it coded %s", stderr, CodeUnreadableTrace)
	}
	if strings.Contains(stdout, "nothing to remove") {
		t.Errorf("a root that could not be read was reported as one holding nothing:\n%s", stdout)
	}
	// And it is still there, which is what the misclassification *cost* rather
	// than merely misreported. A root read as empty goes on to the os.Remove
	// that takes an emptied trace directory away, and os.Remove is perfectly
	// happy to unlink a file — so `trace clean` deleted somebody's file and
	// called it "removed the empty trace directory". What is not ours is never
	// removed, and that is the promise this command is built around.
	if _, err := os.Stat(traceRoot); err != nil {
		t.Errorf("`trace clean` deleted the file sitting where the trace root belongs: %v", err)
	}

	// And the other half of the rule, which the same listing must not take
	// away: a root that is not there at all holds nothing, which is an answer
	// rather than a failure — it is what a workspace that has never traced a run
	// looks like.
	absent := traceRootAt(filepath.Join(t.TempDir(), "never-traced"))
	names, err := namesIn(absent)
	if err != nil || len(names) != 0 {
		t.Errorf("namesIn(a root that was never made) = %q/%v, want nothing and no failure", names, err)
	}
}
