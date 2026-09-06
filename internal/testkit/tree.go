// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// SPDXHeader is the two comment lines every Go file this project writes carries,
// followed by the blank line gofmt wants between them and the package clause.
//
// It is here rather than in each suite because a synthesized module's files are
// held to the same standard as the checked-in ones: `gofmt -l .` and the REUSE
// check walk the filesystem, and a test that grew a fixture out of a temporary
// directory into `fixtures/` should not also have to grow a header.
const SPDXHeader = "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
	"// SPDX-License-Identifier: MIT OR Apache-2.0\n\n"

// TreeAge is how old every tree this package hands out is.
//
// The number is not arbitrary. cmd/go indexes a package directory only when
// every file in it is at least two seconds old, so a tree written a moment ago
// behaves differently from a tree a user has — the first `go list` over it
// indexes nothing and the second one might, which makes any assertion about
// cache entries or misses depend on how long the copy took. An hour is on the
// far side of that cutoff, of the one-second timestamp granularity some
// filesystems still have, and of any clock skew between a container and its
// host.
const TreeAge = time.Hour

// Copy copies a corpus module into a directory of the test's own and returns
// its root, aged.
//
// Every test that runs a real `go` command against a fixture wants this rather
// than [Fixture]: the fixtures are checked in, `git status --porcelain
// fixtures/` is a CI gate, and a run writes reports, snapshots and scratch
// directories beside the module it is pointed at. Running in the corpus is why
// the engine's own integration tests had to blank `report.formats`, which is a
// test working around a default rather than exercising it.
func Copy(t testing.TB, name string) string {
	t.Helper()
	// The fixture is resolved directly rather than through [Fixture], so that a
	// copy logs the one line naming both ends of it instead of two.
	from, err := fixturePath(Root(t), name)
	if err != nil {
		t.Fatalf("resolving a fixture to copy: %v", err)
	}
	to := filepath.Join(t.TempDir(), name)
	CopyTree(t, from, to)
	AgeTree(t, to)
	logInputs(t, "fixture="+from, "scratch="+to)
	return to
}

// CopyTree copies a whole tree, creating the directories above the destination.
//
// Only directories and regular files are copied, and anything else — a symlink,
// a socket, a device — fails the test. That is the same rule internal/snapshot
// enforces for a real workspace, and for the same reason: a link is a path into
// a tree nobody asked to copy, and silently following or skipping one makes the
// copy a different program from the original.
func CopyTree(t testing.TB, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatalf("creating the directories above %s: %v", to, err)
	}
	err := filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// The owner's bits are added so the copy can be walked, written into
			// and — this is the one that bites — removed again on Windows.
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if err := refuseIrregular(path, entry); err != nil {
			return err
		}
		// The write bit is added for the same reason as the directory bits above:
		// a read-only file copied read-only cannot be deleted on Windows, so
		// t.TempDir's cleanup fails on a tree that held one. Every other bit,
		// the executable one included, travels as it was.
		return copyFile(path, target, info.Mode().Perm()|0o200)
	})
	if err != nil {
		t.Fatalf("copying %s to %s: %v", from, to, err)
	}
}

// refuseIrregular fails a walk on anything that is not a regular file.
//
// A symlink is the case this exists for, and it is refused rather than followed
// or skipped: it is a path into a tree nobody asked to touch, every write in
// this package follows one, and internal/snapshot refuses a link in a real
// workspace for exactly the same reason.
func refuseIrregular(path string, entry fs.DirEntry) error {
	if entry.Type().IsRegular() {
		return nil
	}
	return fmt.Errorf("%s is a %v, and only directories and regular files are handled here", path, entry.Type())
}

// copyFile writes one regular file to target with the source's permissions.
func copyFile(from, to string, perm fs.FileMode) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	// Nothing was written to the source, so its close can only report what the
	// read already did.
	defer func() { _ = source.Close() }()

	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return err
	}
	// The close of the destination is returned rather than deferred: it is where
	// a full disk or a quota is reported, and a copy that lost its last block
	// would otherwise look like a copy that worked.
	return destination.Close()
}

// AgeTree sets the modification time of every path under root, root included, to
// [TreeAge] ago.
//
// Directories are aged after the files inside them and deepest-first, because
// writing a file updates the modification time of the directory holding it: a
// walk that aged a parent before its children would undo its own work on the
// parent and leave exactly the fresh timestamp the go command reads differently.
//
// Anything that is not a directory or a regular file fails the test, by
// [CopyTree]'s rule and for a sharper version of its reason: os.Chtimes follows
// symlinks, so a link in the tree would have had this function writing a
// timestamp onto a file outside the directory it was given — the developer's own
// source, quite possibly — from a test that believed it was working in a
// temporary copy.
func AgeTree(t testing.TB, root string) {
	t.Helper()
	when := time.Now().Add(-TreeAge)

	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if err := refuseIrregular(path, entry); err != nil {
			return err
		}
		return os.Chtimes(path, when, when)
	})
	if err != nil {
		t.Fatalf("ageing the files under %s: %v", root, err)
	}

	// Deepest first: a longer path can only be below a shorter one, so reverse
	// lexical order visits every child before its parent.
	slices.Sort(directories)
	slices.Reverse(directories)
	for _, dir := range directories {
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatalf("ageing the directory %s: %v", dir, err)
		}
	}
}

// WriteFile writes a file, creating the directories above it.
//
// It does not age what it wrote: a test that writes into a tree a `go` command
// is about to read wants [AgeTree] afterwards, or the module builder, which ages
// on every write. The split is deliberate — a helper that both wrote and aged
// would make "the file was written just now" impossible to express.
func WriteFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the directories above %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// WriteSource writes one Go file of a module built for a single test, with the
// [SPDXHeader] in front of the body.
//
// The body is a body rather than a file: every caller passes a package clause
// and some declarations, and a header each of them has to remember is a header
// one of them forgets.
func WriteSource(t testing.TB, dir, name, body string) {
	t.Helper()
	WriteFile(t, filepath.Join(dir, name), []byte(SPDXHeader+body))
}

// agePath ages one file and every directory above it up to root, deepest first.
//
// It is [AgeTree] for a single write: what writing a file can change is that
// file and the modification times of the directories holding it, so a builder
// that writes a hundred files does not have to re-stamp the whole tree a hundred
// times.
func agePath(t testing.TB, root, path string) {
	t.Helper()
	when := time.Now().Add(-TreeAge)
	for current := path; ; current = filepath.Dir(current) {
		if err := os.Chtimes(current, when, when); err != nil {
			t.Fatalf("ageing %s: %v", current, err)
		}
		if SamePath(current, root) || !within(root, current) {
			return
		}
		if parent := filepath.Dir(current); parent == current {
			return
		}
	}
}

// ReadFile reads a file or fails the test.
func ReadFile(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// Entries lists the names directly under a directory, sorted, so that "what is
// in this directory" can be compared as data.
func Entries(t testing.TB, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(found))
	for _, entry := range found {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// SamePath reports whether two paths name the same file or directory.
//
// No assertion in this repository compares two paths with ==, and macOS is why:
// it resolves /var to /private/var, so t.TempDir hands back one spelling and a
// child process that resolved its own working directory reports the other. Both
// name the same directory and a string comparison calls them different. Windows
// needs the resolution too, for the other reason — EvalSymlinks normalises a
// path's letter case there, which a case-insensitive filesystem otherwise makes
// a false negative out of.
//
// A path that cannot be resolved — most often because it does not exist — is
// compared lexically after [filepath.Clean], which is the best answer available
// and is exact whenever both paths were built from the same root.
func SamePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

// resolvePath returns the canonical form of a path, or its cleaned form when the
// filesystem cannot answer.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}
