// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The layout of the workspace directory, under the operating system's cache
// directory:
//
//	<cache>/go-mutants/workspaces/<key>/go-mutants.marker
//	<cache>/go-mutants/workspaces/<key>/latest.json
//	<cache>/go-mutants/workspaces/<key>/runs/<run-id>.json
//	<cache>/go-mutants/workspaces/<key>/outcomes/<context>/<mutant-id>.json
//
// `runs/` holds nothing but immutable per-run documents, so that listing past
// runs is a directory listing and every entry in it is a real run. The pointer
// to the newest and the ownership marker sit beside it rather than inside it,
// for the same reason.
//
// `outcomes/` is internal/cache's, and this package neither writes nor reads
// it. It is named here because one marker governs the whole directory: both
// stores claim it through [History.Claim] before writing anything, and both
// refuse a directory whose marker names another workspace. See [History] for
// why that matters.
const (
	// DirName is go-mutants' own directory inside the OS cache directory. It
	// holds the run history and, unless `cache.directory` moves it, the outcome
	// cache: the two share a workspace directory and an ownership marker.
	DirName = "go-mutants"
	// WorkspacesDirName holds one directory per workspace.
	WorkspacesDirName = "workspaces"
	// RunsDirName holds the per-run documents.
	RunsDirName = "runs"
	// LatestFileName is the copy of the newest run's document.
	LatestFileName = "latest.json"
	// MarkerFileName is the ownership marker; see [History].
	MarkerFileName = "go-mutants.marker"

	// markerHeader is the marker's first line. It carries its own version, so
	// that a future marker format is a refusal to touch the directory rather
	// than a silent misreading of it.
	markerHeader = "go-mutants-workspace-v1"

	// workspaceKeyLength is how many hex characters of the workspace key name
	// the directory. Sixteen is short enough to be typed into a `cd` and long
	// enough that a collision needs about 2^32 distinct workspaces on one
	// machine — and the marker turns even that into a refusal rather than into
	// two projects' histories interleaved.
	workspaceKeyLength = 16

	// tempPattern is the name temporary files are created under. The prefix is
	// deliberately recognisable: anything matching it is this package's
	// leftovers from an interrupted write and is safe to delete.
	tempPattern = "go-mutants-report-*.tmp"
)

// renameDelays are the waits between attempts at moving a temporary file into
// place. Windows refuses a rename onto a file another process still has open —
// a report someone is reading in an editor, an antivirus scanner that has not
// let go yet — and the condition clears in milliseconds. The delays are short
// and finite: three retries and then the honest error, never an unbounded wait
// inside a report write.
var renameDelays = []time.Duration{5 * time.Millisecond, 20 * time.Millisecond, 100 * time.Millisecond}

// errMarkerExists is [createMarker]'s answer when the directory is already
// claimed. It is not a failure: it is the question the claim was asked.
//
// It is a sentinel of this package's own rather than the operating system's
// fs.ErrExist because the claim goes through a temporary file, and creating one
// can raise an ErrExist of its own when os.CreateTemp runs out of names to try.
// The two would be indistinguishable and mean opposite things — "another
// process got here first", which is answerable by reading its marker, against
// "nothing could be written at all", which is not.
var errMarkerExists = errors.New("the workspace marker already exists")

// WorkspaceKey is the directory name one workspace's history lives under: the
// first 16 hex characters of the SHA-256 of its workspace digest.
//
// The digest is hashed again rather than used directly for two reasons. It is
// half the length, which matters for a path on Windows; and it keeps the
// content digest of somebody's source tree out of a directory name that a
// screenshot or a `ls` in a bug report will show.
func WorkspaceKey(workspaceDigest string) string {
	sum := sha256.Sum256([]byte(workspaceDigest))
	return hex.EncodeToString(sum[:])[:workspaceKeyLength]
}

// A History is a run-history store rooted at one directory.
//
// # Why an ownership marker
//
// The store lives in the operating system's cache directory, which is shared
// with every other tool on the machine and is routinely emptied, synchronised,
// and pointed somewhere else by CI images. go-mutants writes files there and
// will later delete them wholesale — `report clean` — so it must be able to
// prove that a directory is its own before touching it. The marker is that
// proof: a two-line file naming the format and the full workspace digest. A
// directory with no marker, or with a marker naming a different workspace, is
// refused rather than written to, which turns the one failure mode a truncated
// key has — two workspaces landing on one key — into a diagnosable error
// instead of two projects' histories quietly interleaving.
type History struct {
	// Root is the directory the store lives in. Empty means
	// <os.UserCacheDir>/go-mutants, which is what every real run uses; the
	// tests set it so that they never touch the developer's own cache.
	Root string
}

// WriteHistory writes a report to the default history store, returning the path
// of the per-run document and of the latest pointer.
func WriteHistory(r *Report) (runPath, latestPath string, err error) {
	return History{}.Write(r)
}

// Write stores one report.
//
// Both files are the same bytes, marshalled once: `latest.json` is a copy of
// the newest run rather than a pointer to it, so that reading the newest run is
// one file open and cannot race a `report clean` that removed the file a
// pointer would have named.
//
// The order is the crash contract. The marker is claimed first, so a foreign
// directory is refused before anything is written to it; the run file next; the
// latest pointer last. Both documents are written to a temporary file in their
// own destination directory and then renamed, which is atomic on both platforms
// this targets, so a crash at any point leaves either the previous state or the
// new one — never a half-written report, and never a `latest.json` naming a run
// that is not on disk. The marker is the one file not written that way, because
// a rename replaces what it lands on and an ownership claim must not; see
// [claim].
func (h History) Write(r *Report) (runPath, latestPath string, err error) {
	if r == nil {
		return "", "", &Error{
			Code:    CodeNoReport,
			Message: "there is no report to write",
		}
	}
	if !runIDPattern.MatchString(r.RunID) {
		return "", "", &Error{
			Code:    CodeInvalidRunID,
			Message: "the report's run id " + quote(r.RunID) + " cannot be used as a file name",
		}
	}
	if !digestPattern.MatchString(r.Workspace.WorkspaceDigest) {
		return "", "", &Error{
			Code:    CodeInvalidWorkspaceDigest,
			Message: "the report's workspace digest " + quote(r.Workspace.WorkspaceDigest) + " cannot name a history directory",
		}
	}
	data, err := r.Marshal()
	if err != nil {
		return "", "", err
	}

	// Claimed before anything below it is created: a directory that turns out
	// to belong to something else must be left exactly as it was found, and a
	// stray empty `runs/` in somebody's cache is still a change to it.
	dir, err := h.Claim(r.Workspace.WorkspaceDigest)
	if err != nil {
		return "", "", err
	}
	runs := filepath.Join(dir, RunsDirName)
	if mkErr := os.MkdirAll(runs, 0o700); mkErr != nil {
		return "", "", &Error{
			Code:    CodeHistoryDirectory,
			Message: "the run history directory " + runs + " could not be created",
			Err:     mkErr,
		}
	}

	runPath = filepath.Join(runs, r.RunID+".json")
	if writeErr := writeAtomic(runPath, data); writeErr != nil {
		return "", "", writeErr
	}
	latestPath = filepath.Join(dir, LatestFileName)
	if writeErr := writeAtomic(latestPath, data); writeErr != nil {
		// The run file is already on disk and stays there. A history with a
		// stale pointer is worth more than one with a missing run, and the
		// caller is told which write failed.
		return runPath, "", writeErr
	}
	return runPath, latestPath, nil
}

// WriteFile writes a report to a path of the caller's choosing, atomically.
//
// It is the history store's own write without the store: the same
// temporary-file-and-rename, so that a crash leaves either the previous file or
// the new one and never a correctly named file full of nothing. `report merge
// --output` is what it exists for — a document a CI job is about to publish
// deserves the same care as one go-mutants files for itself.
func WriteFile(path string, r *Report) error {
	if r == nil {
		return &Error{
			Code:    CodeNoReport,
			Message: "there is no report to write",
		}
	}
	data, err := r.Marshal()
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// WorkspaceDir returns the directory one workspace's history lives in, without
// creating it.
func (h History) WorkspaceDir(workspaceDigest string) (string, error) {
	root, err := h.root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, WorkspacesDirName, WorkspaceKey(workspaceDigest)), nil
}

// Claim creates the workspace directory if it is not there, proves it is
// go-mutants' own, and returns its path.
//
// It is exported because the run history is not the only thing filed under a
// workspace directory: the outcome cache writes `outcomes/` beside `runs/`, and
// the ownership argument in [History] is about the directory rather than about
// either store. A second implementation of the marker dance would be a second
// place for the one property that makes deleting files in somebody's cache
// directory safe to be wrong. See [claim] for the race the marker settles.
func (h History) Claim(workspaceDigest string) (string, error) {
	if !digestPattern.MatchString(workspaceDigest) {
		return "", &Error{
			Code:    CodeInvalidWorkspaceDigest,
			Message: "the workspace digest " + quote(workspaceDigest) + " cannot name a workspace directory",
		}
	}
	dir, err := h.WorkspaceDir(workspaceDigest)
	if err != nil {
		return "", err
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return "", &Error{
			Code:    CodeHistoryDirectory,
			Message: "the workspace directory " + dir + " could not be created",
			Err:     mkErr,
		}
	}
	if claimErr := claim(dir, workspaceDigest); claimErr != nil {
		return "", claimErr
	}
	return dir, nil
}

// ErrNoMarker reports a directory with no ownership marker at all.
//
// It is a sentinel rather than a coded [Error] because it is the one answer
// [ReadMarker] gives that is not a diagnosis: a directory in somebody's cache
// with no marker is a directory that is not go-mutants', which is a fact a
// caller walking a tree acts on rather than reports.
var ErrNoMarker = errors.New("the directory carries no go-mutants workspace marker")

// ReadMarker returns the workspace digest the marker in dir names.
//
// It is the read-only half of [History.Claim], for the commands that walk the
// cache root and must not create anything: `cache status`, `cache gc` and
// `cache clean` each ask it about every directory they find, and act only on
// the ones that answer. A directory with no marker reports [ErrNoMarker]; one
// whose marker is not a marker this build wrote reports
// [CodeForeignWorkspace], which is a refusal rather than a skip — a file called
// `go-mutants.marker` that this build cannot read means something is there that
// nobody should be deleting.
func ReadMarker(dir string) (string, error) {
	path := filepath.Join(dir, MarkerFileName)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", ErrNoMarker
	case err != nil:
		return "", &Error{
			Code:    CodeHistoryDirectory,
			Message: "the workspace marker " + path + " could not be read",
			Err:     err,
		}
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != markerHeader || !digestPattern.MatchString(lines[1]) {
		return "", &Error{
			Code: CodeForeignWorkspace,
			Message: "the marker " + path + " is not one this build of go-mutants wrote, " +
				"so the directory holding it will not be written to or deleted",
		}
	}
	return lines[1], nil
}

// root resolves the store's root directory.
func (h History) root() (string, error) {
	if h.Root != "" {
		return h.Root, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", &Error{
			Code:    CodeCacheUnavailable,
			Message: "the operating system's cache directory could not be determined, so this run cannot be kept in the history",
			Err:     err,
		}
	}
	return filepath.Join(cache, DirName), nil
}

// claim proves the directory is go-mutants' own, and makes it so the first
// time.
//
// Between the read that finds no marker and the write that creates one, another
// process may claim the same directory for a different workspace — a truncated
// key collision, or one restored CI cache reached by two checkouts at once —
// and noticing exactly that is what the marker is for. So the marker is not
// written through the temporary-file-and-rename dance the reports use:
// [createMarker] refuses an existing marker where a rename would silently
// replace it, and that difference is the whole race argument. Under a rename
// every racer's write would succeed and overwrite the last, so reading the
// marker back afterwards could prove nothing and the marker would end up naming
// whichever workspace wrote last — with the others filing their runs underneath
// it. As it is, exactly one racer creates the file; the rest are told it exists,
// read what the winner left, and are answered by [sameMarker] — a refusal for
// another workspace, and nothing at all for a second run of this one.
func claim(dir, workspaceDigest string) error {
	path := filepath.Join(dir, MarkerFileName)
	want := markerHeader + "\n" + workspaceDigest + "\n"

	switch got, err := os.ReadFile(path); {
	case err == nil:
		return sameMarker(path, string(got), want)
	case !errors.Is(err, fs.ErrNotExist):
		return &Error{
			Code:    CodeHistoryDirectory,
			Message: "the workspace marker " + path + " could not be read",
			Err:     err,
		}
	}
	switch err := createMarker(path, want); {
	case err == nil:
		// The create succeeded, so this process is the one that made the file.
		// There is nothing to read back: no other claim can have replaced it,
		// because no other claim can replace anything.
		return nil
	case !errors.Is(err, errMarkerExists):
		return err
	}
	// A marker appeared between the read and the create. Whether it is this
	// workspace's — a second run of the same project — or another workspace's is
	// the question sameMarker answers, and losing the race is safe because the
	// marker that won it is still intact.
	got, err := os.ReadFile(path)
	if err != nil {
		return &Error{
			Code:    CodeHistoryDirectory,
			Message: "the workspace marker " + path + " could not be read back",
			Err:     err,
		}
	}
	return sameMarker(path, string(got), want)
}

// createMarker creates the marker, refusing to touch an existing one.
//
// It reports [errMarkerExists] when the marker is already there, which is not a
// failure but the answer [claim] asked for; every other failure comes back as
// this package's [Error].
//
// The contents are written to a temporary file and flushed, and the marker's
// name is then created by hard-linking that file into place. The order is the
// point: a name that exists before its contents do is a marker naming no
// workspace, and a second run of the *same* project reading it in that window
// would be told its own history directory belongs to something else. os.Link is
// the one operation on both platforms this targets that creates a name with the
// contents already behind it and fails, rather than replaces, when the name is
// taken — which is the other half of what a claim has to do.
//
// The temporary file lives in the directory being claimed, so a claim that
// loses the race does touch a directory it then refuses. It is one file under
// the recognisable [tempPattern], and it is removed before the refusal is
// returned; a claim refused against a marker that was already there — the
// ordinary collision, the one [History] is paranoid about — never gets this far
// and creates nothing at all.
func createMarker(path, content string) error {
	temp, err := writeTemp(filepath.Dir(path), "the workspace marker", []byte(content))
	if err != nil {
		return err
	}
	// Removed whether the link succeeds or not: on success the marker is the
	// other name for the same contents, and the temporary one has no further use.
	defer func() { _ = os.Remove(temp) }()

	switch err = os.Link(temp, path); {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return errMarkerExists
	}
	// A filesystem that will not hard-link: a removable exFAT drive, an exotic
	// network mount, somebody's cache directory pointed at either. Creating the
	// name and then filling it keeps the refusal — an exclusive create fails
	// against an existing marker exactly as a link does — and gives up only the
	// window above, which needs two runs of one project on that filesystem
	// starting in the same instant to matter. Losing the history of every run to
	// an unlinkable cache would be the worse trade.
	return createMarkerInPlace(path, content)
}

// createMarkerInPlace creates the marker without hard-linking. See
// [createMarker] for when it is reached and what it costs.
//
// Like [createMarker] it reports [errMarkerExists] when the marker already
// exists. A failure after the exclusive create leaves the marker's name behind
// with nothing in it, which would refuse the directory to every later run, so
// the name is removed again when the contents never arrived.
func createMarkerInPlace(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errMarkerExists
		}
		return &Error{
			Code:    CodeHistoryWrite,
			Message: "the workspace marker " + path + " could not be created",
			Err:     err,
		}
	}
	if _, err = file.WriteString(content); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// The claim behind the name was never made, so the name goes too.
		_ = os.Remove(path)
		return &Error{
			Code:    CodeHistoryWrite,
			Message: "the workspace marker " + path + " could not be written",
			Err:     err,
		}
	}
	return nil
}

// sameMarker reports whether the marker on disk is this workspace's.
func sameMarker(path, got, want string) error {
	if got == want {
		return nil
	}
	return &Error{
		Code: CodeForeignWorkspace,
		Message: "the history directory holding " + path + " belongs to something else: " +
			"its marker does not name this workspace, so go-mutants will not write to it",
	}
}

// writeAtomic writes data to path through a temporary file in the same
// directory.
//
// Same directory, therefore same volume, therefore a rename that is a rename
// rather than a copy — the one property that makes this atomic. The contents
// are flushed to the device before the rename, so that a crash immediately
// after cannot leave a correctly named file full of nothing, which is the
// failure mode that makes a report file worse than no report file.
func writeAtomic(path string, data []byte) error {
	name, err := writeTemp(filepath.Dir(path), "the report", data)
	if err != nil {
		return err
	}
	// Removed on every failing path. A successful rename makes this a no-op
	// against a name that no longer exists, which is why the error is dropped.
	defer func() { _ = os.Remove(name) }()

	if err = rename(name, path); err != nil {
		return &Error{
			Code:    CodeHistoryWrite,
			Message: "the report could not be moved into place at " + path,
			Err:     err,
		}
	}
	return nil
}

// writeTemp writes data to a fresh temporary file in dir and returns its name,
// which is the caller's to move into place and the caller's to remove.
//
// what names the thing being written — "the report", "the workspace marker" —
// because these messages are read by somebody whose disk just filled up, and
// which file it was is the first thing they need to know.
//
// The contents are flushed to the device before the name is handed back: both
// callers turn a temporary file into a permanent one with a single operation
// afterwards, and a crash just after that operation must not leave a correctly
// named file full of nothing.
func writeTemp(dir, what string, data []byte) (string, error) {
	temp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return "", &Error{
			Code:    CodeHistoryWrite,
			Message: "a temporary file for " + what + " could not be created in " + dir,
			Err:     err,
		}
	}
	name := temp.Name()
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return "", &Error{
			Code:    CodeHistoryWrite,
			Message: what + " could not be written to " + name,
			Err:     err,
		}
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return "", &Error{
			Code:    CodeHistoryWrite,
			Message: what + " written to " + name + " could not be flushed to disk",
			Err:     err,
		}
	}
	if err = temp.Close(); err != nil {
		_ = os.Remove(name)
		return "", &Error{
			Code:    CodeHistoryWrite,
			Message: what + " written to " + name + " could not be closed",
			Err:     err,
		}
	}
	return name, nil
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

// quote renders a rejected value inside a message.
//
// It escapes rather than merely surrounding the value with quotation marks: the
// values quoted here are the ones that failed a shape check, so they are
// exactly the strings likely to contain a newline or a control character that
// would otherwise break the one-error-one-line shape everything else keeps.
func quote(s string) string { return strconv.Quote(s) }
