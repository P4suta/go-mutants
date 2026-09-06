// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestModuleWritesTheGoDirectiveOfThisRepository keeps a synthesized module
// buildable by the toolchain the repository is pinned to.
//
// A `go` directive newer than the toolchain makes every command against the
// module fail with a toolchain-download request, and GOTOOLCHAIN=local — which
// the environment policy sets, so no test ever reaches the network for a
// compiler — turns that request into an error. A directive older than the
// repository's is not wrong, but it is a second version to keep in step with
// go.mod by hand. Reading the repository's own answer is the version of this
// that cannot drift.
func TestModuleWritesTheGoDirectiveOfThisRepository(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/synthetic")
	gomod := string(ReadFile(t, m.Path("go.mod")))

	want, err := goDirective(Root(t))
	if err != nil {
		t.Fatalf("reading this repository's go directive: %v", err)
	}
	if want == "" {
		t.Fatal("this repository's go.mod has no go directive")
	}
	if !strings.Contains(gomod, "\ngo "+want+"\n") {
		t.Errorf("the synthesized go.mod does not carry `go %s`:\n%s", want, gomod)
	}
	if !strings.Contains(gomod, "module fixture.example/synthetic\n") {
		t.Errorf("the synthesized go.mod does not name the module:\n%s", gomod)
	}
	if !strings.HasPrefix(gomod, "// SPDX-FileCopyrightText") {
		t.Errorf("the synthesized go.mod carries no SPDX header:\n%s", gomod)
	}
}

// TestModuleCRLFRewritesEveryLineEndingAndNothingElse is the fixture the corpus
// cannot hold.
//
// `.gitattributes` pins `* -text`, so a CRLF file checked in here would arrive
// as CRLF on every platform and change every mutant ID that covers it — which is
// exactly why the instrumenter's byte-preservation claim needs a CRLF module and
// why that module has to be synthesized. The rewrite is only the line endings:
// the bytes between them, the file's mode and every file that is not Go source
// are left as they were, or the module would be a different program rather than
// the same program with other line endings.
func TestModuleCRLFRewritesEveryLineEndingAndNothingElse(t *testing.T) {
	t.Parallel()

	const body = "package crlf\n\n// Double returns twice v.\nfunc Double(v int) int { return v * 2 }\n"
	m := NewModule(t).
		Module("fixture.example/crlf").
		Source("crlf.go", body).
		File("notes.txt", "a\nb\n")

	before := map[string]string{}
	for _, rel := range []string{"go.mod", "crlf.go", "notes.txt"} {
		before[rel] = string(ReadFile(t, m.Path(rel)))
	}

	m.CRLF()

	for _, rel := range []string{"go.mod", "crlf.go"} {
		got := string(ReadFile(t, m.Path(rel)))
		if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
			t.Errorf("%s still has an LF-only line ending:\n%q", rel, got)
		}
		if restored := strings.ReplaceAll(got, "\r\n", "\n"); restored != before[rel] {
			t.Errorf("%s changed in more than its line endings:\n%q\nwant\n%q", rel, restored, before[rel])
		}
	}
	if got := string(ReadFile(t, m.Path("notes.txt"))); got != before["notes.txt"] {
		t.Errorf("notes.txt was rewritten too:\n%q\nwant\n%q", got, before["notes.txt"])
	}
}

// TestModuleCRLFIsIdempotent stops a second call from producing CR CR LF, which
// is not a line ending on any platform and would be a corrupt fixture that still
// compiled in most places.
func TestModuleCRLFIsIdempotent(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/crlf").Source("crlf.go", "package crlf\n")
	once := string(ReadFile(t, m.CRLF().Path("crlf.go")))
	twice := string(ReadFile(t, m.CRLF().Path("crlf.go")))
	if once != twice {
		t.Errorf("a second CRLF rewrite changed the file:\n%q\nwant\n%q", twice, once)
	}
	if strings.Contains(twice, "\r\r") {
		t.Errorf("the rewrite doubled a carriage return:\n%q", twice)
	}
}

// TestModuleFromAgesTheTree carries [TreeAge]'s rule through the builder: a
// fixture copied in and then written to is still a tree the go command indexes
// the way it indexes a real one.
func TestModuleFromAgesTheTree(t *testing.T) {
	t.Parallel()

	m := NewModule(t).From("simple").Source("extra.go", "package simple\n").CRLF()
	cutoff := time.Now().Add(-TreeAge).Add(time.Second)

	var young []string
	walkTree(t, m.Root(), func(path string, info fs.FileInfo) {
		if info.ModTime().After(cutoff) {
			rel, _ := filepath.Rel(m.Root(), path)
			young = append(young, rel)
		}
	})
	if len(young) != 0 {
		t.Errorf("the builder left %d path(s) younger than %v: %q", len(young), TreeAge, young)
	}
	if got := Entries(t, m.Root()); !slices.Contains(got, "go.mod") || !slices.Contains(got, "extra.go") {
		t.Errorf("the module holds %q, want the fixture's go.mod and the added file", got)
	}
}

// TestModuleCRLFRefusesToRewriteThroughALink is [AgeTree]'s rule for the other
// walk in this package: os.WriteFile follows a symlink, so a `.go` link in the
// module would have had CRLF written onto whatever it pointed at — a file outside
// the module, and quite possibly one of this repository's own sources.
func TestModuleCRLFRefusesToRewriteThroughALink(t *testing.T) {
	t.Parallel()

	outsideDir := t.TempDir()
	WriteSource(t, outsideDir, "outside.go", "package outside\n")
	outside := filepath.Join(outsideDir, "outside.go")
	before := string(ReadFile(t, outside))

	m := NewModule(t).Module("fixture.example/linked")
	if err := os.Symlink(outside, m.Path("linked.go")); err != nil {
		t.Skipf("this platform does not allow this test to create a symlink: %v", err)
	}

	// The recorder does not stop at a Fatalf the way testing.T does, so the
	// builder runs on past the refusal and reports again from the ageing pass.
	// The first report is the one a real test would have seen.
	rec := &recorder{TB: t}
	NewModuleAt(rec, m.Root()).CRLF()
	if len(rec.fatals) == 0 {
		t.Fatal("CRLF rewrote a module with a symlink in it without reporting anything")
	}
	if !strings.Contains(rec.fatals[0], "only directories and regular files") {
		t.Errorf("the report does not say what the rule is:\n%s", rec.fatals[0])
	}
	if got := string(ReadFile(t, outside)); got != before {
		t.Errorf("the file outside the module was rewritten through the link:\n%q\nwant\n%q", got, before)
	}
}

// TestModuleFileAgesWhatItWroteWithoutWalkingTheTree keeps the builder linear in
// the number of writes rather than quadratic.
//
// Ageing the whole tree after every write is O(files) per write, and the corpus
// modules the builder starts from have dozens of them; a module built up file by
// file would spend its time re-stamping files nothing had touched. What a write
// can change is the file itself and the modification times of the directories
// above it, so those are what is aged.
func TestModuleFileAgesWhatItWroteWithoutWalkingTheTree(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/ages").Source("a/b/c.go", "package c\n")
	cutoff := time.Now().Add(-TreeAge).Add(time.Second)

	for _, path := range []string{
		m.Root(),
		m.Path("a"),
		m.Path("a/b"),
		m.Path("a/b/c.go"),
		m.Path("go.mod"),
	} {
		if got := modTime(t, path); got.After(cutoff) {
			t.Errorf("%s is %v old, want at least %v", path, time.Since(got).Truncate(time.Second), TreeAge)
		}
	}
}

// TestASynthesizedModuleBuildsUnderTheHermeticEnvironment is the one test that
// puts the whole harness together, because every part of it is a claim about
// what a real `go` command will accept.
//
// The `go` directive has to be one the pinned toolchain can use, since
// GOTOOLCHAIN=local turns a request for another one into an error; the module
// path has to be resolvable with GOPROXY=off and GOFLAGS=-mod=readonly, which is
// what "no dependencies, ever" buys the corpus; and the module cache and build
// cache have to be reachable under a moved HOME. Each of those is asserted as a
// value elsewhere in this package. This asserts that the go command agrees.
func TestASynthesizedModuleBuildsUnderTheHermeticEnvironment(t *testing.T) {
	e := Env(t)
	gobin := GoBinary(t)

	m := NewModule(t).
		Module("fixture.example/synthetic").
		Source("synthetic.go", "package synthetic\n\n// Double returns twice v.\nfunc Double(v int) int { return v * 2 }\n")

	result := Exec(t, m.Root(), e.Vars(), gobin, "build", "./...")
	RequireExit(t, result, 0, "`go build ./...` in the synthesized module")
}

// TestModuleFileWritesUnderTheRootWithSlashPaths keeps the builder's paths
// portable: every call site spells a relative path with forward slashes, which
// is what a Go source file's import path and a `go list` pattern look like, and
// the builder is the one place that has to turn that into a native path.
func TestModuleFileWritesUnderTheRootWithSlashPaths(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/nested").Source("pkg/inner/inner.go", "package inner\n")
	got := m.Path("pkg/inner/inner.go")
	if want := filepath.Join(m.Root(), "pkg", "inner", "inner.go"); got != want {
		t.Errorf("Path = %s, want %s", got, want)
	}
	if !strings.Contains(string(ReadFile(t, got)), "package inner") {
		t.Error("the nested source file does not hold what was written")
	}
}
