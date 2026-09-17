// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	cleanupAttempts = 5

	cleanupBackoff = 20 * time.Millisecond
)

func (s *Snapshot) Keep() error {
	if s == nil {
		return nil
	}
	if err := s.owner.Keep(); err != nil {
		return &Error{Code: CodeCleanupFailed, Path: s.dir, Message: "cannot mark the snapshot directory kept", Err: err}
	}
	s.kept = true
	return nil
}

func (s *Snapshot) Cleanup() error {
	if s == nil || s.kept {
		return nil
	}
	if err := s.guardDir(); err != nil {
		return err
	}
	release := s.release
	if release == nil {
		release = s.owner.Release
	}
	if err := release(); err != nil {
		return &Error{Code: CodeCleanupFailed, Path: s.dir, Message: "cannot release the snapshot directory's lock", Err: err}
	}
	remove, sleep := s.remove, s.sleep
	if remove == nil {
		remove = os.RemoveAll
	}
	if sleep == nil {
		sleep = time.Sleep
	}

	var err error
	for attempt := range cleanupAttempts {
		if attempt > 0 {
			clearReadOnly(s.dir)
			sleep(cleanupBackoff << (attempt - 1))
		}
		if err = remove(s.dir); err == nil {
			return nil
		}
	}
	return &Error{
		Code:    CodeCleanupFailed,
		Path:    s.dir,
		Message: fmt.Sprintf("the snapshot directory survived %d removal attempts", cleanupAttempts),
		Err:     err,
	}
}

func (s *Snapshot) guardDir() error {
	refuse := func(reason string) error {
		return &Error{
			Code:    CodeCleanupRefused,
			Path:    s.dir,
			Message: "refuses to remove a path that is not a snapshot directory: " + reason,
		}
	}
	if s.dir == "" {
		return refuse("the directory is empty")
	}
	if !filepath.IsAbs(s.dir) {
		return refuse("the directory is not absolute")
	}
	if !strings.HasPrefix(filepath.Base(s.dir), DirPrefix) {
		return refuse("the name does not begin with " + DirPrefix)
	}
	parent := filepath.Dir(s.dir)
	if pathsEqual(parent, s.destParent) || pathsEqual(parent, os.TempDir()) {
		return nil
	}
	return refuse("the parent is neither the destination parent nor the temporary directory")
}
