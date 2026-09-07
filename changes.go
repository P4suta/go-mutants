// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

type fileState struct {
	digest string
	mode   fs.FileMode
}

// Changes compares the current snapshot with the state captured after
// preparation. It waits for in-flight [Session.Exec], [Session.Control] and
// [Session.Probe] calls so the result cannot observe a target halfway through a
// write.
//
// It waits for those and for nothing else. A [Workspace.Exec] command is held
// by the *workspace's* lock, which this call does not take, so a command
// writing into the tree concurrently with a Changes can be observed part-way
// through its write. A consumer that wants a settled answer sequences its own
// commands against this call; the engine cannot do it for one, because a
// workspace command is not the session's to wait for.
//
// What it reports is every change in the session's own snapshot, whoever made
// it: a target under [Session.Exec] or [Session.Control], and equally a
// [Workspace.Exec] command run beside the session. A [Session.Probe] target is
// *not* among them, and its absence is not an omission: a probe pass runs in
// the probe tree, a second snapshot beside this one, so what it writes is not
// in the tree this call scans and no call reports it.
//
// What it compares against is the **frozen snapshot manifest**, captured at the
// top of the instrumentation window, which is what the test binaries were
// compiled from: the copies the overlay names were taken there, and the
// compiler read no other spelling of them. So the window's end is where a write
// stops failing a preparation and starts being reported here — a command that
// writes during the binary build, which is the longest phase of a preparation
// and one a command is allowed to run beside, is reported by this call and
// refused by nothing. The answer is "what has moved since this session was
// frozen" and not "who moved it".
//
// A write does not invalidate the session — the overlay still names the frozen
// sources — but it does change the tree every later target runs in, and this is
// where a caller finds out that it did.
func (s *Session) Changes() ([]Change, error) {
	if s == nil {
		return nil, errors.New("gomutants: changes: nil session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("gomutants: changes: %w", ErrSessionClosed)
	}
	current, err := scanFiles(s.root)
	if err != nil {
		return nil, fmt.Errorf("gomutants: changes: %w", err)
	}
	paths := make([]string, 0, len(s.preparedFiles)+len(current))
	seen := make(map[string]bool, len(s.preparedFiles)+len(current))
	for path := range s.preparedFiles {
		paths = append(paths, path)
		seen[path] = true
	}
	for path := range current {
		if !seen[path] {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)

	changes := make([]Change, 0)
	for _, path := range paths {
		before, hadBefore := s.preparedFiles[path]
		after, hasAfter := current[path]
		switch {
		case !hadBefore:
			changes = append(changes, Change{Kind: ChangeAdded, Path: path, AfterSHA256: after.digest})
		case !hasAfter:
			changes = append(changes, Change{Kind: ChangeRemoved, Path: path, BeforeSHA256: before.digest})
		case before != after:
			changes = append(changes, Change{
				Kind:         ChangeModified,
				Path:         path,
				BeforeSHA256: before.digest,
				AfterSHA256:  after.digest,
			})
		}
	}
	return changes, nil
}

func scanFiles(root string) (map[string]fileState, error) {
	states := make(map[string]fileState)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := fileState{mode: info.Mode().Type() | info.Mode().Perm()}
		if info.Mode().IsRegular() {
			digest, err := digestFile(path)
			if err != nil {
				return err
			}
			state.digest = digest
		} else {
			state.digest = "mode:" + info.Mode().String()
			if info.Mode()&fs.ModeSymlink != 0 {
				target, readErr := os.Readlink(path)
				if readErr != nil {
					return readErr
				}
				state.digest += ":" + target
			}
		}
		states[relative] = state
		return nil
	})
	if err != nil {
		return nil, err
	}
	return states, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
