// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// removeDirectory removes an empty directory and can remove nothing else.
//
// It is rmdir(2) rather than [os.Remove], and that is the whole point of its
// existing. os.Remove tries unlink first and falls back to rmdir, so it takes a
// regular file as readily as an empty directory — which is the wrong tool for
// "the root is empty now, take it away", because whether the path is still the
// directory the collector measured is exactly what the collector cannot know. A
// process replacing the root with a file between the listing and here would
// otherwise have that file unlinked. rmdir cannot make that mistake whatever the
// timing, so the window closes rather than narrowing.
//
// A path that is not a directory comes back as [errNotDirectory], which the
// caller reports; every other refusal — most often a directory that still holds
// something — is returned as it was and passed over in silence.
func removeDirectory(path string) error {
	err := syscall.Rmdir(path)
	if errors.Is(err, syscall.ENOTDIR) {
		return errNotDirectory
	}
	return err
}
