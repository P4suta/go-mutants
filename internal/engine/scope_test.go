// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
)

func TestTestScopeReadsGoTestOverPatternsAndNothingElse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command []string
		want    []string
	}{
		{name: "the built-in default", command: config.DefaultTestCommand(), want: []string{"./..."}},
		{
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
			name:    "a wildcard under a dot directory",
			command: []string{"go", "test", "./.config/..."},
			want:    []string{"./.config/..."},
		},

		{name: "one extra flag", command: []string{"go", "test", "-count=1", "./..."}},
		{name: "a flag after the patterns", command: []string{"go", "test", "./...", "-race"}},
		{
			name:    "a run filter",
			command: []string{"go", "test", "-run", "TestFast", "./..."},
		},
		{name: "a build tag", command: []string{"go", "test", "-tags", "integration", "./..."}},
		{name: "another program", command: []string{"gotestsum", "--", "./..."}},
		{name: "a shell script", command: []string{"./scripts/test.sh"}},
		{name: "a wrapper that ends in go test", command: []string{"mise", "exec", "--", "go", "test", "./..."}},
		{name: "a subcommand that is not test", command: []string{"go", "run", "./cmd/tests"}},
		{
			name:    "a bare import path",
			command: []string{"go", "test", "github.com/P4suta/go-mutants/internal/glob"},
		},
		{name: "a pattern that climbs out of the module", command: []string{"go", "test", "../sibling/..."}},
		{
			name:    "a climb wearing the `./` prefix",
			command: []string{"go", "test", "./../sibling/..."},
		},
		{
			name:    "a climb that lands back inside the module",
			command: []string{"go", "test", "./../project/internal/..."},
		},
		{name: "a climb in the middle of a pattern", command: []string{"go", "test", "./internal/../cmd/..."}},
		{name: "the parent directory itself", command: []string{"go", "test", "./.."}},
		{
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
			name:   "a pattern the go command invented a record for",
			output: scopeMarker + "\n",
		},
		{
			name:   "a real package beside a warning",
			output: "go: warning: \"./docs/...\" matched no packages\n" + scopeMarker + "/snap/a\n",
			want:   1,
		},
		{
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
