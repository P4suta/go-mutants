// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// cacheDigest and cacheMutant are one workspace and one mutant for the
// maintenance tests.
var (
	cacheDigest = strings.Repeat("ab", 32)
	cacheMutant = strings.Repeat("a1", 32)
)

// isolatedCache points os.UserCacheDir and the working directory at temporary
// ones, and returns the cache root the commands will resolve.
//
// Both environment variables are set because os.UserCacheDir reads a different
// one on each platform, and a test that redirected only the POSIX spelling
// would quietly operate on the developer's own cache on Windows.
func isolatedCache(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	t.Setenv("LocalAppData", base)
	t.Chdir(t.TempDir())
	return filepath.Join(base, report.DirName)
}

// seedCache stores one outcome under the given root and returns the directory
// it was filed in.
func seedCache(t *testing.T, root string) string {
	t.Helper()
	store, err := cache.Open(cache.Options{
		Root:    root,
		Timeout: 10 * time.Second,
		Context: cache.Context{
			ToolVersion: Version,
			ToolDigest:  strings.Repeat("11", 32),
			// Any real release token will do: these tests care where the entry
			// is filed, never which directory the key names.
			ToolchainVersion: "go1.26.5",
			WorkspaceDigest:  cacheDigest,
			CatalogDigest:    strings.Repeat("cd", 32),
			TestCommand:      config.DefaultTestCommand(),
			Env:              cache.EnvFrom(func(string) (string, bool) { return "", false }),
		},
	})
	if err != nil {
		t.Fatalf("opening the cache: %v", err)
	}
	entry := cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 120, Attempts: 1}
	if err = store.Put(cacheMutant, entry); err != nil {
		t.Fatalf("seeding the cache: %v", err)
	}
	return store.Dir()
}

// TestCacheStatusOnAMachineThatHasNeverRunIsNotAFailure. "Nothing cached yet" is
// an answer, and a non-zero status for it would break a script that checks the
// tool is present.
func TestCacheStatusOnAMachineThatHasNeverRunIsNotAFailure(t *testing.T) {
	root := isolatedCache(t)

	code, stdout, stderr := execute(t, "cache", "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, root) {
		t.Errorf("the status does not name the cache root %s:\n%s", root, stdout)
	}
	if !strings.Contains(stdout, "nothing cached yet") {
		t.Errorf("an empty cache did not say so:\n%s", stdout)
	}
}

// TestCacheStatusCountsWhatIsStored.
func TestCacheStatusCountsWhatIsStored(t *testing.T) {
	root := isolatedCache(t)
	seedCache(t, root)

	code, stdout, stderr := execute(t, "cache", "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{root, "1 workspace", "1 outcome", report.WorkspaceKey(cacheDigest)} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the status does not mention %q:\n%s", want, stdout)
		}
	}
}

// TestCacheCleanRemovesTheOutcomes, and says how many.
func TestCacheCleanRemovesTheOutcomes(t *testing.T) {
	root := isolatedCache(t)
	dir := seedCache(t, root)

	code, stdout, stderr := execute(t, "cache", "clean")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "removed 1 outcome") {
		t.Errorf("clean did not say what it removed:\n%s", stdout)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the entries are still there: %v", err)
	}
	// A second clean is not a failure: there is simply nothing left.
	code, stdout, _ = execute(t, "cache", "clean")
	if code != 0 {
		t.Fatalf("cleaning an empty cache exited %d", code)
	}
	if !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("cleaning an empty cache did not say so:\n%s", stdout)
	}
}

// TestCacheGCKeepsWhatIsRecentAndRemovesWhatIsNot.
func TestCacheGCKeepsWhatIsRecentAndRemovesWhatIsNot(t *testing.T) {
	root := isolatedCache(t)
	dir := seedCache(t, root)
	entry := filepath.Join(dir, cacheMutant+".json")

	code, stdout, stderr := execute(t, "cache", "gc")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("gc removed a freshly written outcome:\n%s", stdout)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("gc deleted a fresh entry: %v", err)
	}

	old := time.Now().AddDate(0, 0, -(cache.DefaultGCDays + 1))
	if err := os.Chtimes(entry, old, old); err != nil {
		t.Fatalf("backdating the entry: %v", err)
	}
	code, stdout, stderr = execute(t, "cache", "gc")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "removed 1 outcome") {
		t.Errorf("gc did not remove the stale outcome:\n%s", stdout)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Errorf("the stale entry is still there: %v", err)
	}
}

// TestCacheGCHonoursTheDayWindow: `--days 0` is "everything already written",
// which is a legitimate thing to ask for and distinct from `clean` only in that
// it prunes rather than removes the tree.
func TestCacheGCHonoursTheDayWindow(t *testing.T) {
	root := isolatedCache(t)
	dir := seedCache(t, root)
	entry := filepath.Join(dir, cacheMutant+".json")
	yesterday := time.Now().AddDate(0, 0, -1)
	if err := os.Chtimes(entry, yesterday, yesterday); err != nil {
		t.Fatalf("backdating the entry: %v", err)
	}

	code, stdout, stderr := execute(t, "cache", "gc", "--days", "7")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("a seven-day window removed a one-day-old outcome:\n%s", stdout)
	}

	code, stdout, _ = execute(t, "cache", "gc", "--days", "0")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "removed 1 outcome") {
		t.Errorf("a zero-day window kept a one-day-old outcome:\n%s", stdout)
	}
}

// TestCacheGCRefusesANegativeWindow before it touches anything: a negative age
// is a mistake about the invocation, and the remedy is to retype it.
func TestCacheGCRefusesANegativeWindow(t *testing.T) {
	isolatedCache(t)

	code, _, stderr := execute(t, "cache", "gc", "--days", "-1")
	if code == 0 {
		t.Fatal("cache gc --days -1 was accepted")
	}
	if !strings.Contains(stderr, string(CodeUsage)) {
		t.Errorf("the refusal does not carry %s: %s", CodeUsage, stderr)
	}
}

// TestTheMaintenanceCommandsRefuseADirectoryTheyDoNotOwn is the safety property
// the ownership marker exists for. The cache root is a directory other programs
// keep things in, and go-mutants deletes files there.
func TestTheMaintenanceCommandsRefuseADirectoryTheyDoNotOwn(t *testing.T) {
	root := isolatedCache(t)
	seedCache(t, root)

	stranger := filepath.Join(root, report.WorkspacesDirName, "somebody-elses-tool")
	kept := filepath.Join(stranger, cache.OutcomesDirName, "ctx", "a.json")
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		t.Fatalf("creating the stranger's directory: %v", err)
	}
	if err := os.WriteFile(kept, []byte("{}"), 0o600); err != nil {
		t.Fatalf("writing the stranger's file: %v", err)
	}

	for _, args := range [][]string{{"cache", "status"}, {"cache", "gc", "--days", "0"}, {"cache", "clean"}} {
		code, stdout, stderr := execute(t, args...)
		if code != 0 {
			t.Fatalf("%v exited %d: %s", args, code, stderr)
		}
		if !strings.Contains(stdout, "skipped 1 directory") {
			t.Errorf("%v did not report the directory it would not touch:\n%s", args, stdout)
		}
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%v deleted a file that is not go-mutants': %v", args, err)
		}
	}
}

// TestCacheWithNoSubcommandPrintsHelp, exactly as `report` and the bare root do.
func TestCacheWithNoSubcommandPrintsHelp(t *testing.T) {
	isolatedCache(t)

	code, stdout, stderr := execute(t, "cache")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"status", "gc", "clean"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the help does not name %q:\n%s", want, stdout)
		}
	}
}

// TestFormatBytesReadsLikeAFileManager. The numbers here sit beside what a file
// manager reports, so the units are the unambiguous binary ones.
func TestFormatBytesReadsLikeAFileManager(t *testing.T) {
	t.Parallel()

	cases := map[int64]string{
		0:                  "0 B",
		1023:               "1023 B",
		1024:               "1.0 KiB",
		1536:               "1.5 KiB",
		1024 * 1024:        "1.0 MiB",
		1024 * 1024 * 1024: "1.0 GiB",
	}
	for n, want := range cases {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestRunAcceptsEveryCacheMode and refuses anything else, with the flag's own
// name in the diagnostic rather than the TOML key nobody wrote.
func TestRunAcceptsEveryCacheMode(t *testing.T) {
	t.Parallel()

	for _, mode := range config.CacheModes() {
		cmd := newRunCommand()
		if err := cmd.Flags().Parse([]string{"--cache", mode.String()}); err != nil {
			t.Fatalf("parsing --cache %s: %v", mode, err)
		}
		overlay, err := runOverlay(cmd, &runOptions{cache: mode.String()})
		if err != nil {
			t.Fatalf("--cache %s was refused: %v", mode, err)
		}
		if got, ok := overlay.CacheMode.Get(); !ok || got != mode {
			t.Errorf("--cache %s produced %v (set=%t)", mode, got, ok)
		}
	}

	cmd := newRunCommand()
	if err := cmd.Flags().Parse([]string{"--cache", "sometimes"}); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if _, err := runOverlay(cmd, &runOptions{cache: "sometimes"}); err == nil {
		t.Error("--cache sometimes was accepted")
	} else if !strings.Contains(err.Error(), "--cache") {
		t.Errorf("the refusal does not name the flag: %v", err)
	}
}
