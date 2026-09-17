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

func TestGoldenPathIsRelativeToTheTestsOwnPackage(t *testing.T) {
	t.Parallel()

	if got, want := GoldenPath("run-report.golden.json"), filepath.Join("testdata", "run-report.golden.json"); got != want {
		t.Errorf("GoldenPath = %q, want %q", got, want)
	}
}

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

func TestGoldenPrintsAUnifiedDiffNotBothDocuments(t *testing.T) {
	t.Parallel()

	var want, got []string
	for i := range 20 {
		line := fmt.Sprintf("  \"filler_%02d\": %d,", i, i)
		want = append(want, line)
		got = append(got, line)
	}
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
	if strings.Contains(report, `"filler_00"`) {
		t.Errorf("the report carries a line both documents share, so it is printing the documents:\n%s", report)
	}
}

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

	quiet := &recorder{TB: t}
	goldenAt(quiet, path, []byte("what the code produced\n"), false)
	if len(quiet.logs) != 0 {
		t.Errorf("a comparison that matched logged %q", quiet.logs)
	}
}

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

func updateTaskCommands(toml string) ([]string, error) {
	inTask, inArray := false, false
	var commands []string
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

func commandPackages(command string) []string {
	var packages []string
	for _, field := range strings.Fields(command) {
		if strings.HasPrefix(field, "./") {
			packages = append(packages, field)
		}
	}
	return packages
}

func TestGoldenUpdateTaskHandlesTaggedPackages(t *testing.T) {
	t.Parallel()

	t.Run("both spellings of run", func(t *testing.T) {
		t.Parallel()

		for _, test := range []struct {
			name    string
			toml    string
			want    []string
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
			name: "the array closed after the last command",
			toml: "[tasks.golden-update]\nrun = [\n  \"go test ./a -update\",\n" +
				"  \"go test ./b -update\"]\n",
			want: []string{"go test ./a -update", "go test ./b -update"},
		}, {
			name: "a comment naming a pattern",
			toml: "[tasks.golden-update]\nrun = [\n  \"go test ./a -update\", # not ./ignored\n" +
				"  \"go test ./b -update\",\n] # nor ./elsewhere\n",
			want: []string{"go test ./a -update", "go test ./b -update"},
		}, {
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

const goldenCall = "Golden("

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
