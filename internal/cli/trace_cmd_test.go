// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

func tracedWorkspace(t *testing.T) string {
	t.Helper()
	root := resolvedTempDir(t)
	t.Chdir(root)
	return root
}

func traceRootOf(root string) string {
	return filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory), traceDirectoryName)
}

type refusingSink struct{}

func (refusingSink) Emit(trace.Event) error { return os.ErrClosed }
func (refusingSink) Close() error           { return nil }

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

	code, byID, stderr := execute(t, "trace", "summary", "20260901T120000Z-0001")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(byID, "20260901T120000Z-0001") {
		t.Errorf("summary of a named run reports another one:\n%s", byID)
	}

	t.Chdir(resolvedTempDir(t))
	code, _, stderr = execute(t, "trace", "summary")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for a workspace with no recording, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeNoTraceRecorded)) {
		t.Errorf("stderr = %q, want %s", stderr, CodeNoTraceRecorded)
	}
}

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

	code, _, stderr = execute(t, "trace", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if left = entriesOf(t, traceRoot); !slices.Equal(left, []string{"NOTES"}) {
		t.Errorf("the trace root holds %q, want only the file that is not a recording", left)
	}

	code, stdout, stderr = execute(t, "trace", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("stdout = %q, want it to say there was nothing to remove", stdout)
	}
}

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
	if first, second := strings.Index(stdout, "20260902"), strings.Index(stdout, "20260901"); first > second {
		t.Errorf("the listing is oldest first:\n%s", stdout)
	}

	t.Chdir(resolvedTempDir(t))
	code, stdout, stderr = execute(t, "trace", "list")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d for a workspace with no recording, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "no recording") {
		t.Errorf("stdout = %q, want it to say there is no recording", stdout)
	}
}

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
	code, byID, stderr := execute(t, "trace", "diff", "20260901T120000Z-0001", "20260902T120000Z-0002")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if byID != stdout {
		t.Errorf("naming the runs by id gave a different answer:\n%s\n---\n%s", byID, stdout)
	}

	code, _, stderr = execute(t, "trace", "diff", "20260901T120000Z-0001", "20261231T235959Z-ffff")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for a recording that does not exist, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeNoTraceRecorded)) {
		t.Errorf("stderr = %q, want %s", stderr, CodeNoTraceRecorded)
	}
}

func TestAnUnreadableRootIsReportedAsUnreadableRatherThanAsUndeleted(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	if err := os.MkdirAll(filepath.Dir(traceRoot), 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}
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

func withDirectoryListing(t *testing.T, list func(*os.File) ([]os.DirEntry, error)) {
	t.Helper()
	restore := readDir
	t.Cleanup(func() { readDir = restore })
	readDir = list
}

func TestARootThatIsNotADirectoryIsRefusedOnEveryPlatform(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	if err := os.MkdirAll(filepath.Dir(traceRoot), 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}
	if err := os.WriteFile(traceRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("occupying the trace root: %v", err)
	}
	withDirectoryListing(t, func(*os.File) ([]os.DirEntry, error) { return nil, nil })

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
	if _, err := os.Stat(traceRoot); err != nil {
		t.Errorf("`trace clean` deleted the file sitting where the trace root belongs: %v", err)
	}

	absent := traceRootAt(filepath.Join(t.TempDir(), "never-traced"))
	names, err := namesIn(absent)
	if err != nil || len(names) != 0 {
		t.Errorf("namesIn(a root that was never made) = %q/%v, want nothing and no failure", names, err)
	}
}

func TestRemoveDirectoryRemovesOnlyDirectories(t *testing.T) {
	parent := t.TempDir()

	empty := filepath.Join(parent, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatalf("creating the empty directory: %v", err)
	}
	if err := removeDirectory(empty); err != nil {
		t.Errorf("removeDirectory(an empty directory) = %v, want it removed", err)
	}
	if _, err := os.Stat(empty); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the empty directory survived (%v)", err)
	}

	file := filepath.Join(parent, "theirs")
	if err := os.WriteFile(file, []byte("somebody else's file"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	err := removeDirectory(file)
	if !errors.Is(err, errNotDirectory) {
		t.Errorf("removeDirectory(a file) = %v, want it refused as not a directory", err)
	}
	if _, statErr := os.Stat(file); statErr != nil {
		t.Errorf("removeDirectory unlinked a file: %v", statErr)
	}

	full := filepath.Join(parent, "full")
	if err := os.MkdirAll(filepath.Join(full, "inside"), 0o755); err != nil {
		t.Fatalf("creating the directory that holds something: %v", err)
	}
	switch err := removeDirectory(full); {
	case err == nil, errors.Is(err, errNotDirectory), errors.Is(err, errIsALink):
		t.Errorf("removeDirectory(a directory holding something) = %v, want an ordinary refusal", err)
	}
	if _, statErr := os.Stat(full); statErr != nil {
		t.Errorf("removeDirectory emptied a directory that was not empty: %v", statErr)
	}

	linked := filepath.Join(parent, "linked")
	if err := os.Symlink(empty2(t, parent), linked); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("this machine will not create a directory symbolic link (%v); "+
				"Windows needs SeCreateSymbolicLinkPrivilege or Developer Mode", err)
		}
		t.Fatalf("linking %s: %v", linked, err)
	}
	if err := removeDirectory(linked); !errors.Is(err, errIsALink) {
		t.Errorf("removeDirectory(a link to a directory) = %v, want it refused as a link", err)
	}
	if info, statErr := os.Lstat(linked); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("removeDirectory removed the link itself (%v, %v)", info, statErr)
	}
}

func empty2(t *testing.T, parent string) string {
	t.Helper()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("creating the directory the link points at: %v", err)
	}
	return target
}

func TestARootReplacedBetweenTheListingAndTheRemovalIsNotUnlinked(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	if err := os.MkdirAll(traceRoot, 0o755); err != nil {
		t.Fatalf("creating the trace root: %v", err)
	}

	const theirs = "somebody else's file"
	withDirectoryListing(t, func(f *os.File) ([]os.DirEntry, error) {
		path := f.Name()
		if err := f.Close(); err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
			return nil, err
		}
		return nil, nil
	})

	code, stdout, stderr := execute(t, "trace", "clean")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(CodeUnreadableTrace)) {
		t.Errorf("stderr = %q, want it coded %s", stderr, CodeUnreadableTrace)
	}
	if strings.Contains(stdout, "removed the empty") {
		t.Errorf("the command claims to have removed an empty directory that is a file:\n%s", stdout)
	}
	data, err := os.ReadFile(traceRoot)
	if err != nil {
		t.Fatalf("`trace clean` unlinked the file that replaced the trace root: %v", err)
	}
	if string(data) != theirs {
		t.Errorf("the file at the trace root reads %q, want %q", data, theirs)
	}
}

func TestADirectorySymlinkAtTheRootIsNeverTheThingRemoved(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	if err := os.MkdirAll(filepath.Dir(traceRoot), 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}
	target := filepath.Join(t.TempDir(), "recordings")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("creating the directory the link points at: %v", err)
	}
	if err := os.Symlink(target, traceRoot); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("this machine will not create a directory symbolic link (%v); "+
				"Windows needs SeCreateSymbolicLinkPrivilege or Developer Mode", err)
		}
		t.Fatalf("linking %s to %s: %v", traceRoot, target, err)
	}

	code, stdout, stderr := execute(t, "trace", "clean")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(CodeUnreadableTrace)) {
		t.Errorf("stderr = %q, want it coded %s", stderr, CodeUnreadableTrace)
	}
	if !strings.Contains(stderr, "is a link") {
		t.Errorf("stderr = %q, want it to say the root is a link rather than a directory", stderr)
	}
	if strings.Contains(stdout, "removed the empty") {
		t.Errorf("the command claims to have removed a link:\n%s", stdout)
	}

	info, err := os.Lstat(traceRoot)
	if err != nil {
		t.Fatalf("`trace clean` removed the link at the trace root: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the trace root is %s, want it left as a symbolic link", info.Mode())
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the directory the link pointed at is gone: %v", err)
	}
}
