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

const DefaultGCDays = 30

type Workspace struct {
	Key      string
	Dir      string
	Digest   string
	Contexts int
	Entries  int
	Bytes    int64
	Newest   time.Time
}

type Skipped struct {
	Name   string
	Reason string
}

type Survey struct {
	Root       string
	Workspaces []Workspace
	Skipped    []Skipped
}

func (s Survey) Entries() int {
	total := 0
	for _, workspace := range s.Workspaces {
		total += workspace.Entries
	}
	return total
}

func (s Survey) Bytes() int64 {
	var total int64
	for _, workspace := range s.Workspaces {
		total += workspace.Bytes
	}
	return total
}

type Sweep struct {
	Root       string
	Workspaces int
	Entries    int
	Contexts   int
	Bytes      int64
	Skipped    []Skipped
}

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
		if key := report.WorkspaceKey(digest); entry.Name() != key {
			skipped = append(skipped, Skipped{
				Name: entry.Name(),
				Reason: "its marker names the workspace filed under " + key +
					", so it is a copy of that directory rather than one of this cache's own",
			})
			continue
		}
		owned = append(owned, Workspace{Key: entry.Name(), Dir: dir, Digest: digest})
	}
	return owned, skipped, nil
}

func reasonOf(err error) string {
	message := err.Error()
	if _, rest, found := strings.Cut(message, ": "); found && strings.HasPrefix(message, "GOM") {
		return rest
	}
	return message
}

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

func isEmpty(dir string) (bool, error) {
	entries, err := readDir(dir)
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

func within(path, root string) (bool, error) {
	resolvedPath, err := resolveParent(path)
	if err != nil {
		return false, &Error{
			Code:    CodeNotRemoved,
			Message: path + " could not be resolved on disk, so it was not deleted",
			Err:     err,
		}
	}
	resolvedRoot, err := resolvePath(root)
	if err != nil {
		return false, &Error{
			Code:    CodeNotRemoved,
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
	absolute, err := absPath(path)
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
	resolved, err := evalSymlinks(path)
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
