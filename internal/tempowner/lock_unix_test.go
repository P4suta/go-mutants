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
