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

type StoredRun struct {
	RunID      string
	Path       string
	ModulePath string
	Modules    []string
	Status     Status
	FinishedAt time.Time
	Summary    Summary
	Bytes      int64
}

func (r StoredRun) Score() (float64, bool) {
	if r.Summary.ScorePercent == nil {
		return 0, false
	}
	return *r.Summary.ScorePercent, true
}

type Damaged struct {
	Path   string
	Reason string
}

type Skipped struct {
	Name   string
	Reason string
}

type StoredWorkspace struct {
	Key     string
	Dir     string
	Digest  string
	Runs    []StoredRun
	Latest  string
	Damaged []Damaged
}

type Listing struct {
	Root       string
	Workspaces []StoredWorkspace
	Skipped    []Skipped
}

type Removed struct {
	Dir   string
	Runs  int
	Bytes int64
}

func (h History) List() (Listing, error) {
	root, err := h.root()
	if err != nil {
		return Listing{}, err
	}
	listing := Listing{Root: root, Workspaces: []StoredWorkspace{}, Skipped: []Skipped{}}

	base := filepath.Join(root, WorkspacesDirName)
	entries, err := readDir(base)
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
	slices.SortFunc(listing.Workspaces, func(x, y StoredWorkspace) int {
		return strings.Compare(x.Key, y.Key)
	})
	slices.SortFunc(listing.Skipped, func(x, y Skipped) int { return strings.Compare(x.Name, y.Name) })
	return listing, nil
}

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

var readDir = os.ReadDir

type storedFile struct {
	path string
	size int64
}

func storedFiles(dir string) ([]storedFile, error) {
	entries, err := readDir(dir)
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
	Modules []struct {
		ModulePath string `json:"module_path"`
	} `json:"modules"`
}

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
	case doc.DocumentType != DocumentType && doc.DocumentType != WorkspaceDocumentType:
		return StoredRun{}, "it is " + quote(doc.DocumentType) + ", not a " + DocumentType +
			" or a " + WorkspaceDocumentType + " document"
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
	var modules []string
	for _, module := range doc.Modules {
		modules = append(modules, module.ModulePath)
	}
	return StoredRun{
		RunID:      doc.RunID,
		Path:       file.path,
		ModulePath: doc.Workspace.ModulePath,
		Modules:    modules,
		Status:     doc.Status,
		FinishedAt: finished,
		Summary:    doc.Summary,
		Bytes:      file.size,
	}, ""
}

func reasonOf(err error) string {
	message := err.Error()
	if _, rest, found := strings.Cut(message, ": "); found && strings.HasPrefix(message, "GOM") {
		return rest
	}
	return message
}

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
		return path, nil
	}
	resolvedParent, err := resolvePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

func trimExtendedPrefix(path string) string {
	if rest, found := strings.CutPrefix(path, `\\?\UNC\`); found {
		return `\\` + rest
	}
	if rest, found := strings.CutPrefix(path, `\\?\`); found {
		return rest
	}
	return path
}
