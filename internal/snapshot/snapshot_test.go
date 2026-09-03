// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/glob"
)

// writeTree creates every file in files under root, making parent directories
// as needed. Keys are '/'-normalized relative paths.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", abs, err)
		}
	}
}

func mkdirAll(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", rel, err)
	}
}

// create snapshots src and registers cleanup, so a test never leaves a
// snapshot behind even when it fails. DestParent defaults to a directory the
// testing package owns, keeping debris out of the real temporary directory.
func create(t *testing.T, src string, opts Options) *Snapshot {
	t.Helper()
	if opts.DestParent == "" {
		opts.DestParent = t.TempDir()
	}
	snap, err := Create(src, opts)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		if err := snap.Cleanup(); err != nil {
			t.Errorf("Cleanup: %v", err)
		}
	})
	return snap
}

func assertManifest(t *testing.T, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("manifest has %d entries, want %d:\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("manifest[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func assertIsDir(t *testing.T, abs string) {
	t.Helper()
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("Stat(%s): %v", abs, err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", abs)
	}
}

func assertCode(t *testing.T, err error, want Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with code %s, got nil", want)
	}
	if got := CodeOf(err); got != want {
		t.Fatalf("error code = %q, want %q (error: %v)", got, want, err)
	}
}

func relPaths(entries []Entry) []string {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.RelPath
	}
	return paths
}

func readFile(t *testing.T, abs string) string {
	t.Helper()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", abs, err)
	}
	return string(b)
}

// symlinkOrSkip creates a symbolic link, skipping the test when the platform
// refuses. Unprivileged Windows without Developer Mode cannot create one, and
// that is a property of the machine rather than a failure of this package.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symbolic link on this machine (%v); the rejection path is unexercised here", err)
	}
}

func TestCreateCopiesBytesExactly(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"lf.go":       "package a\n\nvar x = 1\n",
		"crlf.go":     "package a\r\n\r\nvar x = 1\r\n",
		"bom.go":      "\ufeffpackage a\n",
		"binary.dat":  "\x00\x01\x02\xff\xfe",
		"empty.txt":   "",
		"a/nested.go": "package nested\n",
	}
	src := t.TempDir()
	writeTree(t, src, files)

	snap := create(t, src, Options{})

	for rel, want := range files {
		got := readFile(t, filepath.Join(snap.Root, filepath.FromSlash(rel)))
		if got != want {
			t.Errorf("%s copied as %q, want %q", rel, got, want)
		}
	}
	// The CRLF file in particular: a newline translated on the way in would
	// change the source digest and rename every mutant in the file.
	if !strings.Contains(readFile(t, filepath.Join(snap.Root, "crlf.go")), "\r\n") {
		t.Error("CRLF line endings did not survive the copy")
	}
	for _, e := range snap.Manifest {
		if want := int64(len(files[e.RelPath])); e.Size != want {
			t.Errorf("manifest size for %s = %d, want %d", e.RelPath, e.Size, want)
		}
	}
}

func TestCreateManifestIsSortedByPath(t *testing.T) {
	t.Parallel()

	// "a.go" sorts before "a/b.go" by path ('.' is 0x2E, '/' is 0x2F) but is
	// visited after it by any walk that recurses into a directory when it
	// meets it. This is the case that separates sorting from traversal order.
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"a.go":     "package a\n",
		"a/b.go":   "package b\n",
		"a/c.go":   "package c\n",
		"a-b.go":   "package ab\n",
		"z/y/x.go": "package x\n",
		"B.go":     "package B\n",
	})

	snap := create(t, src, Options{})

	want := []string{"B.go", "a-b.go", "a.go", "a/b.go", "a/c.go", "z/y/x.go"}
	if got := relPaths(snap.Manifest); !slices.Equal(got, want) {
		t.Errorf("manifest paths = %v, want %v", got, want)
	}
}

func TestCreateIsDeterministic(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"go.mod":      "module example.com/x\n",
		"a/one.go":    "package a\n\nfunc One() int { return 1 }\n",
		"a/two.go":    "package a\n\nfunc Two() int { return 2 }\n",
		"b/three.go":  "package b\n",
		"c/d/four.go": "package d\n",
	}

	first := t.TempDir()
	writeTree(t, first, files)
	second := t.TempDir()
	writeTree(t, second, files)

	a := create(t, first, Options{})
	b := create(t, second, Options{})

	if a.Root == b.Root {
		t.Fatal("two snapshots share a root directory")
	}
	if a.WorkspaceDigest != b.WorkspaceDigest {
		t.Errorf("two copies of the same tree digest differently: %s vs %s", a.WorkspaceDigest, b.WorkspaceDigest)
	}
	assertManifest(t, a.Manifest, b.Manifest)

	// A one-byte content change moves the digest.
	changedFiles := maps.Clone(files)
	changedFiles["a/one.go"] = "package a\n\nfunc One() int { return 2 }\n"
	changed := t.TempDir()
	writeTree(t, changed, changedFiles)
	if c := create(t, changed, Options{}); c.WorkspaceDigest == a.WorkspaceDigest {
		t.Error("changing a file's contents did not change the workspace digest")
	}

	// So does moving a file, even though the bytes are all still there.
	movedFiles := maps.Clone(files)
	delete(movedFiles, "a/one.go")
	movedFiles["a/uno.go"] = files["a/one.go"]
	moved := t.TempDir()
	writeTree(t, moved, movedFiles)
	if m := create(t, moved, Options{}); m.WorkspaceDigest == a.WorkspaceDigest {
		t.Error("renaming a file did not change the workspace digest")
	}
}

func TestCreateExclusions(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"keep.go":                      "package keep\n",
		".git/config":                  "[core]\n",
		".git/objects/ab/cdef":         "binary\n",
		"nested/module/.git/HEAD":      "ref: refs/heads/main\n",
		"nested/module/keep.go":        "package keep\n",
		"reports/mutation/report.json": "{}\n",
		"reports/keep.md":              "# keep\n",
		"vendor/dep/dep.go":            "package dep\n",
		"build/out.bin":                "\x00",
		"docs/deep/notes.md":           "notes\n",
	})

	snap := create(t, src, Options{
		Exclude: []glob.Pattern{
			glob.MustCompile("vendor/**"),
			glob.MustCompile("**/*.bin"),
		},
	})

	want := []string{
		"docs/deep/notes.md",
		"keep.go",
		"nested/module/keep.go",
		"reports/keep.md",
	}
	if got := relPaths(snap.Manifest); !slices.Equal(got, want) {
		t.Errorf("manifest paths = %v, want %v", got, want)
	}
	// An excluded directory is not descended into, so it is not created in the
	// snapshot either.
	for _, gone := range []string{".git", "vendor", "reports/mutation"} {
		if _, err := os.Stat(filepath.Join(snap.Root, filepath.FromSlash(gone))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("excluded %s exists in the snapshot (err=%v)", gone, err)
		}
	}
}

func TestCreateExcludesConfiguredReportDir(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"keep.go":                      "package keep\n",
		"artifacts/mutation.json":      "{}\n",
		"reports/mutation/report.json": "{}\n",
	})

	// The configured directory is excluded in addition to the conventional
	// one, never instead of it.
	snap := create(t, src, Options{ReportDir: "artifacts"})

	if got := relPaths(snap.Manifest); !slices.Equal(got, []string{"keep.go"}) {
		t.Errorf("manifest paths = %v, want [keep.go]", got)
	}
}

func TestCreateRejectsBadReportDir(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"keep.go": "package keep\n"})

	for _, dir := range []string{"/absolute", "../escaping"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			_, err := Create(src, Options{ReportDir: dir, DestParent: t.TempDir()})
			assertCode(t, err, CodeInvalidOptions)
		})
	}
}

func TestCreateRejectsSymlink(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/real.go": "package pkg\n"})
	symlinkOrSkip(t, filepath.Join(src, "pkg", "real.go"), filepath.Join(src, "pkg", "link.go"))

	dest := t.TempDir()
	_, err := Create(src, Options{DestParent: dest})
	assertCode(t, err, CodeSymlink)
	if !strings.Contains(err.Error(), "pkg/link.go") {
		t.Errorf("error does not name the offending path: %v", err)
	}
	assertEmptyDir(t, dest)
}

func TestCreateRejectsSymlinkedDirectory(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"real/file.go": "package real\n"})
	symlinkOrSkip(t, filepath.Join(src, "real"), filepath.Join(src, "alias"))

	_, err := Create(src, Options{DestParent: t.TempDir()})
	assertCode(t, err, CodeSymlink)
	if !strings.Contains(err.Error(), "alias") {
		t.Errorf("error does not name the offending path: %v", err)
	}
}

// TestCreateSkipsExcludedSymlink proves the exclusion check happens before the
// rejection check, which is how a user gets past a link they do not need.
func TestCreateSkipsExcludedSymlink(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/real.go": "package pkg\n"})
	symlinkOrSkip(t, filepath.Join(src, "pkg", "real.go"), filepath.Join(src, "pkg", "link.go"))

	snap := create(t, src, Options{Exclude: []glob.Pattern{glob.MustCompile("pkg/link.go")}})
	if got := relPaths(snap.Manifest); !slices.Equal(got, []string{"pkg/real.go"}) {
		t.Errorf("manifest paths = %v, want [pkg/real.go]", got)
	}
}

// TestCreateReportsFirstRejectionByPath pins which of several bad entries is
// named. Reporting the first in path order rather than in visit order keeps
// the message stable across filesystems that hand back directory entries in
// different orders.
func TestCreateReportsFirstRejectionByPath(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"target.go": "package t\n"})
	symlinkOrSkip(t, filepath.Join(src, "target.go"), filepath.Join(src, "zzz.go"))
	symlinkOrSkip(t, filepath.Join(src, "target.go"), filepath.Join(src, "aaa.go"))

	_, err := Create(src, Options{DestParent: t.TempDir()})
	assertCode(t, err, CodeSymlink)
	if !strings.Contains(err.Error(), "aaa.go") {
		t.Errorf("error should name the first path in order, got: %v", err)
	}
}

func TestCreateRejectsMissingOrNonDirectoryRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	writeTree(t, base, map[string]string{"file.go": "package f\n"})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		_, err := Create(filepath.Join(base, "nope"), Options{DestParent: t.TempDir()})
		assertCode(t, err, CodeSourceRoot)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error should wrap fs.ErrNotExist, got %v", err)
		}
	})
	t.Run("not a directory", func(t *testing.T) {
		t.Parallel()
		_, err := Create(filepath.Join(base, "file.go"), Options{DestParent: t.TempDir()})
		assertCode(t, err, CodeSourceRoot)
	})
}

func TestCreateRejectsMissingDestParent(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})

	_, err := Create(src, Options{DestParent: filepath.Join(t.TempDir(), "does-not-exist")})
	assertCode(t, err, CodeDestination)
}

// TestCreateDeepTree is the end-to-end long path case: on Windows the copied
// paths run past MAX_PATH, and the run must survive. It does not prove that
// extendedPath is what saved it — the standard library rewrites long absolute
// paths of its own accord — so the helper's output is pinned separately in
// longpath_windows_test.go.
func TestCreateDeepTree(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	// Nest until the absolute path is past MAX_PATH with room to spare,
	// whatever the temporary directory in front of it costs.
	segment := strings.Repeat("d", 24)
	rel := segment
	for len(src)+len(rel) < 300 {
		rel += "/" + segment
	}
	deep := rel + "/leaf.go"
	writeTree(t, src, map[string]string{deep: "package leaf\n"})
	if length := len(filepath.Join(src, filepath.FromSlash(deep))); length <= 260 {
		t.Fatalf("the fixture path is only %d characters, which is not a long path", length)
	}

	snap := create(t, src, Options{})
	if got := relPaths(snap.Manifest); !slices.Equal(got, []string{deep}) {
		t.Fatalf("manifest paths = %v, want [%s]", got, deep)
	}
	if got := readFile(t, filepath.Join(snap.Root, filepath.FromSlash(deep))); got != "package leaf\n" {
		t.Errorf("deep file copied as %q", got)
	}
	if drifts, err := snap.Redigest(); err != nil || len(drifts) != 0 {
		t.Errorf("Redigest over a deep tree = %v, %v; want no drift", drifts, err)
	}
}

func TestCreateEmptyTree(t *testing.T) {
	t.Parallel()

	snap := create(t, t.TempDir(), Options{})
	if len(snap.Manifest) != 0 {
		t.Errorf("manifest = %v, want empty", snap.Manifest)
	}
	// An empty workspace still has a digest: the domain separator alone.
	if snap.WorkspaceDigest != goldenEmptyDigest {
		t.Errorf("WorkspaceDigest = %s, want %s", snap.WorkspaceDigest, goldenEmptyDigest)
	}
}

func TestCreateLeavesNothingBehindOnFailure(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})
	symlinkOrSkip(t, filepath.Join(src, "a.go"), filepath.Join(src, "b.go"))

	dest := t.TempDir()
	if _, err := Create(src, Options{DestParent: dest}); err == nil {
		t.Fatal("Create succeeded on a tree holding a symbolic link")
	}
	assertEmptyDir(t, dest)
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("%s is not empty: %v", dir, names)
	}
}

// TestCreateDoesNotCopyItself pins an ordering guarantee that is invisible in
// the API: the tree is walked before the destination is created, so a caller
// that puts the snapshot inside the source root — a plausible way to keep test
// debris on the same volume — does not snapshot the snapshot.
func TestCreateDoesNotCopyItself(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})

	snap := create(t, src, Options{DestParent: src})

	if got := relPaths(snap.Manifest); !slices.Equal(got, []string{"a.go"}) {
		t.Errorf("manifest paths = %v, want [a.go]", got)
	}
	if drifts, err := snap.Redigest(); err != nil || len(drifts) != 0 {
		t.Errorf("Redigest = %v, %v; want no drift", drifts, err)
	}
}

// TestCreateWithRelativeDestParent covers the path where the snapshot is
// perfectly usable and undeletable: a relative DestParent used as it stands —
// joined with the stable name, or handed to os.MkdirTemp — yields a relative
// root, and the Cleanup guard refuses anything that is not absolute. Both the
// success and the abandoned-copy paths have to survive it.
func TestCreateWithRelativeDestParent(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	relativeDest, err := filepath.Rel(cwd, dest)
	if err != nil {
		t.Skipf("no relative path from %s to %s (%v)", cwd, dest, err)
	}

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})

	snap := create(t, src, Options{DestParent: relativeDest})
	if !filepath.IsAbs(snap.Root) {
		t.Errorf("Root = %q, want an absolute path", snap.Root)
	}
	if got := snap.Parent(); !pathsEqual(got, dest) {
		t.Errorf("snapshot parent = %s, want %s", got, dest)
	}

	// Cleanup has to accept it too, which the helper's deferred call asserts.
	// A relative DestParent that produced a relative Root would fail there and
	// nowhere else: the snapshot would work perfectly and never go away.
}

// assertAbandoned is the shared assertion of the two platform-specific tests
// that make a copy fail after the destination directory already exists. It is
// the only path where Create has to undo work it has already done.
func assertAbandoned(t *testing.T, src, dest string) {
	t.Helper()
	_, err := Create(src, Options{DestParent: dest})
	assertCode(t, err, CodeCopy)
	assertEmptyDir(t, dest)
}

// TestUnsupportedName is a unit test rather than a tree test because most of
// these names cannot be created on the platform this suite usually runs on:
// Windows forbids a backslash in a file name outright, and no filesystem hands
// back "." or ".." from a directory listing. The branch still has to be right
// the day a POSIX tree really does contain "a\b.go", where treating it as the
// path "a/b.go" would give two different files one identity.
func TestUnsupportedName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		refuseIt bool
	}{
		{name: "main.go", refuseIt: false},
		{name: ".gitignore", refuseIt: false},
		{name: "テスト.txt", refuseIt: false},
		{name: "a file with spaces.go", refuseIt: false},
		{name: `a\b.go`, refuseIt: true},
		{name: "a/b.go", refuseIt: true},
		{name: "a\x00b.go", refuseIt: true},
		{name: "", refuseIt: true},
		{name: ".", refuseIt: true},
		{name: "..", refuseIt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reason := unsupportedName(tt.name)
			if refused := reason != ""; refused != tt.refuseIt {
				t.Errorf("unsupportedName(%q) = %q, want refused=%v", tt.name, reason, tt.refuseIt)
			}
		})
	}
}

func TestSnapshotRootShape(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dest := t.TempDir()
	snap := create(t, src, Options{DestParent: dest})

	if !strings.HasPrefix(filepath.Base(snap.Dir()), DirPrefix) {
		t.Errorf("snapshot directory %q does not begin with %q", filepath.Base(snap.Dir()), DirPrefix)
	}
	// The copy is a subdirectory of the directory that carries the ownership
	// files; see TestCreateOwnsItsDirectoryWithoutTouchingTheTree.
	if got := filepath.Dir(snap.Root); !pathsEqual(got, snap.Dir()) {
		t.Errorf("the tree lives in %s, want %s", got, snap.Dir())
	}
	if got := snap.Parent(); !pathsEqual(got, dest) {
		t.Errorf("snapshot parent = %s, want %s", got, dest)
	}
	if !filepath.IsAbs(snap.SourceRoot) {
		t.Errorf("SourceRoot = %s, want an absolute path", snap.SourceRoot)
	}
}
