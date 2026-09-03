// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/P4suta/go-mutants/internal/tempowner"
)

// stableNameHexLength is how much of the source root's digest [StableName]
// spells out. Sixty-four bits keeps the temporary directories of every module
// on a machine apart with room to spare, and the whole name stays short enough
// that the deep paths of a real module still fit underneath it.
const stableNameHexLength = 16

// StableName is the name of the snapshot directory of absSourceRoot:
// [DirPrefix] followed by the first stableNameHexLength lowercase hex
// characters of the SHA-256 of the path string.
//
// A snapshot's name is derived from the tree it copies rather than drawn at
// random, and the reason is the Go build cache. The go command hashes the
// absolute directory of every package into its compile action id whenever
// -trimpath is not passed — the "dir %s" line of buildActionID in
// cmd/go/internal/work/exec.go — so a package compiled out of a fresh random
// directory is never a cache hit, however identical its bytes are to the ones
// the last run compiled. The cost is paid twice: every run recompiles the whole
// module from scratch, and the cache the consumer shares between runs fills up
// with one copy of the project's objects per run.
//
// go-mutants deliberately does not pass -trimpath, which would make the
// question go away. That flag changes the program under test: a test that reads
// runtime.Caller paths, or embeds a source path in a golden file, behaves
// differently under it, and the build that was verified would no longer be the
// build the user's own `go test` runs. So the fix belongs on this side of the
// line, where a name is free to choose.
//
// The argument is the absolute source root exactly as filepath.Abs spells it:
// no symlink resolution and no case folding. Two spellings of one directory
// therefore get two names, which costs the second spelling a first run's cache
// hits and nothing else — while canonicalizing would mean deciding what to do
// on the day the answer changes underneath a directory that already exists,
// and that question has no safe answer.
func StableName(absSourceRoot string) string {
	sum := sha256.Sum256([]byte(absSourceRoot))
	return DirPrefix + hex.EncodeToString(sum[:stableNameHexLength/2])
}

// destination creates the directory a snapshot of absSrc will own inside
// destParent, and reports whether it got the [StableName] or the random
// fallback.
//
// The returned path is absolute whatever destParent was. A relative parent
// would otherwise produce a relative path, which the [Snapshot.Cleanup] guard
// refuses on sight — the snapshot would be perfectly usable and impossible to
// delete. It also means every consumer downstream, which will hand this path
// to subprocesses running in their own working directories, gets a path that
// still means the same thing there.
func destination(destParent, absSrc string) (string, bool, error) {
	parent := destParent
	if parent == "" {
		// os.MkdirTemp's own answer for an empty directory, spelled out here
		// because the stable name is joined onto the parent rather than handed
		// to MkdirTemp. The Cleanup guard allows this parent in its own right.
		parent = os.TempDir()
	}
	parent, err := filepath.Abs(parent)
	if err != nil {
		return "", false, &Error{Code: CodeDestination, Path: destParent, Message: "cannot resolve the destination parent", Err: err}
	}

	name := StableName(absSrc)
	dir := filepath.Join(parent, name)
	claim := os.Mkdir(extendedPath(dir), 0o700)
	if claim == nil {
		return dir, true, nil
	}
	if !errors.Is(claim, fs.ErrExist) {
		return "", false, &Error{Code: CodeDestination, Path: dir, Message: "cannot create the snapshot directory", Err: claim}
	}

	// Something is already using the name, and internal/tempowner's sweep
	// already knows every case of what that can mean: a locked directory
	// belongs to a run that is still going, a kept one was preserved on
	// purpose, a released lock means the owner is gone and the directory is an
	// orphan to remove, and an unowned young one is spared because it may be a
	// run in progress under an older binary.
	//
	// Its error is deliberately dropped. There is no logger in this package,
	// and a directory that refused to go is a reason to take another name
	// rather than to fail a run that has not copied a byte yet — the fallback
	// below is what happens either way, and [Snapshot.StableDir] is where a
	// caller reads that it did.
	_, _ = tempowner.Sweep(parent, []string{name}, time.Now())
	if os.Mkdir(extendedPath(dir), 0o700) == nil {
		return dir, true, nil
	}

	created, err := os.MkdirTemp(parent, DirPrefix)
	if err != nil {
		return "", false, &Error{Code: CodeDestination, Path: parent, Message: "cannot create the snapshot directory", Err: err}
	}
	return created, false, nil
}

// claimDestination takes ownership of the directory [destination] just made,
// and decides what happens to that directory when it cannot.
//
// A claim that lost to another process's lock is left exactly as found. With a
// random name that could not happen — nobody else could be inside a directory
// this process had just made — but a stable name is a name another run can
// arrive at: a run stopped between its Mkdir and this claim for longer than
// [tempowner.LegacyMaxAge] would find, on resuming, that another run of the
// same root had swept the empty directory, recreated it and claimed it. The
// directory is that run's now, whoever first created it, and removing it would
// remove a live snapshot. Every other failure happens inside a directory no
// other process has entered, and the empty directory is removed so that a
// failed Create leaves nothing behind.
func claimDestination(dir string, now time.Time) (*tempowner.Owner, error) {
	owner, err := tempowner.Claim(dir, now)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, tempowner.ErrOwned) {
		_ = os.RemoveAll(dir)
	}
	return nil, &Error{Code: CodeDestination, Path: dir, Message: "cannot claim the snapshot directory", Err: err}
}
