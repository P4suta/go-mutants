// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
)

const aCompileAndARun = 2

func TestASnapshotExcludesWhatARunItselfWrites(t *testing.T) {
	t.Parallel()
	excluded := assuranceSnapshotExclusions()
	for _, want := range []string{reportOutputDirectory, distributionOutputDirectory} {
		if !slices.Contains(excluded, want) {
			t.Errorf("a snapshot excludes %q, want it to leave out %q", excluded, want)
		}
	}
	if slices.Contains(excluded, internalOutputDirectory) {
		t.Errorf("a snapshot excludes %q; the run reads its own %q", excluded, internalOutputDirectory)
	}
}

func TestATargetReachesConcurrencyThroughItselfOrADependency(t *testing.T) {
	t.Parallel()
	concurrent := map[string]struct{}{"fixture/worker": {}}
	for _, test := range []struct {
		name    string
		target  goanalysis.Target
		reaches bool
	}{
		{name: "a package that is concurrent itself",
			target: goanalysis.Target{Package: "fixture/worker"}, reaches: true},
		{name: "a package that depends on a concurrent one",
			target:  goanalysis.Target{Package: "fixture/app", Dependencies: []string{"fixture/worker"}},
			reaches: true},
		{name: "a package whose dependencies are not concurrent",
			target: goanalysis.Target{Package: "fixture/app", Dependencies: []string{"fixture/plain"}}},
		{name: "a package with no dependency at all",
			target: goanalysis.Target{Package: "fixture/plain"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := reachesConcurrency(goanalysis.Model{}, concurrent, TargetEvidence{Target: test.target})
			if got != test.reaches {
				t.Fatalf("%s reaches concurrency=%t, want %t", test.name, got, test.reaches)
			}
		})
	}
}

func TestARaceRunCarriesBuildTagsAndTestArgumentsOnlyWhereThereAreSome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		options   RaceOptions
		tags      bool
		arguments bool
	}{
		{name: "a run that names neither", options: RaceOptions{PersistCompile: true}},
		{
			name:    "a run that names build tags",
			options: RaceOptions{PersistCompile: true, BuildTags: []string{"integration"}}, tags: true,
		},
		{
			name:      "a run that names test arguments",
			options:   RaceOptions{PersistCompile: true, TestArgs: []string{"-short"}},
			arguments: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			workspace := &baselineFakeWorkspace{
				exec: func(gomutants.Command) (gomutants.CommandResult, error) {
					return gomutants.CommandResult{}, nil
				},
			}
			if _, err := CollectRaceWithOptions(context.Background(), workspace, goanalysis.Model{},
				[]string{"fixture/app"}, "standard-v1", test.options); err != nil {
				t.Fatal(err)
			}
			if len(workspace.commands) != aCompileAndARun {
				t.Fatalf("a race run started %d commands, want a compile and a run", len(workspace.commands))
			}
			for _, command := range workspace.commands {
				carried := slices.ContainsFunc(command.Argv, func(argument string) bool {
					return strings.HasPrefix(argument, "-tags=")
				})
				if carried != test.tags {
					t.Errorf("%q carries build tags=%t, want %t", command.Argv, carried, test.tags)
				}
			}
			compile, run := workspace.commands[0], workspace.commands[1]
			if slices.Contains(compile.Argv, "-args") {
				t.Errorf("the compile %q carries test arguments, which belong to a test binary", compile.Argv)
			}
			if carried := slices.Contains(run.Argv, "-args"); carried != test.arguments {
				t.Errorf("the race run %q carries test arguments=%t, want %t", run.Argv, carried, test.arguments)
			}
		})
	}
}

func TestARunScratchThatIsUnavailableAnswersNoPlaceToWrite(t *testing.T) {
	t.Parallel()
	var scratch runScratch
	if directory, name, err := scratch.subdirectory("probe"); err == nil || directory != "" || name != "" {
		t.Fatalf("a scratch with no directory answered (%q, %q, %v), want nothing", directory, name, err)
	}
	if directory, err := scratch.buildCacheLayer(); err == nil || directory != "" {
		t.Fatalf("a scratch with no directory made %q, %v, want nothing", directory, err)
	}
	standing := runScratch{dir: t.TempDir()}
	directory, name, err := standing.subdirectory("probe")
	if err != nil || directory != standing.dir || name != "probe" {
		t.Fatalf("a scratch answered (%q, %q, %v), want its own directory", directory, name, err)
	}
	layer, err := standing.buildCacheLayer()
	if err != nil {
		t.Fatalf("a scratch could not make its build cache layer: %v", err)
	}
	if _, statErr := os.Stat(layer); statErr != nil {
		t.Errorf("the layer it says it made is not there: %v", statErr)
	}
	if _, again := standing.buildCacheLayer(); again == nil {
		t.Error("a scratch made its build cache layer twice; the second must refuse")
	}
}

func TestAPackageDirectoryIsTheOneTheFileSitsIn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "a file under a directory", path: "internal/assure/run.go", want: "internal/assure"},
		{name: "a file at the root", path: "run.go", want: "."},
		{name: "a name that is nothing at all", want: "."},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := packageDirectoryOfPath(test.path); got != test.want {
				t.Fatalf("%s answered %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func TestAMutationRecordSaysWhetherItKeyedTheWholeTree(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		reason wholeTreeReason
		whole  bool
	}{
		{name: "a mutant the log observed", reason: wholeTreeObserved},
		{name: "a mutant nothing could observe", reason: wholeTreeStaticUnobservable, whole: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := mutantExecutionRecord(
				gomutants.ExecRequest{Mutant: "m-1"}, gomutants.MutantResult{}, test.reason, nil)
			if record.WholeTree != test.whole {
				t.Fatalf("%s reads whole-tree=%t, want %t", test.name, record.WholeTree, test.whole)
			}
			if record.WholeTreeReason != string(test.reason) {
				t.Errorf("%s reads the reason %q, want %q", test.name, record.WholeTreeReason, test.reason)
			}
			if record.Error != "" {
				t.Errorf("%s carries the error %q, want none", test.name, record.Error)
			}
		})
	}
	failed := mutantExecutionRecord(gomutants.ExecRequest{Mutant: "m-1"}, gomutants.MutantResult{},
		wholeTreeObserved, errors.New("the mutant could not be run"))
	if failed.Error != "the mutant could not be run" {
		t.Errorf("a mutant that failed carries %q, want the failure", failed.Error)
	}
}

func TestATraceArgumentListLeavesOutTheTestLogFileAndNothingElse(t *testing.T) {
	t.Parallel()
	arguments := []string{"-test.run=^TestOne$", "-test.testlogfile=/tmp/log", "-test.v", "-test.timeout=1m"}
	kept := mutationTraceArguments(arguments)
	want := []string{"-test.run=^TestOne$", "-test.v", "-test.timeout=1m"}
	if !slices.Equal(kept, want) {
		t.Fatalf("the trace records %q, want %q", kept, want)
	}
	if !slices.Equal(arguments[:1], []string{"-test.run=^TestOne$"}) {
		t.Errorf("the arguments it was given were changed: %q", arguments)
	}
}
