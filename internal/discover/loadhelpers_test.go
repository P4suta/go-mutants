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

	windows := runtime.GOOS == "windows"
	if got := sameEnvKey("Path", "PATH"); got != windows {
		t.Errorf("sameEnvKey(Path, PATH) = %v, want %v on %s", got, windows, runtime.GOOS)
	}
	if got := pathsEqual("/tmp/Mod", "/tmp/mod"); got != windows {
		t.Errorf("pathsEqual differing only in case = %v, want %v on %s", got, windows, runtime.GOOS)
	}
}

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
	gone := filepath.Join(root, "gone")
	if !samePath(gone, filepath.Join(root, "sub", "..", "gone")) {
		t.Error("two spellings of one path that does not exist are not the same")
	}
	if samePath(gone, filepath.Join(root, "elsewhere")) {
		t.Error("two paths that do not exist and are not the same are the same")
	}
}

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

	empty := cgoExemption{}
	if empty.covers(&packages.Package{ID: "x", PkgPath: "example.com/m/pkg"}) {
		t.Error("an empty exemption covers a package")
	}
}

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

	if !exemption.covers(&packages.Package{ID: "unplaced"}) {
		t.Error("a package named only by its loader ID is not covered")
	}
}

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

		loaded := &loadResult{packages: []*packages.Package{failing("example.com/m/cgopkg", 4)}}
		exempt := cgoExemption{"example.com/m/cgopkg": true}
		if err := gate(loaded, exempt); err != nil {
			t.Fatalf("gate over an exempt package's errors: %v", err)
		}
	})
}

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

	got := environmentFrom(base, gocmd.Toolchain{}, true)
	for _, entry := range got {
		if strings.HasPrefix(entry, "GOWORK=") {
			t.Errorf("a workspace load carries %q, want no GOWORK at all", entry)
		}
	}
}

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
