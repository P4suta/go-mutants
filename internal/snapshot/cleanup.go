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
// struct field that anything in this process could have written. So it refuses
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
	if s == nil {
		return nil
	}
	if err := s.guardRoot(); err != nil {
		return err
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
			clearReadOnly(s.Root)
			sleep(cleanupBackoff << (attempt - 1))
		}
		if err = remove(s.Root); err == nil {
			return nil
		}
	}
	return &Error{
		Code:    CodeCleanupFailed,
		Path:    s.Root,
		Message: fmt.Sprintf("the snapshot directory survived %d removal attempts", cleanupAttempts),
		Err:     err,
	}
}

// guardRoot reports why Root is not safe to delete, or nil if it is.
func (s *Snapshot) guardRoot() error {
	refuse := func(reason string) error {
		return &Error{
			Code:    CodeCleanupRefused,
			Path:    s.Root,
			Message: "refuses to remove a path that is not a snapshot directory: " + reason,
		}
	}
	if s.Root == "" {
		return refuse("the root is empty")
	}
	if !filepath.IsAbs(s.Root) {
		return refuse("the root is not absolute")
	}
	if !strings.HasPrefix(filepath.Base(s.Root), DirPrefix) {
		return refuse("the name does not begin with " + DirPrefix)
	}
	parent := filepath.Dir(s.Root)
	if pathsEqual(parent, s.destParent) || pathsEqual(parent, os.TempDir()) {
		return nil
	}
	return refuse("the parent is neither the destination parent nor the temporary directory")
}
