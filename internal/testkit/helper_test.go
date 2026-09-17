// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	selfHelperEnv = "TESTKIT_SELF_HELPER"
	argvTargetEnv = "TESTKIT_ARGV_TARGET"
)

const helperMisuseStatus = 97

const (
	coverRootMarker   = "cover-root="
	argvTargetExitEnv = "TESTKIT_ARGV_TARGET_EXIT"
)

const (
	gocoverdirProbeEnv  = "TESTKIT_GOCOVERDIR_FLAG_PROBE"
	gocoverdirProbeTest = "TestHelperUnderTheGocoverdirFlagProcess"
	gocoverdirProbeText = "exactly these bytes on standard error"
)

func TestMain(m *testing.M) {
	os.Exit(guardTheDefaultKeptRoot(func() int {
		return Helper(m, selfHelperEnv, selfHelperProgram)
	}))
}

func guardTheDefaultKeptRoot(run func() int) int {
	if pinned.userCache == "" {
		return run()
	}
	root := filepath.Join(pinned.userCache, harnessDirName, keptDirName)
	if _, err := os.Lstat(root); err == nil {
		return run()
	}
	allowed := KeepPolicy() != KeepNever && os.Getenv(KeepDirEnv) == ""
	code := run()
	if _, err := os.Lstat(root); err == nil && !allowed {
		fmt.Fprintf(os.Stderr, "\nthis suite created %s, which is the developer's own kept root and "+
			"the one CI does not upload: a test that wants the keep policy on has to point %s at a "+
			"directory of its own\n", root, KeepDirEnv)
		if code == 0 {
			return 1
		}
	}
	return code
}

func selfHelperProgram(args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "helper: bad invocation %q\n", args)
		return helperMisuseStatus
	}
	switch verb, rest := args[0], args[1:]; verb {
	case "print":
		_, _ = fmt.Fprint(os.Stdout, rest[0])
		return 0
	case "printerr":
		_, _ = fmt.Fprint(os.Stderr, rest[0])
		return 0
	case "env":
		_, _ = fmt.Fprint(os.Stdout, os.Getenv(rest[0]))
		return 0
	case "exit":
		code, err := strconv.Atoi(rest[0])
		if err != nil {
			return helperMisuseStatus
		}
		return code
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown verb %q\n", verb)
		return helperMisuseStatus
	}
}

func TestHelperArgvTargetProcess(t *testing.T) {
	SkipUnlessHelper(t, argvTargetEnv)
	t.Log("the named test ran")
	_, _ = fmt.Fprintln(os.Stdout, "target-process-marker")
}

func TestHelperArgvNeighbourProcess(t *testing.T) {
	SkipUnlessHelper(t, argvTargetEnv)
	_, _ = fmt.Fprintln(os.Stdout, "neighbour-process-marker")
}

func TestHelperCoverRootTargetProcess(t *testing.T) {
	SkipUnlessHelper(t, argvTargetEnv)
	_, _ = fmt.Fprintln(os.Stdout, coverRootMarker+HelperCoverRoot())
	status := os.Getenv(argvTargetExitEnv)
	if status == "" {
		return
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		t.Fatalf("%s = %q, which is not a status", argvTargetExitEnv, status)
	}
	os.Exit(code)
}

func TestHelperUnderTheGocoverdirFlagProcess(t *testing.T) {
	SkipUnlessHelper(t, gocoverdirProbeEnv)
	_, _ = fmt.Fprintln(os.Stdout, coverRootMarker+HelperCoverRoot())

	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1")
	result := Exec(t, t.TempDir(), env, TestBinary(), "printerr", gocoverdirProbeText)

	RequireExit(t, result, 0, "the helper program")
	if got := string(result.Output); got != gocoverdirProbeText {
		t.Errorf("the helper's whole output is %q, want exactly %q: an instrumented helper with "+
			"nowhere private to write warns on the stream its caller is asserting the bytes of",
			got, gocoverdirProbeText)
	}
}

func TestHelperArgvReexecutesOnlyTheNamedTest(t *testing.T) {
	t.Parallel()

	argv := HelperArgv("TestHelperArgvTargetProcess")
	want := []string{TestBinary(), "-test.run=^TestHelperArgvTargetProcess$"}
	if !slices.Equal(argv, want) {
		t.Fatalf("HelperArgv = %q, want %q", argv, want)
	}
	if !filepath.IsAbs(argv[0]) {
		t.Errorf("HelperArgv starts with %q, which a child that runs in another directory cannot resolve", argv[0])
	}

	env := withEntries(Compose(t, t.TempDir()), argvTargetEnv+"=1")
	result := Exec(t, t.TempDir(), env, argv...)

	RequireExit(t, result, 0, "the re-executed test")
	RequireOutput(t, result, "the re-executed test", "target-process-marker")
	RequireNoOutput(t, result, "the re-executed test", "neighbour-process-marker")
}

func TestHelperRunsTheProgramWithByteExactStdout(t *testing.T) {
	t.Parallel()

	const payload = "exactly these bytes and no newline"
	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1")
	result := Exec(t, t.TempDir(), env, os.Args[0], "print", payload)

	RequireExit(t, result, 0, "the helper program")
	if got := string(result.Stdout); got != payload {
		t.Errorf("the helper's stdout is %q, want exactly %q", got, payload)
	}
}

func TestHelperForwardsTheProgramsExitStatus(t *testing.T) {
	t.Parallel()

	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1")
	result := Exec(t, t.TempDir(), env, os.Args[0], "exit", "3")

	if result.Err != nil {
		t.Fatalf("the helper could not be run: %v\n%s", result.Err, result.Output)
	}
	if result.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want the status the program returned:\n%s", result.ExitCode, result.Output)
	}
}

func TestHelperIsolatesCoverageOutputPerProcess(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	shared := t.TempDir()
	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
		HelperCoverRootEnv+"="+root, CoverDirEnv+"="+shared)
	var seen []string
	for range 2 {
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)
		RequireExit(t, result, 0, "the helper program")
		seen = append(seen, string(result.Stdout))
	}

	for _, dir := range seen {
		if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			t.Errorf("a helper's %s is %q, want a directory under the private root %s", CoverDirEnv, dir, root)
		}
		if _, err := strconv.Atoi(filepath.Base(dir)); err != nil {
			t.Errorf("a helper's %s is %q, want one named by the process id", CoverDirEnv, dir)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("the helper's coverage directory was not created: %v", err)
		}
		if dir == shared || strings.HasPrefix(dir, shared+string(filepath.Separator)) {
			t.Errorf("a helper's %s is %q, inside the directory the parent's own profile is collected in", CoverDirEnv, dir)
		}
	}
	if seen[0] == seen[1] {
		t.Errorf("two helper processes shared %s = %q, which is the collision this exists to prevent",
			CoverDirEnv, seen[0])
	}
}

func TestHelperCoverageRedirectionTurnsOnTheRoot(t *testing.T) {
	t.Parallel()

	t.Run("no coverage anywhere runs", func(t *testing.T) {
		t.Parallel()

		env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
			HelperCoverRootEnv+"=", CoverDirEnv+"=")
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)

		RequireExit(t, result, 0, "the helper program")
		if got := string(result.Stdout); got != "" {
			t.Errorf("a helper in a run with no coverage set %s = %q, so it made a directory nothing "+
				"will ever read", CoverDirEnv, got)
		}
	})

	t.Run("a coverage run with no root refuses", func(t *testing.T) {
		t.Parallel()

		env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
			HelperCoverRootEnv+"=", CoverDirEnv+"="+t.TempDir())
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)

		if result.ExitCode != HelperMisuse {
			t.Errorf("a helper handed the parent's %s and no %s exited %d, want the misuse status %d:\n%s",
				CoverDirEnv, HelperCoverRootEnv, result.ExitCode, HelperMisuse, result.Output)
		}
		RequireOutput(t, result, "the refusal", HelperCoverRootEnv+" is unset")
	})

	t.Run("a root without the variable is still a coverage run", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
			HelperCoverRootEnv+"="+root, CoverDirEnv+"=")
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)

		RequireExit(t, result, 0, "the helper program")
		dir := string(result.Stdout)
		if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			t.Errorf("a helper whose %s was stripped on the way in set it to %q, want a directory under "+
				"the private root %s", CoverDirEnv, dir, root)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("the helper's coverage directory was not created: %v", err)
		}
	})
}

func TestTheCoverModeDecidesTheRootAndTheRootDecidesTheHelper(t *testing.T) {
	t.Parallel()

	const (
		root    = "/tmp/go-mutants-helper-cover-1"
		profile = "/tmp/the-parents-own-profile"
	)
	for _, test := range []struct {
		name     string
		mode     string
		root     string
		coverDir string
		publish  bool
		action   coverAction
	}{{
		name:   "a plain `go test`",
		action: coverNothing,
	}, {
		name:     "`go test -cover`",
		mode:     "set",
		root:     root,
		coverDir: profile,
		publish:  true,
		action:   coverCarve,
	}, {
		name:    "a `-cover` binary run with -test.gocoverdir",
		mode:    "atomic",
		root:    root,
		publish: true,
		action:  coverCarve,
	}, {
		name:     "a coverage run with nowhere private to write",
		mode:     "count",
		coverDir: profile,
		publish:  true,
		action:   coverRefuse,
	}, {
		name:   "a root inherited by a process with no coverage of its own",
		root:   root,
		action: coverCarve,
	}, {
		name:     "a GOCOVERDIR handed to a process with no root",
		coverDir: profile,
		action:   coverRefuse,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := suitePublishesCoverRoot(test.mode); got != test.publish {
				t.Errorf("suitePublishesCoverRoot(%q) = %v, want %v", test.mode, got, test.publish)
			}
			if got := helperCoverAction(test.root, test.coverDir); got != test.action {
				t.Errorf("helperCoverAction(%q, %q) = %v, want %v",
					test.root, test.coverDir, got, test.action)
			}
		})
	}
}

func TestHelperMakesACoverageDirectoryOnlyForAnInstrumentedRun(t *testing.T) {
	t.Parallel()

	argv := HelperArgv("TestHelperCoverRootTargetProcess")
	plainRun := []string{CoverDirEnv + "=", HelperCoverRootEnv + "="}
	instrumented := testing.CoverMode() != ""

	requireRoot := func(t *testing.T, result Result, scratch string) {
		t.Helper()
		root := publishedCoverRoot(t, result)
		switch {
		case !instrumented:
			if root != "" {
				t.Errorf("a suite with no coverage to keep apart published %s = %q; a directory "+
					"nothing writes to is a directory every killed process leaves behind",
					HelperCoverRootEnv, root)
			}
		case root == "":
			t.Errorf("a coverage-instrumented suite published no %s, so its helpers have nowhere "+
				"private to write: every one of them writes covmeta into the parent's directory, or "+
				"says on stderr that it wrote nowhere", HelperCoverRootEnv)
		case !SamePath(filepath.Dir(root), scratch):
			t.Errorf("the coverage root is %q, want one carved out of the temporary directory the "+
				"child was given, %s", root, scratch)
		}
	}

	t.Run("a run that ends normally leaves nothing behind", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		env := withEntries(Compose(t, scratch), append([]string{argvTargetEnv + "=1"}, plainRun...)...)
		result := Exec(t, t.TempDir(), env, argv...)

		RequireExit(t, result, 0, "the re-executed test")
		requireRoot(t, result, scratch)
		requireCoverageDirectoriesIn(t, scratch, 0)
	})

	t.Run("a GOCOVERDIR does not make a run a coverage run", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		env := withEntries(Compose(t, scratch),
			argvTargetEnv+"=1", HelperCoverRootEnv+"=", CoverDirEnv+"="+t.TempDir())
		result := Exec(t, t.TempDir(), env, argv...)

		RequireExit(t, result, 0, "the re-executed test")
		requireRoot(t, result, scratch)
		requireCoverageDirectoriesIn(t, scratch, 0)
	})

	t.Run("a run that is killed leaves only what it was carrying", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		env := withEntries(Compose(t, scratch),
			append([]string{argvTargetEnv + "=1", argvTargetExitEnv + "=9"}, plainRun...)...)
		result := Exec(t, t.TempDir(), env, argv...)

		if result.ExitCode != 9 {
			t.Fatalf("the child exited %d, want the 9 it was told to leave with:\n%s",
				result.ExitCode, result.Output)
		}
		want := 0
		if instrumented {
			want = 1
		}
		requireCoverageDirectoriesIn(t, scratch, want)
	})
}

func publishedCoverRoot(t *testing.T, r Result) string {
	t.Helper()

	for line := range strings.SplitSeq(string(r.Stdout), "\n") {
		if root, ok := strings.CutPrefix(strings.TrimSpace(line), coverRootMarker); ok {
			return root
		}
	}
	t.Fatalf("the child never reported its coverage root:\n%s", r.Output)
	return ""
}

func requireCoverageDirectoriesIn(t *testing.T, dir string, want int) {
	t.Helper()

	left, err := filepath.Glob(filepath.Join(dir, helperCoverPrefix+"*"))
	if err != nil {
		t.Fatalf("scanning %s for coverage directories: %v", dir, err)
	}
	if len(left) != want {
		t.Errorf("the child left %d coverage directory(ies) behind in the temporary directory it was "+
			"given, want %d: %q", len(left), want, left)
	}
}

func TestTestBinaryIsAlwaysAnAbsolutePath(t *testing.T) {
	t.Parallel()

	if got := testBinaryFrom("/opt/bin/pkg.test", nil, "ignored"); got != "/opt/bin/pkg.test" {
		t.Errorf("testBinaryFrom with a usable os.Executable = %q, want it used unchanged", got)
	}

	failed := errors.New("os.Executable is not supported here")
	relative := filepath.Join(".", "pkg.test")
	got := testBinaryFrom("", failed, relative)
	if !filepath.IsAbs(got) {
		t.Errorf("testBinaryFrom fell back to %q, which a child in another directory cannot resolve", got)
	}
	if filepath.Base(got) != "pkg.test" {
		t.Errorf("testBinaryFrom fell back to %q, which no longer names the test binary", got)
	}

	absolute := filepath.Join(t.TempDir(), "pkg.test")
	if fellBack := testBinaryFrom("", failed, absolute); fellBack != absolute {
		t.Errorf("testBinaryFrom rewrote an already absolute argv[0]: %q, want %q", fellBack, absolute)
	}
}

func TestHelperArgvEscapesTheTestName(t *testing.T) {
	t.Parallel()

	argv := HelperArgv("TestVersion1.2+build/sub(case)")
	want := "-test.run=^" + regexp.QuoteMeta("TestVersion1.2+build/sub(case)") + "$"
	if argv[1] != want {
		t.Errorf("HelperArgv run pattern = %q, want %q", argv[1], want)
	}
	pattern, err := regexp.Compile(strings.TrimPrefix(argv[1], "-test.run="))
	if err != nil {
		t.Fatalf("the run pattern is not a valid regular expression: %v", err)
	}
	if !pattern.MatchString("TestVersion1.2+build/sub(case)") {
		t.Error("the run pattern does not match the test it names")
	}
	if pattern.MatchString("TestVersion1x2+build/sub(case)") {
		t.Error("the run pattern matches a name the `.` was never meant to stand for")
	}
}

func TestHelperEnabledReadsANonEmptyValue(t *testing.T) {
	t.Setenv(argvTargetEnv, "0")
	if !HelperEnabled(argvTargetEnv) {
		t.Error("HelperEnabled is false for a variable that is set to 0")
	}
	t.Setenv(argvTargetEnv, "")
	if HelperEnabled(argvTargetEnv) {
		t.Error("HelperEnabled is true for a variable set to the empty string")
	}
}
