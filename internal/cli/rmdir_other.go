// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package cli

import (
	"errors"
	"os"
	"syscall"
)

// removeDirectory removes an empty directory and can remove nothing else.
//
// It is rmdir(2) rather than [os.Remove], and that is the whole point of its
// existing. os.Remove tries unlink first and falls back to rmdir, so it takes a
// regular file — and a symbolic link — as readily as an empty directory, which
// is the wrong tool for "the root is empty now, take it away": whether the path
// is still the directory the collector measured is exactly what the collector
// cannot know. A process replacing the root between the listing and here would
// otherwise have its replacement removed. rmdir cannot make that mistake
// whatever the timing, so the window closes rather than narrowing — it refuses
// a file and it refuses a link, both with ENOTDIR.
//
// Which of the two it was is asked afterwards, and only to choose the words.
// Asking first would have been the race over again; asking after is safe,
// because the removal has already happened or not and rmdir could not have
// touched either of them whatever the answer turns out to be.
//
// Every other refusal — most often a directory that still holds something — is
// returned as it was, for the caller to pass over in silence.
func removeDirectory(path string) error {
	err := syscall.Rmdir(path)
	if errors.Is(err, syscall.ENOTDIR) {
		return refusedKind(path)
	}
	return err
}

// refusedKind is which of the two refusals a path rmdir would not take earns.
//
// A path it cannot look at at all, or one that is neither a link nor anything
// else in particular, is reported as not a directory: that is the true and
// useful half of what is known, and a third answer for a case nobody could act
// on differently would be worse than the general one.
func refusedKind(path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errIsALink
	}
	return errNotDirectory
}
