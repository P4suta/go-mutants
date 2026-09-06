// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGoldenPathIsRelativeToTheTestsOwnPackage states the one convention the
// helper does not take as an argument.
//
// `go test` runs a test binary in its package's source directory, so every
// golden in this repository is already `testdata/<name>` and a helper that took
// a whole path would let one package spell it differently from the next.
func TestGoldenPathIsRelativeToTheTestsOwnPackage(t *testing.T) {
	t.Parallel()

	if got, want := GoldenPath("run-report.golden.json"), filepath.Join("testdata", "run-report.golden.json"); got != want {
		t.Errorf("GoldenPath = %q, want %q", got, want)
	}
}

// TestGoldenFailsClosedWhenTheFileIsMissing is the rule that separates a golden
// file from a cache of whatever the code did last.
//
// A helper that recorded a missing golden silently would turn the first run of
// a new test — and every run after a golden was deleted, moved or renamed by a
// bad merge — into a green one that pins nothing. The recording has to be a
// decision somebody made with -update and read the diff of, so an absent file
// ends the test and says which file and how to create it on purpose.
func TestGoldenFailsClosedWhenTheFileIsMissing(t *testing.T) {
	t.Parallel()

	absent := filepath.Join(t.TempDir(), "testdata", "never-recorded.golden")
	rec := expectFatal(t, func(tb testing.TB) {
		goldenAt(tb, absent, []byte("whatever the code produced"), false)
	})

	report := rec.first(t, "a golden comparison against a file that is not there")
	if !strings.Contains(report, absent) {
		t.Errorf("the report does not name the missing file:\n%s", report)
	}
	if !strings.Contains(report, "-update") {
		t.Errorf("the report does not say how to record the file on purpose:\n%s", report)
	}
	if _, err := os.Stat(absent); err == nil {
		t.Errorf("%s was recorded by a comparison, which is the silent first recording this helper exists to refuse", absent)
	}
}

// TestGoldenPrintsAUnifiedDiffNotBothDocuments is what a golden failure is for.
//
// The goldens in this repository are whole documents — a run report, a
// generated runtime, a recording of every event — and the four suites that had
// their own comparison printed `--- got ---` and `--- want ---` in full. In CI
// that is two thousand lines of identical JSON around the one field that moved,
// and reading it means saving both halves out of a log and diffing them by
// hand. The failure has to name the line instead.
func TestGoldenPrintsAUnifiedDiffNotBothDocuments(t *testing.T) {
	t.Parallel()

	var want, got []string
	for i := range 20 {
		line := fmt.Sprintf("  \"filler_%02d\": %d,", i, i)
		want = append(want, line)
		got = append(got, line)
	}
	// One line moves, in the middle, with plenty of agreement on both sides.
	want[10] = `  "score_percent": 66.6,`
	got[10] = `  "score_percent": 71.4,`

	path := filepath.Join(t.TempDir(), "report.golden.json")
	WriteFile(t, path, []byte(strings.Join(want, "\n")+"\n"))

	rec := &recorder{TB: t}
	goldenAt(rec, path, []byte(strings.Join(got, "\n")+"\n"), false)

	if len(rec.errors) != 1 {
		t.Fatalf("a mismatch reported %d time(s), want once: %q", len(rec.errors), rec.errors)
	}
	report := rec.errors[0]
	if !strings.Contains(report, path) {
		t.Errorf("the report does not name the golden file:\n%s", report)
	}
	for _, needle := range []string{`66.6`, `71.4`} {
		if !strings.Contains(report, needle) {
			t.Errorf("the report does not show %s, so it is not a diff of the line that moved:\n%s", needle, report)
		}
	}
	// The claim is the whole point: a line both documents agree on is not in
	// the report at all, so the report is a diff rather than two documents.
	if strings.Contains(report, `"filler_00"`) {
		t.Errorf("the report carries a line both documents share, so it is printing the documents:\n%s", report)
	}
}

// TestCompareGoldenRewritesOnlyWhenUpdating pins the two directions of the one
// flag, because a helper that got this backwards would rewrite the file it was
// asked to compare against and every golden test in the repository would pass
// for ever.
func TestCompareGoldenRewritesOnlyWhenUpdating(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pinned.golden")
	const recorded = "what was committed\n"
	WriteFile(t, path, []byte(recorded))

	if err := CompareGolden(path, []byte("what the code does now\n"), false); err == nil {
		t.Error("CompareGolden reported no difference between two different documents")
	}
	if got := string(ReadFile(t, path)); got != recorded {
		t.Errorf("a comparison rewrote the golden file: %q", got)
	}

	if err := CompareGolden(path, []byte("what the code does now\n"), true); err != nil {
		t.Fatalf("CompareGolden under -update: %v", err)
	}
	if got, want := string(ReadFile(t, path)), "what the code does now\n"; got != want {
		t.Errorf("the rewritten golden is %q, want %q", got, want)
	}
	if err := CompareGolden(path, []byte("what the code does now\n"), false); err != nil {
		t.Errorf("the rewritten golden does not compare equal to what wrote it: %v", err)
	}
}

// TestGoldenSaysSoWhenItRewritesAFile pins the one line a -update run prints.
//
// A rewrite is silent otherwise: `go test ./internal/report -update` passes
// whether it regenerated a document or compared against one, and the difference
// is the whole point of the flag. The log line is what tells the reader there is
// a diff waiting to be read — and, when a golden did *not* move, that the file
// they expected to change was not the one this test writes.
func TestGoldenSaysSoWhenItRewritesAFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "recorded.golden")
	rec := &recorder{TB: t}
	goldenAt(rec, path, []byte("what the code produced\n"), true)

	if len(rec.fatals)+len(rec.errors) != 0 {
		t.Fatalf("a rewrite reported a failure: fatals %q, errors %q", rec.fatals, rec.errors)
	}
	if len(rec.logs) != 1 {
		t.Fatalf("a rewrite logged %d line(s), want exactly one: %q", len(rec.logs), rec.logs)
	}
	for _, want := range []string{"rewrote", path} {
		if !strings.Contains(rec.logs[0], want) {
			t.Errorf("the log line does not mention %q:\n%s", want, rec.logs[0])
		}
	}

	// And a comparison says nothing, so that a passing run stays quiet.
	quiet := &recorder{TB: t}
	goldenAt(quiet, path, []byte("what the code produced\n"), false)
	if len(quiet.logs) != 0 {
		t.Errorf("a comparison that matched logged %q", quiet.logs)
	}
}

// TestCompareGoldenRefusesAMissingFileEvenAtTheErrorLevel keeps the fail-closed
// rule in the function every other caller composes with, rather than only in
// the [testing.TB] wrapper above it.
func TestCompareGoldenRefusesAMissingFileEvenAtTheErrorLevel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "absent.golden")
	err := CompareGolden(path, []byte("x"), false)
	if err == nil {
		t.Fatal("CompareGolden accepted a golden file that does not exist")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// TestGoldenPackagesAreNamedByTheUpdateTask is the ledger that keeps one flag
// usable.
//
// There is exactly one -update flag now, registered in this package, and it is
// therefore the same flag in every test binary that links the harness. The cost
// of that is that `go test -update ./...` would rewrite every golden in the
// repository in one command, including the ones whose diff nobody looked at, so
// the supported spelling is a task naming the packages explicitly — and a task
// that names a stale list is worse than no task, because the golden it forgot
// is the one that silently stops being regenerated.
func TestGoldenPackagesAreNamedByTheUpdateTask(t *testing.T) {
	t.Parallel()

	root := Root(t)
	found, err := goldenPackages(root)
	if err != nil {
		t.Fatalf("scanning %s for golden files: %v", root, err)
	}
	if len(found) == 0 {
		t.Fatal("the scan found no golden files at all, so it is not looking where they are")
	}

	named, err := updateTaskPackages(filepath.Join(root, "mise.toml"))
	if err != nil {
		t.Fatalf("reading the golden-update task: %v", err)
	}
	if !slices.Equal(found, named) {
		t.Errorf("the golden-update task names\n\t%s\nbut goldens live in\n\t%s",
			strings.Join(named, " "), strings.Join(found, " "))
	}
}

// goldenPackages lists every package holding a `testdata/*.golden*` file, as
// `./`-prefixed slash-separated paths relative to root, sorted.
func goldenPackages(root string) ([]string, error) {
	var packages []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && slices.Contains(skippedDirectories, entry.Name()) {
			return fs.SkipDir
		}
		matches, err := filepath.Glob(filepath.Join(path, "testdata", "*.golden*"))
		if err != nil || len(matches) == 0 {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		packages = append(packages, "./"+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(packages)
	return packages, nil
}

// updateTaskPackages reads the package patterns out of mise.toml's
// golden-update task, sorted.
//
// The parse is the four lines it needs rather than a TOML library, for the
// reason every other read in this package is: the harness's import list holds
// nothing from this module, and outside the standard library only
// github.com/google/go-cmp, so the tests of the pure packages can use it without
// pulling a dependency in behind them.
func updateTaskPackages(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var run string
	inTask := false
	for line := range strings.Lines(string(data)) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inTask = trimmed == "[tasks.golden-update]"
			continue
		}
		if !inTask {
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "run ="); ok {
			run = strings.Trim(strings.TrimSpace(rest), `"`)
			break
		}
	}
	if run == "" {
		return nil, fmt.Errorf("%s has no [tasks.golden-update] with a run string", path)
	}
	var packages []string
	for _, field := range strings.Fields(run) {
		if strings.HasPrefix(field, "./") {
			packages = append(packages, field)
		}
	}
	slices.Sort(packages)
	return packages, nil
}
