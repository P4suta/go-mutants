// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
)

const (
	// directoryPermissions and filePermissions are what a recording is written
	// with. A trace is meant to be read — attached to a bug report, uploaded
	// by CI — so it is world-readable and never world-writable.
	directoryPermissions fs.FileMode = 0o755
	filePermissions      fs.FileMode = 0o644
)

// errSinkClosed is what a sink returns for an event that arrived after it was
// closed. It is counted as a drop rather than reported to the run.
var errSinkClosed = errors.New("go-mutants: trace sink is closed")

// A Sink is where a [Recorder] puts its events.
//
// Emit may fail, and a failure costs the event rather than the run. A sink is
// therefore obliged to be honest about what it lost, which is what [Dropper]
// is for.
type Sink interface {
	Emit(event Event) error
	Close() error
}

// A Dropper is a sink that counts the events it could not keep.
//
// A sink that implements it is authoritative about its own losses, because some
// losses are not failures: a bounded ring discards an old event without any
// call returning an error, and a reader still needs to be told.
type Dropper interface {
	Dropped() int64
}

// A File is the stream a [DirSink] appends to. It is an interface so that a
// test can hand the sink a stream that cannot be written, which is the one
// failure a diagnostic has to survive without costing the run.
type File interface {
	Write(data []byte) (int, error)
	Sync() error
	Close() error
}

// Filesystem is the set of operations a [DirSink] performs, each of which
// defaults to the os function of the same name when it is nil.
//
// It exists for the tests: "a full disk costs the event, not the run" is a
// promise that can only be checked by producing a full disk.
type Filesystem struct {
	MkdirAll   func(path string, perm fs.FileMode) error
	Mkdir      func(path string, perm fs.FileMode) error
	OpenAppend func(path string, perm fs.FileMode) (File, error)
	WriteFile  func(path string, data []byte, perm fs.FileMode) error
}

func (hooks Filesystem) resolved() Filesystem {
	if hooks.MkdirAll == nil {
		hooks.MkdirAll = os.MkdirAll
	}
	if hooks.Mkdir == nil {
		hooks.Mkdir = os.Mkdir
	}
	if hooks.OpenAppend == nil {
		hooks.OpenAppend = func(name string, perm fs.FileMode) (File, error) {
			return os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perm)
		}
	}
	if hooks.WriteFile == nil {
		hooks.WriteFile = os.WriteFile
	}
	return hooks
}

// DirSink collects one recording in a directory of its own: the JSON Lines
// stream, and the captured output of the commands the stream digested.
type DirSink struct {
	directory string
	hooks     Filesystem

	mutex        sync.Mutex
	file         File
	closed       bool
	outputExists bool

	dropped atomic.Int64
}

// NewDirSink creates `<root>/<run>/` and opens the stream inside it.
//
// The run directory is created exclusively. Everything in a recording is
// numbered from its first event, so a second recording in one directory would
// append to the first one's stream and write its preserved output over the
// first one's files: a directory that already exists is somebody else's
// recording, and taking it would corrupt both.
func NewDirSink(root, run string, hooks Filesystem) (*DirSink, error) {
	hooks = hooks.resolved()
	if err := hooks.MkdirAll(root, directoryPermissions); err != nil {
		return nil, fmt.Errorf("go-mutants: create trace directory %s: %w", root, err)
	}
	directory := filepath.Join(root, run)
	if err := hooks.Mkdir(directory, directoryPermissions); err != nil {
		return nil, fmt.Errorf("go-mutants: create trace directory %s: %w", directory, err)
	}
	stream := filepath.Join(directory, FileName)
	file, err := hooks.OpenAppend(stream, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("go-mutants: open trace stream %s: %w", stream, err)
	}
	return &DirSink{directory: directory, hooks: hooks, file: file}, nil
}

// Directory is the run directory this sink owns.
func (sink *DirSink) Directory() string { return sink.directory }

// Emit appends one line to the stream, preserving the event's captured output
// beside it first.
//
// The line is written rather than buffered, so a run that hangs, is
// interrupted, or is killed leaves a readable prefix and only its run-end
// event is missing.
//
// The lock is held across the output write as well as the line, which trades
// throughput for ordering: a mutant execution that printed a megabyte makes
// every concurrent worker's event wait behind that write. Ordering is the
// property worth buying, because the order of the file is what makes `seq` the
// order of the file, and a reader that could not rely on that would have to
// sort a recording before reading it. goatest holds the same lock for the same
// reason, and the cost is bounded by the 1 MiB cap on what is preserved.
func (sink *DirSink) Emit(event Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		sink.dropped.Add(1)
		return errSinkClosed
	}
	event = sink.preserveOutput(event)
	encoded, err := json.Marshal(event)
	if err != nil {
		sink.dropped.Add(1)
		return fmt.Errorf("go-mutants: encode trace event %d: %w", event.Seq, err)
	}
	if _, err := sink.file.Write(append(encoded, '\n')); err != nil {
		sink.dropped.Add(1)
		return fmt.Errorf("go-mutants: write trace event %d: %w", event.Seq, err)
	}
	return nil
}

// Close syncs and closes the stream once.
func (sink *DirSink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		return nil
	}
	sink.closed = true
	var failures []error
	if err := sink.file.Sync(); err != nil {
		failures = append(failures, fmt.Errorf("go-mutants: sync trace stream: %w", err))
	}
	if err := sink.file.Close(); err != nil {
		failures = append(failures, fmt.Errorf("go-mutants: close trace stream: %w", err))
	}
	return errors.Join(failures...)
}

// Dropped is how many events this sink could not write.
func (sink *DirSink) Dropped() int64 { return sink.dropped.Load() }

// preserveOutput writes the event's captured output beside the stream and
// returns the event pointing at it.
//
// A failure returns the event unchanged: the path is lost, the digest and the
// byte count are not, and the command is still in the account of the run.
func (sink *DirSink) preserveOutput(event Event) Event {
	if event.Exec == nil || len(event.Exec.Output) == 0 {
		return event
	}
	data, truncated := limitOutput(event.Exec.Output)
	name := strconv.FormatInt(event.Seq, 10) + ".txt"
	if err := sink.writeOutput(name, data); err != nil {
		return event
	}
	record := *event.Exec
	record.OutputTruncated = truncated
	record.OutputPath = path.Join(OutputDirectoryName, name)
	event.Exec = &record
	return event
}

func (sink *DirSink) writeOutput(name string, data []byte) error {
	directory := filepath.Join(sink.directory, OutputDirectoryName)
	if !sink.outputExists {
		if err := sink.hooks.MkdirAll(directory, directoryPermissions); err != nil {
			return err
		}
		sink.outputExists = true
	}
	return sink.hooks.WriteFile(filepath.Join(directory, name), data, filePermissions)
}

// limitOutput cuts a capture at [OutputFileLimit] and marks it. The digest and
// the byte count in the event always cover the whole capture, so a reader can
// tell a truncated file from a shorter run.
func limitOutput(output []byte) ([]byte, bool) {
	if len(output) <= OutputFileLimit {
		return output, false
	}
	limited := make([]byte, 0, OutputFileLimit+len(TruncationMarker))
	limited = append(limited, output[:OutputFileLimit]...)
	return append(limited, TruncationMarker...), true
}

// MemorySink keeps a bounded ring of events in memory.
//
// It is what an untraced run records into: no file, no directory, and a bounded
// price a run of any length pays once. When the ring is full the oldest event
// falls out and is counted, so the recording says how much of itself is
// missing.
type MemorySink struct {
	capacity int

	mutex  sync.Mutex
	events []Event
	closed bool

	dropped atomic.Int64
}

// NewMemorySink returns a ring of the given capacity. A capacity of zero or
// less is unbounded, which is what a test wants and a run does not.
func NewMemorySink(capacity int) *MemorySink { return &MemorySink{capacity: capacity} }

// Emit appends a clone of the event, dropping the oldest if the ring is full.
//
// The ring holds its last slot back for the run-end event. The accounting is
// taken before that event is written, so a ring with no room left for it would
// drop the one event that could have reported the loss.
func (sink *MemorySink) Emit(event Event) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	if sink.closed {
		sink.dropped.Add(1)
		return errSinkClosed
	}
	sink.events = append(sink.events, event.Clone())
	if sink.capacity <= 0 {
		return nil
	}
	room := sink.capacity
	if event.Type != TypeRunEnd {
		room--
	}
	if overflow := len(sink.events) - room; overflow > 0 {
		sink.events = append(sink.events[:0], sink.events[overflow:]...)
		sink.dropped.Add(int64(overflow))
	}
	return nil
}

// Close marks the ring closed. The events it holds stay readable, because the
// reason to keep a ring is to read it after the run that filled it has ended.
func (sink *MemorySink) Close() error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.closed = true
	return nil
}

// Dropped is how many events fell out of the ring or arrived after it closed.
func (sink *MemorySink) Dropped() int64 { return sink.dropped.Load() }

// Events returns the events the ring holds, cloned, oldest first.
func (sink *MemorySink) Events() []Event {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	snapshot := make([]Event, 0, len(sink.events))
	for _, event := range sink.events {
		snapshot = append(snapshot, event.Clone())
	}
	return snapshot
}

// TeeSink hands every event to several sinks.
//
// It is how a run records into a file and into a ring at once — the file for a
// reader, the ring for the diagnostics bundle a failed run writes.
type TeeSink struct {
	sinks []Sink

	dropped atomic.Int64
}

// NewTeeSink fans out to the sinks given, ignoring nil ones so that a caller
// can pass an optional sink without a branch.
func NewTeeSink(sinks ...Sink) *TeeSink {
	kept := make([]Sink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			kept = append(kept, sink)
		}
	}
	return &TeeSink{sinks: kept}
}

// Emit hands the event to every sink and returns the first failure.
func (tee *TeeSink) Emit(event Event) error {
	var first error
	for _, sink := range tee.sinks {
		err := sink.Emit(event)
		if err == nil {
			continue
		}
		if first == nil {
			first = err
		}
		// A sink that counts its own losses is authoritative, so counting the
		// same refusal here as well would report it twice.
		if _, counts := sink.(Dropper); !counts {
			tee.dropped.Add(1)
		}
	}
	return first
}

// Close closes every sink and returns the first failure.
func (tee *TeeSink) Close() error {
	var first error
	for _, sink := range tee.sinks {
		if err := sink.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Dropped is what the tee and its sinks lost between them.
func (tee *TeeSink) Dropped() int64 {
	total := tee.dropped.Load()
	for _, sink := range tee.sinks {
		if dropper, ok := sink.(Dropper); ok {
			total += dropper.Dropped()
		}
	}
	return total
}

// Digested strips the bytes an event carries for a sink that would keep them,
// and forwards the rest to inner.
//
// It is what a bounded ring is wrapped in. A ring exists to cost a bounded
// amount of memory, and captured output grows with the run rather than with the
// ring: the size and the digest of a capture stay, the bytes and the killing
// binary's output tail go. A nil inner is the disabled sink, so that "no trace"
// stays one representation.
func Digested(inner Sink) Sink {
	if inner == nil {
		return nil
	}
	return &digestedSink{inner: inner}
}

type digestedSink struct {
	inner Sink

	// dropped counts what an inner sink that keeps no tally of its own refused.
	//
	// The wrapper has to count that case itself, because a sink answering
	// Dropped() is authoritative: a wrapper that reported zero for an inner
	// sink which counts nothing would make the recorder prefer that zero over
	// its own tally of refusals, and a [TeeSink] around the wrapper would stop
	// counting the branch too — a recording that lost every event and said it
	// lost none. It has to count only that case for the mirror-image reason: an
	// inner sink that does keep a tally has already counted the refusal it
	// returned, so counting it here as well would report one loss as two.
	dropped atomic.Int64
}

// Emit strips the bytes the inner sink must not grow with, and everything that
// describes the file they were preserved in.
//
// The markers go with the bytes because they describe a file this sink is not
// writing. Keeping them would leave an event claiming a truncation of a file
// nothing wrote — and when a ring is later written out as a diagnostics
// bundle, the sink writing it has no bytes to preserve and therefore no path
// to set, so the claim would survive into a recording nothing can satisfy.
func (sink *digestedSink) Emit(event Event) error {
	if record := event.Exec; record != nil && (len(record.Output) > 0 || record.OutputTruncated || record.OutputPath != "") {
		stripped := *record
		stripped.Output = nil
		stripped.OutputTruncated = false
		stripped.OutputPath = ""
		event.Exec = &stripped
	}
	if event.Mutant != nil && event.Mutant.OutputTail != "" {
		record := *event.Mutant
		record.OutputTail = ""
		event.Mutant = &record
	}
	err := sink.inner.Emit(event)
	if err != nil {
		// A sink that counts its own losses is authoritative, so counting the
		// same refusal here as well would report it twice. [TeeSink.Emit]
		// makes the same distinction for the same reason.
		if _, counts := sink.inner.(Dropper); !counts {
			sink.dropped.Add(1)
		}
	}
	return err
}

func (sink *digestedSink) Close() error { return sink.inner.Close() }

// Dropped is what the recording lost behind this wrapper: the inner sink's own
// accounting where it keeps one, and the wrapper's tally of refusals where it
// does not. Never both, because both would be the same losses counted twice.
func (sink *digestedSink) Dropped() int64 {
	if dropper, ok := sink.inner.(Dropper); ok {
		return dropper.Dropped()
	}
	return sink.dropped.Load()
}
