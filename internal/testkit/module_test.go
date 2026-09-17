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

	rec := expectFatal(t, func(tb testing.TB) { NewModuleAt(tb, m.Root()).CRLF() })
	if report := rec.first(t, "CRLF over a module with a symlink in it"); !strings.Contains(report, "only directories and regular files") {
		t.Errorf("the report does not say what the rule is:\n%s", report)
	}
	if got := string(ReadFile(t, outside)); got != before {
		t.Errorf("the file outside the module was rewritten through the link:\n%q\nwant\n%q", got, before)
	}
}

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

func TestASynthesizedModuleBuildsUnderTheHermeticEnvironment(t *testing.T) {
	e := Env(t)
	gobin := GoBinary(t)

	m := NewModule(t).
		Module("fixture.example/synthetic").
		Source("synthetic.go", "package synthetic\n\n// Double returns twice v.\nfunc Double(v int) int { return v * 2 }\n")

	result := Exec(t, m.Root(), e.Vars(), gobin, "build", "./...")
	RequireExit(t, result, 0, "`go build ./...` in the synthesized module")
}

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
