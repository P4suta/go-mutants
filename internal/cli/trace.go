// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/trace"
)

const (
	// traceDirectoryName is the directory under `report.directory` that holds
	// recordings, one per run. See [traceRoot] for why it lives there.
	traceDirectoryName = "trace"

	// traceDefaultDirectory is the value a bare `--trace` carries.
	//
	// pflag expresses an optional value as a default the flag takes when it is
	// written without one, and that default has to be a string — so `--help`
	// renders it, and it has to read there as a word rather than as a path
	// somebody might mean. `--trace=default` is accepted and means exactly what
	// the bare flag means, which is the only reading of that spelling anybody
	// would expect; a directory really named `default` is asked for as
	// `--trace=./default`.
	traceDefaultDirectory = "default"

	// traceTailBytes is how much of the end of a stream is read to find out
	// whether a recording was finished. One event line is far smaller than
	// this, and reading a whole recording to decide whether to delete it would
	// make collection cost what reading costs.
	traceTailBytes = 64 << 10
)

// recordingName matches the directories a run's recording is written into, and
// is half of what [pruneTraceRoot] and `trace clean` will remove. The pattern is
// the engine's, because the id is the engine's; see [engine.RunIDPattern].
var recordingName = regexp.MustCompile(engine.RunIDPattern)

// traceFilesystem is how a recording is written, and is a package variable for
// exactly one reason: "a disk that will not take a write costs the events and
// not the run" is a promise that can only be checked by producing such a disk.
//
// Nothing outside the suite assigns it. The zero value is the real filesystem —
// [trace.Filesystem] resolves each nil operation to the os function of the same
// name — so production reads it and gets what it would have got had the seam
// not been here.
var traceFilesystem trace.Filesystem

// A traceRequest is everything the CLI knows about where a run should record.
type traceRequest struct {
	// workspace is the run's root, which a relative directory resolves against
	// and the refusal rule is stated in terms of.
	workspace string
	// reportDirectory is `report.directory`, relative to the workspace.
	reportDirectory string
	// requested is the `--trace` value: empty for a run that asked for no
	// recording, [traceDefaultDirectory] for a bare flag, a directory
	// otherwise.
	requested string
	// runID is the identity the run is filed under, which names the recording's
	// own directory so that the trace and the report can be paired.
	runID string
	// hooks are the filesystem operations the sink performs. The zero value is
	// the real filesystem; see [traceFilesystem].
	hooks trace.Filesystem
}

// A traceRecording is the account one run keeps of itself.
//
// Every run has one. A run that asked for no recording keeps the last
// [trace.DefaultRingCapacity] events in memory instead of writing them, which
// is a bounded price a run of any length pays once — and it is what makes the
// failure nobody expected explicable, since that is exactly the failure nobody
// thought to ask for a recording of in advance.
type traceRecording struct {
	// sink is what the engine records into, and what this type closes.
	sink trace.Sink
	// ring holds the events of a run that wrote no file, and is nil for one
	// that did. It is what the diagnostics bundle of a failed untraced run is
	// written from.
	ring *trace.MemorySink
	// directory is this run's own recording directory, and empty for a run that
	// wrote no file. It is published in [engine.ReportPublished.TracePath].
	directory string
	// notes are what was learned about this recording before the run began: a
	// directory that was refused, and the collection that ran before this
	// recording was opened. They are handed to the engine, which records them
	// right after the run-start — the moment they are about, and the one place
	// they can go without displacing the run-end a reader relies on being the
	// last line. See [engine.Options.Notes].
	notes []trace.NoteRecord
}

// Events returns the recording a run kept in memory, or nil when it wrote a
// file instead. It is read after the run, by the diagnostics bundle.
func (r *traceRecording) Events() []trace.Event {
	if r == nil || r.ring == nil {
		return nil
	}
	return r.ring.Events()
}

// openTrace opens the recording for one run.
//
// It always returns a usable recording, and returns an error beside it for the
// two ways a directory can be refused: one the run must not write into, and one
// it cannot. Both are warnings rather than failures — the run goes on recording
// into memory, and the reason travels into the recording as a
// `trace-unavailable` note — because a diagnostic that can stop a run inverts
// the point of having one.
//
// Collection happens here rather than at the end of the run, and that is what
// makes it safe. Pruning before this run's own directory exists means the
// directory cannot be a candidate for its own collector, so the rule needs no
// exception carved out for the recording being written; and the note it
// produces is recorded by the run that did the collecting, at the moment it did
// it.
func openTrace(request traceRequest) (*traceRecording, error) {
	if strings.TrimSpace(request.requested) == "" {
		return ringRecording(), nil
	}
	root, err := traceRoot(request.workspace, request.reportDirectory, request.requested)
	if err != nil {
		return refusedRecording(err), err
	}
	collected := collectionNotes(root)
	sink, err := trace.NewDirSink(root, request.runID, request.hooks)
	if err != nil {
		refusal := &Error{
			Code:    CodeTraceUnavailable,
			Message: "the trace directory under " + root + " could not be opened, so this run records in memory instead",
			Err:     err,
			Hint:    "name a directory go-mutants can create with --trace=DIR, or drop --trace",
		}
		recording := refusedRecording(refusal)
		recording.notes = append(recording.notes, collected...)
		return recording, refusal
	}
	return &traceRecording{sink: sink, directory: sink.Directory(), notes: collected}, nil
}

// ringRecording is what a run that writes no file records into.
//
// [trace.Digested] is not optional around a ring. A ring exists to cost a
// bounded amount of memory, and captured output grows with the run rather than
// with the ring; the wrapper strips those bytes and leaves the size and the
// digest a reader joins on.
func ringRecording() *traceRecording {
	ring := trace.NewMemorySink(trace.DefaultRingCapacity)
	return &traceRecording{sink: trace.Digested(ring), ring: ring}
}

// refusedRecording is [ringRecording] with the reason there is no file written
// into it, so that the account of the run says why it is the account it is.
func refusedRecording(refusal error) *traceRecording {
	recording := ringRecording()
	recording.notes = append(recording.notes,
		trace.NoteRecord{Kind: trace.NoteTraceUnavailable, Detail: refusal.Error()})
	return recording
}

// collectionNotes prunes a trace root to the newest [trace.RetainRuns]
// recordings and says what it did, as the note the run will record.
//
// Every byte a run writes has an owner and a collector, and this is the
// collector for the diagnostic exhaust: without it a trace root grows for as
// long as somebody keeps asking for recordings. A failure is a note and never
// the exit status — the run is about to measure what it was asked to measure,
// and a diagnostic directory that could not be tidied is no reason to tell a CI
// job the tool broke — and a collection that removed nothing says nothing,
// because a note in every recording reporting that nothing happened is noise in
// the one place a reader is looking for signal.
func collectionNotes(root string) []trace.NoteRecord {
	removed, err := pruneTraceRoot(root, retention{keep: trace.RetainRuns})
	switch {
	case err != nil:
		return []trace.NoteRecord{{Kind: trace.NoteTraceGC, Detail: "collecting " + root + ": " + err.Error()}}
	case len(removed) > 0:
		return []trace.NoteRecord{{Kind: trace.NoteTraceGC, Detail: fmt.Sprintf(
			"removed %s from %s, keeping the newest %d",
			countNoun(len(removed), "recording"), root, trace.RetainRuns)}}
	default:
		return nil
	}
}

// close finishes the recording: the stream is synced and closed. It is safe to
// call twice, which is what lets the run close it on the path it took and on
// its deferred one.
func (r *traceRecording) close() error {
	if r == nil || r.sink == nil {
		return nil
	}
	return r.sink.Close()
}

// traceRoot decides where a run's recordings go, and refuses the directories
// that would cost the run its evidence.
//
// The default is `<workspace>/<report.directory>/trace/`, and that is not an
// arbitrary corner. internal/snapshot digests the workspace and excludes
// `report.directory` and nothing else inside it, so it is the one place in a
// user's tree a file may appear during a run. A recording grows while the run
// measures: a stream written anywhere else in the workspace is a file that
// changed under the run, and the run would fail with drift it caused itself.
// Refusing the directory is what keeps the trace from failing the run.
//
// A directory outside the workspace is nobody's source and is always accepted.
// A relative one resolves against the workspace rather than against the process
// working directory, so that `--trace=recordings` means the same thing wherever
// the command was typed.
func traceRoot(workspace, reportDirectory, requested string) (string, error) {
	reports := filepath.Join(workspace, filepath.FromSlash(reportDirectory))
	directory := strings.TrimSpace(requested)
	if directory == "" || directory == traceDefaultDirectory {
		return filepath.Join(reports, traceDirectoryName), nil
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(workspace, directory)
	}
	directory = filepath.Clean(directory)
	if digestedBySnapshot(workspace, reports, directory) {
		return "", &Error{
			Code: CodeTraceUnavailable,
			Message: "the trace directory " + directory + " is inside the workspace and outside " + reports +
				", where a recording would be read as part of the tree and reported as drift",
			Hint: "record under " + reports + ", or name a directory outside the workspace",
		}
	}
	return directory, nil
}

// digestedBySnapshot reports whether a directory would land where the snapshot
// reads: inside the workspace and outside the report directory.
//
// It is asked twice, of the path as it was written and of the path with every
// symbolic link on the way to it followed, because the refusal is a statement
// about where the stream lands rather than about how it was spelled. A name
// outside the workspace that resolves into it would put the stream in the tree
// the snapshot digests, and a link is the one spelling that hides it.
func digestedBySnapshot(workspace, reports, directory string) bool {
	return insideButNotUnder(workspace, reports, directory) ||
		insideButNotUnder(existingPathOf(workspace), existingPathOf(reports), existingPathOf(directory))
}

func insideButNotUnder(workspace, reports, directory string) bool {
	return under(workspace, directory) && !under(reports, directory)
}

// under reports whether path is base or sits beneath it.
func under(base, path string) bool {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// existingPathOf resolves the longest prefix of a path that exists and puts the
// rest back on the end.
//
// Only what exists can be resolved, and that is exactly the part that decides
// where the directory the sink is about to create will land. It is goatest's
// rule, spelled the same way, because it is the same refusal.
func existingPathOf(path string) string {
	cleaned := filepath.Clean(path)
	remainder := ""
	for current := cleaned; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// A retention is how much of a trace root survives collection.
type retention struct {
	// keep is how many of the newest candidates are left behind.
	keep int
	// unfinished makes a recording with no run-end a candidate. It is off for
	// the collector a run runs and on only for `trace clean --all`; see
	// [staleRecordings].
	unfinished bool
}

// pruneTraceRoot removes the recordings in root that a retention collects, and
// returns what it removed, oldest first.
func pruneTraceRoot(root string, keep retention) ([]string, error) {
	stale, err := staleRecordings(root, keep)
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0, len(stale))
	for _, name := range stale {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	return removed, nil
}

// staleRecordings names the recordings a retention collects, oldest first. It
// is the one statement of the rule, so that what `trace clean` measures before
// it deletes is exactly what [pruneTraceRoot] deletes.
//
// A recording whose stream does not end with a run-end is not a candidate
// unless the caller asked for one, and that exception is the point of the rule.
// Such a recording is a run still in progress or a run that died, and the
// second is the recording a reader most wants: a collector that removed the
// account of the crash while keeping ten accounts of runs that went fine would
// be collecting exactly backwards. It is also what makes a live run safe from a
// concurrent collector, rather than only from being the newest name in the root.
// `trace clean --all` is how somebody who has read them says so.
func staleRecordings(root string, keep retention) ([]string, error) {
	recordings, err := recordingsIn(root)
	if err != nil {
		return nil, err
	}
	candidates := recordings
	if !keep.unfinished {
		candidates = nil
		for _, name := range recordings {
			if finishedRecording(filepath.Join(root, name, trace.FileName)) {
				candidates = append(candidates, name)
			}
		}
	}
	if keep.keep < 0 || len(candidates) <= keep.keep {
		return nil, nil
	}
	return candidates[:len(candidates)-keep.keep], nil
}

// recordingsIn names the recordings in a trace root, oldest first.
//
// os.ReadDir sorts by name and a run id sorts by the moment it was minted, so
// the order of the listing is the order of the runs. A root that does not exist
// holds no recordings, which is an answer rather than a failure: it is what a
// workspace that has never traced a run looks like.
func recordingsIn(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recordings []string
	for _, entry := range entries {
		if !entry.IsDir() || !recordingName.MatchString(entry.Name()) {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(root, entry.Name(), trace.FileName)); statErr != nil {
			continue
		}
		recordings = append(recordings, entry.Name())
	}
	slices.Sort(recordings)
	return recordings, nil
}

// finishedRecording reports whether a stream ends with its run-end event, which
// is what tells a recording somebody can read to the end from one that stops.
//
// Only the tail is read. A recording is written to be read, not to be walked
// every time a run decides what to collect, and the question here is about one
// line. A tail that does not decode — the half-written line a killed process
// leaves — is not a run-end, which is the right answer: that recording is
// exactly the one collection must leave alone.
func finishedRecording(stream string) bool {
	file, err := os.Open(stream)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	if offset := info.Size() - traceTailBytes; offset > 0 {
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			return false
		}
	}
	tail, err := io.ReadAll(file)
	if err != nil {
		return false
	}
	var last []byte
	for line := range bytes.SplitSeq(tail, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			last = line
		}
	}
	var event struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(last, &event) == nil && event.Type == trace.TypeRunEnd
}

// directorySize is what one recording takes up: its stream and the output its
// events digested. A file that cannot be measured contributes nothing rather
// than failing a listing, since a size is a convenience and the listing is the
// answer.
func directorySize(directory string) int64 {
	var total int64
	_ = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal
		}
		if info, statErr := entry.Info(); statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
