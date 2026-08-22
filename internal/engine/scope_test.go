// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
)

// TestTestScopeReadsGoTestOverPatternsAndNothingElse pins the rule two
// optimisations switch on.
//
// It is not a preference and there is nothing to configure. Recognising a
// command buys the run two things it cannot take back — the test binaries are
// built for the named packages only, and coverage-guided selection is on — and
// both rest on go-mutants being able to state in full what the command does. So
// the table is mostly refusals: every row that is not `go`, then `test`, then
// package patterns is a command whose meaning go-mutants has not been taught,
// and the safe answer for one of those is the slow one.
func TestTestScopeReadsGoTestOverPatternsAndNothingElse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command []string
		want    []string
	}{
		{name: "the built-in default", command: config.DefaultTestCommand(), want: []string{"./..."}},
		{
			// The same argv written out by hand, which is what a project that
			// pins `test.command` in its configuration file most often has.
			name:    "the default spelled out",
			command: []string{"go", "test", "./..."},
			want:    []string{"./..."},
		},
		{
			name:    "one narrowing pattern",
			command: []string{"go", "test", "./internal/..."},
			want:    []string{"./internal/..."},
		},
		{
			name:    "several patterns",
			command: []string{"go", "test", "./internal/mutation/...", "./internal/glob/..."},
			want:    []string{"./internal/mutation/...", "./internal/glob/..."},
		},
		{
			name:    "a single package with no wildcard",
			command: []string{"go", "test", "./internal/glob"},
			want:    []string{"./internal/glob"},
		},
		{name: "the current directory", command: []string{"go", "test", "."}, want: []string{"."}},
		{
			// The `..` rows below are refused by reading path elements, not by
			// looking for two dots anywhere in the string: the wildcard is three
			// dots, and a directory may legitimately be named with a leading one.
			// This row and every `./...` above are what hold the two apart.
			name:    "a wildcard under a dot directory",
			command: []string{"go", "test", "./.config/..."},
			want:    []string{"./.config/..."},
		},

		// Every refusal below is one shape, and each is here because it is a
		// shape somebody's `test.command` really has.
		{name: "one extra flag", command: []string{"go", "test", "-count=1", "./..."}},
		{name: "a flag after the patterns", command: []string{"go", "test", "./...", "-race"}},
		{
			// The dangerous one, and the reason this reader has no shortlist of
			// harmless flags: `-run` makes the command a fraction of the suite,
			// and coverage attributed to the whole of it would skip mutants a
			// test does cover.
			name:    "a run filter",
			command: []string{"go", "test", "-run", "TestFast", "./..."},
		},
		{name: "a build tag", command: []string{"go", "test", "-tags", "integration", "./..."}},
		{name: "another program", command: []string{"gotestsum", "--", "./..."}},
		{name: "a shell script", command: []string{"./scripts/test.sh"}},
		{name: "a wrapper that ends in go test", command: []string{"mise", "exec", "--", "go", "test", "./..."}},
		{name: "a subcommand that is not test", command: []string{"go", "run", "./cmd/tests"}},
		{
			// An import path is a package pattern to the go command and is
			// deliberately not one here: `go list` would resolve it through the
			// module cache rather than from the snapshot, so the binaries built
			// need not be the tree being measured.
			name:    "a bare import path",
			command: []string{"go", "test", "github.com/P4suta/go-mutants/internal/glob"},
		},
		{name: "a pattern that climbs out of the module", command: []string{"go", "test", "../sibling/..."}},
		{
			// The spelling the missing-`./` rule above does *not* catch, and the
			// one this refusal exists for: `./../sibling/...` is rooted in `./`
			// and still resolves somewhere else entirely.
			name:    "a climb wearing the `./` prefix",
			command: []string{"go", "test", "./../sibling/..."},
		},
		{
			// Refused even though the go command would resolve it to a package in
			// the workspace. The run resolves patterns against the snapshot, which
			// is a temporary copy under a name of its own, so a pattern that climbs
			// out and back in by the module's directory name finds nothing there —
			// and sorting that from a true escape would be a second rule to get
			// wrong.
			name:    "a climb that lands back inside the module",
			command: []string{"go", "test", "./../project/internal/..."},
		},
		{name: "a climb in the middle of a pattern", command: []string{"go", "test", "./internal/../cmd/..."}},
		{name: "the parent directory itself", command: []string{"go", "test", "./.."}},
		{
			// A climb spelled with the Windows separator, which `.\internal\...`
			// below is refused for lacking and this one is not.
			name:    "a climb with the Windows separator",
			command: []string{"go", "test", `./..\sibling`},
		},
		{name: "the Windows spelling", command: []string{"go", "test", `.\internal\...`}},
		{name: "an absolute path", command: []string{"go", "test", "/src/project/..."}},
		{name: "an empty argument", command: []string{"go", "test", "./...", ""}},
		{name: "go test with no patterns", command: []string{"go", "test"}},
		{name: "go alone", command: []string{"go"}},
		{name: "nothing at all", command: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			patterns, ok := testScope(test.command)
			if want := test.want != nil; ok != want {
				t.Fatalf("testScope(%q) recognised = %t, want %t", test.command, ok, want)
			}
			if !slices.Equal(patterns, test.want) {
				t.Errorf("testScope(%q) = %q, want %q", test.command, patterns, test.want)
			}
		})
	}
}

// TestNarrowedIsFalseForAScopeThatHoldsTheWholeModule pins the distinction the
// empty-scope refusal rests on.
//
// `./...` is every package there is, so a scope containing it is not a claim
// about which suites matter — and a module with no test files at all is a fact
// about the project that the score already states, not a mistake in a command.
func TestNarrowedIsFalseForAScopeThatHoldsTheWholeModule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patterns []string
		want     bool
	}{
		{name: "the whole module", patterns: []string{"./..."}},
		{name: "the whole module beside a narrower one", patterns: []string{"./internal/...", "./..."}},
		{name: "an unrecognised command, which has no patterns", patterns: nil},
		{name: "one package tree", patterns: []string{"./internal/..."}, want: true},
		{name: "one package", patterns: []string{"."}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := narrowed(test.patterns); got != test.want {
				t.Errorf("narrowed(%q) = %t, want %t", test.patterns, got, test.want)
			}
		})
	}
}

// TestResolvedPackagesCountsOnlyMarkedRowsWithADirectory covers the two things
// a `go list` capture can hold that are not a package.
//
// internal/runner merges stdout and stderr, so a "matched no packages" warning
// arrives in the same bytes as the rows; and `go list -e` answers a pattern that
// names no directory with a record whose Dir is empty. Counting either as a
// package would make the scope check pass for exactly the mistakes it exists to
// catch.
func TestResolvedPackagesCountsOnlyMarkedRowsWithADirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   int
	}{
		{name: "nothing at all"},
		{
			name:   "two packages",
			output: scopeMarker + "/snap/a\n" + scopeMarker + "/snap/b\n",
			want:   2,
		},
		{
			name:   "a warning about a directory with no Go files",
			output: "go: warning: \"./docs/...\" matched no packages\n",
		},
		{
			// `go list -e ./nope/...` prints a synthetic record for a pattern
			// that names no directory: the marker is there and the directory is
			// not.
			name:   "a pattern the go command invented a record for",
			output: scopeMarker + "\n",
		},
		{
			name:   "a real package beside a warning",
			output: "go: warning: \"./docs/...\" matched no packages\n" + scopeMarker + "/snap/a\n",
			want:   1,
		},
		{
			// A Windows child writes CRLF, and a row whose directory survived
			// only as "\r" would be a scope check that passed on whitespace.
			name:   "carriage returns",
			output: scopeMarker + "/snap/a\r\n" + scopeMarker + "\r\n",
			want:   1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := resolvedPackages([]byte(test.output)); got != test.want {
				t.Errorf("resolvedPackages(%q) = %d, want %d", test.output, got, test.want)
			}
		})
	}
}

// TestScopedBinariesRefusesOnlyANarrowedScopeThatBuiltNothing pins the
// asymmetry, which is the whole of the rule.
func TestScopedBinariesRefusesOnlyANarrowedScopeThatBuiltNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patterns []string
		built    int
		wantErr  bool
	}{
		{name: "a narrowed scope with binaries", patterns: []string{"./internal/..."}, built: 3},
		{name: "a narrowed scope with none", patterns: []string{"./internal/..."}, built: 0, wantErr: true},
		{
			// A module with no test files anywhere is a fact about the project,
			// and the score, the survivor list and `policy.require_mutants` all
			// say so already.
			name:     "the whole module with none",
			patterns: []string{"./..."},
			built:    0,
		},
		{name: "an unrecognised command with none", patterns: nil, built: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := scopedBinaries(test.patterns, test.built)
			if (err != nil) != test.wantErr {
				t.Fatalf("scopedBinaries(%q, %d) = %v, want an error: %t",
					test.patterns, test.built, err, test.wantErr)
			}
			if err == nil {
				return
			}
			if code := CodeOf(err); code != CodeTestScope {
				t.Errorf("code = %s, want %s", code, CodeTestScope)
			}
			if !strings.Contains(err.Error(), "./internal/...") {
				t.Errorf("the refusal does not name the scope: %v", err)
			}
		})
	}
}

// TestCustomTestCommandWarningNamesBothCommands is what makes the recognition
// rule diagnosable.
//
// A user who has just set `test.command` and noticed the run got slower needs
// three things in one line: which command they wrote, which shape would have
// been understood, and what the run is doing instead.
func TestCustomTestCommandWarningNamesBothCommands(t *testing.T) {
	t.Parallel()

	message := customTestCommand([]string{"go", "test", "-count=1", "./..."})
	for _, needle := range []string{
		`"go test -count=1 ./..."`,
		`"go test ./..."`,
		"every mutant will be measured against every one of them",
	} {
		if !strings.Contains(message, needle) {
			t.Errorf("the warning does not mention %q:\n%s", needle, message)
		}
	}
	if strings.ContainsAny(message, "\n\r") {
		t.Errorf("the warning is not one line: %q", message)
	}
}
