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
	traceDirectoryName = "trace"

	traceDefaultDirectory = "default"

	traceTailBytes = 64 << 10
)

var recordingName = regexp.MustCompile(engine.RunIDPattern)

var traceFilesystem trace.Filesystem

type traceRequest struct {
	workspace       string
	reportDirectory string
	requested       string
	runID           string
	hooks           trace.Filesystem
}

type traceRecording struct {
	sink      trace.Sink
	ring      *trace.MemorySink
	directory string
	notes     []trace.NoteRecord
}

func (r *traceRecording) Events() []trace.Event {
	if r == nil || r.ring == nil {
		return nil
	}
	return r.ring.Events()
}

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

func ringRecording() *traceRecording {
	ring := trace.NewMemorySink(trace.DefaultRingCapacity)
	return &traceRecording{sink: trace.Digested(ring), ring: ring}
}

func refusedRecording(refusal error) *traceRecording {
	recording := ringRecording()
	recording.notes = append(recording.notes,
		trace.NoteRecord{Kind: trace.NoteTraceUnavailable, Detail: refusal.Error()})
	return recording
}

func collectionNotes(root string) []trace.NoteRecord {
	removed, err := collect(traceRootAt(root), retention{keep: trace.RetainRuns})
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

func (r *traceRecording) close() error {
	if r == nil || r.sink == nil {
		return nil
	}
	return r.sink.Close()
}

type exhaust struct {
	noun      string
	directory string
	written   string
	code      Code
	remedy    func(reports string) string
}

var (
	traceExhaust = exhaust{
		noun:      "trace",
		directory: traceDirectoryName,
		written:   "a recording",
		code:      CodeTraceUnavailable,
		remedy: func(reports string) string {
			return "record under " + reports + ", or name a directory outside the workspace"
		},
	}
	diagnosticsExhaust = exhaust{
		noun:      "diagnostics",
		directory: diagnosticsDirectoryName,
		written:   "a bundle",
		code:      CodeDiagnosticsUnavailable,
		remedy: func(reports string) string {
			return "let the bundle land under " + reports + ", or point report.directory outside the workspace"
		},
	}
)

func ownedRoot(kind exhaust, workspace, reportDirectory, requested string) (string, error) {
	reports := filepath.Join(workspace, filepath.FromSlash(reportDirectory))
	directory := strings.TrimSpace(requested)
	switch {
	case directory == "" || directory == traceDefaultDirectory:
		directory = filepath.Join(reports, kind.directory)
	case !filepath.IsAbs(directory):
		directory = filepath.Join(workspace, directory)
	}
	directory = filepath.Clean(directory)
	if digestedBySnapshot(workspace, reports, directory) {
		return "", &Error{
			Code: kind.code,
			Message: "the " + kind.noun + " directory " + directory + " is inside the workspace and outside " +
				reports + ", where " + kind.written + " would be read as part of the tree and reported as drift",
			Hint: kind.remedy(reports),
		}
	}
	return directory, nil
}

func traceRoot(workspace, reportDirectory, requested string) (string, error) {
	return ownedRoot(traceExhaust, workspace, reportDirectory, requested)
}

func diagnosticsRoot(workspace, reportDirectory string) (string, error) {
	return ownedRoot(diagnosticsExhaust, workspace, reportDirectory, "")
}

func digestedBySnapshot(workspace, reports, directory string) bool {
	return insideButNotUnder(workspace, reports, directory) ||
		insideButNotUnder(existingPathOf(workspace), existingPathOf(reports), existingPathOf(directory))
}

func insideButNotUnder(workspace, reports, directory string) bool {
	return under(workspace, directory) && !under(reports, directory)
}

func under(base, path string) bool {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

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

type retentionRoot struct {
	path       string
	label      string
	noun       string
	unfinished string
	marker     string
	finished   func(directory string) bool
}

func traceRootAt(path string) retentionRoot {
	return retentionRoot{
		path:       path,
		label:      "trace",
		noun:       "recording",
		unfinished: "ended with its run-end and finished the bundle beside it",
		marker:     trace.FileName,
		finished: func(directory string) bool {
			return finishedRecording(filepath.Join(directory, trace.FileName)) &&
				finishedBundle(directory)
		},
	}
}

func diagnosticsRootAt(path string) retentionRoot {
	return retentionRoot{
		path:       path,
		label:      diagnosticsDirectoryName,
		noun:       "bundle",
		unfinished: "was finished, so each is a run that died while writing one",
		marker:     errorFileName,
		finished:   finishedBundle,
	}
}

func finishedBundle(directory string) bool {
	if _, err := os.Stat(filepath.Join(directory, errorFileName)); err != nil {
		return true
	}
	_, err := os.Stat(filepath.Join(directory, preservedPathsFileName))
	return err == nil
}

type retention struct {
	keep       int
	unfinished bool
}

func prune(r retentionRoot, found sweep) ([]string, error) {
	removed := make([]string, 0, len(found.stale))
	for _, name := range found.stale {
		if err := os.RemoveAll(filepath.Join(r.path, name)); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	return removed, nil
}

func collect(r retentionRoot, keep retention) ([]string, error) {
	found, err := planSweep(r, keep)
	if err != nil {
		return nil, err
	}
	return prune(r, found)
}

type sweep struct {
	held       int
	candidates int
	stale      []string
}

func planSweep(r retentionRoot, keep retention) (sweep, error) {
	names, err := namesIn(r)
	if err != nil {
		return sweep{}, err
	}
	candidates := names
	if !keep.unfinished {
		candidates = nil
		for _, name := range names {
			if r.finished(filepath.Join(r.path, name)) {
				candidates = append(candidates, name)
			}
		}
	}
	found := sweep{held: len(names), candidates: len(candidates)}
	if keep.keep < 0 || len(candidates) <= keep.keep {
		return found, nil
	}
	found.stale = candidates[:len(candidates)-keep.keep]
	return found, nil
}

var (
	errNotDirectory = errors.New("is not a directory")
	errIsALink      = errors.New("is a link, not a directory")
)

func describePath(path string, reason error) error { return fmt.Errorf("%s %w", path, reason) }

var readDir = func(f *os.File) ([]os.DirEntry, error) { return f.ReadDir(-1) }

func namesIn(r retentionRoot) ([]string, error) {
	f, err := os.Open(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, describePath(r.path, errNotDirectory)
	}
	entries, err := readDir(f)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || !recordingName.MatchString(entry.Name()) {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(r.path, entry.Name(), r.marker)); statErr != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}

func recordingsIn(path string) ([]string, error) { return namesIn(traceRootAt(path)) }

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
