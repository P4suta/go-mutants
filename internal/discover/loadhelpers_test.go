// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

// TestWhichPackagesACgoImportExempts pins [cgoExemption.covers].
//
// A cgo package is excluded from mutation wholesale, so whether its C
// preprocessing step succeeded is not a question discovery has to have an
// answer to -- and the compile gate has to know that before it refuses the
// tree. The exemption answers for the packages the loader returned and for no
// others: one entry per package the scan found a cgo import in, matched by the
// loader's own ID, and nothing inferred from how a path is spelled.
func TestWhichPackagesACgoImportExempts(t *testing.T) {
	t.Parallel()

	exemption := cgoExemption{"example.com/m/cgopkg": true}
	for _, c := range []struct {
		name string
		pkg  *packages.Package
		want bool
	}{
		{
			name: "the package the cgo file was found in",
			pkg:  &packages.Package{ID: "example.com/m/cgopkg", PkgPath: "example.com/m/cgopkg"},
			want: true,
		},
		{
			// The loader is not asked for test variants, so a path spelled
			// like one is a package somebody wrote under that name -- and its
			// build errors are its own.
			name: "a package named like the cgo package's external test package",
			pkg:  &packages.Package{ID: "x", PkgPath: "example.com/m/cgopkg_test"},
		},
		{
			name: "a package named like the cgo package's test binary",
			pkg:  &packages.Package{ID: "y", PkgPath: "example.com/m/cgopkg.test"},
		},
		{
			name: "another package whose name ends in _test",
			pkg:  &packages.Package{ID: "z", PkgPath: "example.com/m/other_test"},
		},
		{
			name: "another package whose name ends in .test",
			pkg:  &packages.Package{ID: "w", PkgPath: "example.com/m/other.test"},
		},
		{
			name: "a package that imports it",
			pkg:  &packages.Package{ID: "v", PkgPath: "example.com/m/user"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := exemption.covers(c.pkg); got != c.want {
				t.Errorf("covers(%s) = %v, want %v", c.pkg.PkgPath, got, c.want)
			}
		})
	}

	// And an exemption that found nothing exempts nothing, which is what every
	// run on a tree with no cgo in it is.
	empty := cgoExemption{}
	if empty.covers(&packages.Package{ID: "x", PkgPath: "example.com/m/pkg"}) {
		t.Error("an empty exemption covers a package")
	}
}

// TestACgoImportIsFoundInTheSourceRatherThanInTheGraph pins [findCgoPackages].
//
// The question is asked of the file on disk because that is the only place the
// truth survives: with cgo enabled the import is rewritten away before the
// loader produces syntax, and with cgo disabled the file is not part of the
// package at all. What is recorded is the loader's own ID, which is the one
// coordinate every package has -- a package the loader could not place has no
// import path at all, and keying on that would exempt every other such package
// with it.
func TestACgoImportIsFoundInTheSourceRatherThanInTheGraph(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("making the directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		return path
	}
	cgoFile := write("cgopkg/cgo.go", "package cgopkg\n\nimport \"C\"\n")
	plainFile := write("plain/plain.go", "package plain\n")

	loaded := &loadResult{packages: []*packages.Package{
		{ID: "example.com/m/cgopkg", PkgPath: "example.com/m/cgopkg", GoFiles: []string{cgoFile}},
		{ID: "example.com/m/plain", PkgPath: "example.com/m/plain", GoFiles: []string{plainFile}},
		// The loader leaves the path empty for a package it could not place,
		// and the ID is then the only coordinate there is.
		{ID: "unplaced", PkgPath: "", GoFiles: []string{cgoFile}},
	}}

	exemption := findCgoPackages(loaded, root)
	for _, id := range []string{"example.com/m/cgopkg", "unplaced"} {
		if !exemption[id] {
			t.Errorf("the exemption does not name the loader ID %q", id)
		}
	}
	if exemption["example.com/m/plain"] {
		t.Error("the exemption names a package with no cgo file in it")
	}
	if exemption[""] {
		t.Error("the exemption holds the empty ID, which every unnamed package would match")
	}

	// And what recording the ID is for: the compile gate asks `covers`, and a
	// package the loader could only give an ID has to be covered by it.
	if !exemption.covers(&packages.Package{ID: "unplaced"}) {
		t.Error("a package named only by its loader ID is not covered")
	}
}

// TestTheCompileGateNamesWhatStoppedIt pins [gate] and the sample it quotes.
//
// Discovery needs a tree that compiles, because the types it reads are what
// every rule's applicability is decided by. What makes the refusal usable is
// the sample: a build with four hundred errors in it is a wall of text nobody
// reads, so a handful are quoted and the rest are counted -- and the count has
// to be the arithmetic rather than an impression, because "and 3 more errors"
// is how somebody decides whether to look.
func TestTheCompileGateNamesWhatStoppedIt(t *testing.T) {
	t.Parallel()

	failing := func(path string, n int) *packages.Package {
		pkg := &packages.Package{ID: path, PkgPath: path}
		for i := range n {
			pkg.Errors = append(pkg.Errors, packages.Error{
				Pos: "a.go:" + strconv.Itoa(i+1) + ":1",
				Msg: "undefined: x" + strconv.Itoa(i+1),
			})
		}
		return pkg
	}
	empty := cgoExemption{}

	t.Run("a tree that compiles", func(t *testing.T) {
		t.Parallel()

		loaded := &loadResult{packages: []*packages.Package{{ID: "a", PkgPath: "example.com/m/a"}}}
		if err := gate(loaded, empty); err != nil {
			t.Fatalf("gate over a tree with no errors: %v", err)
		}
	})

	t.Run("one error", func(t *testing.T) {
		t.Parallel()

		loaded := &loadResult{packages: []*packages.Package{failing("example.com/m/a", 1)}}
		err := gate(loaded, empty)
		if code := CodeOf(err); code != CodePackageErrors {
			t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodePackageErrors)
		}
		for _, want := range []string{"1 package error", "undefined: x1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q does not say %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "more error") {
			t.Errorf("the refusal %q counts errors it already quoted", err)
		}
	})

	t.Run("more errors than the sample holds", func(t *testing.T) {
		t.Parallel()

		loaded := &loadResult{packages: []*packages.Package{failing("example.com/m/a", errorSample+3)}}
		err := gate(loaded, empty)
		if err == nil {
			t.Fatal("gate over a tree that does not compile succeeded")
		}
		if want := strconv.Itoa(errorSample+3) + " package errors"; !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
		if want := "and " + plural(3, "more error"); !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
		if got := strings.Count(err.Error(), "undefined: x"); got != errorSample {
			t.Errorf("the refusal quotes %d errors, want the sample's %d", got, errorSample)
		}
	})

	t.Run("exactly as many errors as the sample holds", func(t *testing.T) {
		t.Parallel()

		// The boundary the count is written across: with nothing left over,
		// the refusal must not offer to count what it has already quoted.
		loaded := &loadResult{packages: []*packages.Package{failing("example.com/m/a", errorSample)}}
		err := gate(loaded, empty)
		if err == nil {
			t.Fatal("gate over a tree that does not compile succeeded")
		}
		if strings.Contains(err.Error(), "more error") {
			t.Errorf("the refusal %q counts errors it already quoted", err)
		}
	})

	t.Run("errors in an exempt package", func(t *testing.T) {
		t.Parallel()

		// A cgo package is excluded from mutation wholesale, so whether its C
		// preprocessing step succeeded is not a question this gate has to have
		// an answer to. A machine with no C compiler must still be able to run.
		loaded := &loadResult{packages: []*packages.Package{failing("example.com/m/cgopkg", 4)}}
		exempt := cgoExemption{"example.com/m/cgopkg": true}
		if err := gate(loaded, exempt); err != nil {
			t.Fatalf("gate over an exempt package's errors: %v", err)
		}
	})
}

// TestTheMainModuleIsTheOneRootedAtTheSnapshot pins [mainModule], whose two
// refusals are different discoveries about the same tree.
//
// Every identity go-mutants mints is module-relative and the snapshot manifest
// is rooted at the snapshot, so a main module rooted anywhere else would make a
// candidate's path name a file the snapshot does not hold. "No package here
// belongs to a module rooted here" and "the module is rooted somewhere else"
// send a user to two different places, so they are two sentences.
func TestTheMainModuleIsTheOneRootedAtTheSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	main := &packages.Module{Path: "example.com/m", Main: true, Dir: root}

	t.Run("the module rooted at the snapshot", func(t *testing.T) {
		t.Parallel()

		loaded := &loadResult{packages: []*packages.Package{
			{ID: "dep", Module: &packages.Module{Path: "example.com/dep", Dir: "/elsewhere"}},
			{ID: "ours", Module: main},
		}}
		got, err := mainModule(loaded, root)
		if err != nil {
			t.Fatalf("mainModule: %v", err)
		}
		if got != main {
			t.Errorf("mainModule = %+v, want the module rooted at the snapshot", got)
		}
	})

	t.Run("a main module rooted elsewhere", func(t *testing.T) {
		t.Parallel()

		loaded := &loadResult{packages: []*packages.Package{
			{ID: "ours", Module: &packages.Module{Path: "example.com/m", Main: true, Dir: filepath.Join(root, "sub")}},
		}}
		_, err := mainModule(loaded, root)
		if code := CodeOf(err); code != CodeModuleNotFound {
			t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeModuleNotFound)
		}
		if !strings.Contains(err.Error(), "not at the snapshot root") {
			t.Errorf("the refusal %q does not say where it expected the module", err)
		}
	})

	for _, c := range []struct {
		name string
		pkgs []*packages.Package
	}{
		{name: "no packages at all"},
		{
			name: "a package with no module",
			pkgs: []*packages.Package{{ID: "ours"}},
		},
		{
			name: "a module that is not the main one",
			pkgs: []*packages.Package{{ID: "dep", Module: &packages.Module{Path: "example.com/dep", Dir: root}}},
		},
		{
			name: "a main module with no directory",
			pkgs: []*packages.Package{{ID: "ours", Module: &packages.Module{Path: "example.com/m", Main: true}}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, err := mainModule(&loadResult{packages: c.pkgs}, root)
			if code := CodeOf(err); code != CodeModuleNotFound {
				t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeModuleNotFound)
			}
			if !strings.Contains(err.Error(), "belongs to a module rooted there") {
				t.Errorf("the refusal %q is not the one about finding no module at all", err)
			}
		})
	}
}

// TestAFailedLoadNamesTheToolchainItFound pins [toolchainHint].
//
// go/packages runs the `go` command found on the child's PATH, and a load that
// failed is most often a load that ran a different `go` from the one the run
// reported. Naming the located toolchain in the refusal is what lets the two be
// compared at a glance; naming nothing when nothing was located is what keeps
// the sentence from ending in a dangling parenthesis.
func TestAFailedLoadNamesTheToolchainItFound(t *testing.T) {
	t.Parallel()

	located := gocmd.Toolchain{GoBin: filepath.Join("opt", "go", "bin", "go")}
	hint := toolchainHint(located)
	if !strings.Contains(hint, located.GoBin) {
		t.Errorf("the hint %q does not name the toolchain", hint)
	}
	if !strings.HasPrefix(hint, " ") {
		t.Errorf("the hint %q does not join onto the sentence before it", hint)
	}
	if got := toolchainHint(gocmd.Toolchain{}); got != "" {
		t.Errorf("toolchainHint with nothing located = %q, want nothing", got)
	}
}

// TestAToolchainWithNoDirectoryChangesNoPath is the pair of early returns in
// the environment builder.
//
// Prepending the toolchain's directory to PATH is what keeps a `go` that hands
// work to another `go` -- the toolchain line in a go.mod is resolved that way --
// from resolving a different one. When there is no directory to prepend, the
// environment has to come back as it was: an empty entry in front of PATH would
// put the *working directory* on it, which is a path this run does not control.
func TestAToolchainWithNoDirectoryChangesNoPath(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=" + filepath.Join("usr", "bin")}
	for _, c := range []struct {
		name  string
		goBin string
	}{
		{name: "no toolchain located at all", goBin: ""},
		{name: "a toolchain named without a directory", goBin: "go"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := environmentFrom(base, gocmd.Toolchain{GoBin: c.goBin}, false)
			want := []string{"PATH=" + filepath.Join("usr", "bin"), "GOWORK=off"}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("environmentFrom = %v, want %v", got, want)
			}
		})
	}

	// And a workspace run, which *removes* GOWORK rather than pinning it off:
	// a module of a workspace has to be loaded with the workspace in force, and
	// an empty GOWORK is read by the go command as "no workspace" rather than
	// as "decide for yourself".
	got := environmentFrom(base, gocmd.Toolchain{}, true)
	for _, entry := range got {
		if strings.HasPrefix(entry, "GOWORK=") {
			t.Errorf("a workspace load carries %q, want no GOWORK at all", entry)
		}
	}
}

// TestAPathWithNoRelativeFormIsOutsideTheModule is [relativePath]'s third
// refusal, which is neither "outside" nor "unnormalizable".
//
// `filepath.Rel` refuses a pair it cannot express -- a relative root and an
// absolute file have no relative path between them without knowing the working
// directory -- and the answer has to be "not this module's" rather than a
// guess. The root a real run passes is absolute, so this is the fail-closed
// arm; without it a caller that passed a relative root would get a path that
// looks fine and names a file somewhere else.
func TestAPathWithNoRelativeFormIsOutsideTheModule(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the paths here are POSIX ones; the rule they state is not platform-specific")
	}
	if rel, ok := relativePath("module", "/abs/pkg/file.go"); ok {
		t.Errorf("relativePath with a relative root = (%q, true), want a refusal", rel)
	}
	if rel, ok := relativePath("/work", "/work/pkg/file.go"); !ok || rel != "pkg/file.go" {
		t.Errorf("relativePath = (%q, %v), want (pkg/file.go, true)", rel, ok)
	}
	if rel, ok := relativePath("/work", "/elsewhere/file.go"); ok {
		t.Errorf("relativePath outside the root = (%q, true), want a refusal", rel)
	}
	if rel, ok := relativePath("/work", "/work"); ok {
		t.Errorf("relativePath of the root itself = (%q, true), want a refusal", rel)
	}
}
