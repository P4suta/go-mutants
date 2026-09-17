// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const corpusModulePrefix = "fixture.example/"

const corpusStatePrefix = ".go-mutants"

const corpusReadme = "README.md"

var corpusRow = regexp.MustCompile("^\\|\\s*`([^`]+)/`\\s*\\|\\s*`" + regexp.QuoteMeta(corpusModulePrefix))

func TestCorpusConformance(t *testing.T) {
	t.Parallel()

	root := Root(t)
	problems, err := corpusProblems(root, runtime.Version())
	if err != nil {
		t.Fatalf("scanning the corpus under %s: %v", root, err)
	}
	if len(problems) != 0 {
		t.Errorf("the corpus under %s breaks %d of its own conventions:\n\t%s\n"+
			"fixtures/README.md states them, and each is one a fixture would otherwise break in silence",
			filepath.Join(root, FixturesDir), len(problems), strings.Join(problems, "\n\t"))
	}
}

func TestCorpusConformanceReportsEveryKindOfBreach(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		build func(t *testing.T, dir string)
		want  string
	}{{
		name:  "nomodule",
		build: func(t *testing.T, dir string) { WriteSource(t, dir, "orphan.go", "package orphan\n") },
		want:  "is neither a module nor a workspace",
	}, {
		name: "wrongpath",
		build: func(t *testing.T, dir string) {
			WriteFile(t, filepath.Join(dir, "go.mod"), []byte(SPDXHeader+"module example.com/elsewhere\n\ngo 1.20\n"))
		},
		want: "module path",
	}, {
		name: "required",
		build: func(t *testing.T, dir string) {
			WriteFile(t, filepath.Join(dir, "go.mod"),
				[]byte(SPDXHeader+"module fixture.example/required\n\ngo 1.20\n\nrequire example.com/dep v1.2.3\n"))
		},
		want: "require",
	}, {
		name: "replaced",
		build: func(t *testing.T, dir string) {
			WriteFile(t, filepath.Join(dir, "go.mod"),
				[]byte(SPDXHeader+"module fixture.example/replaced\n\ngo 1.20\n\nreplace example.com/dep => ../dep\n"))
		},
		want: "replace",
	}, {
		name: "summed",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/summed", "1.20")
			WriteFile(t, filepath.Join(dir, "go.sum"), []byte("example.com/dep v1.2.3 h1:whatever\n"))
		},
		want: "go.sum",
	}, {
		name: "futuristic",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/futuristic", "99.0")
		},
		want: "newer than the toolchain",
	}, {
		name: "headerless",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/headerless", "1.20")
			WriteFile(t, filepath.Join(dir, "headerless.go"), []byte("package headerless\n"))
		},
		want: "SPDX",
	}, {
		name: "carriage",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/carriage", "1.20")
			WriteFile(t, filepath.Join(dir, "carriage.go"),
				[]byte(SPDXHeader+"package carriage\r\n"))
		},
		want: "CRLF",
	}, {
		name: "headerlessmodule",
		build: func(t *testing.T, dir string) {
			WriteFile(t, filepath.Join(dir, "go.mod"),
				[]byte("module fixture.example/headerlessmodule\n\ngo 1.20\n"))
		},
		want: "SPDX",
	}, {
		name: "reporting",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/reporting", "1.20")
			WriteFile(t, filepath.Join(dir, "reports", "mutation", "latest.json"), []byte("{}\n"))
		},
		want: "a run wrote",
	}, {
		name: "stateful",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/stateful", "1.20")
			WriteFile(t, filepath.Join(dir, corpusStatePrefix+".toml"), []byte("[mutation]\n"))
		},
		want: "a run wrote",
	}, {
		name: "compiled",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/compiled", "1.20")
			WriteFile(t, filepath.Join(dir, "compiled.test"), []byte("\x7fELF binary"))
		},
		want: "a compiled binary",
	}, {
		name: "undocumented",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/undocumented", "1.20")
		},
		want: "is not in " + corpusReadme,
	}, {
		name: "undriven",
		build: func(t *testing.T, dir string) {
			writeFixtureModule(t, dir, "fixture.example/undriven", "1.20")
		},
		want: "no test names",
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			dir := filepath.Join(root, FixturesDir, test.name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("creating %s: %v", dir, err)
			}
			test.build(t, dir)
			ledger := "| `" + test.name + "/` | `" + corpusModulePrefix + test.name + "` | a specimen |\n"
			if test.name == "undocumented" {
				ledger = ""
			}
			WriteFile(t, filepath.Join(root, FixturesDir, corpusReadme), []byte(ledger))
			naming := "package p\n\nvar _ = testkit.Copy(t, " + strconv.Quote(test.name) + ")\n"
			if test.name == "undriven" {
				naming = "package p\n\n// undriven is worth a fixture one day.\nvar _ = " +
					strconv.Quote(test.name) + "\n"
			}
			WriteFile(t, filepath.Join(root, "driver_test.go"), []byte(naming))

			problems, err := corpusProblems(root, "go1.30.0")
			if err != nil {
				t.Fatalf("scanning the corpus under %s: %v", root, err)
			}
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, test.want) }) {
				t.Errorf("the scan of a corpus whose %s fixture is wrong reported\n\t%s\nand nothing about %q",
					test.name, strings.Join(problems, "\n\t"), test.want)
			}
		})
	}
}

func TestCorpusConformanceAcceptsAConformingFixture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, FixturesDir, "specimen")
	writeFixtureModule(t, dir, corpusModulePrefix+"specimen", "1.20")
	WriteSource(t, dir, "specimen.go", "package specimen\n")
	nested := filepath.Join(dir, "inner")
	writeFixtureModule(t, nested, corpusModulePrefix+"specimen/inner", "1.20")
	WriteSource(t, nested, "inner.go", "package inner\n")
	WriteFile(t, filepath.Join(root, FixturesDir, corpusReadme),
		[]byte("| `specimen/` | `"+corpusModulePrefix+"specimen` | a specimen |\n"))
	WriteFile(t, filepath.Join(root, "driver_test.go"),
		[]byte("package p\n\nvar _ = testkit.Copy(t, \"specimen\")\n"))

	problems, err := corpusProblems(root, "go1.30.0")
	if err != nil {
		t.Fatalf("scanning the corpus under %s: %v", root, err)
	}
	if len(problems) != 0 {
		t.Errorf("a conforming corpus was reported as breaking %d convention(s):\n\t%s",
			len(problems), strings.Join(problems, "\n\t"))
	}
}

func writeFixtureModule(t testing.TB, dir, path, directive string) {
	t.Helper()
	WriteFile(t, filepath.Join(dir, "go.mod"), []byte(SPDXHeader+"module "+path+"\n\ngo "+directive+"\n"))
}

func corpusProblems(root, toolchain string) ([]string, error) {
	corpus := filepath.Join(root, FixturesDir)
	entries, err := os.ReadDir(corpus)
	if err != nil {
		return nil, err
	}

	var problems []string
	report := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	var fixtures []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fixtures = append(fixtures, entry.Name())
		module := statExists(filepath.Join(corpus, entry.Name(), "go.mod"))
		workspace := statExists(filepath.Join(corpus, entry.Name(), "go.work"))
		if !module && !workspace {
			report("%s is neither a module nor a workspace: it holds no go.mod and no go.work, "+
				"so this repository's own `go test ./...` compiles it", entry.Name())
		}
	}

	if walked := filepath.WalkDir(corpus, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(corpus, path)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && (entry.Name() == "reports" || strings.HasPrefix(entry.Name(), corpusStatePrefix)) {
				report("%s is state a run wrote into the corpus; the fixtures are inputs, "+
					"and a run belongs in a copy of one", slashed)
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), corpusStatePrefix) {
			report("%s is state a run wrote into the corpus; the fixtures are inputs, "+
				"and a run belongs in a copy of one", slashed)
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if binaryFile(entry.Name(), data) {
			report("%s is a compiled binary, which nothing in the corpus is: "+
				"it is what a `go test -c` or a `go build` inside a fixture leaves behind", slashed)
			return nil
		}
		if bytes.Contains(data, []byte("\r")) {
			report("%s holds CRLF line endings, and a mutant identity is the digest of a file's bytes: "+
				"the corpus is checked out byte for byte (.gitattributes pins `* -text`), "+
				"so a fixture written this way has different ids here and everywhere else", slashed)
		}
		if name := entry.Name(); name == "go.mod" || name == "go.work" || strings.HasSuffix(name, ".go") {
			if !bytes.HasPrefix(data, []byte(SPDXHeader)) {
				report("%s does not open with the SPDX header pair: the licensing check and `gofmt -l .` "+
					"walk the filesystem rather than the module graph, so a fixture file is held to the "+
					"same standard as the rest of the tree", slashed)
			}
		}
		switch entry.Name() {
		case "go.mod":
			problems = append(problems, moduleProblems(corpus, rel, data, toolchain)...)
		case "go.sum":
			report("%s is a checksum file, so the fixture beside it has a dependency: "+
				"a fixture needs the standard library and nothing else, or the integration suite "+
				"needs the network", slashed)
		}
		return nil
	}); walked != nil {
		return nil, walked
	}

	listed, err := ledgerFixtures(filepath.Join(corpus, corpusReadme))
	if err != nil {
		return nil, err
	}
	named, err := fixturesNamedByTests(root)
	if err != nil {
		return nil, err
	}
	for _, name := range fixtures {
		if !slices.Contains(listed, name) {
			report("%s is not in %s, so nothing says what it is for", name, corpusReadme)
		}
		if !slices.Contains(named, name) {
			report("no test names %q, so the fixture costs a checkout and proves nothing", name)
		}
	}
	for _, name := range listed {
		if !slices.Contains(fixtures, name) {
			report("%s lists %s, and there is no such fixture", corpusReadme, name)
		}
	}

	slices.Sort(problems)
	return problems, nil
}

func moduleProblems(corpus, rel string, data []byte, toolchain string) []string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	slashed := filepath.ToSlash(rel)

	var problems []string
	report := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	want := corpusModulePrefix + strings.ReplaceAll(dir, "-", "")
	var path, directive string
	for line := range strings.Lines(string(data)) {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "module "):
			path = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "module ")), `"`)
		case strings.HasPrefix(trimmed, "go "):
			directive = strings.TrimSpace(strings.TrimPrefix(trimmed, "go "))
		case strings.HasPrefix(trimmed, "require"):
			report("%s has a require directive, which needs go.sum entries and a module cache inside "+
				"the snapshot: a fixture needs the standard library and nothing else", slashed)
		case strings.HasPrefix(trimmed, "replace"):
			report("%s has a replace directive, which makes the module unbuildable anywhere but here", slashed)
		}
	}
	if path != want {
		report("%s declares the module path %q, want %q: the domain is reserved for documentation "+
			"by RFC 2606, so no fixture can collide with a module somebody might publish",
			slashed, path, want)
	}
	switch {
	case directive == "":
		report("%s declares no `go` directive", slashed)
	case !directiveWithin(directive, toolchain):
		report("%s declares `go %s`, which is newer than the toolchain %s: every command against the "+
			"module would ask to download another one, and GOTOOLCHAIN=local turns that into an error",
			slashed, directive, toolchain)
	}
	return problems
}

var goVersion = regexp.MustCompile(`go(\d+)\.(\d+)(?:\.(\d+))?`)

func directiveWithin(directive, toolchain string) bool {
	found := goVersion.FindStringSubmatch(toolchain)
	if found == nil {
		return true
	}
	return compareVersions(directive, strings.TrimPrefix(found[0], "go")) <= 0
}

func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(left), len(right)) {
		if diff := versionField(left, i) - versionField(right, i); diff != 0 {
			return diff
		}
	}
	return 0
}

func versionField(fields []string, index int) int {
	if index >= len(fields) {
		return 0
	}
	digits := fields[index]
	for i, r := range digits {
		if r < '0' || r > '9' {
			digits = digits[:i]
			break
		}
	}
	value, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return value
}

func binaryFile(name string, data []byte) bool {
	if strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".exe") {
		return true
	}
	for _, magic := range [][]byte{
		[]byte("\x7fELF"),
		[]byte("MZ"),
		{0xcf, 0xfa, 0xed, 0xfe},
	} {
		if bytes.HasPrefix(data, magic) {
			return true
		}
	}
	return bytes.IndexByte(data, 0) >= 0
}

func ledgerFixtures(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.Lines(string(data)) {
		if found := corpusRow.FindStringSubmatch(strings.TrimSpace(line)); found != nil {
			names = append(names, found[1])
		}
	}
	slices.Sort(names)
	return names, nil
}

var fixtureHelpers = []string{
	"Copy", "Fixture", "FixtureTree", "FixtureWith", "Snapshot", "From", "options",
}

var fixtureCall = regexp.MustCompile(
	`(?:` + strings.Join(fixtureHelpers, "|") + `)\(\s*(?:t,\s*)?"([a-z][a-z0-9-]*)"`)

func fixturesNamedByTests(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && slices.Contains(skippedDirectories, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range fixtureCall.FindAllStringSubmatch(string(data), -1) {
			found = append(found, match[1])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(found)
	return slices.Compact(found), nil
}

func statExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
