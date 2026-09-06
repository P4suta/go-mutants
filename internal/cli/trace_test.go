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

// resolvedTempDir is a temporary directory with every symbolic link on the way
// to it already followed.
//
// macOS puts a test's temporary directory under /var, which is a link to
// /private/var, so a path built from t.TempDir() and the same path read back
// through [filepath.EvalSymlinks] are two different strings for one directory.
// Every test here compares one against the other, and the ones about symbolic
// links have to be about the link they created rather than about that one.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temporary directory: %v", err)
	}
	return resolved
}

// recordInto drives one whole recording through the sink a run would be given:
// a recorder over it, the notes the CLI decided before the run — which is what
// the engine does with them — one event, and the run-end that closes the
// account.
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

// TestTraceDirectoryDefaultsToTheReportDirectory pins where a recording lands
// when nobody names a directory.
//
// It is under `report.directory` and not beside it, because that is the one
// directory in a workspace the snapshot already excludes: a stream written
// anywhere else in the tree grows while the run reads the tree, and the run
// would report its own recording as drift.
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

	// And the run directory underneath it is the run's own id, so that the
	// recording and the report the run filed can be paired afterwards.
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

// TestTraceDirectoryInsideTheWorkspaceOutsideTheReportDirectoryIsRefused is the
// rule that keeps a diagnostic from costing a run its evidence.
//
// internal/snapshot digests the workspace and excludes `report.directory` and
// nothing else inside it. A recording grows while the run is measuring, so a
// stream written anywhere else in the tree is a file that changed during the
// run — and the run would fail with drift it caused itself. Refusing the
// directory is what keeps the trace from failing the run.
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

// TestTraceDirectoryRefusalIsSymlinkAware keeps the refusal a statement about
// where the directory lands rather than about how it was spelled.
//
// A name outside the workspace that resolves into it would put the stream in
// the tree the snapshot digests, which is exactly what the refusal exists to
// prevent — and a link is the one spelling that hides it.
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

	// The link is outside the workspace by name and inside it by resolution:
	// only the part of the path that already exists can be resolved, and that
	// is the part that decides where the directory the sink creates will land.
	if got, err := traceRoot(root, config.DefaultReportDirectory, filepath.Join(alias, "recordings")); err == nil {
		t.Errorf("traceRoot through a link into the workspace = %q, want a refusal", got)
	}

	// And the other direction: a name under the report directory that resolves
	// out of the workspace is somebody's deliberate arrangement, not a mistake.
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

// TestARefusedTraceFallsBackToTheRing is the promise that a trace never costs a
// run.
//
// Three things are refused — a directory inside the workspace, one that cannot
// be created, and a run directory another recording already owns — and each
// leaves the same three things behind: a run that records into memory as every
// untraced run does, a note saying why there is no file, and a warning the user
// can read.
//
// The note travels to the engine rather than into the sink here, which is what
// puts it immediately after the run-start when the run records it. The CLI never
// writes an event of its own into a recording: sequence numbers, timestamps and
// the position of the last line all belong to the recorder, and a caller
// splicing an event into a stream it does not number is a caller that can break
// every one of them. [recordInto] does with the notes what the engine does.
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
	// The third case needs the run directory to exist already, which is what a
	// second recording of one run id would collide with.
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
			// The account still reads as one recording: the note goes in ahead
			// of the run-end rather than after it, because a reader who found
			// the last line has read the whole run.
			if last := events[len(events)-1]; last.Type != trace.TypeRunEnd {
				t.Errorf("the recording ends with a %s, want the run-end last:\n%s", last.Type, describeEvents(events))
			}
			requireReadableRecording(t, events)
		})
	}
}

// TestTracePruningKeepsTheNewestRunsAndNeverTouchesForeignNames is the
// collector half of "every byte a run writes has an owner and a collector".
//
// Collection is what keeps a diagnostic directory from growing without limit,
// and the rule that makes it safe is that it removes only what it can prove is
// a finished go-mutants recording: a directory named by [engine.RunIDPattern]
// holding a stream that ends with its run-end. A directory somebody else put
// there, a file, a run-id-shaped directory with no stream, and a recording no
// run finished are all left exactly as they were found.
//
// The recordings are real ones, written through a recorder into a [trace.DirSink]
// rather than faked with a file of the right name. What makes a recording
// collectable is now a property of its last line, and a fixture that only looked
// like one would have made this test agree with any implementation at all.
func TestTracePruningKeepsTheNewestRunsAndNeverTouchesForeignNames(t *testing.T) {
	root := filepath.Join(resolvedTempDir(t), "trace")

	var recordings []string
	for day := range trace.RetainRuns + 2 {
		id := fmt.Sprintf("202609%02dT120000Z-a1b2", day+1)
		recordings = append(recordings, id)
		record(t, root, id, false, nil)
	}
	// Everything that is not a recording, and must survive being beside twelve
	// of them.
	foreign := []string{"notes", "20260907T120000Z-zzzz"}
	for _, name := range foreign {
		if err := os.MkdirAll(filepath.Join(root, name, "keep"), 0o755); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}
	// A run-id-shaped directory with no stream at all is not a recording: it is
	// a run that is about to open one, and this collector has nothing to say
	// about it.
	if err := os.MkdirAll(filepath.Join(root, "20260908T120000Z-c3d4"), 0o755); err != nil {
		t.Fatalf("creating the streamless directory: %v", err)
	}
	// And a recording whose stream stops without a run-end: a run in progress,
	// or one that died. It is the oldest name in the root, so it would be the
	// first thing collected if the rule were age alone — and it is the recording
	// somebody most wants to keep.
	unfinished := "20260801T120000Z-dead"
	recordUnfinished(t, root, unfinished)
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("mine"), 0o600); err != nil {
		t.Fatalf("writing the stray file: %v", err)
	}

	removed, err := pruneTraceRoot(root, retention{keep: trace.RetainRuns})
	if err != nil {
		t.Fatalf("pruneTraceRoot: %v", err)
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

// TestTraceFlagRequiresAnEqualsSign reports the one mistake a correct-looking
// command line produces, and says how to write it instead.
//
// `--trace` takes an optional value, which pflag can only express as
// `--trace=DIR`: written with a space, the directory becomes a positional
// argument and the run would record into the default directory instead of the
// one the user named. It is the mistake `--changed` already makes possible, so
// it gets the same answer.
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

// TestAnEmptyTraceDirectoryIsRefused is the one spelling of the flag that can
// only be a mistake.
//
// `--trace=` is a typed flag with nothing after the equals sign, which in a
// script is almost always a shell variable that expanded to nothing. Reading it
// as a bare `--trace` would record somewhere the author did not name, and
// reading it as the workspace root would be refused for a reason that has
// nothing to do with the mistake they made. An empty GO_MUTANTS_TRACE is the
// opposite case and stays "off": that is how a job switches an inherited
// request back off, and it never becomes a flag at all.
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
	// And the environment's empty value is not this: it asks for nothing at all
	// and never reaches the command line.
	if flag, requested := traceFlag(""); requested {
		t.Errorf("GO_MUTANTS_TRACE= asked for %q, want no flag", flag)
	}
}

// TestCollectionRunsWhenTheRecordingIsOpened pins when the collector runs, which
// is what makes it safe.
//
// Pruning before this run's own directory exists means the directory cannot be a
// candidate for its own collector — the rule needs no exception carved out for
// the recording being written — and the note it produces belongs to the run that
// did the collecting, at the moment it did it.
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
	// The newest ten of the old ones, and this run's own, which was created
	// after the collector had already decided.
	left := entriesOf(t, root)
	if len(left) != trace.RetainRuns+1 {
		t.Errorf("the trace root holds %q, want the newest %d and this run's own", left, trace.RetainRuns)
	}
	if !slices.Contains(left, runID) {
		t.Errorf("the run collected its own recording: %q", left)
	}

	// A root with nothing to collect says nothing. A note in every recording
	// reporting that nothing happened is noise where a reader is looking for
	// signal.
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

// recordUnfinished writes the recording a killed run leaves: a stream that opens
// and stops, with no run-end on the end of it.
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

// entriesOf is the names in a directory.
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

// describeEvents renders a recording for a failure message.
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

// requireReadableRecording writes the events out as a stream and reads them
// back through the published reader, which is the check a note the CLI added
// has to pass: a recording is a contract, and an event inserted into one has to
// keep it.
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
