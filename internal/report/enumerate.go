// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The read-only half of the history store, and the one deletion it allows:
// what `report list`, `report latest` and `report clean` are made of.
//
// Four rules hold everything here together. The first three are the ones the
// outcome cache's maintenance obeys too — see internal/cache — because both
// walk a directory go-mutants shares with every other program on the machine:
//
//   - Nothing is looked inside without an ownership marker naming a workspace.
//     A directory with no marker, or with one this build did not write, is
//     skipped and reported rather than read or deleted; see [ReadMarker].
//   - A document that cannot be read is a row in the listing, never an error.
//     One truncated file in a directory of fifty runs must not cost a user the
//     other forty-nine, and "this file is not a run report" is exactly the
//     thing they need told.
//   - Nothing outside `<root>/workspaces/<key>/runs` and that directory's
//     `latest.json` is ever deleted. The outcome cache's `outcomes/` sits
//     beside them and belongs to `cache clean`, the marker stays so that the
//     directory keeps its identity, and every path is built here from the root
//     and a workspace key rather than accepted from a caller.
//
// The fourth is this package's alone, and it is what makes the third one hold
// across a listing and the deletion that follows it. A workspace directory has
// to be named after the digest its marker states — `WorkspaceKey(digest)`, and
// nothing else — or it is skipped and reported rather than listed; see [List].
// [History.RemoveRuns] is asked for a digest and rebuilds the canonical path
// from it, so a directory somebody copied or renamed — a restored CI cache, a
// `cp -r` backup — would otherwise be listed as history, "cleaned" against the
// original's path, reported as removed, and still be sitting there afterwards.
//
// Only the fields a listing needs are materialised from each document. A run
// report carries every mutant in the catalogue, and a history directory can
// hold hundreds of them, so decoding one into this package's full [Report] to
// print four columns would allocate tens of megabytes of structs nothing reads.
// The bytes are still parsed end to end, and deliberately: that is what makes a
// truncated file a [Damaged] row rather than a listing that quietly believes
// half a document.

// A StoredRun is one run the history store holds, decoded as far as listing it
// needs and no further.
type StoredRun struct {
	// RunID is the run's identity, as the document states it. It is also the
	// file's name, but it is read from inside rather than off the directory
	// entry: the document is the record, and a file somebody renamed should not
	// change what the listing says happened.
	RunID string
	// Path is the absolute path of the document this was read from.
	Path string
	// ModulePath is `workspace.module_path`: which project the run measured.
	// It is how the commands tell one workspace's history from another's, since
	// a history directory is named after a digest of the tree's contents and
	// therefore changes with every edit; see [History].
	ModulePath string
	// Status is how the run ended.
	Status Status
	// FinishedAt is when it ended, parsed from the document's RFC 3339 stamp.
	FinishedAt time.Time
	// Summary is the counted breakdown and the score, exactly as stored.
	Summary Summary
	// Bytes is the document's size on disk.
	Bytes int64
}

// Score returns the run's mutation score, and false when the run measured
// nothing to take one over. See [Summary.ScorePercent] for why there is no
// sentinel percentage.
func (r StoredRun) Score() (float64, bool) {
	if r.Summary.ScorePercent == nil {
		return 0, false
	}
	return *r.Summary.ScorePercent, true
}

// A Damaged is one file in a history directory that is not a run report this
// build can read, and why.
//
// It is reported rather than returned as an error; see the rules above.
type Damaged struct {
	// Path is the absolute path of the file.
	Path string
	// Reason is one line saying what is wrong with it, without a code: the
	// listing that prints these is already a list of things that could not be
	// read, and a code on every line of it would be noise.
	Reason string
}

// A Skipped is one directory under `workspaces/` that the walk would not look
// inside, and why.
type Skipped struct {
	// Name is the directory's name under `workspaces/`.
	Name string
	// Reason is one line saying why it was left alone.
	Reason string
}

// A StoredWorkspace is one workspace directory and the runs filed in it.
//
// One project has as many of these as it has had distinct trees measured, and
// they are not distinguishable by their names: the directory is named after a
// digest of the workspace's contents, so an edit between two runs files them
// apart. [StoredRun.ModulePath] is what joins them back together.
type StoredWorkspace struct {
	// Key is the directory's name: the truncated workspace key.
	Key string
	// Dir is the absolute path of the workspace directory.
	Dir string
	// Digest is the full workspace digest its marker names.
	Digest string
	// Runs are the documents under `runs/`, newest first, plus whatever
	// `latest.json` names that is no longer filed there; see [History.List].
	Runs []StoredRun
	// Latest is the run id `latest.json` holds, or empty when there is no
	// pointer or it could not be read.
	Latest string
	// Damaged are the files here that are not run reports this build can read.
	Damaged []Damaged
}

// A Listing is the whole history store as it was found.
type Listing struct {
	// Root is the store root that was walked.
	Root string
	// Workspaces are the owned workspace directories, ordered by key.
	Workspaces []StoredWorkspace
	// Skipped are the directories under `workspaces/` that are not go-mutants'.
	Skipped []Skipped
}

// A Removed is what one [History.RemoveRuns] deleted.
type Removed struct {
	// Dir is the workspace directory that was operated on. It is reported even
	// when nothing was removed, so that a caller can say where it looked.
	Dir string
	// Runs is how many stored documents were deleted, `latest.json` included.
	Runs int
	// Bytes is what they took up on disk.
	Bytes int64
}

// List reads the whole history store.
//
// A root that does not exist is an empty store rather than a failure: nothing
// has been run on this machine yet, which is a perfectly good answer to
// `report list`. Every other failure to *walk* is returned, because a store
// that cannot be listed is not a store with nothing in it — and the difference
// between those two is the whole reason this returns an error at all.
//
// `latest.json` is read as well as `runs/`, and for one reason: it is a copy of
// the newest run rather than a pointer to it, so a history whose `runs/` was
// partly removed still has the newest document in full. When it names a run
// that is no longer filed under `runs/`, that run joins the listing from
// `latest.json` instead of disappearing from it.
//
// A directory whose name is not the key its own marker's digest names is
// skipped and reported, alongside the ones that carry no marker at all. It is
// the one skip that is not about ownership — the marker may be perfectly
// genuine — and it is what keeps this listing and `report clean` describing the
// same store; see the fourth rule above.
func (h History) List() (Listing, error) {
	root, err := h.root()
	if err != nil {
		return Listing{}, err
	}
	listing := Listing{Root: root, Workspaces: []StoredWorkspace{}, Skipped: []Skipped{}}

	base := filepath.Join(root, WorkspacesDirName)
	entries, err := os.ReadDir(base)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return listing, nil
	case err != nil:
		return Listing{}, &Error{
			Code:    CodeHistoryDirectory,
			Message: "the history directory " + base + " could not be listed",
			Err:     err,
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		digest, markerErr := ReadMarker(dir)
		switch {
		case errors.Is(markerErr, ErrNoMarker):
			listing.Skipped = append(listing.Skipped, Skipped{
				Name:   entry.Name(),
				Reason: "it carries no go-mutants workspace marker",
			})
			continue
		case markerErr != nil:
			listing.Skipped = append(listing.Skipped, Skipped{Name: entry.Name(), Reason: reasonOf(markerErr)})
			continue
		}
		// A marker is a claim about one directory, and the directory it claims is
		// the one this build would have named for that workspace. Anything else is
		// a copy of a workspace rather than a workspace, and listing it as history
		// would promise a `report clean` this store cannot keep: the sweep deletes
		// by digest, which names the original, so the copy would be reported as
		// removed and would survive. See the fourth rule above.
		if key := WorkspaceKey(digest); entry.Name() != key {
			listing.Skipped = append(listing.Skipped, Skipped{
				Name: entry.Name(),
				Reason: "its marker names the workspace filed under " + key +
					", so it is a copy of that directory rather than one of this store's own",
			})
			continue
		}
		workspace, readErr := readWorkspace(entry.Name(), dir, digest)
		if readErr != nil {
			return Listing{}, readErr
		}
		listing.Workspaces = append(listing.Workspaces, workspace)
	}
	// Ordered by the directory name, which is a hash and so arbitrary — but
	// arbitrary and stable, so two listings of an unchanged store can be diffed.
	slices.SortFunc(listing.Workspaces, func(x, y StoredWorkspace) int {
		return strings.Compare(x.Key, y.Key)
	})
	slices.SortFunc(listing.Skipped, func(x, y Skipped) int { return strings.Compare(x.Name, y.Name) })
	return listing, nil
}

// RemoveRuns deletes one workspace's run history: everything under `runs/` and
// the `latest.json` beside it.
//
// The directory is named from the digest rather than passed in, and the marker
// on disk is read back and required to name that same digest, so a caller
// cannot ask for a path — the one shape of request that could reach outside the
// store. What is left behind is deliberate: the marker, because the directory's
// identity outlives its contents and a claim that vanished would let the next
// run's directory be adopted by something else, and `outcomes/`, because the
// outcome cache filed it and `cache clean` is what removes it.
//
// A workspace directory that is not there has no history to delete, and that is
// not a failure: it is the answer.
func (h History) RemoveRuns(workspaceDigest string) (Removed, error) {
	if !digestPattern.MatchString(workspaceDigest) {
		return Removed{}, &Error{
			Code:    CodeInvalidWorkspaceDigest,
			Message: "the workspace digest " + quote(workspaceDigest) + " cannot name a workspace directory",
		}
	}
	root, err := h.root()
	if err != nil {
		return Removed{}, err
	}
	dir := filepath.Join(root, WorkspacesDirName, WorkspaceKey(workspaceDigest))
	removed := Removed{Dir: dir}

	switch digest, markerErr := ReadMarker(dir); {
	case errors.Is(markerErr, ErrNoMarker):
		if _, statErr := os.Stat(dir); errors.Is(statErr, fs.ErrNotExist) {
			return removed, nil
		}
		return removed, &Error{
			Code: CodeForeignWorkspace,
			Message: "the directory " + dir + " carries no go-mutants workspace marker, " +
				"so nothing in it was deleted",
		}
	case markerErr != nil:
		return removed, markerErr
	case digest != workspaceDigest:
		return removed, &Error{
			Code: CodeForeignWorkspace,
			Message: "the marker in " + dir + " names another workspace, " +
				"so nothing in it was deleted",
		}
	}

	runs := filepath.Join(dir, RunsDirName)
	files, err := storedFiles(runs)
	if err != nil {
		return removed, err
	}
	for _, file := range files {
		removed.Runs++
		removed.Bytes += file.size
	}
	if err = removeInside(runs, root); err != nil {
		return removed, err
	}

	latest := filepath.Join(dir, LatestFileName)
	switch info, statErr := os.Stat(latest); {
	case statErr == nil:
		removed.Runs++
		removed.Bytes += info.Size()
		if err = removeInside(latest, root); err != nil {
			return removed, err
		}
	case !errors.Is(statErr, fs.ErrNotExist):
		return removed, &Error{
			Code:    CodeHistoryNotRemoved,
			Message: latest + " could not be measured, so it was not deleted",
			Err:     statErr,
		}
	}
	return removed, nil
}

// ReadStored reads a document out of the history store, verbatim.
//
// It is what `report latest --json` prints: the bytes that were filed, rather
// than this build's re-encoding of them. A document written by an older release
// would otherwise be silently reshaped on its way to standard output, which is
// the one thing an archive must never do.
func ReadStored(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{
			Code:    CodeHistoryUnreadable,
			Message: "the stored run report " + path + " could not be read",
			Err:     err,
		}
	}
	return data, nil
}

// readWorkspace reads one owned workspace directory.
func readWorkspace(key, dir, digest string) (StoredWorkspace, error) {
	workspace := StoredWorkspace{
		Key:     key,
		Dir:     dir,
		Digest:  digest,
		Runs:    []StoredRun{},
		Damaged: []Damaged{},
	}
	files, err := storedFiles(filepath.Join(dir, RunsDirName))
	if err != nil {
		return StoredWorkspace{}, err
	}
	for _, file := range files {
		run, reason := readStoredRun(file)
		if reason != "" {
			workspace.Damaged = append(workspace.Damaged, Damaged{Path: file.path, Reason: reason})
			continue
		}
		workspace.Runs = append(workspace.Runs, run)
	}

	// The pointer is read last, so that a run already found under `runs/` wins:
	// the two hold the same bytes, and the copy under `runs/` is the one whose
	// path a user can act on.
	latest := filepath.Join(dir, LatestFileName)
	switch info, statErr := os.Stat(latest); {
	case statErr == nil:
		run, reason := readStoredRun(storedFile{path: latest, size: info.Size()})
		switch {
		case reason != "":
			workspace.Damaged = append(workspace.Damaged, Damaged{Path: latest, Reason: reason})
		default:
			workspace.Latest = run.RunID
			if !slices.ContainsFunc(workspace.Runs, func(other StoredRun) bool { return other.RunID == run.RunID }) {
				workspace.Runs = append(workspace.Runs, run)
			}
		}
	case !errors.Is(statErr, fs.ErrNotExist):
		return StoredWorkspace{}, &Error{
			Code:    CodeHistoryDirectory,
			Message: "the pointer to the newest run, " + latest + ", could not be read",
			Err:     statErr,
		}
	}

	slices.SortFunc(workspace.Runs, NewestFirst)
	slices.SortFunc(workspace.Damaged, func(x, y Damaged) int { return strings.Compare(x.Path, y.Path) })
	return workspace, nil
}

// NewestFirst orders runs by when they finished, most recent first.
//
// It is exported because one module's runs are gathered out of several
// workspace directories by the commands that print them — a workspace digest
// changes with every edit, so a project's history is spread across as many
// directories as it has had trees measured — and a second comparator over there
// would be a second place for the order `report list` and `report latest`
// promise to be wrong.
//
// Two tiebreaks follow the timestamp, and both are needed for the total order
// this claims. The run id carries the second the run started and four random
// hex digits, so two runs that finished inside one second are still ordered.
// The path settles what the id cannot, which is one document reachable under
// two names: the ids are equal there because it is the same run. Without it the
// comparator answers 0 for two distinct rows, and an unstable sort is then free
// to return either — which is how `report latest` came to name a different file
// on two runs of the same command over one store.
func NewestFirst(x, y StoredRun) int {
	if !x.FinishedAt.Equal(y.FinishedAt) {
		if x.FinishedAt.After(y.FinishedAt) {
			return -1
		}
		return 1
	}
	if order := strings.Compare(y.RunID, x.RunID); order != 0 {
		return order
	}
	return strings.Compare(x.Path, y.Path)
}

// A storedFile is one candidate document and its size.
type storedFile struct {
	path string
	size int64
}

// storedFiles lists the documents in a `runs/` directory.
//
// A directory that is not there holds no runs, which is the answer for a
// workspace the outcome cache claimed and no report was ever filed under. Only
// `.json` files are returned, so that a temporary file from an interrupted
// write is neither listed as a run nor deleted as one.
func storedFiles(dir string) ([]storedFile, error) {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &Error{
			Code:    CodeHistoryDirectory,
			Message: "the run history directory " + dir + " could not be listed",
			Err:     err,
		}
	}
	files := make([]storedFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			// The file went away between the listing and the stat: another
			// process's `report clean`, or somebody's cache cleaner. It is not
			// there to list.
			if errors.Is(infoErr, fs.ErrNotExist) {
				continue
			}
			return nil, &Error{
				Code:    CodeHistoryDirectory,
				Message: "the stored run " + filepath.Join(dir, entry.Name()) + " could not be measured",
				Err:     infoErr,
			}
		}
		files = append(files, storedFile{path: filepath.Join(dir, entry.Name()), size: info.Size()})
	}
	slices.SortFunc(files, func(x, y storedFile) int { return strings.Compare(x.path, y.path) })
	return files, nil
}

// storedRun is the part of a run report the history commands read. Everything
// else in the document — every mutant, every rejection, every skip — is left in
// the file.
type storedRun struct {
	DocumentType  string `json:"document_type"`
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Status        Status `json:"status"`
	FinishedAt    string `json:"finished_at"`
	Workspace     struct {
		ModulePath string `json:"module_path"`
	} `json:"workspace"`
	Summary Summary `json:"summary"`
}

// readStoredRun decodes one document's header, or says in one line why it is
// not one.
//
// The checks are the identity of the document and nothing else: the type, the
// version, and the four fields a listing prints. A report that is valid here
// and would fail the published schema is possible, and is not this function's
// business — `report validate` is — because a listing that refused to name a
// run until it had validated every mutant in it would be unusable on exactly
// the histories worth looking at.
func readStoredRun(file storedFile) (StoredRun, string) {
	data, err := os.ReadFile(file.path)
	if err != nil {
		return StoredRun{}, "it could not be read: " + err.Error()
	}
	var doc storedRun
	if err = json.Unmarshal(data, &doc); err != nil {
		return StoredRun{}, "it is not a run report this build can read: " + err.Error()
	}
	switch {
	case doc.DocumentType != DocumentType:
		return StoredRun{}, "it is " + quote(doc.DocumentType) + ", not a " + DocumentType + " document"
	case doc.SchemaVersion != SchemaVersion:
		return StoredRun{}, "it is schema version " + strconv.Itoa(doc.SchemaVersion) +
			", and this build reads version " + strconv.Itoa(SchemaVersion)
	case !runIDPattern.MatchString(doc.RunID):
		return StoredRun{}, "its run id " + quote(doc.RunID) + " is not a run id"
	case !doc.Status.Valid():
		return StoredRun{}, "its status " + quote(string(doc.Status)) + " is not one a run can end in"
	}
	finished, err := time.Parse(time.RFC3339, doc.FinishedAt)
	if err != nil {
		return StoredRun{}, "its finish time " + quote(doc.FinishedAt) + " is not an RFC 3339 timestamp"
	}
	return StoredRun{
		RunID:      doc.RunID,
		Path:       file.path,
		ModulePath: doc.Workspace.ModulePath,
		Status:     doc.Status,
		FinishedAt: finished,
		Summary:    doc.Summary,
		Bytes:      file.size,
	}, ""
}

// reasonOf renders a refusal for a [Skipped] row, dropping the code in front of
// it: the row is already a list of things not touched, and repeating GOM5133 on
// every line of it would be noise.
func reasonOf(err error) string {
	message := err.Error()
	if _, rest, found := strings.Cut(message, ": "); found && strings.HasPrefix(message, "GOM") {
		return rest
	}
	return message
}

// removeInside deletes one path, having proved it is inside the store root.
//
// The containment check is not there because a caller might get it wrong today:
// every path handed to it is built from the root, a workspace key, and a
// constant. It is there because this is one of the two places in go-mutants
// that delete files in a directory shared with every other tool on the machine
// — internal/cache's `remove` is the other, and makes the same argument — and a
// check that makes an escape unrepresentable is worth more than an argument
// that it cannot happen.
//
// A path that is not there is already gone, which is what was asked for.
func removeInside(path, root string) error {
	inside, err := within(path, root)
	if err != nil {
		return err
	}
	if !inside {
		return &Error{
			Code:    CodeHistoryNotRemoved,
			Message: path + " is not inside the history store at " + root + ", so it was not deleted",
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return &Error{
			Code:    CodeHistoryNotRemoved,
			Message: path + " could not be deleted",
			Err:     err,
		}
	}
	return nil
}

// within reports whether path is strictly inside root, comparing the two as the
// filesystem resolves them rather than as they are spelled.
//
// The resolution is the point. A comparison of the two strings answers a
// question about two strings, and [os.RemoveAll] asks the filesystem: a
// workspace directory replaced by a link to somewhere else is lexically inside
// the store and physically wherever it points, so deleting
// `<root>/workspaces/<key>/runs` through it would take the target's `runs` with
// it. Nothing go-mutants writes creates such a link, which is exactly why this
// is worth checking — the store is a directory in the operating system's cache
// that anything on the machine can write to.
//
// The last element is deliberately left unresolved. RemoveAll unlinks a
// symbolic link rather than following it, so a linked leaf deletes the link and
// nothing else; it is the directories leading to it that are a way out, because
// walking them is what follows them. Resolving the parent alone also keeps the
// contract that a path which is not there is already gone: `runs/` is routinely
// absent by the time it is deleted, and the directory holding it is not.
//
// A path that cannot be resolved at all is refused rather than assumed
// innocent, and the refusal keeps this package's [CodeHistoryNotRemoved]: not
// knowing where a deletion would land is the one answer that must not end in a
// deletion.
//
// internal/cache's `within` is this function with the outcome cache's root and
// error code. The two are deliberate twins — see the note on [removeInside] —
// and a change to either belongs in the other.
func within(path, root string) (bool, error) {
	resolvedPath, err := resolveParent(path)
	if err != nil {
		return false, &Error{
			Code:    CodeHistoryNotRemoved,
			Message: path + " could not be resolved on disk, so it was not deleted",
			Err:     err,
		}
	}
	resolvedRoot, err := resolvePath(root)
	if err != nil {
		return false, &Error{
			Code:    CodeHistoryNotRemoved,
			Message: root + " could not be resolved on disk, so nothing under it was deleted",
			Err:     err,
		}
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return false, nil
	}
	return relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative), nil
}

// resolveParent resolves the directories leading to path, and leaves path's own
// last element alone. See [within] for why the leaf is left as it is.
func resolveParent(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := resolvePath(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

// resolvePath resolves every symbolic link in a path, tolerating a path that is
// not all there.
//
// [filepath.EvalSymlinks] needs the whole path to exist, and the paths this is
// asked about routinely do not: a workspace directory that was never created, a
// `runs/` a previous clean already emptied. So a missing name is resolved as
// far as the filesystem goes and the rest is appended verbatim, which is the
// same answer the full resolution would give once those names existed — and it
// is an answer about where a deletion *would* land, which is what the caller is
// deciding. Any other failure is returned, and refuses the deletion.
func resolvePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	switch {
	case err == nil:
		return trimExtendedPrefix(resolved), nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		// The volume root itself, which is where walking up stops. There is
		// nothing above it to resolve against and its own name is the answer.
		return path, nil
	}
	resolvedParent, err := resolvePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

// trimExtendedPrefix drops the `\\?\` Windows uses to spell a path that escapes
// the traditional length limit, so that a resolved path and a resolved root are
// compared in one spelling whichever of the two the operating system chose to
// hand back. Nothing outside Windows can be affected: a resolved path on any
// other platform begins with a separator that is not a backslash.
func trimExtendedPrefix(path string) string {
	if rest, found := strings.CutPrefix(path, `\\?\UNC\`); found {
		return `\\` + rest
	}
	if rest, found := strings.CutPrefix(path, `\\?\`); found {
		return rest
	}
	return path
}
