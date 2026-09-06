// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/go-mutants/trace"
)

// errHook is what a stubbed filesystem hook fails with.
var errHook = errors.New("stubbed filesystem failure")

func TestDirSinkCreatesItsRunDirectoryAndAppendsJSONL(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "trace")
	sink, err := trace.NewDirSink(root, fixtureRunID, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink: %v", err)
	}
	if want := filepath.Join(root, fixtureRunID); sink.Directory() != want {
		t.Errorf("Directory() = %q, want %q", sink.Directory(), want)
	}
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.Artifact(trace.ArtifactTrace, sink.Directory())
	recorder.RunEnd(fixtureVerdict, 0, nil)
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readLines(t, filepath.Join(sink.Directory(), trace.FileName))
	if len(lines) != 3 {
		t.Fatalf("the stream holds %d lines, want 3: %v", len(lines), lines)
	}
	for i, line := range lines {
		var event trace.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %v", i+1, err)
		}
		if event.Seq != int64(i+1) {
			t.Errorf("line %d carries seq %d", i+1, event.Seq)
		}
	}
	if sink.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want 0", sink.Dropped())
	}
}

func TestDirSinkRefusesARunDirectoryThatAlreadyExists(t *testing.T) {
	t.Parallel()

	// Two recordings in one directory would append to each other's stream and
	// write over each other's preserved output, because everything in a
	// recording is numbered from its first event. Refusing the directory is
	// what keeps two processes tracing one workspace out of each other's
	// recording.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, fixtureRunID), 0o755); err != nil {
		t.Fatal(err)
	}
	sink, err := trace.NewDirSink(root, fixtureRunID, trace.Filesystem{})
	if err == nil {
		t.Fatalf("NewDirSink accepted an existing run directory and returned %v", sink)
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("NewDirSink failed with %v, want an already-exists error", err)
	}
}

func TestDirSinkMakesEachLineReadableBeforeClose(t *testing.T) {
	t.Parallel()

	// A run that hangs, is interrupted, or is killed leaves a readable prefix:
	// only run-end is missing. That is the whole reason a completed line is
	// written rather than buffered until shutdown.
	root := t.TempDir()
	sink, err := trace.NewDirSink(root, fixtureRunID, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink: %v", err)
	}
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.Note(trace.NoteWarning, fixtureNoteCode, fixtureNoteDetail)

	stream := filepath.Join(sink.Directory(), trace.FileName)
	lines := readLines(t, stream)
	if len(lines) != 2 {
		t.Fatalf("before Close the stream holds %d lines, want 2", len(lines))
	}
	var last trace.Event
	if err := json.Unmarshal([]byte(lines[1]), &last); err != nil {
		t.Fatalf("the second line is not JSON: %v", err)
	}
	if last.Type != trace.TypeNote {
		t.Errorf("the second line is %q, want %q", last.Type, trace.TypeNote)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDirSinkPreservesOutputBesideTheStreamAsOutputSeqTxt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sink, err := trace.NewDirSink(root, fixtureRunID, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink: %v", err)
	}
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	output := []byte("--- FAIL: TestScore (0.00s)\n    score_test.go:41: 3 != 4\n")
	record := fixtureExecRecord()
	record.Output = output
	seq := recorder.Exec(record)
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	event := eventAt(t, sink, 1)
	want := trace.OutputDirectoryName + "/" + itoa(seq) + ".txt"
	if event.Exec.OutputPath != want {
		t.Fatalf("output_path = %q, want %q", event.Exec.OutputPath, want)
	}
	if event.Exec.OutputTruncated {
		t.Error("output_truncated is set for an output that fitted")
	}
	preserved, err := os.ReadFile(filepath.Join(sink.Directory(), filepath.FromSlash(want)))
	if err != nil {
		t.Fatalf("reading the preserved output: %v", err)
	}
	if !slices.Equal(preserved, output) {
		t.Errorf("the preserved output is %q, want %q", preserved, output)
	}
	digest := sha256.Sum256(output)
	if event.Exec.OutputSHA256 != hex.EncodeToString(digest[:]) {
		t.Errorf("output_sha256 = %q, which does not cover the preserved bytes", event.Exec.OutputSHA256)
	}
	if strings.Contains(readFile(t, filepath.Join(sink.Directory(), trace.FileName)), "score_test.go") {
		t.Error("the output bytes reached the stream")
	}
}

func TestDirSinkTruncatesAPreservedOutputAtOneMiBWithTheMarker(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sink, err := trace.NewDirSink(root, fixtureRunID, trace.Filesystem{})
	if err != nil {
		t.Fatalf("NewDirSink: %v", err)
	}
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	output := make([]byte, trace.OutputFileLimit+4096)
	for i := range output {
		output[i] = 'x'
	}
	record := fixtureExecRecord()
	record.Output = output
	seq := recorder.Exec(record)
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	event := eventAt(t, sink, 1)
	if !event.Exec.OutputTruncated {
		t.Error("output_truncated is not set for an output that did not fit")
	}
	// The digest and the byte count always cover the whole capture, whether or
	// not the file was cut: a reader comparing two runs compares what the
	// commands produced rather than what fitted.
	if event.Exec.OutputBytes != len(output) {
		t.Errorf("output_bytes = %d, want %d", event.Exec.OutputBytes, len(output))
	}
	digest := sha256.Sum256(output)
	if event.Exec.OutputSHA256 != hex.EncodeToString(digest[:]) {
		t.Error("output_sha256 covers the truncated file rather than the whole capture")
	}
	preserved, err := os.ReadFile(filepath.Join(sink.Directory(), trace.OutputDirectoryName, itoa(seq)+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := trace.OutputFileLimit + len(trace.TruncationMarker); len(preserved) != want {
		t.Fatalf("the preserved file is %d bytes, want %d", len(preserved), want)
	}
	if !strings.HasSuffix(string(preserved), trace.TruncationMarker) {
		t.Error("the preserved file does not end with the truncation marker")
	}
}

func TestDirSinkKeepsTheEventWhenPreservingOutputFails(t *testing.T) {
	t.Parallel()

	// Preserving output is best effort, and a failure costs the path rather
	// than the event: the digest is still the join key, and the command is
	// still in the account of the run.
	root := t.TempDir()
	sink, err := trace.NewDirSink(root, fixtureRunID, trace.Filesystem{
		WriteFile: func(string, []byte, fs.FileMode) error { return errHook },
	})
	if err != nil {
		t.Fatalf("NewDirSink: %v", err)
	}
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.Exec(fixtureExecRecord())
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readLines(t, filepath.Join(sink.Directory(), trace.FileName))
	if len(lines) != 2 {
		t.Fatalf("the stream holds %d lines, want 2", len(lines))
	}
	var event trace.Event
	if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
		t.Fatal(err)
	}
	if event.Exec.OutputPath != "" {
		t.Errorf("output_path = %q, want it absent", event.Exec.OutputPath)
	}
	if event.Exec.OutputSHA256 == "" {
		t.Error("the event lost its digest along with the file")
	}
	if sink.Dropped() != 0 {
		t.Errorf("Dropped() = %d: a lost output file was counted as a lost event", sink.Dropped())
	}
}

func TestDirSinkCountsTheEventsItCouldNotWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stream := &brokenFile{}
	sink, err := trace.NewDirSink(root, fixtureRunID, trace.Filesystem{
		OpenAppend: func(string, fs.FileMode) (trace.File, error) { return stream, nil },
	})
	if err != nil {
		t.Fatalf("NewDirSink: %v", err)
	}
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.Note(trace.NoteWarning, fixtureNoteCode, fixtureNoteDetail)
	recorder.RunEnd(fixtureVerdict, 0, nil)

	// run-start, the note and run-end: three events the stream refused, and
	// run-end is the line that would have said so had it been writable.
	if sink.Dropped() != 3 {
		t.Errorf("Dropped() = %d, want 3", sink.Dropped())
	}
	if err := sink.Close(); err == nil {
		t.Error("Close reported success for a stream it could not sync")
	}
	// An event that arrives after Close is a drop as well, not a panic.
	if err := sink.Emit(trace.Event{Seq: 99, Type: trace.TypeNote}); err == nil {
		t.Error("Emit after Close reported success")
	}
	if sink.Dropped() != 4 {
		t.Errorf("Dropped() = %d after a post-Close emit, want 4", sink.Dropped())
	}
}

// brokenFile is a stream that cannot be written, synced, or closed.
type brokenFile struct{}

func (*brokenFile) Write([]byte) (int, error) { return 0, errHook }
func (*brokenFile) Sync() error               { return errHook }
func (*brokenFile) Close() error              { return errHook }

func TestMemorySinkDropsTheOldestWhenFullAndKeepsTheLastSlotForRunEnd(t *testing.T) {
	t.Parallel()

	const capacity = 3
	sink := trace.NewMemorySink(capacity)
	for seq := int64(1); seq <= 5; seq++ {
		if err := sink.Emit(trace.Event{Seq: seq, Type: trace.TypeArtifact}); err != nil {
			t.Fatal(err)
		}
	}
	// Two ordinary events fit, because the third slot is held back.
	if got := seqsOf(sink.Events()); !slices.Equal(got, []int64{4, 5}) {
		t.Fatalf("the ring holds %v, want the newest two", got)
	}
	if sink.Dropped() != 3 {
		t.Fatalf("Dropped() = %d, want 3", sink.Dropped())
	}
	if err := sink.Emit(trace.Event{Seq: 6, Type: trace.TypeRunEnd}); err != nil {
		t.Fatal(err)
	}
	if got := seqsOf(sink.Events()); !slices.Equal(got, []int64{4, 5, 6}) {
		t.Errorf("the ring holds %v, want the run-end kept beside the newest two", got)
	}
	if sink.Dropped() != 3 {
		t.Errorf("Dropped() = %d after run-end, want 3: the ring dropped an event to make room for it", sink.Dropped())
	}
}

func seqsOf(events []trace.Event) []int64 {
	numbers := make([]int64, 0, len(events))
	for _, event := range events {
		numbers = append(numbers, event.Seq)
	}
	return numbers
}

func TestMemorySinkClonesOnTheWayInAndOut(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	record := fixtureExecRecord()
	record.Argv = []string{"go", "test"}
	if err := sink.Emit(trace.Event{Seq: 1, Type: trace.TypeExec, Exec: &record}); err != nil {
		t.Fatal(err)
	}
	// A caller that reuses its record must not be able to rewrite history.
	record.Argv[0] = "rewritten"
	record.Kind = trace.ExecKindVerify
	if held := sink.Events()[0].Exec; held.Argv[0] != "go" || held.Kind != trace.ExecKindMutantRun {
		t.Errorf("a later write reached the ring: %v %q", held.Argv, held.Kind)
	}
	// And a reader that mutates what it was handed must not reach the ring
	// either.
	handed := sink.Events()[0]
	handed.Exec.Argv[1] = "vet"
	if again := sink.Events()[0].Exec; again.Argv[1] != "test" {
		t.Errorf("a reader's write reached the ring: %v", again.Argv)
	}
}

func TestTeeSinkFansOutAndSumsDrops(t *testing.T) {
	t.Parallel()

	// One sink that counts its own losses, one that only fails, and one nil
	// that the tee must drop rather than dereference.
	ring := trace.NewMemorySink(0)
	silent := &countingSink{fail: true}
	tee := trace.NewTeeSink(ring, silent, nil)
	recorder := trace.New(tee, fixtureClock(), fixtureStartRecord())
	recorder.Note(trace.NoteWarning, fixtureNoteCode, fixtureNoteDetail)
	recorder.RunEnd(fixtureVerdict, 0, nil)

	if len(ring.Events()) != 3 {
		t.Fatalf("the ring holds %d events, want 3", len(ring.Events()))
	}
	if silent.emitted != 3 {
		t.Fatalf("the second sink saw %d events, want 3", silent.emitted)
	}
	// The ring lost nothing; the failing sink lost all three, and the tee
	// counts them because that sink does not count them itself.
	if tee.Dropped() != 3 {
		t.Errorf("Dropped() = %d, want 3", tee.Dropped())
	}
	last := ring.Events()[2]
	if last.Run.EventsDropped != 2 {
		t.Errorf("run-end reported %d drops, want the 2 taken before it was written", last.Run.EventsDropped)
	}
	if err := tee.Close(); err == nil {
		t.Error("Close reported success although a sink failed to close")
	}
	if !silent.closed {
		t.Error("Close did not reach every sink")
	}
}

// countingSink counts what it is handed and optionally refuses all of it.
type countingSink struct {
	mutex   sync.Mutex
	fail    bool
	emitted int
	closed  bool
	events  []trace.Event
}

func (sink *countingSink) Emit(event trace.Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.emitted++
	sink.events = append(sink.events, event)
	if sink.fail {
		return errHook
	}
	return nil
}

func (sink *countingSink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.closed = true
	if sink.fail {
		return errHook
	}
	return nil
}

func TestDigestedSinkStripsExecOutputAndMutantOutputTail(t *testing.T) {
	t.Parallel()

	// A bounded ring must cost a bounded amount of memory, and captured output
	// grows with the run rather than with the ring. The digest and the byte
	// count survive, which is what a reader joins on.
	inner := &countingSink{}
	digested := trace.Digested(inner)
	recorder := trace.New(digested, fixtureClock(), fixtureStartRecord())
	recorder.Exec(fixtureExecRecord())
	recorder.MutantExec(fixtureMutantRecord())

	if len(inner.events) != 3 {
		t.Fatalf("the inner sink saw %d events, want 3", len(inner.events))
	}
	exec := inner.events[1].Exec
	if len(exec.Output) != 0 {
		t.Errorf("the exec event still carries %d output bytes", len(exec.Output))
	}
	if exec.OutputBytes == 0 || exec.OutputSHA256 == "" {
		t.Error("the exec event lost the digest along with the bytes")
	}
	if tail := inner.events[2].Mutant.OutputTail; tail != "" {
		t.Errorf("the mutant event still carries an output tail %q", tail)
	}
	if inner.events[2].Mutant.ID != fixtureMutantID {
		t.Error("the mutant event lost its identity")
	}
	if trace.Digested(nil) != nil {
		t.Error("Digested(nil) is not the disabled sink")
	}
	if err := digested.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !inner.closed {
		t.Error("Close did not reach the inner sink")
	}
}

// eventAt decodes one line of a sink's stream.
func eventAt(t *testing.T, sink *trace.DirSink, index int) trace.Event {
	t.Helper()
	lines := readLines(t, filepath.Join(sink.Directory(), trace.FileName))
	if index >= len(lines) {
		t.Fatalf("the stream holds %d lines, want line %d", len(lines), index+1)
	}
	var event trace.Event
	if err := json.Unmarshal([]byte(lines[index]), &event); err != nil {
		t.Fatalf("line %d is not JSON: %v", index+1, err)
	}
	return event
}

// readLines returns the non-empty lines of a file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return lines
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// itoa spells a sequence number the way a preserved output file is named.
func itoa(seq int64) string { return strconv.FormatInt(seq, 10) }

// TestDigestedSinkReportsTheDropsOfASinkThatDoesNotCountItsOwn is why the
// wrapper keeps a counter at all.
//
// A digested ring is what an untraced run records into, so the wrapper sits
// between the recorder and everything that could refuse an event. A wrapper
// that answered zero would make the recorder prefer that zero over its own
// count of refusals — and a `TeeSink` around it would stop counting the branch
// too, because it defers to any sink that claims to count itself. The result
// would be a recording that lost every event and said it lost none, which is
// the one failure the accounting exists to prevent.
func TestDigestedSinkReportsTheDropsOfASinkThatDoesNotCountItsOwn(t *testing.T) {
	t.Parallel()

	refusing := &countingSink{fail: true}
	recorder := trace.New(trace.Digested(refusing), fixtureClock(), fixtureStartRecord())
	recorder.Artifact(trace.ArtifactTrace, fixtureArtifactPath)
	recorder.Artifact(trace.ArtifactTrace, fixtureArtifactPath)
	recorder.RunEnd(fixtureVerdict, 0, nil)

	// run-start and the two artifacts were refused before run-end was written.
	if got := lastRunRecord(t, refusing); got.EventsDropped != 3 || got.EventsEmitted != 0 {
		t.Errorf("run-end reported %d dropped and %d emitted, want 3 and 0", got.EventsDropped, got.EventsEmitted)
	}

	// And the same wrapper inside a tee, where the tee must not double-count
	// what the wrapper already counts and must not lose it either.
	ring := trace.NewMemorySink(0)
	second := &countingSink{fail: true}
	tee := trace.NewTeeSink(ring, trace.Digested(second))
	teed := trace.New(tee, fixtureClock(), fixtureStartRecord())
	teed.Artifact(trace.ArtifactTrace, fixtureArtifactPath)
	teed.RunEnd(fixtureVerdict, 0, nil)
	if tee.Dropped() != 3 {
		t.Errorf("the tee reported %d drops, want the 3 the digested branch refused", tee.Dropped())
	}
	last := ring.Events()[len(ring.Events())-1]
	if last.Run.EventsDropped != 2 {
		t.Errorf("run-end reported %d drops, want the 2 taken before it was written", last.Run.EventsDropped)
	}

	// A wrapper around a sink that does count itself reports both: the ring's
	// own overflow and whatever the wrapper could not hand it.
	bounded := trace.NewMemorySink(2)
	wrapped := trace.Digested(bounded)
	for seq := int64(1); seq <= 4; seq++ {
		if err := wrapped.Emit(trace.Event{Seq: seq, Type: trace.TypeArtifact}); err != nil {
			t.Fatal(err)
		}
	}
	dropper, ok := wrapped.(trace.Dropper)
	if !ok {
		t.Fatal("Digested does not count its drops at all")
	}
	if got := dropper.Dropped(); got != bounded.Dropped() {
		t.Errorf("the wrapper reported %d drops, want the ring's %d", got, bounded.Dropped())
	}
	if bounded.Dropped() == 0 {
		t.Fatal("the bounded ring dropped nothing, so the test proves nothing")
	}
}

// lastRunRecord is the run payload of the last event a sink was handed, even
// when it refused every one of them.
func lastRunRecord(t *testing.T, sink *countingSink) trace.RunRecord {
	t.Helper()
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if len(sink.events) == 0 {
		t.Fatal("the sink was handed no events")
	}
	last := sink.events[len(sink.events)-1]
	if last.Run == nil {
		t.Fatalf("the last event was a %s rather than a run-end", last.Type)
	}
	return *last.Run
}

// TestDigestedSinkClearsThePreservedOutputMarkersWithTheBytes pins the shape a
// ring-then-file recording must never have.
//
// `output_truncated` and `output_path` describe a file the sink that preserved
// the bytes wrote. Stripping the bytes and keeping those two would leave an
// event claiming a 1 MiB truncation of a file that is not there — and when the
// ring is later written out as a diagnostics bundle, the sink writing it has no
// bytes to preserve and so no path to set, so the claim would survive into a
// recording nothing could satisfy.
func TestDigestedSinkClearsThePreservedOutputMarkersWithTheBytes(t *testing.T) {
	t.Parallel()

	inner := &countingSink{}
	digested := trace.Digested(inner)
	preserved := fixtureExecRecord()
	preserved.Output = nil
	preserved.OutputBytes = trace.OutputFileLimit + 1
	preserved.OutputSHA256 = fixtureDigest
	preserved.OutputTruncated = true
	preserved.OutputPath = trace.OutputDirectoryName + "/7.txt"
	if err := digested.Emit(trace.Event{Seq: 1, Type: trace.TypeExec, Exec: &preserved}); err != nil {
		t.Fatal(err)
	}

	got := inner.events[0].Exec
	if got.OutputTruncated || got.OutputPath != "" {
		t.Errorf("the stripped event still claims a preserved file: truncated=%v path=%q",
			got.OutputTruncated, got.OutputPath)
	}
	if got.OutputBytes != trace.OutputFileLimit+1 || got.OutputSHA256 != fixtureDigest {
		t.Error("the stripped event lost the size and the digest, which are what a reader joins on")
	}
	// The caller's own record is untouched, because a sink that rewrote what it
	// was handed would change what every other sink of a tee sees.
	if !preserved.OutputTruncated || preserved.OutputPath == "" {
		t.Error("Digested wrote into the caller's record")
	}
}
