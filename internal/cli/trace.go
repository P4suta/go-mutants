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

// close finishes the recording: the stream is synced and closed. It is safe to
// call twice, which is what lets the run close it on the path it took and on
// its deferred one.
func (r *traceRecording) close() error {
	if r == nil || r.sink == nil {
		return nil
	}
	return r.sink.Close()
}

// An exhaust is one kind of diagnostic output a run writes into
// `report.directory`, and everything the rule that places it has to say.
//
// There are two — the recording and the diagnostics bundle — and they are
// placed by one rule for one reason: the rule is a statement about the
// workspace rather than about either of them. See [ownedRoot].
type exhaust struct {
	// noun is what the directory is called in a refusal.
	noun string
	// directory is its subdirectory of `report.directory`.
	directory string
	// written is what a refusal says would be read as part of the tree.
	written string
	// code is the warning a refusal is reported under.
	code Code
	// remedy composes the refusal's hint around the report directory.
	remedy func(reports string) string
}

// The two kinds of exhaust a run produces.
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

// ownedRoot decides where one kind of a run's diagnostic exhaust goes, and
// refuses the directories that would cost the run its evidence.
//
// The default is `<workspace>/<report.directory>/<kind>/`, and that is not an
// arbitrary corner. internal/snapshot digests the workspace and excludes
// `report.directory` and nothing else inside it, so it is the one place in a
// user's tree a file may appear during a run. A recording grows while the run
// measures and a bundle appears at the end of one: written anywhere else in the
// workspace, either is a file that changed under the run, and the run would
// fail with drift it caused itself. Refusing the directory is what keeps the
// diagnostic from failing the run.
//
// A directory outside the workspace is nobody's source and is always accepted.
// A relative one resolves against the workspace rather than against the process
// working directory, so that `--trace=recordings` means the same thing wherever
// the command was typed.
//
// The default is checked as well as a named one, which costs nothing and is not
// vacuous: a symbolic link *at* the default directory, pointing back into the
// tree, lands the files where the snapshot reads while spelling a path that
// does not.
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

// traceRoot decides where a run's recordings go. See [ownedRoot] for the rule.
func traceRoot(workspace, reportDirectory, requested string) (string, error) {
	return ownedRoot(traceExhaust, workspace, reportDirectory, requested)
}

// diagnosticsRoot decides where a run's diagnostics bundles go.
//
// Nothing on the command line names one: a bundle is written for a failure
// rather than requested, and a run that was traced puts it beside its recording
// instead. The rule is still the trace's, applied to the default it always
// resolves, because the refusal is about where the files land.
func diagnosticsRoot(workspace, reportDirectory string) (string, error) {
	return ownedRoot(diagnosticsExhaust, workspace, reportDirectory, "")
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

// A retentionRoot is a directory a run fills one subdirectory of per run, with
// a collector over it: the trace root, and the diagnostics root.
//
// The two are collected by one implementation because they are collected by one
// rule — only a directory named by a run id and holding something of ours is
// collectable at all, the newest [trace.RetainRuns] survive, and one nothing
// finished writing is left alone. What differs between them is two predicates,
// which is exactly what this type carries.
//
// The name is long on purpose: `root` is what half this package's functions call
// the *path* of one of these, and a type sharing that name would be shadowed by
// a local in most of the places it is used.
type retentionRoot struct {
	// path is the directory the run directories sit in.
	path string
	// label is what the root itself is called in a message: `trace root:`,
	// `removed the empty diagnostics directory`.
	label string
	// noun is what one of the things in it is called.
	noun string
	// unfinished completes the sentence that says why a directory nothing
	// finished writing was left alone, which is the one message a `trace clean`
	// that removed nothing has to get right.
	unfinished string
	// marker is the file whose presence makes a subdirectory one of ours. A
	// run-id-shaped directory without it belongs to somebody else, or to a run
	// that has not written anything yet, and either way is not the collector's.
	marker string
	// finished reports whether nothing will be written into the directory
	// again, which is what makes it a candidate for collection at all. A
	// recording is finished when its stream ends with the run-end; a bundle is
	// finished when its last file is there.
	finished func(directory string) bool
}

// traceRootAt is the recordings in a trace root.
//
// A recording is finished when its stream ends with the run-end *and* whatever
// else is in its directory is finished too — which for a traced run means the
// diagnostics bundle, because that is written into the recording's own
// directory rather than into the diagnostics root. Asking only about the stream
// would call such a directory complete while half a bundle sat in it, and the
// same half-written bundle in the diagnostics root is held back: the answer
// would then depend on whether the run happened to be traced, which is not a
// fact about how complete the account is.
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

// diagnosticsRootAt is the bundles in a diagnostics root.
//
// The marker is the first file a bundle writes and the finished-predicate is
// the last, which is what makes a half-written bundle visible to `trace clean
// --all` and invisible to the retention a run applies — the same asymmetry a
// recording with no run-end has, and for the same reason: the account of the
// crash is the one a reader most wants.
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

// finishedBundle reports whether a directory holds no bundle, or holds one
// nothing will write to again.
//
// A directory with no [errorFileName] never had a bundle started in it, which is
// every successful traced run and is finished as far as this question goes. One
// that has the marker and not [preservedPathsFileName] is a run that died while
// writing its diagnosis, and is exactly what a collector must leave alone.
func finishedBundle(directory string) bool {
	if _, err := os.Stat(filepath.Join(directory, errorFileName)); err != nil {
		return true
	}
	_, err := os.Stat(filepath.Join(directory, preservedPathsFileName))
	return err == nil
}

// A retention is how much of a root survives collection.
type retention struct {
	// keep is how many of the newest candidates are left behind.
	keep int
	// unfinished makes a directory nothing finished writing a candidate. It is
	// off for the collector a run runs and on only for `trace clean --all`; see
	// [planSweep].
	unfinished bool
}

// prune removes the directories a sweep collected, and returns what it removed,
// oldest first.
//
// It takes the plan rather than the retention that produced it, and that is the
// whole of what keeps one sweep to one look at the directory. A collector that
// decided what to remove and then decided again as it removed would be acting
// on a directory it had not measured: a run finishing in between is protected
// by the first decision and collectable by the second, so it would be deleted
// having never been counted, and a recording appearing in between moves which
// ones the newest N are. Separating the decision from the action makes that
// race unexpressible rather than merely unlikely.
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

// collect plans one sweep of a root and carries it out.
//
// It is for the caller that has nothing to measure — the collector a run runs
// as it opens its recording or writes its bundle, which reports a count and no
// bytes. `trace clean` deliberately does not use it: it plans, sizes what the
// plan names, and then prunes that same plan, which is three steps precisely so
// that the sizes and the deletions are of one observation.
func collect(r retentionRoot, keep retention) ([]string, error) {
	found, err := planSweep(r, keep)
	if err != nil {
		return nil, err
	}
	return prune(r, found)
}

// A sweep is what a retention found in a root.
//
// It carries all three counts rather than only the collectable ones, because
// "there is nothing here" and "there is something here and I am keeping it" are
// different answers and a command that deletes must never print the first for
// the second: somebody reading that concludes their recordings are gone and
// stops looking for the disk they are still sitting on.
type sweep struct {
	// held is everything of ours in the root, whatever the retention makes of it.
	held int
	// candidates is how many of those this retention would even consider — the
	// finished ones, unless the caller asked for the rest as well.
	candidates int
	// stale is what it collects, oldest first.
	stale []string
}

// planSweep works out what a retention collects from a root and what it leaves
// behind. It is the one statement of the rule, so that what `trace clean`
// measures and reports is exactly what [prune] deletes.
//
// A directory nothing finished writing is not a candidate unless the caller
// asked for one, and that exception is the point of the rule. Such a directory
// is a run still in progress or a run that died, and the second is the account a
// reader most wants: a collector that removed the record of the crash while
// keeping ten records of runs that went fine would be collecting exactly
// backwards. It is also what makes a live run safe from a concurrent collector,
// rather than only from being the newest name in the root. `trace clean --all`
// is how somebody who has read them says so.
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

// The two ways a root can turn out not to be a directory this command may
// remove, whichever step found out. [namesIn] raises the first and
// [removeDirectory] returns either, so one condition gets one sentence wherever
// it is noticed.
//
// A link is worth its own words rather than being folded into the first. It is
// not a mistake — a trace root pointing out of the workspace is an arrangement
// the refusal rule deliberately allows, and its recordings are collected through
// it like anybody else's — it is only an object this command will not delete,
// and telling somebody "is not a directory" about a link they made on purpose
// would send them looking for a problem that is not there.
var (
	errNotDirectory = errors.New("is not a directory")
	errIsALink      = errors.New("is a link, not a directory")
)

// describePath names a path in the words of the reason it was refused.
func describePath(path string, reason error) error { return fmt.Errorf("%s %w", path, reason) }

// readDir is how a collector reads an open root, and is a package variable for
// the reason [traceFilesystem] is one: "a root that changed under the collector
// is never unlinked" is a promise that can only be checked by changing one
// under it. See the tests that inject a listing which does exactly that.
//
// It takes the handle rather than the path, because that is the protocol; see
// [namesIn]. Nothing outside the suite assigns it.
var readDir = func(f *os.File) ([]os.DirEntry, error) { return f.ReadDir(-1) }

// namesIn names what a root holds of ours, oldest first.
//
// The listing sorts by name and a run id sorts by the moment it was minted, so
// the order of the listing is the order of the runs. A root that does not exist
// holds nothing, which is an answer rather than a failure: it is what a
// workspace that has never traced or failed a run looks like.
//
// The protocol is **one handle to read, and rmdir alone to remove**, and both
// halves are about the same window. Everything here — that the path is a
// directory at all, and what is inside it — is read from a single [os.Open], so
// the kind and the contents describe one object rather than whatever happened to
// be at that path at two different moments. And [removeDirectory] is what takes
// an emptied root away, so a path that became a regular file after this looked
// at it cannot be unlinked however the timing falls out.
//
// Asking the listing what the root *is* would have been wrong even without the
// race. Go's Windows readdir queries the handle for directory information and,
// on two of the error codes that can come back, breaks out of its loop and
// returns `names, dirents, infos, nil`: the error it was holding is dropped, so
// a caller listing a *file* is handed an empty directory and no failure at all.
// `trace clean` then reported "nothing to remove: no recording here" about a
// root it had never read — and went on to remove it.
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

// recordingsIn names the recordings in a trace root, oldest first. It is
// [namesIn] for the two `trace` subcommands that list rather than collect.
func recordingsIn(path string) ([]string, error) { return namesIn(traceRootAt(path)) }

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
