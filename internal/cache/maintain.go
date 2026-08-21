// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/report"
)

// The maintenance side of the store: what `cache status`, `cache gc` and
// `cache clean` are.
//
// All three walk one directory — `<root>/workspaces` — and all three obey the
// same two rules, which are the whole safety argument for a tool that deletes
// files in a directory it shares with every other program on the machine:
//
//   - Nothing is touched without an ownership marker naming a workspace. A
//     directory with no marker, or with one this build did not write, is
//     skipped and counted; see [report.ReadMarker].
//   - Nothing outside `<root>/workspaces/<key>/outcomes` is touched at all.
//     The run history's `runs/` and `latest.json` sit in the same directory and
//     are none of this file's business, and every path is built from the root
//     and a directory entry's own base name, so there is no way for one to be
//     assembled that climbs out.
//
// `cache gc` deletes by modification time and never by content: the age of an
// entry is a fact on the filesystem, and deciding by content would mean parsing
// every document in a directory of thousands before removing any of them.
//
// The age is the age since the entry was *written*, and not since it was last
// useful. Reading an entry touches nothing — [Cache.Lookup] only reads the file,
// and a hit is never written back — so an entry's modification time is fixed at
// the moment it was stored, and one read by every CI run for thirty-one days is
// removed exactly like one nothing has ever asked for. That is a deliberately
// blunt rule: the alternative is a write on every hit, which would turn the
// cheapest operation this package has into an fsync and would make two
// concurrent runs contend over files neither is changing.

// DefaultGCDays is how old an entry has to be before `cache gc` removes it.
//
// Thirty days is a month since it was written — see the note above; nothing
// here counts reads. An entry is only ever read by a run whose tool version,
// toolchain, code, catalogue, command, timeout and environment all still match
// the ones that wrote it, so anything a month old has almost certainly outlived
// the context that could read it, whether or not that context was still asking
// for it yesterday.
const DefaultGCDays = 30

// A Workspace is one workspace directory, and what the outcome cache holds in
// it.
type Workspace struct {
	// Key is the directory's name: the truncated workspace key.
	Key string
	// Dir is the absolute path of the workspace directory.
	Dir string
	// Digest is the full workspace digest the marker names.
	Digest string
	// Contexts is how many key contexts have entries filed under them.
	Contexts int
	// Entries is how many stored outcomes there are.
	Entries int
	// Bytes is what they take up on disk, as the file sizes report it.
	Bytes int64
	// Newest is the modification time of the most recently written entry, and
	// the zero time when there are none.
	Newest time.Time
}

// A Skipped is one directory the walk refused to look inside, and why.
//
// It is reported rather than swallowed. A cache that quietly ignored half of
// what it found would make "0 entries" and "5 directories I would not touch"
// the same output, and only one of them means the cache is empty.
type Skipped struct {
	// Name is the directory's name under `workspaces/`.
	Name string
	// Reason is one line saying why it was left alone.
	Reason string
}

// A Survey is what `cache status` found.
type Survey struct {
	// Root is the cache root that was walked.
	Root string
	// Workspaces are the owned workspace directories, ordered by key.
	Workspaces []Workspace
	// Skipped are the directories under `workspaces/` that are not go-mutants'.
	Skipped []Skipped
}

// Entries is how many stored outcomes the whole cache holds.
func (s Survey) Entries() int {
	total := 0
	for _, workspace := range s.Workspaces {
		total += workspace.Entries
	}
	return total
}

// Bytes is what the whole cache takes up.
func (s Survey) Bytes() int64 {
	var total int64
	for _, workspace := range s.Workspaces {
		total += workspace.Bytes
	}
	return total
}

// A Sweep is what a deletion removed.
type Sweep struct {
	// Root is the cache root that was walked.
	Root string
	// Workspaces is how many workspace directories were touched.
	Workspaces int
	// Entries is how many stored outcomes were removed.
	Entries int
	// Contexts is how many now-empty context directories were removed with
	// them.
	Contexts int
	// Bytes is what the removed entries took up.
	Bytes int64
	// Skipped are the directories the walk would not touch.
	Skipped []Skipped
}

// Status surveys the cache without changing anything.
func Status(root string) (Survey, error) {
	survey := Survey{Root: root, Workspaces: []Workspace{}, Skipped: []Skipped{}}
	owned, skipped, err := walk(root)
	if err != nil {
		return Survey{}, err
	}
	survey.Skipped = skipped
	for _, workspace := range owned {
		measured, err := measure(workspace)
		if err != nil {
			return Survey{}, err
		}
		survey.Workspaces = append(survey.Workspaces, measured)
	}
	return survey, nil
}

// GC removes every entry last modified before cutoff, and every context
// directory that is left with nothing in it.
//
// The cutoff is passed in rather than computed from a day count here, so that
// the clock is the caller's and a test can state a moment instead of waiting
// for one.
//
// A deletion that fails stops the sweep and is reported, with everything
// removed up to that point in the returned [Sweep]. That is the opposite of
// this package's behaviour inside a run — where the cache never fails anything
// — and deliberately so: deleting is the whole of what `cache gc` was asked to
// do, so a `gc` that could not delete has not done its job and must not exit 0
// saying it did.
func GC(root string, cutoff time.Time) (Sweep, error) {
	sweep := Sweep{Root: root, Skipped: []Skipped{}}
	owned, skipped, err := walk(root)
	if err != nil {
		return sweep, err
	}
	sweep.Skipped = skipped
	for _, workspace := range owned {
		touched, err := collect(workspace, cutoff, &sweep)
		if touched {
			sweep.Workspaces++
		}
		if err != nil {
			return sweep, err
		}
	}
	return sweep, nil
}

// Clean removes every stored outcome, leaving the run history alone.
//
// It removes the `outcomes/` directory of each owned workspace and nothing
// else: the runs a workspace has filed are a record of what happened and are
// `report clean`'s to remove, not this command's.
func Clean(root string) (Sweep, error) {
	sweep := Sweep{Root: root, Skipped: []Skipped{}}
	owned, skipped, err := walk(root)
	if err != nil {
		return sweep, err
	}
	sweep.Skipped = skipped
	for _, workspace := range owned {
		measured, err := measure(workspace)
		if err != nil {
			return sweep, err
		}
		outcomes := filepath.Join(workspace.Dir, OutcomesDirName)
		if _, err = os.Stat(outcomes); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err = remove(outcomes, root); err != nil {
			return sweep, err
		}
		sweep.Workspaces++
		sweep.Entries += measured.Entries
		sweep.Contexts += measured.Contexts
		sweep.Bytes += measured.Bytes
	}
	return sweep, nil
}

// walk lists the workspace directories under a cache root, separating the ones
// go-mutants owns from the ones it will not touch.
//
// A root that does not exist is an empty cache rather than a failure: nothing
// has been cached on this machine yet, which is a perfectly good answer to
// `cache status` and to `cache gc` alike.
func walk(root string) ([]Workspace, []Skipped, error) {
	if root == "" {
		return nil, nil, &Error{
			Code:    CodeScanFailed,
			Message: "the outcome cache has no root directory to walk",
		}
	}
	base := filepath.Join(root, report.WorkspacesDirName)
	entries, err := os.ReadDir(base)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, []Skipped{}, nil
	case err != nil:
		return nil, nil, &Error{
			Code:    CodeScanFailed,
			Message: "the outcome cache directory " + base + " could not be listed",
			Err:     err,
		}
	}

	owned := make([]Workspace, 0, len(entries))
	skipped := make([]Skipped, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		digest, err := report.ReadMarker(dir)
		switch {
		case errors.Is(err, report.ErrNoMarker):
			skipped = append(skipped, Skipped{Name: entry.Name(), Reason: "it carries no go-mutants workspace marker"})
			continue
		case err != nil:
			skipped = append(skipped, Skipped{Name: entry.Name(), Reason: reasonOf(err)})
			continue
		}
		owned = append(owned, Workspace{Key: entry.Name(), Dir: dir, Digest: digest})
	}
	// Ordered by the directory name, which is a hash and therefore arbitrary —
	// but arbitrary and stable, so two runs of `cache status` over an unchanged
	// cache produce the same output and can be diffed.
	slices.SortFunc(owned, func(x, y Workspace) int { return strings.Compare(x.Key, y.Key) })
	slices.SortFunc(skipped, func(x, y Skipped) int { return strings.Compare(x.Name, y.Name) })
	return owned, skipped, nil
}

// reasonOf renders a refusal for a [Skipped] row, dropping the code that
// prefixes it: the row is already a list of things not touched, and repeating
// GOM5133 on every line of it would be noise.
func reasonOf(err error) string {
	message := err.Error()
	if _, rest, found := strings.Cut(message, ": "); found && strings.HasPrefix(message, "GOM") {
		return rest
	}
	return message
}

// measure counts what one workspace holds, without changing anything.
func measure(workspace Workspace) (Workspace, error) {
	contexts, err := contextDirs(workspace)
	if err != nil {
		return Workspace{}, err
	}
	for _, context := range contexts {
		files, err := entryFiles(context)
		if err != nil {
			return Workspace{}, err
		}
		if len(files) > 0 {
			workspace.Contexts++
		}
		for _, file := range files {
			info, err := file.Info()
			if err != nil {
				// The file went away between the listing and the stat, which is
				// another process's `gc` or somebody's cache cleaner. It is not
				// there to count.
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return Workspace{}, &Error{
					Code:    CodeScanFailed,
					Message: "a cached outcome in " + context + " could not be measured",
					Err:     err,
				}
			}
			workspace.Entries++
			workspace.Bytes += info.Size()
			if info.ModTime().After(workspace.Newest) {
				workspace.Newest = info.ModTime()
			}
		}
	}
	return workspace, nil
}

// collect removes one workspace's expired entries and reports whether it
// removed anything.
func collect(workspace Workspace, cutoff time.Time, sweep *Sweep) (bool, error) {
	contexts, err := contextDirs(workspace)
	if err != nil {
		return false, err
	}
	touched := false
	for _, context := range contexts {
		files, err := entryFiles(context)
		if err != nil {
			return touched, err
		}
		kept := 0
		for _, file := range files {
			info, err := file.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return touched, &Error{
					Code:    CodeScanFailed,
					Message: "a cached outcome in " + context + " could not be measured",
					Err:     err,
				}
			}
			// Strictly before, so that `--days 0` with a cutoff of now removes
			// what is already there and not what a concurrent run is writing
			// this instant.
			if !info.ModTime().Before(cutoff) {
				kept++
				continue
			}
			if err = remove(filepath.Join(context, file.Name()), sweep.Root); err != nil {
				return touched, err
			}
			touched = true
			sweep.Entries++
			sweep.Bytes += info.Size()
		}
		// Pruned only when the directory has nothing left at all — not merely no
		// entries — so that a temporary file a concurrent run is in the middle of
		// writing is never deleted out from under it.
		if kept == 0 {
			empty, err := isEmpty(context)
			if err != nil {
				return touched, err
			}
			if empty {
				if err = remove(context, sweep.Root); err != nil {
					return touched, err
				}
				touched = true
				sweep.Contexts++
			}
		}
	}
	return touched, nil
}

// contextDirs lists one workspace's context directories.
func contextDirs(workspace Workspace) ([]string, error) {
	outcomes := filepath.Join(workspace.Dir, OutcomesDirName)
	entries, err := os.ReadDir(outcomes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &Error{
			Code:    CodeScanFailed,
			Message: "the outcome directory " + outcomes + " could not be listed",
			Err:     err,
		}
	}
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(outcomes, entry.Name()))
		}
	}
	slices.Sort(dirs)
	return dirs, nil
}

// entryFiles lists the stored outcomes in one context directory.
//
// Only files ending in the entry suffix are returned, so that a temporary file
// from an interrupted write is neither counted as a stored outcome nor removed
// as one.
func entryFiles(context string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(context)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &Error{
			Code:    CodeScanFailed,
			Message: "the outcome directory " + context + " could not be listed",
			Err:     err,
		}
	}
	files := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), entrySuffix) {
			files = append(files, entry)
		}
	}
	return files, nil
}

// isEmpty reports whether a directory holds nothing at all.
func isEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, &Error{
			Code:    CodeScanFailed,
			Message: "the outcome directory " + dir + " could not be listed",
			Err:     err,
		}
	}
	return len(entries) == 0, nil
}

// remove deletes one path, having proved it is inside the cache root.
//
// The containment check is not there because a caller might get it wrong today:
// every path handed to it is built from the root and directory entries' own
// base names. It is there because this is the one function in go-mutants that
// deletes files somebody else's tools also keep things in, and a check that
// makes an escape unrepresentable is worth more than an argument that it cannot
// happen.
func remove(path, root string) error {
	inside, err := within(path, root)
	if err != nil {
		return err
	}
	if !inside {
		return &Error{
			Code:    CodeNotRemoved,
			Message: path + " is not inside the outcome cache at " + root + ", so it was not deleted",
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return &Error{
			Code:    CodeNotRemoved,
			Message: path + " could not be deleted",
			Err:     err,
		}
	}
	return nil
}

// within reports whether path is strictly inside root, comparing the two after
// resolving both to absolute, cleaned paths.
func within(path, root string) (bool, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return false, &Error{
			Code:    CodeNotRemoved,
			Message: path + " could not be resolved to an absolute path, so it was not deleted",
			Err:     err,
		}
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return false, &Error{
			Code:    CodeNotRemoved,
			Message: root + " could not be resolved to an absolute path, so nothing under it was deleted",
			Err:     err,
		}
	}
	relative, err := filepath.Rel(absoluteRoot, absolutePath)
	if err != nil {
		return false, nil
	}
	return relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative), nil
}
