// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"path/filepath"
)

// The operating-system calls this package cannot be made to fail, held as
// variables so that what it does when they do is measurable.
//
// Everything else about this package's failures is staged for real, because a
// staged failure is the same failure a user will have: a cache root that cannot
// be listed, a workspace directory carrying somebody else's marker, an entry
// file that cannot be read, a context directory that lists its names and
// refuses to stat them, a destination a rename cannot land on. Those are all
// states a test can put a filesystem into.
//
// These five are not. Two of them are about the running executable -- a process
// that cannot locate or read its own binary is not a filesystem a test may
// build, and the digest of that binary is what stops a rebuilt go-mutants from
// adopting its predecessor's answers, so what happens when it cannot be taken
// decides whether a run caches under a key that names nothing. The other three
// are the write, the flush and the close of a file this process created in a
// directory it just wrote to; between them they decide whether a cache entry
// that was never fully written is renamed into place anyway, which is the one
// failure that turns an interrupted run into a permanently poisoned cache.
//
// They are package-level variables and therefore shared: a test that replaces
// one is not [testing.T.Parallel]. That is the same rule and the same reason as
// internal/report's `createTemp` and internal/snapshot's seams.go.
var (
	// executablePath and readExecutable are how [ToolDigest] takes the digest
	// of the running binary.
	executablePath = os.Executable
	readExecutable = os.ReadFile

	// absPath is filepath.Abs, which fails only when os.Getwd does -- that is,
	// when the process has been left without a working directory, which is not
	// a thing one test may do to the others running beside it. What [within]
	// does when it cannot resolve a path is the one answer that must not end in
	// a deletion.
	absPath = filepath.Abs

	// evalSymlinks resolves a path as the filesystem sees it. Some of what it
	// refuses is stageable -- a directory a test may not search -- and some is
	// not: a path that resolved a moment ago and stopped between two steps of
	// walking up it, and the volume root of a volume that is not there, which
	// is a state no POSIX filesystem has.
	evalSymlinks = filepath.EvalSymlinks

	// readDir lists a directory. It is a seam for one call in [collect]: the
	// emptiness check that decides whether a context directory is pruned lists
	// a directory [entryFiles] listed a moment earlier in the same function, so
	// a failure there is a directory that stopped being readable between two
	// calls -- a race with another process's sweep, and the state a test can
	// only stage by being that other process.
	readDir = os.ReadDir

	// writeTemp, syncTemp and closeTemp are the three calls that put a cache
	// entry's bytes on disk before the rename that names them.
	writeTemp = (*os.File).Write
	syncTemp  = (*os.File).Sync
	closeTemp = (*os.File).Close
)
