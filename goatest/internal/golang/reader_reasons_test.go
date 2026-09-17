// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"slices"
	"testing"
	"time"

	gotest "github.com/P4suta/go-mutants/goatest/internal/golang"
)

func scanOne(t *testing.T, source string) (gotest.RepositoryReadCandidate, bool) {
	t.Helper()
	root := t.TempDir()
	writeGo(t, root, "subject/subject.go", source)
	candidates := gotest.RepositoryReadCandidates(root,
		[]gotest.Package{{ImportPath: "example.com/module/subject", RelativeDir: "subject"}})
	candidate, named := candidates["example.com/module/subject"]
	return candidate, named
}

func TestARepositoryReadCandidateSaysWhyItCannotBeObserved(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		source       string
		candidate    bool
		unobservable bool
		reasons      []string
	}{
		{
			name: "a read the action log records",
			source: `package subject

import "os"

func Read() ([]byte, error) { return os.ReadFile("go.mod") }
`,
			candidate: true,
		},
		{
			name: "a read the action log cannot see",
			source: `package subject

import "os"

func Read() (string, error) { return os.Readlink("link") }
`,
			candidate: true, unobservable: true, reasons: []string{"os.Readlink"},
		},
		{
			name: "a read through an import this repository renames",
			source: `package subject

import stdfs "io/fs"

func Read(files stdfs.FS) ([]byte, error) { return stdfs.ReadFile(files, "go.mod") }
`,
			candidate: true, unobservable: true, reasons: []string{"io/fs"},
		},
		{
			name: "a read through a platform package",
			source: `package subject

import "golang.org/x/sys/unix"

func Read(path string) (string, error) { return unix.Readlink(path) }
`,
			candidate: true, unobservable: true, reasons: []string{"golang.org/x/sys"},
		},
		{
			name: "a package that names no reader at all",
			source: `package subject

func Quiet() int { return 1 }
`,
		},
		{
			name: "a reader imported for its side effects alone",
			source: `package subject

import _ "os"

func Quiet() int { return 1 }
`,
		},
		{
			name: "a package whose scope a dot import opens",
			source: `package subject

import . "os"

func Read() ([]byte, error) { return ReadFile("go.mod") }
`,
			candidate: true, unobservable: true, reasons: []string{"dot import"},
		},
		{
			name: "a package the Go compiler hands to a C toolchain",
			source: `package subject

import "C"

func Quiet() int { return 1 }
`,
			candidate: true, unobservable: true, reasons: []string{"cgo"},
		},
		{
			name: "a read that runs before any test does",
			source: `package subject

import "os"

var listing, _ = os.ReadDir(".")

func init() { _, _ = os.ReadDir(".") }
`,
			candidate: true, unobservable: true, reasons: []string{"package initialization"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate, named := scanOne(t, test.source)
			if named != test.candidate {
				t.Fatalf("the package is named a read candidate: %t, want %t", named, test.candidate)
			}
			if !named {
				return
			}
			if candidate.Unobservable != test.unobservable {
				t.Errorf("the candidate is unobservable: %t, want %t", candidate.Unobservable, test.unobservable)
			}
			if !slices.Equal(candidate.Reasons, test.reasons) {
				t.Errorf("the candidate says %q, want %q", candidate.Reasons, test.reasons)
			}
		})
	}
}

func TestARepositoryReadCandidateInheritsWhatItsDependenciesCannotObserve(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "reader/reader.go", `package reader

import "os"

func Read(path string) (string, error) { return os.Readlink(path) }
`)
	writeGo(t, root, "quiet/quiet.go", `package quiet

func Quiet() int { return 1 }
`)
	writeGo(t, root, "user/user.go", `package user

func Use() int { return 1 }
`)
	candidates := gotest.RepositoryReadCandidates(root, []gotest.Package{
		{ImportPath: "example.com/module/reader", RelativeDir: "reader"},
		{ImportPath: "example.com/module/quiet", RelativeDir: "quiet"},
		{
			ImportPath: "example.com/module/user", RelativeDir: "user",
			Dependencies: []string{"example.com/module/reader", "example.com/module/quiet"},
		},
	})

	user, named := candidates["example.com/module/user"]
	if !named {
		t.Fatal("a package depending on a reader is not named a read candidate")
	}
	if !user.Unobservable {
		t.Error("a package depending on a reader nothing can observe is called observable")
	}
	if !slices.Equal(user.Reasons, []string{"os.Readlink"}) {
		t.Errorf("the candidate says %q, want the reason of the dependency it inherited", user.Reasons)
	}
	if _, quiet := candidates["example.com/module/quiet"]; quiet {
		t.Error("a package that reads nothing was named a read candidate")
	}
}

func TestARepositoryReadCandidateAnswersConservativelyForSourceItCannotParse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		file     string
		inherits bool
	}{
		{name: "production source", file: "broken.go", inherits: true},
		{name: "test source", file: "broken_test.go"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeGo(t, root, "subject/quiet.go", "package subject\n\nfunc Quiet() int { return 1 }\n")
			writeGo(t, root, "subject/"+test.file, "package subject\n\nfunc Broken( {}\n")
			writeGo(t, root, "user/user.go", "package user\n\nfunc Use() int { return 1 }\n")
			candidates := gotest.RepositoryReadCandidates(root, []gotest.Package{
				{ImportPath: "example.com/module/subject", RelativeDir: "subject"},
				{
					ImportPath: "example.com/module/user", RelativeDir: "user",
					Dependencies: []string{"example.com/module/subject"},
				},
			})

			subject, named := candidates["example.com/module/subject"]
			if !named {
				t.Fatal("a package with source nobody can parse is not a read candidate")
			}
			if !subject.Unobservable || !slices.Contains(subject.Reasons, "unreadable source") {
				t.Fatalf("the candidate is %+v, want it unobservable because its source is unreadable", subject)
			}
			user, inherited := candidates["example.com/module/user"]
			if inherited != test.inherits {
				t.Fatalf("a package depending on it is a candidate: %t, want %t", inherited, test.inherits)
			}
			if inherited && !slices.Contains(user.Reasons, "unreadable source") {
				t.Errorf("the dependent says %q, want the reason it inherited", user.Reasons)
			}
		})
	}
}

func TestARepositoryReadCandidateAnswersConservativelyForADirectoryItCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "user/user.go", "package user\n\nfunc Use() int { return 1 }\n")
	candidates := gotest.RepositoryReadCandidates(root, []gotest.Package{
		{ImportPath: "example.com/module/absent", RelativeDir: "absent"},
		{
			ImportPath: "example.com/module/user", RelativeDir: "user",
			Dependencies: []string{"example.com/module/absent"},
		},
	})
	candidate, named := candidates["example.com/module/absent"]
	if !named {
		t.Fatal("a package whose directory cannot be read is not a read candidate")
	}
	if !candidate.Unobservable || !slices.Equal(candidate.Reasons, []string{"unreadable source"}) {
		t.Fatalf("the candidate is %+v, want it unobservable because its source is unreadable", candidate)
	}
	user, inherited := candidates["example.com/module/user"]
	if !inherited {
		t.Fatal("a package depending on one nobody can read is not a read candidate")
	}
	if !user.Unobservable || !slices.Contains(user.Reasons, "unreadable source") {
		t.Fatalf("the dependent is %+v, want it unobservable for the reason it inherited", user)
	}
}

func TestARepositoryReadCandidateFollowsAValueThatReadsBeforeAnyTest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		declaration string
		widens      bool
	}{
		{
			name:        "a value a dependency computes now",
			declaration: "var value, _ = reader.Read()", widens: true,
		},
		{
			name:        "a call an initialization function makes",
			declaration: "func init() { _, _ = reader.Read() }", widens: true,
		},
		{
			name:        "a value sync.OnceValue defers",
			declaration: "var value = sync.OnceValue(func() []byte { data, _ := reader.Read(); return data })",
		},
		{
			name:        "a call an ordinary function makes",
			declaration: "func Use() { _, _ = reader.Read() }",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeGo(t, root, "reader/reader.go", `package reader

import "os"

func Read() ([]byte, error) { return os.ReadFile("go.mod") }
`)
			writeGo(t, root, "subject/subject.go", "package subject\n\nimport (\n\t\"sync\"\n\n"+
				"\t\"example.com/module/reader\"\n)\n\nvar _ = sync.OnceValue\n\n"+test.declaration+"\n")
			candidates := gotest.RepositoryReadCandidates(root, []gotest.Package{
				{ImportPath: "example.com/module/reader", RelativeDir: "reader"},
				{
					ImportPath: "example.com/module/subject", RelativeDir: "subject",
					Dependencies: []string{"example.com/module/reader"},
				},
			})

			candidate, named := candidates["example.com/module/subject"]
			if !named {
				t.Fatal("a package depending on a reader is not a read candidate")
			}
			if candidate.Unobservable != test.widens {
				t.Fatalf("the candidate is unobservable %t, want %t: %+v",
					candidate.Unobservable, test.widens, candidate)
			}
			if test.widens && !slices.Contains(candidate.Reasons, "package initialization") {
				t.Errorf("the candidate says %q, want it to name the initialization that widened it",
					candidate.Reasons)
			}
		})
	}
}

const cyclicReadDeadline = 5 * time.Second

func TestRepositoryReadCandidatesReturnForHelpersThatCallEachOther(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "subject/subject.go", `package subject

import "os"

func init() { one() }

func one() { two() }

func two() { three() }

func three() { one(); _, _ = os.Readlink("link") }
`)
	packages := []gotest.Package{{ImportPath: "example.com/module/subject", RelativeDir: "subject"}}

	answered := make(chan map[string]gotest.RepositoryReadCandidate, 1)
	go func() { answered <- gotest.RepositoryReadCandidates(root, packages) }()
	select {
	case candidates := <-answered:
		candidate, named := candidates["example.com/module/subject"]
		if !named || !candidate.Unobservable {
			t.Fatalf("a package whose initialization reads a path answered %+v", candidate)
		}
	case <-time.After(cyclicReadDeadline):
		t.Fatal("the scan did not return for a reference graph that leads back to where it started")
	}
}

func TestARepositoryReadCandidateInheritsOnlyFromProductionDependencies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "quiet/quiet.go", "package quiet\n\nfunc Quiet() int { return 1 }\n")
	writeGo(t, root, "quiet/quiet_test.go", `package quiet

import (
	"os"
	"testing"
)

func TestQuiet(t *testing.T) {
	if _, err := os.Readlink("link"); err != nil {
		t.Fatal(err)
	}
}
`)
	writeGo(t, root, "user/user.go", "package user\n\nfunc Use() int { return 1 }\n")
	candidates := gotest.RepositoryReadCandidates(root, []gotest.Package{
		{ImportPath: "example.com/module/quiet", RelativeDir: "quiet"},
		{
			ImportPath: "example.com/module/user", RelativeDir: "user",
			Dependencies: []string{"example.com/module/quiet"},
		},
	})

	if _, named := candidates["example.com/module/quiet"]; !named {
		t.Fatal("a package whose own test reads a path is not a read candidate")
	}
	if candidate, inherited := candidates["example.com/module/user"]; inherited {
		t.Fatalf("a package inherited %+v from a dependency only its test reads a path in", candidate)
	}
}

func TestARepositoryReadCandidateIgnoresAnInitializationIntoAQuietDependency(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGo(t, root, "quiet/quiet.go", "package quiet\n\nfunc Quiet() int { return 1 }\n")
	writeGo(t, root, "subject/subject.go", `package subject

import "example.com/module/quiet"

func init() { _ = quiet.Quiet() }
`)
	candidates := gotest.RepositoryReadCandidates(root, []gotest.Package{
		{ImportPath: "example.com/module/quiet", RelativeDir: "quiet"},
		{
			ImportPath: "example.com/module/subject", RelativeDir: "subject",
			Dependencies: []string{"example.com/module/quiet"},
		},
	})

	if candidate, named := candidates["example.com/module/subject"]; named {
		t.Fatalf("a package whose initialization calls one that reads nothing answered %+v, want none",
			candidate)
	}
}
