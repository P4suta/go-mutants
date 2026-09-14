// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"os"
	"path/filepath"
)

// The operating-system calls this package cannot be made to fail, held as
// variables so that what it does when they do is measurable.
//
// Everything else in here is staged for real. A source root that is not a
// directory, a tree holding a symbolic link, a destination parent that refuses
// writes, a file that cannot be read, a destination that already exists, a
// directory where a file has to go -- all of those are states a test can put a
// filesystem into, and every one of them is exercised that way, because a
// staged failure is the same failure a user will have.
//
// These four are not. A descriptor this process opened a moment ago will stat,
// chmod and close; a path it has just written will accept a timestamp; and
// filepath.Abs fails only when the working directory has been taken away from
// the process, which is not a thing one test may do to the others running
// beside it. What this package does *after* each of them is nonetheless the
// difference between a snapshot that reports a broken copy and one that hands
// back a manifest describing bytes nobody wrote -- so each is a seam, named for
// the call it stands in for, and the test that uses one says which.
//
// They are package-level variables and therefore shared: a test that replaces
// one is not [testing.T.Parallel]. That is the same rule and the same reason as
// internal/report's `createTemp` and `openMarker`.
var (
	// absPath is filepath.Abs, which fails only when os.Getwd does.
	absPath = filepath.Abs

	// The four that build the snapshot's own directories. Every one of them
	// runs inside a directory this process created and locked moments before,
	// so there is no filesystem a test can hand [Create] that makes any of
	// them fail -- and what [Create] does when one does is the difference
	// between a failed snapshot that leaves nothing behind and a half-built
	// tree with a lock on it.
	//
	// claimDir is here for a second reason as well: it is the one failure
	// after which Create must *not* clean up, because a claim that lost to
	// another process's lock is a directory that is no longer its own.
	makeTreeDir = os.Mkdir
	makeDirTree = os.MkdirAll
	setDirPerm  = finalizeDirPerm
	claimDir    = claimDestination

	// The three that finish one copied file, after the bytes are written.
	//
	// finalizeCopyPerm is the chmod that makes a copied file's permissions
	// exact; see [finalizePerm] for why it works through the descriptor.
	// closeCopy is the checked close -- on a buffered filesystem it is where a
	// failed write is finally reported, and a truncated file whose digest was
	// computed from the bytes we meant to write would be a snapshot that lies
	// about itself. setFileTimes stamps a copied file or directory with the
	// source's modification time, so the go command sees a tree that looks its
	// age rather than one that looks new.
	finalizeCopyPerm = finalizePerm
	closeCopy        = (*os.File).Close
	setFileTimes     = os.Chtimes
)
