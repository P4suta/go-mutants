// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCopyAgesEveryFileByAtLeastAnHour pins the least obvious rule in this
// package.
//
// cmd/go only puts a package directory's index into the build cache when every
// file in it is at least two seconds old, so a tree that was written a moment
// ago is not the tree a user has: the first `go list` over it indexes nothing,
// the second one might, and a test that counts index entries or cache misses
// passes or fails depending on how long the copy took. Every constructor here
// therefore hands back a tree that is an hour old, which is on the far side of
// every cutoff in the go command and of the one-second timestamp granularity
// some filesystems still have.
//
// Directories are checked too, and they are the reason [AgeTree] walks
// deepest-first: writing a file updates the modification time of the directory
// holding it, so a walk that aged the parent before its children would undo its
// own work.
func TestCopyAgesEveryFileByAtLeastAnHour(t *testing.T) {
	t.Parallel()

	root := Copy(t, "simple")
	cutoff := time.Now().Add(-time.Hour).Add(time.Second)

	var young []string
	walkTree(t, root, func(path string, info fs.FileInfo) {
		if info.ModTime().After(cutoff) {
			rel, _ := filepath.Rel(root, path)
			young = append(young, rel+" ("+info.ModTime().Format(time.RFC3339)+")")
		}
	})
	if len(young) != 0 {
		t.Errorf("%d path(s) in the copy are younger than an hour, so the go command indexes them "+
			"differently from a real tree:\n\t%s", len(young), strings.Join(young, "\n\t"))
	}
}

// TestCopyLeavesTheCorpusWhereItIs proves the copy is a copy: the fixtures are
// checked in, `git status --porcelain fixtures/` is a CI gate, and a helper that
// handed a test the corpus itself would let one run's report, snapshot or
// scratch directory land in the repository.
func TestCopyLeavesTheCorpusWhereItIs(t *testing.T) {
	t.Parallel()

	root := Copy(t, "simple")
	if SamePath(root, Fixture(t, "simple")) {
		t.Fatalf("Copy returned the corpus itself: %s", root)
	}
	WriteFile(t, filepath.Join(root, "scribble.txt"), []byte("written by a test"))
	if _, err := os.Stat(filepath.Join(Fixture(t, "simple"), "scribble.txt")); err == nil {
		t.Error("writing into the copy reached the corpus")
	}
	if !slices.Contains(Entries(t, root), "go.mod") {
		t.Errorf("the copy has no go.mod: %q", Entries(t, root))
	}
}

// TestCopyTreePreservesContentsAndCreatesParents states the two things every
// caller assumes: the bytes arrive unchanged, and the destination's parents are
// created rather than reported as missing.
func TestCopyTreePreservesContentsAndCreatesParents(t *testing.T) {
	t.Parallel()

	from := t.TempDir()
	WriteFile(t, filepath.Join(from, "top.txt"), []byte("top\n"))
	WriteFile(t, filepath.Join(from, "nested", "deep", "leaf.txt"), []byte("leaf\r\nwith bytes\x00\n"))
	if err := os.MkdirAll(filepath.Join(from, "empty"), 0o755); err != nil {
		t.Fatalf("creating an empty directory: %v", err)
	}

	to := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	CopyTree(t, from, to)

	if got, want := string(ReadFile(t, filepath.Join(to, "top.txt"))), "top\n"; got != want {
		t.Errorf("top.txt = %q, want %q", got, want)
	}
	if got, want := string(ReadFile(t, filepath.Join(to, "nested", "deep", "leaf.txt"))), "leaf\r\nwith bytes\x00\n"; got != want {
		t.Errorf("leaf.txt = %q, want %q", got, want)
	}
	if got, want := Entries(t, to), []string{"empty", "nested", "top.txt"}; !slices.Equal(got, want) {
		t.Errorf("the copy holds %q, want %q", got, want)
	}
}

// TestCopyTreePreservesTheExecutableBit matters for the corpus modules that
// carry a script and for anything a test builds and then runs: a copy that
// dropped the bit would fail with a permission error rather than with whatever
// the test was about.
func TestCopyTreePreservesTheExecutableBit(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows has no executable bit; chmod there only toggles read-only")
	}
	from := t.TempDir()
	script := filepath.Join(from, "run.sh")
	WriteFile(t, script, []byte("#!/bin/sh\nexit 0\n"))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatalf("making the script executable: %v", err)
	}

	to := filepath.Join(t.TempDir(), "copy")
	CopyTree(t, from, to)

	info, err := os.Stat(filepath.Join(to, "run.sh"))
	if err != nil {
		t.Fatalf("stat of the copied script: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the copied script's mode is %v, which is not executable", info.Mode().Perm())
	}
}

// TestAgeTreeRefusesToFollowALinkOutOfTheTree is the reason the walk checks the
// type of every entry rather than only asking whether it is a directory.
//
// os.Chtimes follows symlinks, so a tree with a link in it — which is a tree a
// user can hand go-mutants, and a shape internal/snapshot refuses for the same
// reason — would have had its ages written through the link onto whatever it
// points at. The target here is a file outside the tree, which is the case that
// matters: a helper that reached out of the directory it was given would be
// modifying the developer's own files from a test that claimed to be working in
// a temporary copy.
func TestAgeTreeRefusesToFollowALinkOutOfTheTree(t *testing.T) {
	t.Parallel()

	outside := filepath.Join(t.TempDir(), "outside.txt")
	WriteFile(t, outside, []byte("not the test's to touch"))
	stamp := modTime(t, outside)

	root := t.TempDir()
	WriteFile(t, filepath.Join(root, "inside.txt"), []byte("x"))
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("this platform does not allow this test to create a symlink: %v", err)
	}

	rec := &recorder{TB: t}
	AgeTree(rec, root)
	if len(rec.fatals) != 1 {
		t.Fatalf("AgeTree produced %d fatal report(s) for a tree with a symlink in it, want 1: %q",
			len(rec.fatals), rec.fatals)
	}
	if !strings.Contains(rec.fatals[0], "only directories and regular files") {
		t.Errorf("the report does not say what the rule is:\n%s", rec.fatals[0])
	}
	if got := modTime(t, outside); !got.Equal(stamp) {
		t.Errorf("the file outside the tree was aged through the link: %v, was %v", got, stamp)
	}
}

// TestCopyTreeMakesEveryCopiedFileWritable is about Windows cleanup.
//
// A read-only file copied read-only cannot be removed on Windows, so t.TempDir's
// cleanup fails on a tree that held one — and a fixture may well hold one, since
// a mode is part of what a copy preserves. The write bit is added to the copy and
// nothing else is: the executable bit still travels, and the original is
// untouched.
func TestCopyTreeMakesEveryCopiedFileWritable(t *testing.T) {
	t.Parallel()

	from := t.TempDir()
	readOnly := filepath.Join(from, "read-only.txt")
	WriteFile(t, readOnly, []byte("locked"))
	if err := os.Chmod(readOnly, 0o444); err != nil {
		t.Fatalf("making the source read-only: %v", err)
	}

	to := filepath.Join(t.TempDir(), "copy")
	CopyTree(t, from, to)

	if got := modeOf(t, filepath.Join(to, "read-only.txt")).Perm(); got&0o200 == 0 {
		t.Errorf("the copy's mode is %v, which cannot be removed on Windows", got)
	}
	if runtime.GOOS != "windows" {
		if got := modeOf(t, readOnly).Perm(); got != 0o444 {
			t.Errorf("the source's mode changed to %v", got)
		}
	}
}

// modTime reads one path's modification time.
func modTime(t testing.TB, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// modeOf reads one path's mode.
func modeOf(t testing.TB, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode()
}

// TestWriteSourcePrefixesTheSPDXHeader keeps the licensing gate green for the
// files that only ever exist inside a t.TempDir.
//
// `gofmt -l .` and the REUSE check walk the filesystem rather than the module
// graph, and a synthesized module written into a temporary directory is not
// walked by either — but the same helper writes the fixture files that *are*
// checked in, and a header a test has to remember is a header a test forgets.
// So the helper writes it, and the body it is given is a body rather than a
// file.
func TestWriteSourcePrefixesTheSPDXHeader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	WriteSource(t, dir, "example.go", "package example\n\nfunc Example() int { return 1 }\n")

	got := string(ReadFile(t, filepath.Join(dir, "example.go")))
	want := "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
		"// SPDX-License-Identifier: MIT OR Apache-2.0\n\n" +
		"package example\n\nfunc Example() int { return 1 }\n"
	if got != want {
		t.Errorf("WriteSource wrote\n%q\nwant\n%q", got, want)
	}
}

// TestWriteFileCreatesTheDirectoriesAboveIt removes the three-line MkdirAll
// preamble every one of these helpers had a copy of.
func TestWriteFileCreatesTheDirectoriesAboveIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a", "b", "c", "file.txt")
	WriteFile(t, path, []byte("content"))
	if got := string(ReadFile(t, path)); got != "content" {
		t.Errorf("the file holds %q, want %q", got, "content")
	}
}

// TestSamePathAgreesWithTheFilesystemRatherThanWithTheString is why no
// assertion in this repository compares two paths with ==.
//
// macOS resolves /var to /private/var, so t.TempDir returns one spelling and a
// child process that resolved its own working directory reports the other. Both
// name the same directory, and a string comparison calls them different.
func TestSamePathAgreesWithTheFilesystemRatherThanWithTheString(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if !SamePath(dir, filepath.Join(dir, ".")) {
		t.Errorf("SamePath(%s, %s/.) is false", dir, dir)
	}
	if SamePath(dir, filepath.Dir(dir)) {
		t.Errorf("SamePath reports %s and its parent as the same directory", dir)
	}

	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("this platform does not allow this test to create a symlink: %v", err)
	}
	if !SamePath(link, dir) {
		t.Errorf("SamePath(%s, %s) is false, but the first is a symlink to the second", link, dir)
	}
}

// TestAgeTreeAgesADirectoryAfterTheFilesInIt states the ordering rule directly,
// so a rewrite of the walk that lost it fails here rather than in whichever
// suite next counts build-cache misses.
func TestAgeTreeAgesADirectoryAfterTheFilesInIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	WriteFile(t, filepath.Join(root, "one", "two", "three.txt"), []byte("x"))
	AgeTree(t, root)

	cutoff := time.Now().Add(-time.Hour).Add(time.Second)
	for _, path := range []string{root, filepath.Join(root, "one"), filepath.Join(root, "one", "two"), filepath.Join(root, "one", "two", "three.txt")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.ModTime().After(cutoff) {
			t.Errorf("%s is %v old, want at least an hour", path, time.Since(info.ModTime()).Truncate(time.Second))
		}
	}
}

// walkTree visits every path under root, root included.
func walkTree(t testing.TB, root string, visit func(path string, info fs.FileInfo)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		visit(path, info)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}
