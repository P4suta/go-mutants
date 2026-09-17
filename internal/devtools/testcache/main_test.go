// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPathRefusesARelativeOverride(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: filepath.Join("relative", "cache")}

	if code := run([]string{"path"}, &stdout, &stderr, env, deps{}); code == 0 {
		t.Errorf("`path` with a relative %s exited 0 and printed %q, want a refusal", buildCacheEnv, stdout.String())
	}
	if !strings.Contains(stderr.String(), buildCacheEnv) {
		t.Errorf("the refusal does not name the variable:\n%s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("`path` printed a directory it had just refused to resolve: %q", stdout.String())
	}
}

func TestPathPrintsTheResolvedDirectory(t *testing.T) {
	t.Parallel()

	cacheRoot := t.TempDir()
	d := deps{userCache: func() (string, error) { return cacheRoot, nil }}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"path"}, &stdout, &stderr, map[string]string{}, d); code != 0 {
		t.Fatalf("`path` exited %d: %s", code, stderr.String())
	}
	want := resolved(t, filepath.Join(cacheRoot, "go-mutants-test", "go-build"))
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Errorf("`path` printed %q, want %q", got, want)
	}

	named := filepath.Join(t.TempDir(), "named")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"path"}, &stdout, &stderr, map[string]string{buildCacheEnv: named}, d); code != 0 {
		t.Fatalf("`path` with %s set exited %d: %s", buildCacheEnv, code, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), resolved(t, named); got != want {
		t.Errorf("`path` printed %q, want the named %q", got, want)
	}
}

func TestStatusReportsSizeAndCountOfATree(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{
		"trim.txt":        11,
		"ab/abcdef-d":     4096,
		"ab/abcdef-a":     512,
		"cd/nested/entry": 1,
	})
	kept := filepath.Join(t.TempDir(), "kept")
	writeTree(t, kept, map[string]int{"engine/TestRun-0a1b2c/KEPT.txt": 300})

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: kept}
	if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`status` exited %d: %s", code, stderr.String())
	}

	out := stdout.String()
	for _, needle := range []string{
		resolved(t, cache),
		"4 files",
		"4620 bytes",
		"4.5 KiB",
		resolved(t, kept),
		"1 file",
		"300 bytes",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("`status` did not report %q:\n%s", needle, out)
		}
	}
}

func TestStatusSaysSoWhenThereIsNothingThere(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "not-created-yet")
	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: missing, keepDirEnv: filepath.Join(t.TempDir(), "kept")}

	if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`status` over a missing directory exited %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, needle := range []string{resolved(t, missing), "does not exist", "0 bytes"} {
		if !strings.Contains(out, needle) {
			t.Errorf("`status` did not report %q:\n%s", needle, out)
		}
	}
}

func TestTrimWipesOnlyWhenOverBudget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		budget     string
		wantWiped  bool
		wantReport string
	}{
		{name: "under", budget: "8KiB", wantWiped: false, wantReport: "under"},
		{name: "over", budget: "4KiB", wantWiped: true, wantReport: "over"},
		{name: "exactly the budget", budget: "4620", wantWiped: false, wantReport: "under"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cache := filepath.Join(t.TempDir(), "go-build")
			writeTree(t, cache, map[string]int{"ab/entry": 4096, "cd/entry": 512, "trim.txt": 12})
			stamp(t, cache, buildCacheMarker)
			wiped := resolved(t, cache)
			kept := filepath.Join(t.TempDir(), "kept")
			writeTree(t, kept, map[string]int{"engine/TestRun-0a1b2c/KEPT.txt": 300})

			var cleaned []string
			d := deps{cleaner: func(dir string) error {
				cleaned = append(cleaned, dir)
				return nil
			}}

			var stdout, stderr bytes.Buffer
			env := map[string]string{buildCacheEnv: cache, keepDirEnv: kept}
			if code := run([]string{"trim", "--budget", tc.budget}, &stdout, &stderr, env, d); code != 0 {
				t.Fatalf("`trim --budget %s` exited %d: %s", tc.budget, code, stderr.String())
			}

			_, err := os.Stat(cache)
			gone := errors.Is(err, fs.ErrNotExist)
			if gone != tc.wantWiped {
				t.Errorf("after `trim --budget %s` the cache is gone: %v, want %v", tc.budget, gone, tc.wantWiped)
			}
			if wantCleaned := tc.wantWiped; wantCleaned != (len(cleaned) == 1) {
				t.Errorf("`go clean -cache` was run against %q, want it run exactly %v", cleaned, wantCleaned)
			}
			if tc.wantWiped && len(cleaned) == 1 && cleaned[0] != wiped {
				t.Errorf("`go clean -cache` was pointed at %s, want the cache %s", cleaned[0], wiped)
			}
			if _, err := os.Stat(filepath.Join(kept, "engine", "TestRun-0a1b2c", "KEPT.txt")); err != nil {
				t.Errorf("a trim removed the kept scratch root, which holds the evidence of failed runs: %v", err)
			}
			if out := stdout.String() + stderr.String(); !strings.Contains(out, tc.wantReport) {
				t.Errorf("`trim --budget %s` did not say it was %s budget:\n%s", tc.budget, tc.wantReport, out)
			}
		})
	}
}

func TestTrimRefusesAMissingOrUnreadableBudget(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"trim"}, {"trim", "--budget", "4GB"}, {"trim", "--budget", ""}} {
		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: t.TempDir(), keepDirEnv: t.TempDir()}
		if code := run(args, &stdout, &stderr, env, deps{}); code == 0 {
			t.Errorf("`%s` exited 0, want a refusal:\n%s", strings.Join(args, " "), stdout.String())
		}
		if !strings.Contains(stderr.String(), "budget") {
			t.Errorf("the refusal of `%s` does not mention the budget:\n%s", strings.Join(args, " "), stderr.String())
		}
	}
}

func TestCleanEmptiesTheCacheAndTheKeptRoot(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{"ab/entry": 4096, "trim.txt": 12})
	stamp(t, cache, buildCacheMarker)
	kept := filepath.Join(t.TempDir(), "kept")
	writeTree(t, kept, map[string]int{"engine/TestRun-0a1b2c/KEPT.txt": 300})
	stamp(t, kept, keptMarker)
	removedCache, removedKept := resolved(t, cache), resolved(t, kept)

	var cleaned []string
	d := deps{cleaner: func(dir string) error {
		cleaned = append(cleaned, dir)
		return nil
	}}

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: kept}
	if code := run([]string{"clean"}, &stdout, &stderr, env, d); code != 0 {
		t.Fatalf("`clean` exited %d: %s", code, stderr.String())
	}

	if want := []string{removedCache}; !slices.Equal(cleaned, want) {
		t.Errorf("`go clean -cache` was run against %q, want it run once against %q", cleaned, want)
	}
	for _, dir := range []string{cache, kept} {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still exists after `clean`: %v", dir, err)
		}
	}
	if out := stdout.String(); !strings.Contains(out, removedCache) || !strings.Contains(out, removedKept) {
		t.Errorf("`clean` did not say what it removed:\n%s", out)
	}
}

func TestCleanReportsARemovalItCannotFinish(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("a read-only parent does not stop a removal on Windows, so the failure has to be provoked differently there")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a removal fail")
	}

	parent := t.TempDir()
	cache := filepath.Join(parent, "go-build")
	writeTree(t, cache, map[string]int{"ab/entry": 16})
	stamp(t, cache, buildCacheMarker)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("making %s read-only: %v", parent, err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	var slept []time.Duration
	d := deps{
		cleaner: func(string) error { return nil },
		sleep:   func(d time.Duration) { slept = append(slept, d) },
	}

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	if code := run([]string{"clean"}, &stdout, &stderr, env, d); code != 0 {
		t.Errorf("`clean` exited %d over a directory it could not remove, want 0 and a report:\n%s", code, stderr.String())
	}
	if len(slept) != 1 {
		t.Errorf("the removal slept %v times, want exactly one retry", len(slept))
	}
	if len(slept) > 0 && slept[0] < time.Second {
		t.Errorf("the retry paused for %v, want long enough for a file handle to be released", slept[0])
	}
	if out := stderr.String(); !strings.Contains(out, cache) {
		t.Errorf("the report does not name the directory that survived:\n%s", out)
	}
}

func TestExecExportsGocacheAndPrintsTheDelta(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{"seeded-by-an-earlier-run": 100})
	stamp(t, cache, buildCacheMarker)
	report := filepath.Join(t.TempDir(), "what-the-child-saw")

	env := helperEnvironment(t, map[string]string{
		helperModeEnv:   "write",
		helperReportEnv: report,
		helperBytesEnv:  "4096",
		buildCacheEnv:   cache,
		keepDirEnv:      filepath.Join(t.TempDir(), "kept"),
	})

	var stdout, stderr bytes.Buffer
	argv := append([]string{"exec", "--"}, helperArgv(t)...)
	if code := run(argv, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`exec` exited %d: %s", code, stderr.String())
	}

	saw := readReport(t, report)
	for _, name := range []string{"GOCACHE", buildCacheEnv} {
		if saw[name] != resolved(t, cache) {
			t.Errorf("the child saw %s=%q, want the cache %s", name, saw[name], resolved(t, cache))
		}
	}

	want := fmt.Sprintf("testcache: %s %d bytes (%+d)", resolved(t, cache), 4196, 4096)
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("`exec` did not report the growth as %q:\n%s", want, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("`exec` wrote to stdout, which belongs to the child it wraps: %q", stdout.String())
	}
}

func TestExecForwardsTheChildExitStatus(t *testing.T) {
	t.Parallel()

	for _, want := range []int{0, 1, 3, 97} {
		t.Run(fmt.Sprintf("exit %d", want), func(t *testing.T) {
			t.Parallel()

			env := helperEnvironment(t, map[string]string{
				helperModeEnv: "exit",
				helperExitEnv: strconv.Itoa(want),
				buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
				keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
			})
			var stdout, stderr bytes.Buffer
			argv := append([]string{"exec", "--"}, helperArgv(t)...)
			if got := run(argv, &stdout, &stderr, env, deps{}); got != want {
				t.Errorf("`exec` exited %d, want the child's %d:\n%s", got, want, stderr.String())
			}
		})
	}

	t.Run("killed by a signal", func(t *testing.T) {
		t.Parallel()

		env := helperEnvironment(t, map[string]string{
			helperModeEnv: "signal",
			buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
			keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
		})
		var stdout, stderr bytes.Buffer
		argv := append([]string{"exec", "--"}, helperArgv(t)...)
		got := run(argv, &stdout, &stderr, env, deps{})

		if runtime.GOOS == "windows" {
			if got == 0 {
				t.Errorf("`exec` exited 0 for a child that was terminated")
			}
			return
		}
		if want := 128 + int(syscall.SIGKILL); got != want {
			t.Errorf("`exec` exited %d for a child killed by SIGKILL, want %d", got, want)
		}
	})
}

func TestExecAppliesTheBudgetAfterTheChildAndKeepsItsStatus(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	env := helperEnvironment(t, map[string]string{
		helperModeEnv:  "exit",
		helperExitEnv:  "7",
		helperBytesEnv: "4096",
		buildCacheEnv:  cache,
		keepDirEnv:     filepath.Join(t.TempDir(), "kept"),
	})

	var cleaned []string
	d := deps{cleaner: func(dir string) error {
		cleaned = append(cleaned, dir)
		return nil
	}}

	var stdout, stderr bytes.Buffer
	argv := append([]string{"exec", "--budget", "1KiB", "--"}, helperArgv(t)...)
	if got := run(argv, &stdout, &stderr, env, d); got != 7 {
		t.Errorf("`exec --budget 1KiB` exited %d, want the child's 7:\n%s", got, stderr.String())
	}
	if !slices.Equal(cleaned, []string{cache}) {
		t.Errorf("`go clean -cache` was run against %q, want it run once against %s", cleaned, cache)
	}
	if _, err := os.Stat(cache); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the cache survived a trim it was over budget for: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("`exec` wrote to stdout, which belongs to the child it wraps: %q", stdout.String())
	}
}

func TestExecRefusesToRunNothing(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: t.TempDir(), keepDirEnv: t.TempDir()}
	if code := run([]string{"exec", "--budget", "4GiB", "--"}, &stdout, &stderr, env, deps{}); code == 0 {
		t.Errorf("`exec` with no command exited 0, want a refusal")
	}
	if !strings.Contains(stderr.String(), "command") {
		t.Errorf("the refusal does not say what was missing:\n%s", stderr.String())
	}
}

const (
	helperModeEnv   = "TESTCACHE_TEST_HELPER"
	helperReportEnv = "TESTCACHE_TEST_HELPER_REPORT"
	helperBytesEnv  = "TESTCACHE_TEST_HELPER_BYTES"
	helperExitEnv   = "TESTCACHE_TEST_HELPER_EXIT"
	helperMisuse    = 99
)

func TestExecHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skipf("this is the child process the exec tests re-execute, and it does nothing unless %s is set", helperModeEnv)
	}

	if path := os.Getenv(helperReportEnv); path != "" {
		body := "GOCACHE=" + os.Getenv("GOCACHE") + "\n" +
			buildCacheEnv + "=" + os.Getenv(buildCacheEnv) + "\n" +
			"ARGV=" + strings.Join(os.Args[1:], " ") + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "helper: reporting the environment: %v\n", err)
			os.Exit(helperMisuse)
		}
	}

	if size := os.Getenv(helperBytesEnv); size != "" {
		n, err := strconv.Atoi(size)
		if err != nil {
			fmt.Fprintf(os.Stderr, "helper: %s=%q is not a number of bytes\n", helperBytesEnv, size)
			os.Exit(helperMisuse)
		}
		dir := os.Getenv("GOCACHE")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "helper: creating %s: %v\n", dir, err)
			os.Exit(helperMisuse)
		}
		if err := os.WriteFile(filepath.Join(dir, "written-by-the-child"), bytes.Repeat([]byte("x"), n), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "helper: filling the cache: %v\n", err)
			os.Exit(helperMisuse)
		}
	}

	if mode == "signal" {
		self, err := os.FindProcess(os.Getpid())
		if err == nil {
			err = self.Signal(os.Kill)
		}
		fmt.Fprintf(os.Stderr, "helper: this process survived being killed: %v\n", err)
		os.Exit(helperMisuse)
	}

	code, err := strconv.Atoi(os.Getenv(helperExitEnv))
	if err != nil {
		code = 0
	}
	os.Exit(code)
}

func helperArgv(t *testing.T) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locating this test binary to re-execute it: %v", err)
	}
	return []string{binary, "-test.run=^TestExecHelper$"}
}

func helperEnvironment(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	env["GOCOVERDIR"] = t.TempDir()
	for name, value := range extra {
		env[name] = value
	}
	return env
}

func readReport(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the child did not report what it was given: %v", err)
	}
	saw := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			saw[name] = value
		}
	}
	return saw
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	full, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return full
}

func stamp(t *testing.T, root, marker string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("creating %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, marker), nil, 0o600); err != nil {
		t.Fatalf("stamping %s: %v", root, err)
	}
}

func writeTree(t *testing.T, root string, files map[string]int) {
	t.Helper()
	for name, size := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

func TestParseSizeAcceptsBinaryUnits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		text string
		want int64
	}{
		{"0", 0},
		{"4096", 4096},
		{"512B", 512},
		{"512 b", 512},
		{"4KiB", 4 << 10},
		{"64MiB", 64 << 20},
		{"4GiB", 4 << 30},
		{" 4gib ", 4 << 30},
		{"1.5GiB", 3 << 29},
		{"2TiB", 2 << 40},
	} {
		got, err := parseSize(tc.text)
		if err != nil {
			t.Errorf("parseSize(%q): %v", tc.text, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSize(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}

	for _, text := range []string{
		"", "   ", "4GB", "4kb", "-1", "-1GiB", "GiB", "four", "4 GiB extra",
		"nan", "NaN", "inf", "-inf", "infGiB",
		"9223372036854775807",
		"9223372036854775808",
		"8388608TiB",
		"9007199254740992TiB",
		"1e30", "1e300GiB",
	} {
		got, err := parseSize(text)
		if err == nil {
			t.Errorf("parseSize(%q) = %d, want a refusal", text, got)
			continue
		}
		if !strings.Contains(err.Error(), "GiB") {
			t.Errorf("the refusal of %q does not name an accepted unit: %v", text, err)
		}
	}

	if got, err := parseSize("9223372036854774784"); err != nil || got != 9223372036854774784 {
		t.Errorf("parseSize(the largest representable budget) = %d (%v), want 9223372036854774784", got, err)
	}
}

func TestMeasureCountsWhatItCanReadAndSaysWhatItCouldNot(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("a directory with no permissions is not how Windows makes one unreadable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads directories whose permissions say otherwise")
	}

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{"aa-locked/entry": 512, "bb-open/entry": 4096})
	stamp(t, cache, buildCacheMarker)
	locked := filepath.Join(cache, "aa-locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", locked, err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	used, err := measure(cache)
	if err == nil {
		t.Errorf("measure returned no error for a tree it could not read completely")
	} else if !strings.Contains(err.Error(), "aa-locked") {
		t.Errorf("the error does not name what could not be read: %v", err)
	}
	if used.bytes < 4096 {
		t.Errorf("measure counted %d bytes, want at least the 4096 it could read: the walk stopped "+
			"at the entry it could not open", used.bytes)
	}

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	d := deps{cleaner: func(string) error { return nil }}
	if code := run([]string{"trim", "--budget", "8KiB"}, &stdout, &stderr, env, d); code != 0 {
		t.Fatalf("`trim` exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "counting only what could be read") {
		t.Errorf("the trim report does not say the total was partial:\n%s", stdout.String())
	}
}

func TestWipeRefusesADirectoryItDoesNotOwn(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (string, deps, *[]string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "pretend")
		writeTree(t, dir, map[string]int{"thesis.txt": 4096, "chapters/three.txt": 512})
		recorded := &[]string{}
		return dir, deps{cleaner: func(name string) error {
			*recorded = append(*recorded, name)
			return nil
		}}, recorded
	}
	survives := func(t *testing.T, dir string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(dir, "thesis.txt")); err != nil {
			t.Errorf("the file this tool had no business touching is gone: %v", err)
		}
	}

	t.Run("clean fails", func(t *testing.T) {
		t.Parallel()
		dir, d, cleaned := setup(t)

		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: dir, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
		if code := run([]string{"clean"}, &stdout, &stderr, env, d); code == 0 {
			t.Errorf("`clean` exited 0 over a directory it does not own, want a failure")
		}
		survives(t, dir)
		if len(*cleaned) != 0 {
			t.Errorf("`go clean -cache` was run against %q, which would have emptied it before the guard could speak", *cleaned)
		}
		for _, needle := range []string{resolved(t, dir), buildCacheMarker} {
			if !strings.Contains(stderr.String(), needle) {
				t.Errorf("the refusal does not name %q:\n%s", needle, stderr.String())
			}
		}
	})

	t.Run("trim reports and succeeds", func(t *testing.T) {
		t.Parallel()
		dir, d, cleaned := setup(t)

		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: dir, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
		if code := run([]string{"trim", "--budget", "0"}, &stdout, &stderr, env, d); code != 0 {
			t.Errorf("`trim` exited %d over a directory it does not own, want 0 and a report", code)
		}
		survives(t, dir)
		if len(*cleaned) != 0 {
			t.Errorf("`go clean -cache` was run against %q", *cleaned)
		}
		if !strings.Contains(stdout.String()+stderr.String(), resolved(t, dir)) {
			t.Errorf("the report does not name the directory:\n%s%s", stdout.String(), stderr.String())
		}
	})

	t.Run("exec keeps the child's status", func(t *testing.T) {
		t.Parallel()
		dir, d, _ := setup(t)

		env := helperEnvironment(t, map[string]string{
			helperModeEnv: "exit",
			helperExitEnv: "3",
			buildCacheEnv: dir,
			keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
		})
		var stdout, stderr bytes.Buffer
		argv := append([]string{"exec", "--budget", "0", "--"}, helperArgv(t)...)
		if code := run(argv, &stdout, &stderr, env, d); code != 3 {
			t.Errorf("`exec` exited %d over a directory it does not own, want the child's 3", code)
		}
		survives(t, dir)

		if _, err := os.Stat(filepath.Join(dir, buildCacheMarker)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("`exec` stamped a directory full of somebody else's files: %v", err)
		}
		if !strings.Contains(stderr.String(), "not") {
			t.Errorf("`exec` did not say it had left the directory unstamped:\n%s", stderr.String())
		}
	})

	t.Run("status says why", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := setup(t)

		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: dir, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
		if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
			t.Fatalf("`status` exited %d: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "not stamped") {
			t.Errorf("`status` does not say the directory carries no marker, so a refused `clean` has "+
				"nowhere to send the reader:\n%s", stdout.String())
		}
	})
}

func TestHarnessDirectoryRefusesRootHomeAndTheUsersGoCache(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cacheRoot := t.TempDir()
	d := deps{
		userCache: func() (string, error) { return cacheRoot, nil },
		userHome:  func() (string, error) { return home, nil },
	}

	for _, tc := range []struct {
		name     string
		dir      string
		variable string
		needle   string
	}{
		{name: "the filesystem root", dir: filesystemRoot(t), variable: buildCacheEnv, needle: "root"},
		{name: "the home directory", dir: home, variable: buildCacheEnv, needle: "home"},
		{name: "the user's own build cache", dir: filepath.Join(cacheRoot, "go-build"), variable: buildCacheEnv, needle: "go clean -cache"},
		{name: "the kept root at home", dir: home, variable: keepDirEnv, needle: "home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			env := map[string]string{buildCacheEnv: cacheRoot, keepDirEnv: cacheRoot, tc.variable: tc.dir}
			if code := run([]string{"status"}, &stdout, &stderr, env, d); code == 0 {
				t.Errorf("%s=%s was accepted:\n%s", tc.variable, tc.dir, stdout.String())
			}
			out := stderr.String()
			if !strings.Contains(out, tc.dir) || !strings.Contains(out, tc.needle) {
				t.Errorf("the refusal does not name both %q and %q:\n%s", tc.dir, tc.needle, out)
			}
		})
	}
}

func filesystemRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func TestExecStampsTheCacheItWritesInto(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	env := helperEnvironment(t, map[string]string{
		helperModeEnv:  "exit",
		helperBytesEnv: "512",
		buildCacheEnv:  cache,
		keepDirEnv:     filepath.Join(t.TempDir(), "kept"),
	})

	for _, pass := range []string{"first", "second"} {
		var stdout, stderr bytes.Buffer
		argv := append([]string{"exec", "--"}, helperArgv(t)...)
		if code := run(argv, &stdout, &stderr, env, deps{}); code != 0 {
			t.Fatalf("the %s `exec` exited %d: %s", pass, code, stderr.String())
		}
		marker := filepath.Join(cache, buildCacheMarker)
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("after the %s run, %s: %v", pass, marker, err)
		}
		if len(data) == 0 {
			t.Errorf("the marker is empty, and it is the only explanation a person who finds this directory gets")
		}
	}

	var stdout, stderr bytes.Buffer
	env2 := map[string]string{buildCacheEnv: cache, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	d := deps{cleaner: func(string) error { return nil }}
	if code := run([]string{"clean"}, &stdout, &stderr, env2, d); code != 0 {
		t.Errorf("`clean` exited %d over the directory `exec` had stamped: %s", code, stderr.String())
	}
	if _, err := os.Stat(cache); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the stamped cache survived `clean`: %v", err)
	}
}

func TestASymlinkedCacheRootIsMeasuredAndWipedByItsContents(t *testing.T) {
	t.Parallel()

	behind := filepath.Join(t.TempDir(), "somewhere-with-room")
	writeTree(t, behind, map[string]int{"ab/entry": 4096})
	stamp(t, behind, buildCacheMarker)
	link := filepath.Join(t.TempDir(), "go-build")
	if err := os.Symlink(behind, link); err != nil {
		t.Skipf("this machine does not let a test create a symlink: %v", err)
	}

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: link, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`status` exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "4.0 KiB") || !strings.Contains(stdout.String(), "2 files") {
		t.Errorf("`status` measured the link rather than what is behind it:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	d := deps{cleaner: func(string) error { return nil }}
	if code := run([]string{"clean"}, &stdout, &stderr, env, d); code != 0 {
		t.Fatalf("`clean` exited %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(behind, "ab", "entry")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("`clean` removed the link and left the cache behind it: %v", err)
	}
}

func TestExecRefusesAnEmptyBudget(t *testing.T) {
	t.Parallel()

	env := helperEnvironment(t, map[string]string{
		helperModeEnv: "exit",
		buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
		keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
	})
	var stdout, stderr bytes.Buffer
	argv := append([]string{"exec", "--budget", "", "--"}, helperArgv(t)...)

	if code := run(argv, &stdout, &stderr, env, deps{}); code == 0 {
		t.Errorf("`exec --budget \"\"` exited 0, want a refusal")
	}
	if !strings.Contains(stderr.String(), "budget") {
		t.Errorf("the refusal does not mention the budget:\n%s", stderr.String())
	}
}

func TestExecPassesTheChildItsOwnFlags(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	report := filepath.Join(t.TempDir(), "what-the-child-saw")
	env := helperEnvironment(t, map[string]string{
		helperModeEnv:   "exit",
		helperReportEnv: report,
		buildCacheEnv:   cache,
		keepDirEnv:      filepath.Join(t.TempDir(), "kept"),
	})

	var stdout, stderr bytes.Buffer
	argv := append(append([]string{"exec", "--budget", "4GiB", "--"}, helperArgv(t)...), "--", "--budget", "8MiB")
	if code := run(argv, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`exec` exited %d: %s", code, stderr.String())
	}

	if got, want := readReport(t, report)["ARGV"], "-test.run=^TestExecHelper$ -- --budget 8MiB"; got != want {
		t.Errorf("the child was given %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "under the budget of 4.0 GiB") {
		t.Errorf("the wrapper did not apply its own 4GiB budget:\n%s", stderr.String())
	}
}

func TestExecReportsACommandItCannotStart(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-command")
	env := map[string]string{
		buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
		keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
	}
	var stdout, stderr bytes.Buffer

	if code := run([]string{"exec", "--", missing}, &stdout, &stderr, env, deps{}); code != 127 {
		t.Errorf("`exec` exited %d for a command that does not exist, want 127", code)
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("the report does not name the command it could not run:\n%s", stderr.String())
	}
}

func TestKeptCleanRefusesARootItDoesNotOwn(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{"ab/entry": 4096})
	stamp(t, cache, buildCacheMarker)

	kept := filepath.Join(t.TempDir(), "failures")
	writeTree(t, kept, map[string]int{"notes.md": 512, "screenshots/one.png": 4096})

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: kept}
	d := deps{cleaner: func(string) error { return nil }}
	if code := run([]string{"clean"}, &stdout, &stderr, env, d); code == 0 {
		t.Errorf("`clean` exited 0 over a kept root it does not own, want a failure")
	}
	if _, err := os.Stat(filepath.Join(kept, "notes.md")); err != nil {
		t.Errorf("the file this tool had no business touching is gone: %v", err)
	}
	for _, needle := range []string{resolved(t, kept), keptMarker, keepDirEnv} {
		if !strings.Contains(stderr.String(), needle) {
			t.Errorf("the refusal does not name %q:\n%s", needle, stderr.String())
		}
	}
	if _, err := os.Stat(cache); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the build cache survived a refusal that was about the kept root: %v", err)
	}
}
