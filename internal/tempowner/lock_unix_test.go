//go:build !windows

// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestTheFlockWrapperKeepsTheThreeAnswersApart is [TestAcquireKeepsTheThreeAnswersApart]
// asked of the syscall itself rather than of the function above it.
//
// Acquire's own test hands the lock in, which is what lets "the filesystem
// would not answer" be stated at all -- and it means the wrapper that really
// calls flock is reached by nothing but the happy path. That wrapper is where
// the three answers are actually separated: an errno that means "somebody else
// holds it" becomes (false, nil), and every other errno has to arrive at the
// caller as an error, because a sweep that read one as the other would delete a
// running workspace.
//
// A closed descriptor is how the third answer is produced. os.File.Fd reports
// -1 once the file is closed, flock(-1) is EBADF on every unix, and EBADF is
// neither of the two errnos that mean the lock is busy -- so it is exactly the
// shape this wrapper has to forward rather than fold into a verdict.
func TestTheFlockWrapperKeepsTheThreeAnswersApart(t *testing.T) {
	t.Parallel()

	t.Run("an errno that is not about an owner", func(t *testing.T) {
		t.Parallel()

		closed := openThenClose(t)
		held, err := tryAdvisoryLock(closed)
		if held {
			t.Error("a lock the kernel refused is reported as taken")
		}
		if !errors.Is(err, unix.EBADF) {
			t.Errorf("tryAdvisoryLock = %v, want the kernel's own errno forwarded", err)
		}
	})

	t.Run("an unlock the kernel refuses", func(t *testing.T) {
		t.Parallel()

		// The close that follows a Release drops the lock whatever the unlock
		// did, so this is the one place the unlock's own answer exists. It is
		// forwarded for the reason Release forwards it: a kernel disagreeing
		// about a descriptor this package holds is not a thing to swallow.
		if err := unlockAdvisory(openThenClose(t)); !errors.Is(err, unix.EBADF) {
			t.Errorf("unlockAdvisory = %v, want the kernel's own errno forwarded", err)
		}
	})

	t.Run("a lock somebody else holds", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "owner.lock")
		first, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockPerm)
		if err != nil {
			t.Fatalf("opening the lock file: %v", err)
		}
		t.Cleanup(func() { _ = first.Close() })
		if held, lockErr := tryAdvisoryLock(first); !held || lockErr != nil {
			t.Fatalf("the first flock: held=%v err=%v", held, lockErr)
		}

		second, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockPerm)
		if err != nil {
			t.Fatalf("opening it again: %v", err)
		}
		t.Cleanup(func() { _ = second.Close() })
		held, err := tryAdvisoryLock(second)
		if held {
			t.Error("two holders of one flock")
		}
		if err != nil {
			t.Errorf("a contended flock = %v, want the busy errno read as an owner rather than a failure", err)
		}
	})
}
