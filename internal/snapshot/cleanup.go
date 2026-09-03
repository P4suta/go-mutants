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
	// cleanupAttempts is how many times removal is tried before giving up.
	// Bounded on purpose: a snapshot that will not go away is a diagnostic to
	// report, not a reason for the process to hang at the end of a run.
	cleanupAttempts = 5

	// cleanupBackoff is the pause before the second attempt; it doubles for
	// each attempt after that, so the ladder is 20, 40, 80, 160 ms and the
	// whole loop costs at most a third of a second.
	cleanupBackoff = 20 * time.Millisecond
)

// Keep preserves the snapshot directory instead of removing it, and records in
// its owner marker that this was asked for.
//
// The record is the whole point. A directory left behind with a lock nobody
// holds is indistinguishable from one a killed process abandoned, and the next
// run's sweep would collect it — so a keep that did not say so in a way the
// sweep obeys would be a keep in name only. The lock is released, because the
// process that held it is about to exit and the marker is now what speaks for
// the directory.
//
// A kept snapshot stays kept: [Snapshot.Cleanup] becomes a no-op afterwards, so
// that the deferred cleanups this codebase is full of cannot undo the decision.
func (s *Snapshot) Keep() error {
	if s == nil {
		return nil
	}
	s.kept = true
	if err := s.owner.Keep(); err != nil {
		return &Error{Code: CodeCleanupFailed, Path: s.dir, Message: "cannot mark the snapshot directory kept", Err: err}
	}
	return nil
}

// Cleanup removes the snapshot directory.
//
// Removal is retried because of Windows. A test binary that has just exited
// can still have its image mapped for a moment, an antivirus scanner can hold
// a handle open, and a file another process opened without FILE_SHARE_DELETE
// cannot be unlinked at all until that handle closes. Every one of those
// clears on its own within milliseconds, so a short backoff ladder turns a
// spurious failure into a pause. Before each retry the read-only attribute is
// cleared from the tree, which is the one cause that would otherwise never
// clear by waiting.
//
// Cleanup is idempotent: removing a directory that is already gone succeeds.
// A nil *Snapshot is also fine, so `defer snap.Cleanup()` is safe to write
// before the error from [Create] has been examined.
//
// # The guard
//
// This function deletes a directory tree, and it runs on a path stored in a
// struct field that anything in this process could have written. The path is
// [Snapshot.Dir] rather than [Snapshot.Root], because the copy is one level
// inside it and the ownership files are the rest of what goes. So it refuses
// unless the path still looks exactly like something [Create] produced: it
// must be non-empty and absolute, its final element must begin with
// [DirPrefix], and its parent must be either the [Options.DestParent] the
// snapshot was created in or the operating system's temporary directory. A
// refusal is an [Error] with [CodeCleanupRefused] and nothing is touched.
//
// The guard is not defence against an attacker, who could satisfy every one of
// those conditions. It is defence against go-mutants — against a future refactor
// that assigns the module root to Root, or a zero Snapshot reaching a deferred
// Cleanup. Deleting a user's source tree is the one bug this tool must never
// have, and the cost of ruling it out here is four comparisons.
func (s *Snapshot) Cleanup() error {
	if s == nil || s.kept {
		return nil
	}
	if err := s.guardDir(); err != nil {
		return err
	}
	// The lock is released before the first removal attempt, not after the last
	// one. On Windows an open handle inside a directory is exactly what makes
	// RemoveAll fail, so a Cleanup that held its own lock open would spend the
	// whole retry ladder losing to itself.
	if err := s.owner.Release(); err != nil {
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

// guardDir reports why the snapshot directory is not safe to delete, or nil if
// it is.
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
