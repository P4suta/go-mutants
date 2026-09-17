// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
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

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temporary directory: %v", err)
	}
	return resolved
}

func recordInto(t *testing.T, recording *traceRecording, runID string) {
	t.Helper()
	recorder := trace.New(recording.sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindRun,
		RunID:       runID,
		ToolVersion: Version,
		PID:         os.Getpid(),
		Root:        "workspace",
	})
	for _, note := range recording.notes {
		recorder.Note(note.Kind, note.Code, note.Detail)
	}
	recorder.Artifact(trace.ArtifactReportJSON, "reports/mutation/mutation.json")
	recorder.RunEnd("ok", 0, nil)
}

func TestTraceDirectoryDefaultsToTheReportDirectory(t *testing.T) {
	root := resolvedTempDir(t)

	cases := []struct {
		name            string
		reportDirectory string
		want            string
	}{
		{"the built-in default", config.DefaultReportDirectory, filepath.Join(root, "reports", "mutation", "trace")},
		{"a configured directory", "out/mutation", filepath.Join(root, "out", "mutation", "trace")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := traceRoot(root, c.reportDirectory, traceDefaultDirectory)
			if err != nil {
				t.Fatalf("traceRoot: %v", err)
			}
			if got != c.want {
				t.Errorf("traceRoot = %q, want %q", got, c.want)
			}
		})
	}

	runID := "20260907T120000Z-a1b2"
	recording, err := openTrace(traceRequest{
		workspace:       root,
		reportDirectory: config.DefaultReportDirectory,
		requested:       traceDefaultDirectory,
		runID:           runID,
	})
	if err != nil {
		t.Fatalf("openTrace: %v", err)
	}
	t.Cleanup(func() { _ = recording.close() })
	want := filepath.Join(root, "reports", "mutation", "trace", runID)
	if recording.directory != want {
		t.Errorf("recording directory = %q, want %q", recording.directory, want)
	}
	if _, err := os.Stat(filepath.Join(want, trace.FileName)); err != nil {
		t.Errorf("the stream was not opened: %v", err)
	}
}

func TestTraceDirectoryInsideTheWorkspaceOutsideTheReportDirectoryIsRefused(t *testing.T) {
	root := resolvedTempDir(t)
	elsewhere := resolvedTempDir(t)

	cases := []struct {
		name      string
		requested string
		want      string
		refused   bool
	}{
		{
			name:      "a relative directory resolves against the workspace",
			requested: "reports/mutation/recordings",
			want:      filepath.Join(root, "reports", "mutation", "recordings"),
		},
		{
			name:      "the report directory itself",
			requested: filepath.Join(root, "reports", "mutation"),
			want:      filepath.Join(root, "reports", "mutation"),
		},
		{
			name:      "outside the workspace altogether",
			requested: filepath.Join(elsewhere, "recordings"),
			want:      filepath.Join(elsewhere, "recordings"),
		},
		{
			name:      "a relative directory that leaves the workspace",
			requested: filepath.Join("..", filepath.Base(elsewhere), "recordings"),
			want:      filepath.Join(filepath.Dir(root), filepath.Base(elsewhere), "recordings"),
		},
		{
			name:      "inside the workspace, outside the report directory",
			requested: "internal/recordings",
			refused:   true,
		},
		{
			name:      "the workspace root itself",
			requested: ".",
			refused:   true,
		},
		{
			name:      "a directory whose name only starts like the report directory",
			requested: "reports/mutation-recordings",
			refused:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := traceRoot(root, config.DefaultReportDirectory, c.requested)
			if c.refused {
				if err == nil {
					t.Fatalf("traceRoot(%q) = %q, want a refusal", c.requested, got)
				}
				if !strings.Contains(err.Error(), string(CodeTraceUnavailable)) {
					t.Errorf("the refusal is %v, want it coded %s", err, CodeTraceUnavailable)
				}
				return
			}
			if err != nil {
				t.Fatalf("traceRoot(%q): %v", c.requested, err)
			}
			if got != c.want {
				t.Errorf("traceRoot(%q) = %q, want %q", c.requested, got, c.want)
			}
		})
	}
}

func TestTraceDirectoryRefusalIsSymlinkAware(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need a privilege this test cannot assume on Windows")
	}
	root := resolvedTempDir(t)
	outside := resolvedTempDir(t)

	inside := filepath.Join(root, "internal")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("creating the directory inside the workspace: %v", err)
	}
	alias := filepath.Join(outside, "alias")
	if err := os.Symlink(inside, alias); err != nil {
		t.Fatalf("linking %s to %s: %v", alias, inside, err)
	}

	if got, err := traceRoot(root, config.DefaultReportDirectory, filepath.Join(alias, "recordings")); err == nil {
		t.Errorf("traceRoot through a link into the workspace = %q, want a refusal", got)
	}

	reportDirectory := filepath.Join(root, "reports", "mutation")
	if err := os.MkdirAll(reportDirectory, 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}
	outward := filepath.Join(reportDirectory, "trace")
	if err := os.Symlink(outside, outward); err != nil {
		t.Fatalf("linking %s to %s: %v", outward, outside, err)
	}
	if _, err := traceRoot(root, config.DefaultReportDirectory, traceDefaultDirectory); err != nil {
		t.Errorf("traceRoot under a report directory that links outward: %v", err)
	}
}

func TestARefusedTraceFallsBackToTheRing(t *testing.T) {
	root := resolvedTempDir(t)
	occupied := filepath.Join(root, "reports", "mutation", "taken")
	if err := os.MkdirAll(filepath.Dir(occupied), 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}
	if err := os.WriteFile(occupied, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("occupying the trace root: %v", err)
	}

	cases := map[string]string{
		"inside the workspace":  "internal/recordings",
		"cannot be created":     occupied,
		"a run directory taken": filepath.Join(root, "reports", "mutation", "trace"),
	}
	runID := "20260907T120000Z-a1b2"
	if err := os.MkdirAll(filepath.Join(cases["a run directory taken"], runID), 0o755); err != nil {
		t.Fatalf("occupying the run directory: %v", err)
	}

	for name, requested := range cases {
		t.Run(name, func(t *testing.T) {
			recording, err := openTrace(traceRequest{
				workspace:       root,
				reportDirectory: config.DefaultReportDirectory,
				requested:       requested,
				runID:           runID,
			})
			if err == nil {
				t.Fatalf("openTrace(%q) was accepted, want a refusal", requested)
			}
			var refusal *Error
			if !errors.As(err, &refusal) || refusal.Code != CodeTraceUnavailable {
				t.Fatalf("the refusal is %v, want an *Error coded %s", err, CodeTraceUnavailable)
			}
			if recording == nil {
				t.Fatal("a refused trace left no recording; every run records")
			}
			if recording.directory != "" {
				t.Errorf("the recording claims the directory %q it was refused", recording.directory)
			}
			if len(recording.notes) == 0 || recording.notes[0].Kind != trace.NoteTraceUnavailable {
				t.Fatalf("the recording carries the notes %+v, want a %s first", recording.notes, trace.NoteTraceUnavailable)
			}
			if !strings.Contains(recording.notes[0].Detail, string(CodeTraceUnavailable)) {
				t.Errorf("the note says %q, which does not carry the code the warning printed", recording.notes[0].Detail)
			}

			recordInto(t, recording, runID)
			if err := recording.close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			events := recording.Events()
			if len(events) == 0 {
				t.Fatal("the run recorded nothing into the ring")
			}
			var noted bool
			for _, event := range events {
				if event.Type == trace.TypeNote && event.Note.Kind == trace.NoteTraceUnavailable {
					noted = true
					if !strings.Contains(event.Note.Detail, "trace") {
						t.Errorf("the note says %q, which does not say what was refused", event.Note.Detail)
					}
				}
			}
			if !noted {
				t.Errorf("the ring holds no %s note:\n%s", trace.NoteTraceUnavailable, describeEvents(events))
			}
			if last := events[len(events)-1]; last.Type != trace.TypeRunEnd {
				t.Errorf("the recording ends with a %s, want the run-end last:\n%s", last.Type, describeEvents(events))
			}
			requireReadableRecording(t, events)
		})
	}
}

func TestTracePruningKeepsTheNewestRunsAndNeverTouchesForeignNames(t *testing.T) {
	root := filepath.Join(resolvedTempDir(t), "trace")

	var recordings []string
	for day := range trace.RetainRuns + 2 {
		id := fmt.Sprintf("202609%02dT120000Z-a1b2", day+1)
		recordings = append(recordings, id)
		record(t, root, id, false, nil)
	}
	foreign := []string{"notes", "20260907T120000Z-zzzz"}
	for _, name := range foreign {
		if err := os.MkdirAll(filepath.Join(root, name, "keep"), 0o755); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "20260908T120000Z-c3d4"), 0o755); err != nil {
		t.Fatalf("creating the streamless directory: %v", err)
	}
	unfinished := "20260801T120000Z-dead"
	recordUnfinished(t, root, unfinished)
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("writing the stray file: %v", err)
	}

	removed, err := collect(traceRootAt(root), retention{keep: trace.RetainRuns})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := recordings[:len(recordings)-trace.RetainRuns]
	if !slices.Equal(removed, want) {
		t.Errorf("removed %q, want the oldest %q", removed, want)
	}

	left := entriesOf(t, root)
	for _, name := range append([]string{"README", "20260908T120000Z-c3d4", unfinished}, foreign...) {
		if !slices.Contains(left, name) {
			t.Errorf("%q was collected; only a finished recording is the collector's", name)
		}
	}
	for _, name := range recordings[len(recordings)-trace.RetainRuns:] {
		if !slices.Contains(left, name) {
			t.Errorf("the recording %q was collected and is among the newest %d", name, trace.RetainRuns)
		}
	}
	for _, name := range want {
		if slices.Contains(left, name) {
			t.Errorf("the recording %q survived and is older than the newest %d", name, trace.RetainRuns)
		}
	}
}

func TestPruningRemovesThePlanItWasGivenAndNotWhatItFindsLater(t *testing.T) {
	root := filepath.Join(resolvedTempDir(t), "trace")
	finished := "20260901T120000Z-0001"
	running := "20260902T120000Z-0002"
	record(t, root, finished, false, nil)
	recordUnfinished(t, root, running)

	plan, err := planSweep(traceRootAt(root), retention{keep: 0})
	if err != nil {
		t.Fatalf("planSweep: %v", err)
	}
	if !slices.Equal(plan.stale, []string{finished}) {
		t.Fatalf("the plan collects %q, want only the finished recording %q", plan.stale, finished)
	}

	finishTheRecording(t, root, running)

	removed, err := prune(traceRootAt(root), plan)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !slices.Equal(removed, plan.stale) {
		t.Errorf("the sweep removed %q, want the plan it was given (%q)", removed, plan.stale)
	}
	if left := entriesOf(t, root); !slices.Contains(left, running) {
		t.Errorf("the trace root holds %q; the recording that finished after the plan was made "+
			"was removed without ever being measured", left)
	}
}

func finishTheRecording(t *testing.T, root, runID string) {
	t.Helper()
	stream := filepath.Join(root, runID, trace.FileName)
	file, err := os.OpenFile(stream, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("opening %s: %v", stream, err)
	}
	defer func() { _ = file.Close() }()
	line := `{"seq":3,"type":"run-end","timestamp":"2026-09-02T12:00:02Z","elapsed_ms":2,` +
		`"run":{"verdict":"ok","exit_code":0,"events_emitted":2,"events_dropped":0}}` + "\n"
	if _, err = file.WriteString(line); err != nil {
		t.Fatalf("finishing %s: %v", stream, err)
	}
}

func TestTraceFlagRequiresAnEqualsSign(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, stderr := execute(t, "run", "--trace", "recordings")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeUsage)) {
		t.Errorf("stderr = %q, want a coded usage error", stderr)
	}
	if !strings.Contains(stderr, "--trace=recordings") {
		t.Errorf("stderr = %q, want the equals-sign spelling", stderr)
	}
}

func TestAnEmptyTraceDirectoryIsRefused(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, value := range []string{"--trace=", "--trace=   "} {
		t.Run(value, func(t *testing.T) {
			code, _, stderr := execute(t, "run", value)
			if code != int(mutation.ExitInfrastructure) {
				t.Errorf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr, "error "+string(CodeUsage)+": --trace was given an empty directory") {
				t.Errorf("stderr = %q, want a coded usage error naming the empty directory", stderr)
			}
			if !strings.Contains(stderr, "hint: ") {
				t.Errorf("stderr = %q, want a hint saying how to write it", stderr)
			}
		})
	}
	if flag, requested := traceFlag(""); requested {
		t.Errorf("GO_MUTANTS_TRACE= asked for %q, want no flag", flag)
	}
}

func TestCollectionRunsWhenTheRecordingIsOpened(t *testing.T) {
	workspace := resolvedTempDir(t)
	root := filepath.Join(workspace, filepath.FromSlash(config.DefaultReportDirectory), traceDirectoryName)
	for day := range trace.RetainRuns + 2 {
		record(t, root, fmt.Sprintf("202609%02dT120000Z-a1b2", day+1), false, nil)
	}

	runID := "20260930T120000Z-c3d4"
	recording, err := openTrace(traceRequest{
		workspace:       workspace,
		reportDirectory: config.DefaultReportDirectory,
		requested:       traceDefaultDirectory,
		runID:           runID,
	})
	if err != nil {
		t.Fatalf("openTrace: %v", err)
	}
	t.Cleanup(func() { _ = recording.close() })

	if len(recording.notes) != 1 || recording.notes[0].Kind != trace.NoteTraceGC {
		t.Fatalf("the recording carries %+v, want one %s note", recording.notes, trace.NoteTraceGC)
	}
	if !strings.Contains(recording.notes[0].Detail, "2 recordings") {
		t.Errorf("the note says %q, want what the collection removed", recording.notes[0].Detail)
	}
	left := entriesOf(t, root)
	if len(left) != trace.RetainRuns+1 {
		t.Errorf("the trace root holds %q, want the newest %d and this run's own", left, trace.RetainRuns)
	}
	if !slices.Contains(left, runID) {
		t.Errorf("the run collected its own recording: %q", left)
	}

	quiet, err := openTrace(traceRequest{
		workspace:       workspace,
		reportDirectory: config.DefaultReportDirectory,
		requested:       traceDefaultDirectory,
		runID:           "20260930T120001Z-e5f6",
	})
	if err != nil {
		t.Fatalf("openTrace: %v", err)
	}
	t.Cleanup(func() { _ = quiet.close() })
	if len(quiet.notes) != 0 {
		t.Errorf("a collection that removed nothing recorded %+v", quiet.notes)
	}
}

func recordUnfinished(t *testing.T, root, runID string) string {
	t.Helper()
	sink, err := trace.NewDirSink(root, runID, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink(%s, %s): %v", root, runID, err)
	}
	recorder := trace.New(sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindRun,
		RunID:       runID,
		ToolVersion: Version,
		PID:         os.Getpid(),
		Root:        "workspace",
	})
	recorder.Artifact(trace.ArtifactReportJSON, "reports/mutation/mutation.json")
	if err := sink.Close(); err != nil {
		t.Fatalf("closing the unfinished recording: %v", err)
	}
	return sink.Directory()
}

func entriesOf(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("reading %s: %v", directory, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func describeEvents(events []trace.Event) string {
	var b strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			b.WriteString(err.Error() + "\n")
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func requireReadableRecording(t *testing.T, events []trace.Event) {
	t.Helper()
	path := filepath.Join(t.TempDir(), trace.FileName)
	var b strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding event %d: %v", event.Seq, err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing the stream: %v", err)
	}
	if _, err := trace.Read(path); err != nil {
		t.Errorf("the recording does not read back: %v\n%s", err, b.String())
	}
}

func TestRunHelpMentionsTrace(t *testing.T) {
	code, stdout, stderr := execute(t, "run", "--help")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`run --help` exited %d\n%s", code, stderr)
	}
	for _, needle := range []string{"--trace", traceEnvironmentVariable} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("run --help does not mention %q:\n%s", needle, stdout)
		}
	}
}
