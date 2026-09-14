// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import "os"

// A Lock is an exclusive advisory lock held on one open file.
//
// The lock belongs to the open file, not to the process: two [Acquire] calls in
// one program contend with each other exactly as two processes do, which is
// what makes concurrent Opens in a single caller — a test binary, or a
// consumer preparing several workspaces — see each other's directories as live.
type Lock struct {
	file *os.File
	// unlock is the syscall this lock is released with. It is a field rather
	// than a package-level variable so that a test may refuse an unlock
	// without reaching every other test running beside it; see [acquire].
	unlock func(*os.File) error
}

// Acquire opens path, creating it, and takes the exclusive lock without
// blocking.
//
// A lock somebody else holds is not an error: it is the answer. The three-value
// return keeps "the owner is alive" and "the filesystem would not answer"
// apart, because a sweep that read the second as the first would delete a
// running workspace.
func Acquire(path string) (*Lock, bool, error) {
	return acquire(path, tryAdvisoryLock, unlockAdvisory)
}

// acquire is [Acquire] with its two syscalls handed in, so that "the filesystem
// would not answer" can be tested without a filesystem that has to be
// persuaded into refusing -- the same reason [sweeper] holds its removal in a
// field, and handed in rather than kept in a package variable so that a test
// refusing a lock does not reach every other test running beside it.
//
// A failing flock is not a hypothetical: a lock file on a filesystem that does
// not implement it answers EOPNOTSUPP, and what this package does then decides
// whether a sweep deletes a directory it could not ask about.
func acquire(path string, lock func(*os.File) (bool, error), unlock func(*os.File) error) (*Lock, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockPerm)
	if err != nil {
		return nil, false, err
	}
	// `!held` is the whole condition: [tryAdvisoryLock] never reports a lock it
	// also failed to take, so an error always arrives with false beside it. The
	// error is still forwarded -- that is what `closeAfter` is for -- and a
	// second `err != nil` here would be a branch no input could tell from its
	// absence.
	held, err := lock(file)
	if !held {
		return nil, false, closeAfter(file, err)
	}
	return &Lock{file: file, unlock: unlock}, true, nil
}

// Release unlocks and closes the file. It is idempotent and nil-safe, so a
// close path may release before removing without tracking whether some earlier
// path already did.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	// The unlock is explicit even though closing the descriptor drops the lock
	// on both platforms: the close is what actually releases it, and an
	// unlock-then-close says so to the next reader of this function.
	err := l.unlock(file)
	return closeAfter(file, err)
}

// closeAfter closes file and returns cause, or the close failure when there is
// no cause to report.
func closeAfter(file *os.File, cause error) error {
	closeErr := file.Close()
	if cause != nil {
		return cause
	}
	return closeErr
}
