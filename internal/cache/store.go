// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// The layout of the outcome store, under the operating system's cache
// directory:
//
//	<cache>/go-mutants/workspaces/<workspace>/go-mutants.marker
//	<cache>/go-mutants/workspaces/<workspace>/outcomes/<context>/<mutant-id>.json
//
// It is the run history's own workspace directory with one more subdirectory in
// it, and that is deliberate: one workspace has one directory and one ownership
// marker, whatever go-mutants files underneath it. See [report.History].
//
// The context directory is what makes a stale entry cost nothing. A run whose
// tool version, Go toolchain, code, catalogue, command, configured timeout, or
// environment differs computes a different context key and simply looks
// somewhere else;
// nothing has to be invalidated, and yesterday's entries sit harmlessly in
// yesterday's directory until `cache gc` removes them.
const (
	// OutcomesDirName holds one directory per key context.
	OutcomesDirName = "outcomes"

	// EntryVersion is the schema version of one stored entry. A file carrying
	// another version is read as corrupt — that is, as a miss — rather than
	// guessed at.
	//
	// Version 2 added the full key an entry was written under; see [Entry.Key].
	// A version 1 document has no such field, so it would decode with an empty
	// one and be refused as written for another key — a true refusal with a
	// misleading diagnosis. Reading it as the version miss it actually is says
	// the accurate thing instead. Ordinary upgrades never meet one, because
	// [KeyDomain] moved to v2 in the same change and no v1 directory is ever
	// consulted again; a cache copied between machines or restored from a CI
	// archive is where a stray v1 file still turns up.
	EntryVersion = 2

	// entrySuffix is the extension of one entry file. It is also the filter
	// `cache gc` and `cache status` count by, so that a temporary file left by
	// an interrupted write is never mistaken for a stored outcome.
	entrySuffix = ".json"

	// tempPattern is the name temporary files are created under, matching the
	// history store's convention: anything under it is a leftover from an
	// interrupted write and is safe to delete.
	tempPattern = "go-mutants-cache-*.tmp"

	// MaxOutputTail is how many bytes of a mutant's test output an entry keeps.
	//
	// The tail exists so that a cached survivor can still show a human why it
	// survived, and it is bounded because a cache is a directory of thousands of
	// small files: an unbounded tail turns one pathological test into hundreds
	// of megabytes in somebody's cache directory. Sixteen kilobytes is far more
	// than the failing assertions anybody reads and far less than that.
	MaxOutputTail = 16 << 10
)

// renameDelays are the waits between attempts at moving a temporary file into
// place. Windows refuses a rename onto a file another process still has open,
// and the condition clears in milliseconds; see [report.History] for the same
// argument at greater length.
var renameDelays = []time.Duration{5 * time.Millisecond, 20 * time.Millisecond, 100 * time.Millisecond}

// Root resolves the cache root directory.
//
// The empty directory is <os.UserCacheDir>/go-mutants, which is the run
// history's root as well: the two stores share a workspace directory. A
// `cache.directory` replaces the `go-mutants` element under the operating
// system's cache root — never under the workspace, and never above the cache
// root, which internal/config has already proved of the value it validated.
func Root(directory string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", &Error{
			Code:    CodeUnavailable,
			Message: "the operating system's cache directory could not be determined, so there is nowhere to keep outcomes",
			Err:     err,
		}
	}
	if directory == "" {
		return filepath.Join(base, report.DirName), nil
	}
	relative, err := mutation.NormalizePath(directory)
	if err != nil {
		return "", &Error{
			Code:    CodeUnavailable,
			Message: "cache.directory " + directory + " is not a relative path under the cache root",
			Err:     err,
		}
	}
	return filepath.Join(base, filepath.FromSlash(relative)), nil
}

// Options is everything [Open] needs.
type Options struct {
	// Root is the cache root. Empty resolves [Root] over Directory, which is
	// what every real run does; the tests set it so that they never touch the
	// developer's own cache.
	Root string
	// Directory is `cache.directory`. It is read only when Root is empty.
	Directory string
	// Context identifies the run. Every field of it is in the key; see
	// [Context].
	Context Context
	// Timeout is the per-mutant bound this run will apply, derived or explicit.
	//
	// It is not part of the key and is not meant to be — see
	// [Context.ConfiguredTimeout] — but it is what every lookup is judged
	// against: an entry whose measurement could not have happened under this
	// bound is not adopted. See [Entry.UsableUnder].
	Timeout time.Duration
}

// A Cache is one run's view of the outcome store: the directory its context
// keys, already claimed and created.
//
// It is safe for concurrent use. Every operation is one file read or one
// atomic file write, and two workers never touch one mutant.
type Cache struct {
	root    string
	dir     string
	key     string
	context string
	timeout time.Duration
}

// Open resolves the cache directory for one run, proves the workspace
// directory is go-mutants' own, and creates what is missing.
//
// Every failure here is the caller's to fail open on: a cache that cannot be
// opened is a run that measures everything, which is the answer it would have
// reached anyway.
func Open(opts Options) (*Cache, error) {
	root := opts.Root
	if root == "" {
		resolved, err := Root(opts.Directory)
		if err != nil {
			return nil, err
		}
		root = resolved
	}
	context, err := opts.Context.ContextKey()
	if err != nil {
		return nil, err
	}
	key, err := opts.Context.Key()
	if err != nil {
		return nil, err
	}
	// The same claim the run history makes, against the same marker: a
	// workspace directory belonging to something else is refused before
	// anything is written into it.
	workspace, err := report.History{Root: root}.Claim(opts.Context.WorkspaceDigest)
	if err != nil {
		return nil, &Error{
			Code:    CodeUnavailable,
			Message: "the outcome cache directory could not be claimed",
			Err:     err,
		}
	}
	dir := filepath.Join(workspace, OutcomesDirName, context)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return nil, &Error{
			Code:    CodeUnavailable,
			Message: "the outcome cache directory " + dir + " could not be created",
			Err:     mkErr,
		}
	}
	return &Cache{root: root, dir: dir, key: key, context: context, timeout: opts.Timeout}, nil
}

// Root returns the cache root this handle was opened under.
func (c *Cache) Root() string { return c.root }

// Dir returns the directory this run's entries are filed in.
func (c *Cache) Dir() string { return c.dir }

// Key returns the full context key, which is what every entry records and is
// checked against; see [Entry.Key].
func (c *Cache) Key() string { return c.key }

// ContextKey returns the truncated key that names the directory.
func (c *Cache) ContextKey() string { return c.context }

// An Entry is one mutant's stored outcome.
//
// It carries the full key and the id it was written for as well as the outcome,
// which costs a few dozen bytes and buys the one check a truncated directory
// name cannot make for itself: an entry read from the wrong place is refused
// rather than adopted. See [ContextKeyLength].
type Entry struct {
	// Version is [EntryVersion].
	Version int `json:"version"`
	// Key is the full 64 hex character context key this entry was written
	// under, from [Cache.Key].
	//
	// It is the field that makes the truncated directory name safe, and it has
	// to be the untruncated key to do that job: two contexts colliding in the
	// first [ContextKeyLength] characters share a directory *and* agree about
	// Context, so an entry checked only against Context would be adopted by the
	// wrong run — which is the worst thing this package could do. Sixty-four hex
	// characters is a collision nobody will produce.
	Key string `json:"key"`
	// Context is the truncated context key this entry was written under: the
	// directory name, restated inside the file.
	//
	// Key already implies it, and it is kept because it is what a human reading
	// a cache directory can check against the path in one glance. It is not what
	// the soundness rests on.
	Context string `json:"context"`
	// ID is the full 64 hex character mutant id.
	ID string `json:"id"`
	// Outcome is what happened, in the core spelling.
	Outcome mutation.Outcome `json:"outcome"`
	// DurationMS is how long the measurement took, summed over every attempt.
	DurationMS int64 `json:"duration_ms"`
	// TimeoutMS is the per-mutant bound the measurement was made under. It is
	// what [Entry.UsableUnder] compares against, and it is stored rather than
	// keyed on so that a derived bound wobbling by a few hundred milliseconds
	// does not throw away a whole cache.
	TimeoutMS int64 `json:"timeout_ms"`
	// KilledBy names the test binary that detected the mutant, or is empty.
	KilledBy string `json:"killed_by,omitempty"`
	// Attempts is how many times the mutant was executed to reach the outcome.
	Attempts int `json:"attempts"`
	// OutputTail is the tail of the test output, truncated to [MaxOutputTail].
	OutputTail string `json:"output_tail,omitempty"`
}

// Duration renders the stored measurement.
func (e Entry) Duration() time.Duration { return time.Duration(e.DurationMS) * time.Millisecond }

// Timeout renders the bound the measurement was made under.
func (e Entry) Timeout() time.Duration { return time.Duration(e.TimeoutMS) * time.Millisecond }

// UsableUnder reports whether this run's per-mutant bound could have produced
// the stored outcome.
//
// It is the whole of the argument that keeping the timeout out of the key costs
// nothing, and it is two rules rather than one because the two kinds of outcome
// depend on the bound in opposite directions:
//
//   - A killed or survived mutant *finished*, in the recorded duration. Under
//     any bound at least that long it finishes again and reaches the same
//     verdict; under a shorter one it might have been cut off instead, so a
//     bound below the recorded duration refuses the entry.
//   - A confirmed timeout *did not finish* within the recorded bound. Under any
//     bound no larger it does not finish either, so the timeout stands; under a
//     larger one it might have completed, so the entry is refused and the
//     mutant is measured again.
//
// A refusal here is an ordinary miss and not a diagnosis. Nothing is wrong with
// the entry — it is simply not evidence about the run being made now.
//
// The comparison is against the recorded duration rather than a margin below
// it. A measurement that finished a hair inside its bound is exactly as
// reproducible as it was the first time, and inventing a safety factor would be
// a second, unwritten timeout policy sitting behind the documented one.
func (e Entry) UsableUnder(timeout time.Duration) bool {
	bound := milliseconds(timeout)
	if bound <= 0 {
		// A run that states no bound cannot say whether the measurement fits
		// inside one. Nothing is adopted, which is the answer that cannot be
		// wrong.
		return false
	}
	if e.Outcome == mutation.OutcomeTimedOut {
		return bound <= e.TimeoutMS
	}
	return e.DurationMS <= bound
}

// Cacheable reports whether an outcome may be stored and later adopted.
//
// Three outcomes are reusable and the rest deliberately are not:
//
//   - killed and survived are settled measurements. The tests either noticed
//     the edit or they did not, and nothing about running it again would be
//     different.
//   - timed-out is a *confirmed* timeout: it timed out, was retried serially,
//     and timed out again. Two observations agreeing is what makes it a
//     detection rather than a suspicion, and a suspicion is not something to
//     file away as proven.
//
// Everything else is mixed or absent evidence and has to be measured again.
// inconclusive is the important one: it is one timeout that did not reproduce,
// which is to say the two attempts disagreed, and a cache that froze a
// disagreement would make a flake permanent — the run after the fix would
// still report it. errored is the harness failing rather than the mutant doing
// anything, and not-run is not a measurement at all: caching either would turn
// a bad afternoon into a bad week.
//
// A mutant no test covers is never offered here either, and that is a property
// of where the partition happens rather than of this function: coverage
// narrowing runs first, so an uncovered survivor is settled before the cache is
// ever asked. It matters because the coverage pass fails open — a run whose
// profiling broke measures everything — and the coverage mode is deliberately
// not in the key, so a cached "survived (uncovered)" could be adopted by a run
// that would have executed the mutant and killed it.
func Cacheable(o mutation.Outcome) bool {
	switch o {
	case mutation.OutcomeKilled, mutation.OutcomeSurvived, mutation.OutcomeTimedOut:
		return true
	default:
		return false
	}
}

// Lookup reads one mutant's stored outcome.
//
// It reports three states rather than two. A missing entry is (false, nil): the
// ordinary miss, and the commonest answer a cache gives. An entry that is
// present and unreadable is (false, error): also a miss, because there is
// nothing to adopt, but one the caller reports so that a cache directory
// somebody's antivirus is quietly corrupting does not present as a permanently
// cold cache. A caller that treats every error as fatal has misread the
// contract; see the package documentation.
func (c *Cache) Lookup(id string) (Entry, bool, error) {
	path, err := c.path(id)
	if err != nil {
		return Entry{}, false, err
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Entry{}, false, nil
	case err != nil:
		return Entry{}, false, &Error{
			Code:    CodeCorruptEntry,
			Message: "the cached outcome " + path + " could not be read, so the mutant will be measured again",
			Err:     err,
		}
	}
	var entry Entry
	if err = json.Unmarshal(data, &entry); err != nil {
		return Entry{}, false, &Error{
			Code:    CodeCorruptEntry,
			Message: "the cached outcome " + path + " is not a cache entry, so the mutant will be measured again",
			Err:     err,
		}
	}
	if err = entry.check(c.key, c.context, id); err != nil {
		return Entry{}, false, &Error{
			Code:    CodeCorruptEntry,
			Message: "the cached outcome " + path + " will not be reused: " + err.Error(),
		}
	}
	// An ordinary miss and not a diagnosis: the entry is perfectly good, it is
	// just not evidence about a run with this bound. See [Entry.UsableUnder].
	if !entry.UsableUnder(c.timeout) {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

// check proves an entry decoded from disk is the entry that was asked for.
//
// The key and the id are compared rather than assumed from the path, because
// the path is a truncated hash of the key and a full hash of the id: a
// collision in the truncation would otherwise be adopted as an answer, and
// adopting the wrong run's outcome is the worst thing this package could do.
// The comparison is against the *full* key for that to mean anything — two
// contexts that collide in the first [ContextKeyLength] characters share a
// directory and agree about the truncation, so checking the truncation checks
// nothing they disagree about. Context is compared too, but only as a
// consistency check on a hand-edited file; the full key is the guard.
//
// Everything else here is a shape check on a document that may have been
// written by another version, truncated by a full disk, or edited by hand.
func (e Entry) check(key, context, id string) error {
	switch {
	case e.Version != EntryVersion:
		return versionError(e.Version)
	case e.Key != key:
		return errors.New("it was written under another cache key")
	case e.Context != context:
		return errors.New("it was written for another cache context")
	case e.ID != id:
		return errors.New("it holds the outcome of another mutant")
	case !Cacheable(e.Outcome):
		return errors.New("it holds " + e.Outcome.String() + ", which is not a reusable outcome")
	case e.DurationMS < 0 || e.Attempts < 1 || e.TimeoutMS <= 0:
		return errors.New("its measurement is not one that could have happened")
	}
	return nil
}

// versionError names an entry from another schema.
func versionError(version int) error {
	return errors.New("it is a version " + strconv.Itoa(version) +
		" entry and this build writes version " + strconv.Itoa(EntryVersion))
}

// Put stores one mutant's outcome.
//
// A non-reusable outcome is refused rather than quietly dropped: the caller
// decides what is worth caching, and a Put that silently did nothing would make
// a bug in that decision invisible. See [Cacheable].
//
// The write goes through a temporary file in the same directory and a rename,
// so a crash leaves either no entry or a whole one — never a correctly named
// file holding half a JSON document, which is the failure that would turn one
// interrupted run into a permanently poisoned cache.
func (c *Cache) Put(id string, entry Entry) error {
	path, err := c.path(id)
	if err != nil {
		return err
	}
	entry.Version = EntryVersion
	entry.Key = c.key
	entry.Context = c.context
	entry.ID = id
	entry.TimeoutMS = milliseconds(c.timeout)
	entry.OutputTail = truncateTail(entry.OutputTail)
	if !Cacheable(entry.Outcome) {
		return &Error{
			Code: CodeEntryNotWritten,
			Message: "the outcome " + entry.Outcome.String() + " of mutant " + display(id) +
				" is not one a later run may reuse, so it was not stored",
		}
	}
	if err = entry.check(c.key, c.context, id); err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "the outcome of mutant " + display(id) + " was not stored: " + err.Error(),
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "the outcome of mutant " + display(id) + " could not be encoded",
			Err:     err,
		}
	}
	return writeAtomic(path, append(data, '\n'))
}

// path is where one mutant's entry lives, refusing an id that could name
// anything but a file in this directory.
//
// The id is checked against the full 64-hex shape rather than merely cleaned,
// which makes path traversal unreachable rather than merely unlikely: there is
// no separator, no dot, and no drive letter in the alphabet the check accepts.
func (c *Cache) path(id string) (string, error) {
	if !mutation.IsID(id) {
		return "", &Error{
			Code:    CodeInvalidContext,
			Message: "an outcome cache entry was addressed by " + display(id) + ", which is not a mutant id",
		}
	}
	return filepath.Join(c.dir, id+entrySuffix), nil
}

// truncateTail bounds a stored output tail, keeping the end of it.
//
// The end rather than the beginning: the useful part of a failed test run is
// the assertion that failed, which is at the bottom. A truncated tail is marked
// so that a reader is never left wondering whether the output really did start
// mid-word.
func truncateTail(tail string) string {
	if len(tail) <= MaxOutputTail {
		return tail
	}
	const marker = "[... truncated by the outcome cache ...]\n"
	return marker + tail[len(tail)-MaxOutputTail:]
}

// writeAtomic writes data to path through a temporary file in the same
// directory, which is the same volume and therefore a rename that is a rename.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "a temporary file for a cached outcome could not be created in " + dir,
			Err:     err,
		}
	}
	name := temp.Name()
	// Removed on every failing path, and a no-op after a successful rename
	// against a name that no longer exists.
	defer func() { _ = os.Remove(name) }()

	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "a cached outcome could not be written to " + name,
			Err:     err,
		}
	}
	if err = rename(name, path); err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "a cached outcome could not be moved into place at " + path,
			Err:     err,
		}
	}
	return nil
}

// rename moves a completed temporary file into place, retrying the transient
// sharing failures Windows produces. See [renameDelays].
func rename(from, to string) error {
	err := os.Rename(from, to)
	for _, delay := range renameDelays {
		if err == nil {
			return nil
		}
		time.Sleep(delay)
		err = os.Rename(from, to)
	}
	return err
}

// display shortens an id for a message, leaving a short or malformed one alone
// rather than slicing past its end.
func display(id string) string {
	if len(id) <= mutation.DisplayIDLength {
		return id
	}
	return id[:mutation.DisplayIDLength]
}
