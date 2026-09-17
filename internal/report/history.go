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

const (
	DirName           = "go-mutants"
	WorkspacesDirName = "workspaces"
	RunsDirName       = "runs"
	LatestFileName    = "latest.json"
	MarkerFileName    = "go-mutants.marker"

	markerHeader = "go-mutants-workspace-v1"

	workspaceKeyLength = 16

	tempPattern = "go-mutants-report-*.tmp"
)

type tempFile interface {
	Name() string
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

var (
	createTemp = func(dir, pattern string) (tempFile, error) { return os.CreateTemp(dir, pattern) }
	openMarker = func(path string) (tempFile, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
)

var renameDelays = []time.Duration{5 * time.Millisecond, 20 * time.Millisecond, 100 * time.Millisecond}

var errMarkerExists = errors.New("the workspace marker already exists")

func WorkspaceKey(workspaceDigest string) string {
	sum := sha256.Sum256([]byte(workspaceDigest))
	return hex.EncodeToString(sum[:])[:workspaceKeyLength]
}

type History struct {
	Root string
}

func WriteHistory(r *Report) (runPath, latestPath string, err error) {
	return History{}.Write(r)
}

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
	return h.store(r.Workspace.WorkspaceDigest, r.RunID, data)
}

func (h History) store(workspaceDigest, runID string, data []byte) (runPath, latestPath string, err error) {
	dir, err := h.Claim(workspaceDigest)
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

	runPath = filepath.Join(runs, runID+".json")
	if writeErr := writeAtomic(runPath, data); writeErr != nil {
		return "", "", writeErr
	}
	latestPath = filepath.Join(dir, LatestFileName)
	if writeErr := writeAtomic(latestPath, data); writeErr != nil {
		return runPath, "", writeErr
	}
	return runPath, latestPath, nil
}

func (h History) WriteWorkspace(w *WorkspaceReport) (runPath, latestPath string, err error) {
	if w == nil {
		return "", "", &Error{
			Code:    CodeNoReport,
			Message: "there is no workspace report to write",
		}
	}
	if !runIDPattern.MatchString(w.RunID) {
		return "", "", &Error{
			Code:    CodeInvalidRunID,
			Message: "the report's run id " + quote(w.RunID) + " cannot be used as a file name",
		}
	}
	if !digestPattern.MatchString(w.Workspace.WorkspaceDigest) {
		return "", "", &Error{
			Code:    CodeInvalidWorkspaceDigest,
			Message: "the report's workspace digest " + quote(w.Workspace.WorkspaceDigest) + " cannot name a history directory",
		}
	}
	data, err := w.Marshal()
	if err != nil {
		return "", "", err
	}
	return h.store(w.Workspace.WorkspaceDigest, w.RunID, data)
}

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
	return WriteBytes(path, data)
}

func WriteBytes(path string, data []byte) error {
	return writeAtomic(path, data)
}

func (h History) WorkspaceDir(workspaceDigest string) (string, error) {
	root, err := h.root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, WorkspacesDirName, WorkspaceKey(workspaceDigest)), nil
}

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

var ErrNoMarker = errors.New("the directory carries no go-mutants workspace marker")

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
		return nil
	case !errors.Is(err, errMarkerExists):
		return err
	}
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

func createMarker(path, content string) error {
	temp, err := writeTemp(filepath.Dir(path), "the workspace marker", []byte(content))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp) }()

	switch err = os.Link(temp, path); {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return errMarkerExists
	}
	return createMarkerInPlace(path, content)
}

func createMarkerInPlace(path, content string) error {
	file, err := openMarker(path)
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
	if _, err = file.Write([]byte(content)); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return &Error{
			Code:    CodeHistoryWrite,
			Message: "the workspace marker " + path + " could not be written",
			Err:     err,
		}
	}
	return nil
}

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

func writeAtomic(path string, data []byte) error {
	name, err := writeTemp(filepath.Dir(path), "the report", data)
	if err != nil {
		return err
	}
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

func writeTemp(dir, what string, data []byte) (string, error) {
	temp, err := createTemp(dir, tempPattern)
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

func quote(s string) string { return strconv.Quote(s) }
