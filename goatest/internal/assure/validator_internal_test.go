// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestValidateCopyRootsRejectsDestinationInsideSource(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(source, "scratch", "candidate")
	if err := os.MkdirAll(destination, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := validateCopyRoots(source, destination); err == nil {
		t.Fatal("candidate clone destination inside repository was accepted")
	}
}

func TestValidateCopyRootsAcceptsSeparateTrees(t *testing.T) {
	if err := validateCopyRoots(t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestAValidatorAsksForThePackagesItWasGivenOrEverythingUnderTheRoot(t *testing.T) {
	t.Parallel()
	named := NewRepositoryValidator(RepositoryValidatorOptions{Packages: []string{"./one", "./two"}})
	if got := named.packages(); !slices.Equal(got, []string{"./one", "./two"}) {
		t.Fatalf("a validator that was given packages asks for %q, want the ones it was given", got)
	}
	named.options.Packages[0] = "./changed"
	if got := named.packages(); got[0] != "./changed" {
		t.Fatalf("a validator answered %q after its own options changed", got)
	}
	silent := NewRepositoryValidator(RepositoryValidatorOptions{})
	if got := silent.packages(); !slices.Equal(got, []string{"./..."}) {
		t.Fatalf("a validator that was given none asks for %q, want everything under the root", got)
	}
}

func TestAValidatorTimesOutAtWhatItWasGivenOrItsOwnDefault(t *testing.T) {
	t.Parallel()
	given := NewRepositoryValidator(RepositoryValidatorOptions{Timeout: time.Minute})
	if got := given.timeout(); got != time.Minute {
		t.Errorf("a validator waits %s, want the minute it was given", got)
	}
	if got := given.mutantTimeout(); got != time.Minute {
		t.Errorf("a mutant waits %s, want the minute the validator was given", got)
	}
	for _, timeout := range []time.Duration{0, -time.Minute} {
		none := NewRepositoryValidator(RepositoryValidatorOptions{Timeout: timeout})
		if got := none.timeout(); got != defaultValidationTimeout {
			t.Errorf("a validator given %s waits %s, want %s", timeout, got, defaultValidationTimeout)
		}
		if got := none.mutantTimeout(); got != defaultMutantValidationTimeout {
			t.Errorf("a mutant given %s waits %s, want %s", timeout, got, defaultMutantValidationTimeout)
		}
	}
	if defaultValidationTimeout == defaultMutantValidationTimeout {
		t.Fatal("a whole validation and one mutant wait the same default; the two are not the same question")
	}
}

func TestAValidatorsTestCommandCarriesOnlyWhatItWasGiven(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		options   RepositoryValidatorOptions
		tags      bool
		arguments bool
	}{
		{name: "a validator given neither"},
		{
			name:    "a validator given build tags",
			options: RepositoryValidatorOptions{BuildTags: []string{"integration", "slow"}}, tags: true,
		},
		{
			name:    "a validator given test arguments",
			options: RepositoryValidatorOptions{TestArgs: []string{"-short"}}, arguments: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			validator := NewRepositoryValidator(test.options)
			for _, compileOnly := range []bool{false, true} {
				argv := validator.testArgv(compileOnly)
				if carried := slices.Contains(argv, "-tags=integration,slow"); carried != test.tags {
					t.Errorf("%q carries build tags=%t, want %t", argv, carried, test.tags)
				}
				if carried := slices.Contains(argv, "-args"); carried != test.arguments {
					t.Errorf("%q carries test arguments=%t, want %t", argv, carried, test.arguments)
				}
				if compiled := slices.Contains(argv, "-run=^$"); compiled != compileOnly {
					t.Errorf("%q compiles only=%t, want %t", argv, compiled, compileOnly)
				}
				if counted := slices.Contains(argv, "-count=1"); counted == compileOnly {
					t.Errorf("%q counts once=%t, want %t", argv, counted, !compileOnly)
				}
			}
		})
	}
}

func TestAValidatorsListCommandCarriesBuildTagsOnlyWhenGiven(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		options RepositoryValidatorOptions
		want    []string
	}{
		{name: "no tags", want: []string{"go", "list", "-json", "./..."}},
		{
			name: "tags and packages",
			options: RepositoryValidatorOptions{
				BuildTags: []string{"integration", "slow"}, Packages: []string{"./one", "./two"},
			},
			want: []string{"go", "list", "-json", "-tags=integration,slow", "./one", "./two"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := NewRepositoryValidator(test.options).listArgv(); !slices.Equal(got, test.want) {
				t.Fatalf("list argv = %q, want %q", got, test.want)
			}
		})
	}
}
