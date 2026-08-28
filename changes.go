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
// preparation. It waits for in-flight Exec calls so the result cannot observe
// a target halfway through a write.
func (s *Session) Changes() ([]Change, error) {
	if s == nil {
		return nil, errors.New("gomutants: changes: nil session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("gomutants: changes: session is closed")
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
