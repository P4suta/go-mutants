// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang_test

import (
	"slices"
	"testing"

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
