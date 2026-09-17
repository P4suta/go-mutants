// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestTestdataOwnerNamesThePackageADirectoryBelongsTo(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		owner string
		found bool
	}{
		{name: "testdata/fuzz/FuzzOne/seed", owner: ".", found: true},
		{name: "testdata/sample.txt", owner: ".", found: true},
		{name: "pkg/testdata/fuzz/FuzzOne/seed", owner: "pkg", found: true},
		{name: "a/b/testdata/sample.txt", owner: "a/b", found: true},
		{name: "testdata/"},
		{name: "testdata"},
		{name: "pkg/sample.txt"},
		{name: "/testdata/sample.txt", owner: "", found: false},
		{name: ""},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			owner, found := testdataOwner(test.name)
			if found != test.found || owner != test.owner {
				t.Fatalf("testdataOwner(%q) = (%q, %t), want (%q, %t)",
					test.name, owner, found, test.owner, test.found)
			}
		})
	}
}

func TestCorpusOwnerNamesThePackageAndTheTargetASeedBelongsTo(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		owner  string
		target string
		found  bool
	}{
		{name: "testdata/fuzz/FuzzOne/seed", owner: ".", target: "FuzzOne", found: true},
		{name: "pkg/testdata/fuzz/FuzzOne/seed", owner: "pkg", target: "FuzzOne", found: true},
		{name: "a/b/testdata/fuzz/FuzzOne/nested/seed", owner: "a/b", target: "FuzzOne", found: true},
		{name: "pkg/testdata/sample.txt"},
		{name: "testdata/sample.txt"},
		{name: "pkg/testdata/fuzz/FuzzOne"},
		{name: "pkg/testdata/fuzz//seed"},
		{name: "pkg/sample.txt"},
		{name: ""},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			owner, target, found := corpusOwner(test.name)
			if found != test.found || owner != test.owner || target != test.target {
				t.Fatalf("corpusOwner(%q) = (%q, %q, %t), want (%q, %q, %t)",
					test.name, owner, target, found, test.owner, test.target, test.found)
			}
		})
	}
}

func TestTargetBehaviorEnvironmentKeepsTheLastValueOfEachName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		base    []string
		overlay []string
		want    []string
	}{
		{name: "nothing at all", want: []string{}},
		{name: "one name", base: []string{"PATH=/bin"}, want: []string{"PATH=/bin"}},
		{
			name: "a name the overlay replaces",
			base: []string{"PATH=/bin"}, overlay: []string{"PATH=/usr/bin"}, want: []string{"PATH=/usr/bin"},
		},
		{
			name: "an entry with no equals sign",
			base: []string{"PATH=/bin", "NOTANENTRY"}, want: []string{"PATH=/bin"},
		},
		{
			name: "an entry with no name",
			base: []string{"PATH=/bin", "=value"}, want: []string{"PATH=/bin"},
		},
		{
			name: "two names, answered in order",
			base: []string{"ZONE=utc", "ALPHA=one"}, want: []string{"ALPHA=one", "ZONE=utc"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := targetBehaviorEnvironment(test.base, test.overlay); !slices.Equal(got, test.want) {
				t.Fatalf("targetBehaviorEnvironment = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTargetBehaviorEnvironmentFoldsNamesOnlyWhereTheSystemDoes(t *testing.T) {
	t.Parallel()
	got := targetBehaviorEnvironment([]string{"Path=/bin"}, []string{"PATH=/usr/bin"})
	if runtime.GOOS == "windows" {
		if !slices.Equal(got, []string{"PATH=/usr/bin"}) {
			t.Fatalf("targetBehaviorEnvironment = %q, want one name on a system that folds them", got)
		}
		return
	}
	if !slices.Equal(got, []string{"PATH=/usr/bin", "Path=/bin"}) {
		t.Fatalf("targetBehaviorEnvironment = %q, want both names on a system that tells them apart", got)
	}
}

func TestARepositoryRelativePathStaysInsideTheRootItIsGiven(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "repository")
	for _, test := range []struct {
		name   string
		root   string
		path   string
		want   string
		inside bool
	}{
		{
			name: "a file inside", root: root, path: filepath.Join(root, "pkg", "value.go"),
			want: "pkg/value.go", inside: true,
		},
		{name: "the root itself", root: root, path: root, want: ".", inside: true},
		{name: "the directory above", root: root, path: filepath.Dir(root)},
		{
			name: "a file beside the root", root: root,
			path: filepath.Join(filepath.Dir(root), "elsewhere", "value.go"),
		},
		{name: "no root at all", path: filepath.Join(root, "value.go")},
		{name: "no path at all", root: root},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			relative, inside := repositoryRelativePath(test.root, test.path)
			if inside != test.inside || relative != test.want {
				t.Fatalf("repositoryRelativePath(%q, %q) = (%q, %t), want (%q, %t)",
					test.root, test.path, relative, inside, test.want, test.inside)
			}
		})
	}
}

func TestARepositoryTestLogPathIsTheOneTheArgumentsName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		arguments []string
		want      string
		found     bool
	}{
		{name: "no argument at all"},
		{name: "another argument", arguments: []string{"-test.v"}},
		{
			name: "the log file", arguments: []string{"-test.testlogfile=/tmp/log"},
			want: "/tmp/log", found: true,
		},
		{
			name:      "the log file among others",
			arguments: []string{"-test.v", "-test.testlogfile=/tmp/log", "-test.count=1"},
			want:      "/tmp/log", found: true,
		},
		{name: "a log file that names nothing", arguments: []string{"-test.testlogfile="}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, found := repositoryTestLogPath(test.arguments)
			if found != test.found || path != test.want {
				t.Fatalf("repositoryTestLogPath(%q) = (%q, %t), want (%q, %t)",
					test.arguments, path, found, test.want, test.found)
			}
		})
	}
}

func TestARepositoryTestLogFailureIsOneTheOutputNamesTheLogIn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		arguments []string
		output    string
		want      bool
	}{
		{name: "no log file at all", output: "testing: /tmp/log: no such file"},
		{
			name: "output that names the log", arguments: []string{"-test.testlogfile=/tmp/log"},
			output: "testing: /tmp/log: no such file", want: true,
		},
		{
			name:      "output that names the log with the path escaped",
			arguments: []string{`-test.testlogfile=C:\tmp\log`},
			output:    `testing: C:\\tmp\\log: no such file`, want: true,
		},
		{
			name: "output about something else", arguments: []string{"-test.testlogfile=/tmp/log"},
			output: "testing: another problem",
		},
		{
			name: "output that is not the testing package", arguments: []string{"-test.testlogfile=/tmp/log"},
			output: "panic: /tmp/log",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := repositoryTestLogFailure(test.output, test.arguments); got != test.want {
				t.Fatalf("repositoryTestLogFailure = %t, want %t", got, test.want)
			}
		})
	}
}

func TestParsingARepositoryTestLogReadsEveryOperationItKnows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "value.go"),
		[]byte("package pkg\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	log := func(lines ...string) []byte {
		return append([]byte("# test log\n"), []byte(strings.Join(lines, "\n")+"\n")...)
	}

	for _, test := range []struct {
		name      string
		data      []byte
		paths     []string
		ambiguous bool
	}{
		{
			name:  "a file it opened",
			data:  log("open " + filepath.Join(root, "pkg", "value.go")),
			paths: []string{"pkg/value.go"},
		},
		{
			name:  "a file it asked about",
			data:  log("stat " + filepath.Join(root, "pkg", "value.go")),
			paths: []string{"pkg/value.go"},
		},
		{
			name:  "a directory it asked about",
			data:  log("stat " + filepath.Join(root, "pkg")),
			paths: []string{"pkg"},
		},
		{
			name:  "a file that is not there",
			data:  log("open " + filepath.Join(root, "pkg", "absent.go")),
			paths: []string{"pkg/absent.go"},
		},
		{
			name: "a file outside the repository",
			data: log("open " + filepath.Join(filepath.Dir(root), "elsewhere")),
		},
		{
			name:  "a directory it moved into",
			data:  log("chdir " + filepath.Join(root, "pkg")),
			paths: []string{"pkg"},
		},
		{
			name:  "a relative path after it moved",
			data:  log("chdir "+filepath.Join(root, "pkg"), "open value.go"),
			paths: []string{"pkg", "pkg/value.go"},
		},
		{name: "an environment variable it read", data: log("getenv HOME")},
		{name: "a relative directory it moved into", data: log("chdir pkg"), ambiguous: true},
		{name: "an operation nobody knows", data: log("mmap /tmp/one"), ambiguous: true},
		{name: "a line with no name", data: log("open"), ambiguous: true},
		{name: "a line with an empty name", data: log("open "), ambiguous: true},
		{name: "a log with no magic", data: []byte("open /tmp/one\n"), ambiguous: true},
		{name: "a log with no final newline", data: []byte("# test log\nopen /tmp/one"), ambiguous: true},
		{name: "a log of nothing at all", data: nil, ambiguous: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observation := parseRepositoryTestLog(test.data, root, root)
			if (observation.reason == wholeTreeLogAmbiguous) != test.ambiguous {
				t.Fatalf("the observation says %q, want ambiguous: %t", observation.reason, test.ambiguous)
			}
			got := make([]string, 0, len(observation.accesses))
			for _, access := range observation.accesses {
				got = append(got, access.path)
			}
			if !slices.Equal(got, test.paths) {
				t.Fatalf("the observation names %q, want %q", got, test.paths)
			}
		})
	}
}
