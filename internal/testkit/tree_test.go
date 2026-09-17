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

	rec := expectFatal(t, func(tb testing.TB) { AgeTree(tb, root) })
	if report := rec.first(t, "AgeTree over a tree with a symlink in it"); !strings.Contains(report, "only directories and regular files") {
		t.Errorf("the report does not say what the rule is:\n%s", report)
	}
	if got := modTime(t, outside); !got.Equal(stamp) {
		t.Errorf("the file outside the tree was aged through the link: %v, was %v", got, stamp)
	}
}

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

func modTime(t testing.TB, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

func modeOf(t testing.TB, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode()
}

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

func TestWriteFileCreatesTheDirectoriesAboveIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a", "b", "c", "file.txt")
	WriteFile(t, path, []byte("content"))
	if got := string(ReadFile(t, path)); got != "content" {
		t.Errorf("the file holds %q, want %q", got, "content")
	}
}

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
