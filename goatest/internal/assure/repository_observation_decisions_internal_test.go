// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
)

type repositoryLogFileStub struct {
	name     string
	closeErr error
}

func (file repositoryLogFileStub) Name() string { return file.name }
func (file repositoryLogFileStub) Close() error { return file.closeErr }

func repositoryFilesStub(root string) repositoryObserverFiles {
	return repositoryObserverFiles{
		absolute: func(string) (string, error) { return root, nil },
		create: func(string, string) (repositoryLogFile, error) {
			return repositoryLogFileStub{name: filepath.Join(root, "action.log")}, nil
		},
		remove: func(string) error { return nil },
		read:   func(string) ([]byte, error) { return repositoryTestLogMagic, nil },
	}
}

func TestCompleteRepositoryObservationScopePreservesStaticCandidates(t *testing.T) {
	t.Parallel()
	const pkg = "fixture.example/module"
	candidate := goanalysis.RepositoryReadCandidate{Unobservable: true}
	candidates, readers := completeRepositoryObservationScope(
		map[string]goanalysis.RepositoryReadCandidate{pkg: candidate},
		[]goanalysis.Package{{ImportPath: pkg}},
	)
	if !reflect.DeepEqual(candidates[pkg], candidate) || !readers[pkg] {
		t.Fatalf("completed scope = (%+v, %+v)", candidates, readers)
	}
}

func TestRepositoryObserverConstructionHandlesEveryAbsolutePathResult(t *testing.T) {
	t.Parallel()
	const resolved = "/resolved/root"
	cause := errors.New("absolute path failed")
	for _, test := range []struct {
		name     string
		root     string
		absolute string
		err      error
		want     string
	}{
		{name: "resolved", root: "root", absolute: resolved + "/.", want: resolved},
		{name: "empty root", absolute: resolved},
		{name: "resolution failure", root: "root", absolute: resolved, err: cause},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := repositoryFilesStub(resolved)
			files.absolute = func(string) (string, error) { return test.absolute, test.err }
			observer := newRepositoryObserverWithFiles(test.root, "logs", nil, targetKeySources{}, files)
			if observer.root != test.want {
				t.Fatalf("observer root = %q, want %q", observer.root, test.want)
			}
		})
	}
}

func TestFixedRepositoryObservationsAreCallableAndExact(t *testing.T) {
	t.Parallel()
	for _, reason := range []wholeTreeReason{wholeTreeObserved, wholeTreeStaticUnobservable, wholeTreeLogUnavailable} {
		finish := fixedRepositoryObservation(reason)
		if finish == nil || !reflect.DeepEqual(finish(), repositoryObservation{reason: reason}) {
			t.Fatalf("fixed observation for %q was nil or inexact", reason)
		}
	}
}

func TestRepositoryInstrumentationStopsAtEveryUnavailableLogStage(t *testing.T) {
	t.Parallel()
	const pkg = "fixture.example/module"
	root := t.TempDir()
	arguments := []string{"-test.run=^TestValue$"}
	cause := errors.New("log failed")
	model := goanalysis.Model{Packages: []goanalysis.Package{{ImportPath: pkg, RelativeDir: "."}}}
	for _, test := range []struct {
		name         string
		candidate    *goanalysis.RepositoryReadCandidate
		change       func(*repositoryObserverFiles, *int)
		removed      int
		instrumented bool
	}{
		{name: "unselected"},
		{name: "static unobservable", candidate: &goanalysis.RepositoryReadCandidate{Unobservable: true}},
		{
			name: "create failure", candidate: &goanalysis.RepositoryReadCandidate{},
			change: func(files *repositoryObserverFiles, _ *int) {
				files.create = func(string, string) (repositoryLogFile, error) { return nil, cause }
			},
		},
		{
			name: "close failure", candidate: &goanalysis.RepositoryReadCandidate{}, removed: 1,
			change: func(files *repositoryObserverFiles, removed *int) {
				files.create = func(string, string) (repositoryLogFile, error) {
					return repositoryLogFileStub{name: filepath.Join(root, "close.log"), closeErr: cause}, nil
				}
				files.remove = func(string) error { *removed++; return nil }
			},
		},
		{
			name: "read failure", candidate: &goanalysis.RepositoryReadCandidate{}, removed: 1, instrumented: true,
			change: func(files *repositoryObserverFiles, removed *int) {
				files.read = func(string) ([]byte, error) { return nil, cause }
				files.remove = func(string) error { *removed++; return nil }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidates := map[string]goanalysis.RepositoryReadCandidate{}
			if test.candidate != nil {
				candidates[pkg] = *test.candidate
			}
			files := repositoryFilesStub(root)
			removed := 0
			if test.change != nil {
				test.change(&files, &removed)
			}
			observer := newRepositoryObserverWithFiles(root, "logs", candidates, targetKeySources{model: model}, files)
			got, finish := observer.instrument(pkg, ".", arguments)
			argumentsMatch := reflect.DeepEqual(got, arguments)
			if test.instrumented {
				argumentsMatch = len(got) == len(arguments)+1 && reflect.DeepEqual(got[:len(arguments)], arguments)
				_, found := repositoryTestLogPath(got)
				argumentsMatch = argumentsMatch && found
			}
			if !argumentsMatch || finish == nil {
				t.Fatalf("instrumentation arguments = %v, finish nil = %t", got, finish == nil)
			}
			wantReason := wholeTreeObserved
			if test.candidate != nil {
				wantReason = wholeTreeLogUnavailable
				if test.candidate.Unobservable {
					wantReason = wholeTreeStaticUnobservable
				}
			}
			if observation := finish(); !reflect.DeepEqual(observation, repositoryObservation{reason: wantReason}) || removed != test.removed {
				t.Fatalf("finished observation = %+v, removals %d", observation, removed)
			}
		})
	}
}

func TestRepositoryWholeTreeReasonsDistinguishEveryAccessBoundary(t *testing.T) {
	t.Parallel()
	const pkg = "fixture.example/module"
	inputs := evidence.Inputs{
		Files:  map[string]string{"value.go": "value"},
		Corpus: map[string]string{"testdata/fuzz/FuzzValue/seed": "seed"},
	}
	model := goanalysis.Model{Packages: []goanalysis.Package{{ImportPath: pkg, RelativeDir: "."}}}
	sources := newTargetKeySources(inputs, model, "standard-v1", Options{}, map[string]bool{pkg: true})
	observer := &RepositoryObserver{
		candidates: map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, sources: sources,
	}
	for _, test := range []struct {
		name   string
		target goanalysis.Target
		access repositoryAccess
		want   wholeTreeReason
	}{
		{name: "known file", target: goanalysis.Target{Package: pkg}, access: repositoryAccess{path: "value.go"}},
		{
			name: "known corpus", target: goanalysis.Target{Package: pkg, RelativeDir: ".", Kind: goanalysis.KindFuzz, Name: "FuzzValue"},
			access: repositoryAccess{path: "testdata/fuzz/FuzzValue/seed"},
		},
		{name: "directory", target: goanalysis.Target{Package: pkg}, access: repositoryAccess{path: "value.go", directory: true}, want: wholeTreeDirectoryAccess},
		{name: "outside input", target: goanalysis.Target{Package: pkg}, access: repositoryAccess{path: "other.go"}, want: wholeTreeOutsideInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := observer.wholeTreeReason(test.target, repositoryObservation{accesses: []repositoryAccess{test.access}})
			if got != test.want {
				t.Fatalf("whole-tree reason = %q, want %q", got, test.want)
			}
		})
	}

	observer.candidates[pkg] = goanalysis.RepositoryReadCandidate{Unobservable: true}
	if got := observer.wholeTreeReason(goanalysis.Target{Package: pkg}, repositoryObservation{}); got != wholeTreeStaticUnobservable {
		t.Fatalf("static reason = %q", got)
	}
}

func TestRepositoryWholeTreeSuiteReasonsDistinguishUnknownPackages(t *testing.T) {
	t.Parallel()
	const pkg = "fixture.example/module"
	for _, test := range []struct {
		name       string
		candidates map[string]goanalysis.RepositoryReadCandidate
		want       wholeTreeReason
	}{
		{name: "not selected"},
		{name: "selected", candidates: map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, want: wholeTreeStaticUnobservable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observer := &RepositoryObserver{candidates: test.candidates, packages: map[string]goanalysis.Package{}}
			if got := observer.wholeTreeSuiteReason(pkg, repositoryObservation{}); got != test.want {
				t.Fatalf("suite reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWholeTreeWideningCountsEachTrueFact(t *testing.T) {
	t.Parallel()
	targets, suites := wholeTreeWideningCounts(
		[]TargetEvidence{{}, {WholeTree: true}, {}},
		map[string]PackageSuiteCoverage{"one": {}, "two": {WholeTree: true}},
	)
	if targets != 1 || suites != 1 {
		t.Fatalf("widening counts = (%d, %d)", targets, suites)
	}
}

func TestRepositoryLogParsingPreservesInsideAndOutsideDirectoryFacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inside := filepath.Join(root, "pkg")
	outside := t.TempDir()
	for _, test := range []struct {
		name string
		log  string
		want []repositoryAccess
	}{
		{name: "inside chdir", log: "chdir " + inside + "\n", want: []repositoryAccess{{path: "pkg", directory: true}}},
		{name: "outside chdir", log: "chdir " + outside + "\n"},
		{name: "missing open", log: "open missing.txt\n", want: []repositoryAccess{{path: "pkg/missing.txt", directory: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observation := parseRepositoryTestLogWithStat(
				append(append([]byte{}, repositoryTestLogMagic...), []byte(test.log)...),
				root, inside,
				func(string) (os.FileInfo, error) { return nil, errors.New("missing") },
			)
			if observation.reason != wholeTreeObserved || !reflect.DeepEqual(observation.accesses, test.want) {
				t.Fatalf("parsed observation = %+v, want %+v", observation, test.want)
			}
		})
	}
}

func TestRepositoryRelativePathRejectsEveryInvalidBoundary(t *testing.T) {
	t.Parallel()
	cause := errors.New("relative path failed")
	inside := func(string, string) (string, error) { return "inside", nil }
	for _, test := range []struct {
		name string
		root string
		path string
		rel  func(string, string) (string, error)
	}{
		{name: "empty root", path: "name", rel: inside},
		{name: "empty name", root: "root", rel: inside},
		{name: "relative failure", root: "root", path: "name", rel: func(string, string) (string, error) { return "inside", cause }},
		{name: "parent", root: "root", path: "name", rel: func(string, string) (string, error) { return "..", nil }},
		{name: "parent descendant", root: "root", path: "name", rel: func(string, string) (string, error) { return filepath.Join("..", "peer"), nil }},
		{name: "absolute", root: "root", path: "name", rel: func(string, string) (string, error) { return filepath.Join(string(filepath.Separator), "inside"), nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, ok := repositoryRelativePathWith(test.root, test.path, test.rel); ok || got != "" {
				t.Fatalf("relative path = (%q, %t)", got, ok)
			}
		})
	}
	if got, ok := repositoryRelativePathWith("root", "name", inside); !ok || got != "inside" {
		t.Fatalf("inside path = (%q, %t)", got, ok)
	}
}
