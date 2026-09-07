// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
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
// The parse is the handful of lines it needs rather than a TOML library, for
// the reason every other read in this package is: the harness's import list
// holds nothing from this module, and outside the standard library only
// github.com/google/go-cmp, so the tests of the pure packages can use it without
// pulling a dependency in behind them.
func updateTaskPackages(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	commands, err := updateTaskCommands(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var packages []string
	for _, command := range commands {
		packages = append(packages, commandPackages(command)...)
	}
	slices.Sort(packages)
	return packages, nil
}

// updateTaskCommands reads the golden-update task's `run` value as the list of
// shell commands it holds, in the order mise would run them.
//
// Both TOML spellings are read, and the array one is not a stylistic
// alternative. `-update` is one flag in one test binary, so the packages have to
// be named explicitly — and a package whose golden test carries
// `//go:build integration` is not even compiled by a `go test` without the tag,
// so a single command cannot regenerate every golden in this repository. It
// takes two, and a parser that knew only about `run = "…"` would read the first
// and silently report the second's packages as missing from the task.
func updateTaskCommands(toml string) ([]string, error) {
	inTask, inArray := false, false
	var commands []string
	// closeArray ends the array, refusing one that named no command at all.
	// An empty `run = []` parses perfectly and regenerates nothing, and the
	// ledger above would then report every golden package as missing from a
	// task that is not broken so much as empty — which is a diff to read rather
	// than a sentence to act on.
	closeArray := func() ([]string, error) {
		if len(commands) == 0 {
			return nil, errors.New("[tasks.golden-update]'s run array holds no command")
		}
		return commands, nil
	}
	for line := range strings.Lines(toml) {
		found, outside := splitTOMLLine(strings.TrimSpace(line))
		if inArray {
			commands = append(commands, found...)
			// The bracket is looked for in the text *outside* the strings, so a
			// command holding a `]` of its own does not end the array early and
			// a `"…"]` with no comma before the bracket does end it.
			if strings.Contains(outside, "]") {
				return closeArray()
			}
			continue
		}
		if strings.HasPrefix(outside, "[") && !strings.HasPrefix(outside, "[tasks.golden-update]") &&
			len(found) == 0 {
			inTask = false
			continue
		}
		if strings.HasPrefix(outside, "[tasks.golden-update]") {
			inTask = true
			continue
		}
		if !inTask {
			continue
		}
		rest, ok := strings.CutPrefix(outside, "run =")
		if !ok {
			continue
		}
		value := strings.TrimSpace(rest)
		if strings.HasPrefix(value, "[") {
			commands = append(commands, found...)
			if strings.Contains(value, "]") {
				return closeArray()
			}
			inArray = true
			continue
		}
		if len(found) != 0 {
			return found[:1], nil
		}
		return nil, errors.New("[tasks.golden-update] has a run value this parser cannot read: " + value)
	}
	if inArray {
		return nil, errors.New("[tasks.golden-update]'s run array is never closed")
	}
	return nil, errors.New("has no [tasks.golden-update] with a run value")
}

// splitTOMLLine separates one line into the double-quoted strings it holds and
// the text outside them, with a `#` comment dropped.
//
// The split is what makes the two structural questions above answerable without
// a TOML parser: a `[` or a `]` or a `#` matters only outside a string, and every
// one of them is a character a shell command in this task may legitimately
// contain. Matching them against the raw line is how a comment naming a package
// pattern gets counted as one, and how a command holding a bracket ends the
// array.
//
// It is deliberately not a TOML implementation. Basic strings with an escaped
// quote are handled because they cost one branch; single-quoted literal strings,
// multi-line strings and inline tables are not, because nothing in this task
// needs them and a half-guessed grammar is worse than a refusal.
func splitTOMLLine(line string) (found []string, outside string) {
	var out strings.Builder
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '#':
			return found, out.String()
		case '"':
			var value strings.Builder
			i++
			for ; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) {
					i++
				}
				value.WriteByte(line[i])
			}
			found = append(found, value.String())
		default:
			out.WriteByte(line[i])
		}
	}
	return found, out.String()
}

// commandPackages lists the Go package patterns one command names.
func commandPackages(command string) []string {
	var packages []string
	for _, field := range strings.Fields(command) {
		if strings.HasPrefix(field, "./") {
			packages = append(packages, field)
		}
	}
	return packages
}

// TestGoldenUpdateTaskHandlesTaggedPackages is the other half of the ledger
// above: the task has to *reach* the goldens it names.
//
// `internal/engine`'s golden test is `//go:build integration`-tagged, because
// what it records is a real run of a real fixture through a real toolchain.
// A `go test ./internal/engine -update` without the tag compiles a package with
// no golden test in it at all, passes, and rewrites nothing — so the task would
// name the package, [TestGoldenPackagesAreNamedByTheUpdateTask] would be
// satisfied, and the golden it was supposed to regenerate would quietly stop
// being regenerated. That is the failure this test exists to make impossible,
// and it is checked in both directions: the parser is shown the two TOML
// spellings, and the real task is required to pass `-tags integration` to every
// package whose tests are all in the integration tier.
func TestGoldenUpdateTaskHandlesTaggedPackages(t *testing.T) {
	t.Parallel()

	t.Run("both spellings of run", func(t *testing.T) {
		t.Parallel()

		for _, test := range []struct {
			name string
			toml string
			want []string
			// wantErr is the phrase a refusal has to carry. A row with one
			// expects no commands at all.
			wantErr string
		}{{
			name: "one command",
			toml: "[tasks.golden-update]\ndescription = \"x\"\nrun = \"go test ./a ./b -update\"\n\n[tasks.other]\n",
			want: []string{"go test ./a ./b -update"},
		}, {
			name: "an array of commands",
			toml: "[tasks.golden-update]\nrun = [\n  \"go test ./a -update\",\n" +
				"  \"go test -tags integration ./b -update\",\n]\n\n[tasks.other]\nrun = \"nope\"\n",
			want: []string{"go test ./a -update", "go test -tags integration ./b -update"},
		}, {
			name: "an array on one line",
			toml: "[tasks.golden-update]\nrun = [\"go test ./a -update\", \"go test ./b -update\"]\n",
			want: []string{"go test ./a -update", "go test ./b -update"},
		}, {
			// The bracket closing the array on the last command's own line,
			// with no comma in front of it. TOML allows it and a parser that
			// looked for a line *starting* with `]` reads to the end of the
			// file and reports an array that is never closed.
			name: "the array closed after the last command",
			toml: "[tasks.golden-update]\nrun = [\n  \"go test ./a -update\",\n" +
				"  \"go test ./b -update\"]\n",
			want: []string{"go test ./a -update", "go test ./b -update"},
		}, {
			// A comment on an array line, holding something that looks exactly
			// like a package pattern. It is not one, and a scan of the raw line
			// would name `./ignored` in the task's package list — which
			// TestGoldenPackagesAreNamedByTheUpdateTask compares for equality,
			// so the comment alone would fail the ledger.
			name: "a comment naming a pattern",
			toml: "[tasks.golden-update]\nrun = [\n  \"go test ./a -update\", # not ./ignored\n" +
				"  \"go test ./b -update\",\n] # nor ./elsewhere\n",
			want: []string{"go test ./a -update", "go test ./b -update"},
		}, {
			// An array that names nothing. It parses, it regenerates nothing,
			// and an empty answer would be reported by the ledger as every
			// golden package missing from a task that is merely empty.
			name:    "an empty array",
			toml:    "[tasks.golden-update]\nrun = []\n",
			wantErr: "holds no command",
		}} {
			t.Run(test.name, func(t *testing.T) {
				t.Parallel()

				got, err := updateTaskCommands(test.toml)
				if test.wantErr != "" {
					if err == nil {
						t.Fatalf("updateTaskCommands = %q, want a refusal naming %q", got, test.wantErr)
					}
					if !strings.Contains(err.Error(), test.wantErr) {
						t.Errorf("the refusal does not say %q: %v", test.wantErr, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("reading the task: %v", err)
				}
				if !slices.Equal(got, test.want) {
					t.Errorf("commands = %q, want %q", got, test.want)
				}
				// And the packages come out of every command, not only the first.
				path := filepath.Join(t.TempDir(), "mise.toml")
				WriteFile(t, path, []byte(test.toml))
				packages, err := updateTaskPackages(path)
				if err != nil {
					t.Fatalf("reading the packages: %v", err)
				}
				if want := []string{"./a", "./b"}; !slices.Equal(packages, want) {
					t.Errorf("packages = %q, want %q", packages, want)
				}
			})
		}
	})

	t.Run("the real task reaches its tagged goldens", func(t *testing.T) {
		t.Parallel()

		root := Root(t)
		commands, err := updateTaskCommands(string(ReadFile(t, filepath.Join(root, "mise.toml"))))
		if err != nil {
			t.Fatalf("reading the golden-update task: %v", err)
		}
		tagged := map[string]bool{}
		for _, command := range commands {
			carries := strings.Contains(command, "-tags "+integrationTag) ||
				strings.Contains(command, "-tags="+integrationTag)
			for _, pkg := range commandPackages(command) {
				tagged[pkg] = tagged[pkg] || carries
			}
		}

		found, err := goldenPackages(root)
		if err != nil {
			t.Fatalf("scanning %s for golden files: %v", root, err)
		}
		for _, pkg := range found {
			dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(pkg, "./")))
			behind, files, err := goldensRecordedBehindTheTag(dir)
			if err != nil {
				t.Fatalf("reading the test files of %s: %v", pkg, err)
			}
			if behind && !tagged[pkg] {
				t.Errorf("%s records goldens from %s, which carries `//go:build %s`, "+
					"but the golden-update task runs that package without the tag: "+
					"the command compiles a package those files are not part of, passes, "+
					"and rewrites nothing",
					pkg, strings.Join(files, ", "), integrationTag)
			}
		}
	})
}

// goldenCall is how a test records or compares a golden file: [Golden],
// [CompareGolden], and nothing else — [GoldenPath] and [Update] hand back a
// path and a flag and rewrite no file on their own.
//
// It is matched as text rather than resolved, for the reason every other scan
// in this package is: `go/types` would need the whole module loaded, with a
// toolchain, in the tier this test belongs to.
const goldenCall = "Golden("

// goldensRecordedBehindTheTag reports whether any test file in a directory both
// records a golden and is kept out of the unit tier by the integration tag, and
// names the files that do.
//
// The question is deliberately about the *recording files* rather than about the
// package. "Every test file in this package is tagged" was the first thing this
// asked, and it was true of no golden package in the repository — internal/engine
// has eleven tagged test files and thirteen untagged ones — so the check passed
// whatever the task said, including with the tag removed from the command
// altogether. What makes a `-update` run rewrite nothing is narrower and exact:
// the file holding the [Golden] call is not compiled, whatever else in the
// package is.
func goldensRecordedBehindTheTag(dir string) (bool, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, nil, err
	}
	var behind []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return false, nil, readErr
		}
		text := string(source)
		if strings.Contains(text, goldenCall) && hasIntegrationTag(text) {
			behind = append(behind, entry.Name())
		}
	}
	return len(behind) != 0, behind, nil
}
