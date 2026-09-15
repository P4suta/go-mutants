// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/gocmd"
)

// The small decisions the loader makes about paths, environments and wording.
//
// Each of them is a rule the rest of the phase relies on without restating:
// which files a package owns, which environment a child gets, whether two
// spellings of a directory are the same directory. None of them needs a
// toolchain to answer, and each of them is wrong in a way the phase above would
// report as something else — a file left out of a package is a mutant nobody
// ever writes, and it looks exactly like a file with no candidates in it.

// TestTwoNamesAreTheSameUnderThePlatformsRules is the pair of comparisons that
// take the platform as a value.
//
// Both halves of each are asserted here whatever this runner is, which is the
// whole reason they take a parameter: written as a `runtime.GOOS` branch, the
// Windows half would be a line only a Windows runner ever reaches, and the
// claim it makes would be one only a Windows runner could check.
func TestTwoNamesAreTheSameUnderThePlatformsRules(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		goos string
		a, b string
		want bool
	}{
		{goos: "windows", a: "Path", b: "PATH", want: true},
		{goos: "windows", a: "PATH", b: "PATH", want: true},
		{goos: "windows", a: "GOFLAGS", b: "GOPATH"},
		{goos: "linux", a: "Path", b: "PATH"},
		{goos: "linux", a: "PATH", b: "PATH", want: true},
		{goos: "darwin", a: "Path", b: "PATH"},
	} {
		t.Run("an environment key on "+c.goos+" "+c.a+" "+c.b, func(t *testing.T) {
			t.Parallel()

			if got := sameEnvKeyOn(c.goos, c.a, c.b); got != c.want {
				t.Errorf("sameEnvKeyOn(%q, %q, %q) = %v, want %v", c.goos, c.a, c.b, got, c.want)
			}
		})
	}

	for _, c := range []struct {
		goos string
		a, b string
		want bool
	}{
		{goos: "windows", a: `C:\Tmp\Mod`, b: `c:\tmp\mod`, want: true},
		{goos: "windows", a: `C:\Tmp\Mod`, b: `C:\Tmp\Other`},
		{goos: "linux", a: "/tmp/Mod", b: "/tmp/mod"},
		{goos: "linux", a: "/tmp/mod", b: "/tmp/mod", want: true},
		{goos: "darwin", a: "/tmp/Mod", b: "/tmp/mod"},
	} {
		t.Run("a path on "+c.goos+" "+c.a+" "+c.b, func(t *testing.T) {
			t.Parallel()

			if got := pathsEqualOn(c.goos, c.a, c.b); got != c.want {
				t.Errorf("pathsEqualOn(%q, %q, %q) = %v, want %v", c.goos, c.a, c.b, got, c.want)
			}
		})
	}

	// And that the platform each is asked about by default is this one, which
	// is the connection a parameterised comparison could otherwise lose.
	windows := runtime.GOOS == "windows"
	if got := sameEnvKey("Path", "PATH"); got != windows {
		t.Errorf("sameEnvKey(Path, PATH) = %v, want %v on %s", got, windows, runtime.GOOS)
	}
	if got := pathsEqual("/tmp/Mod", "/tmp/mod"); got != windows {
		t.Errorf("pathsEqual differing only in case = %v, want %v on %s", got, windows, runtime.GOOS)
	}
}

// TestTwoSpellingsOfOneDirectoryAreOneDirectory is [samePath], which decides
// whether the package the loader placed at the module root is the snapshot
// root.
//
// Cleaning is not enough, and the reason is on every macOS machine: the
// temporary directory is behind a symlink, and the go command may report either
// spelling. So both are resolved, and the resolution falling back to the
// cleaned comparison is what makes a path that no longer exists answerable at
// all.
func TestTwoSpellingsOfOneDirectoryAreOneDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this machine does not allow symlinks: %v", err)
	}

	if !samePath(real, real+string(filepath.Separator)) {
		t.Error("a directory is not the same as itself with a trailing separator")
	}
	if !samePath(link, real) {
		t.Error("a symlink to a directory is not the same directory")
	}
	if samePath(real, filepath.Join(root, "other")) {
		t.Error("two different directories are the same")
	}
	// Neither exists, so neither resolves, and the cleaned comparison is the
	// whole answer. Both directions matter: a run over a tree that has already
	// been cleaned up must not claim two unrelated paths are one.
	gone := filepath.Join(root, "gone")
	if !samePath(gone, filepath.Join(root, "sub", "..", "gone")) {
		t.Error("two spellings of one path that does not exist are not the same")
	}
	if samePath(gone, filepath.Join(root, "elsewhere")) {
		t.Error("two paths that do not exist and are not the same are the same")
	}
}

// TestWhichFilesAPackageOwns is [moduleFiles], and every clause of it is a file
// that would otherwise be mutated or missed.
//
// Ignored files are listed beside the built ones because which of the two a cgo
// file lands in is decided by CGO_ENABLED rather than by anything about the
// file. Test files are never mutated. A file outside the module root is not the
// module's to mutate. And the order is the module-relative path, so that a
// catalogue is the same whatever order the loader answered in.
func TestWhichFilesAPackageOwns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	pkg := &packages.Package{
		GoFiles: []string{
			at("pkg", "z.go"),
			at("pkg", "a.go"),
			at("pkg", "a.go"),
			at("pkg", "a_test.go"),
			at("pkg", "README.md"),
			filepath.Join(filepath.Dir(root), "outside.go"),
		},
		IgnoredFiles: []string{at("pkg", "cgo.go"), at("pkg", "z.go")},
	}

	var got []string
	for _, ref := range moduleFiles(pkg, root) {
		got = append(got, ref.rel)
		if !filepath.IsAbs(ref.abs) {
			t.Errorf("the reference to %q carries the relative path %q as its absolute one", ref.rel, ref.abs)
		}
	}
	want := []string{"pkg/a.go", "pkg/cgo.go", "pkg/z.go"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("moduleFiles = %v, want %v", got, want)
	}
}

// TestAFileThatImportsCIsRecognisedWithoutACompiler pins [importsC], which is
// what lets a cgo package be skipped by name on a machine with no C compiler.
//
// The unparsable file is the clause worth stating. Guessing that it is cgo
// would exempt it from the very check that would have explained the problem,
// so it is reported as not importing C and the parse error is left to be
// reported as a parse error.
func TestAFileThatImportsCIsRecognisedWithoutACompiler(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		return path
	}
	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{name: "cgo.go", src: "package pkg\n\nimport \"C\"\n", want: true},
		{name: "cgo_aliased.go", src: "package pkg\n\nimport (\n\t\"os\"\n\t\"C\"\n)\n\nvar _ = os.Args\n", want: true},
		{name: "plain.go", src: "package pkg\n\nimport \"os\"\n\nvar _ = os.Args\n"},
		{name: "none.go", src: "package pkg\n"},
		{name: "broken.go", src: "package\n"},
	} {
		if got := importsC(token.NewFileSet(), write(c.name, c.src)); got != c.want {
			t.Errorf("importsC(%s) = %v, want %v", c.name, got, c.want)
		}
	}
	if importsC(token.NewFileSet(), filepath.Join(root, "absent.go")) {
		t.Error("importsC(a file that is not there) = true, want false")
	}
}

// TestACountIsRenderedWithItsNoun pins [plural], which is in every message the
// package-error gate writes.
func TestACountIsRenderedWithItsNoun(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		n    int
		want string
	}{
		{0, "0 package errors"},
		{1, "1 package error"},
		{2, "2 package errors"},
	} {
		if got := plural(c.n, "package error"); got != c.want {
			t.Errorf("plural(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestTheEnvironmentAChildLoadIsGiven pins the three edits made to it.
//
// The located toolchain has to be on the child's PATH, because `go list` runs
// `go` again for the toolchain switch and a bare `go` there resolves through
// the child's own PATH — which under a toolchain manager is not the `go` the
// run says it is using. Prepending it is what makes the two the same.
func TestTheEnvironmentAChildLoadIsGiven(t *testing.T) {
	t.Parallel()

	sep := string(filepath.ListSeparator)
	bin := filepath.Join("opt", "go", "bin")
	goBin := filepath.Join(bin, "go")

	for _, c := range []struct {
		name string
		base []string
		want []string
	}{
		{
			name: "the directory is prepended to an existing PATH",
			base: []string{"PATH=" + filepath.Join("usr", "bin")},
			want: []string{"PATH=" + bin + sep + filepath.Join("usr", "bin"), "GOWORK=off"},
		},
		{
			name: "a PATH that already starts with it is left alone",
			base: []string{"PATH=" + bin + sep + filepath.Join("usr", "bin")},
			want: []string{"PATH=" + bin + sep + filepath.Join("usr", "bin"), "GOWORK=off"},
		},
		{
			name: "a PATH that is exactly it is left alone",
			base: []string{"PATH=" + bin},
			want: []string{"PATH=" + bin, "GOWORK=off"},
		},
		{
			name: "an environment with no PATH gains one",
			base: []string{"HOME=/home/x"},
			want: []string{"HOME=/home/x", "GOWORK=off", "PATH=" + bin},
		},
		{
			name: "a PATH that merely contains it elsewhere still gains it in front",
			base: []string{"PATH=" + filepath.Join("usr", "bin") + sep + bin},
			want: []string{"PATH=" + bin + sep + filepath.Join("usr", "bin") + sep + bin, "GOWORK=off"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := environmentFrom(c.base, gocmd.Toolchain{GoBin: goBin}, false)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("environmentFrom = %v, want %v", got, c.want)
			}
		})
	}
}
