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

// corpusModulePrefix is the module path every fixture's own path begins with.
//
// `example` is reserved for documentation by RFC 2606, so no fixture path can
// collide with a module somebody might publish and no `go get` of one can reach
// the network. See fixtures/README.md.
const corpusModulePrefix = "fixture.example/"

// corpusStatePrefix is the name prefix of the state a run leaves in a workspace
// that is not the report directory: `.go-mutants.toml`, and anything else the
// tool ever decides to keep beside it.
const corpusStatePrefix = ".go-mutants"

// corpusReadme is the ledger, relative to the corpus directory.
const corpusReadme = "README.md"

// corpusRow matches one row of the ledger's corpus table: a directory name in
// the first column and a module path under [corpusModulePrefix] in the second.
//
// Requiring both is what separates the corpus table from the other tables in
// the same document — the families fixture's list of under-tested functions has
// a first column too, and its cells are function names rather than directories.
var corpusRow = regexp.MustCompile("^\\|\\s*`([^`]+)/`\\s*\\|\\s*`" + regexp.QuoteMeta(corpusModulePrefix))

// TestCorpusConformance is the gate that keeps every fixture the same kind of
// thing.
//
// fixtures/README.md states the conventions in prose, and prose is exactly what
// a new fixture is written without reading. Each rule below is one somebody
// would otherwise break silently and find out about days later, in a failure
// that names something else entirely:
//
//   - A missing `go.mod` makes a fixture a package of this repository, so
//     `go test ./...` compiles it — and a fixture that fails on purpose fails
//     this repository's own suite.
//   - A `require` needs `go.sum` entries and a module cache inside the
//     snapshot, which makes the integration suite depend on the network.
//   - A `go` directive above the toolchain in use makes every command against
//     the module ask to download another one, which GOTOOLCHAIN=local — set by
//     the environment policy so no test can fetch a compiler — turns into an
//     error.
//   - A CRLF line ending changes the bytes of a file, and a mutant identity is
//     the digest of the bytes. A fixture checked in with one would produce
//     different ids on the machine that wrote it and on every other.
//   - A fixture nothing drives is a module that costs a checkout and proves
//     nothing, and a ledger row with no directory under it is a fixture
//     somebody deleted without reading what it was for.
//   - `reports/`, `.go-mutants*` state and compiled binaries under `fixtures/`
//     are what a run pointed at the corpus by accident leaves behind. CI
//     already fails on them through `git status --porcelain --ignored --
//     fixtures`; this says the same thing on a developer's machine, where that
//     command's strict reading is not affordable.
//
// The scan is a function of a root rather than of this repository, which is
// what lets the rules themselves be tested against a corpus built to break
// them — see [TestCorpusConformanceReportsEveryKindOfBreach]. A gate whose only
// evidence is that it passes on a conforming tree is a gate nobody has seen
// fail.
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

// TestCorpusConformanceReportsEveryKindOfBreach is the gate pointed at a corpus
// built to fail it, one convention at a time.
//
// Every row is a fixture that is wrong in exactly one way, and each is checked
// for the sentence naming it. A scan that silently stopped applying one of its
// rules — a walk that no longer descends into nested modules, a header check
// that reads only the first line — would go on passing this repository's own
// corpus for ever, because this repository's corpus conforms.
func TestCorpusConformanceReportsEveryKindOfBreach(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		// build writes one fixture into <root>/fixtures/<name>/.
		build func(t *testing.T, dir string)
		// want is a phrase the problem naming this fixture has to hold.
		want string
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
			// The header stays LF and only the body is converted, so that this
			// row breaks the line-ending convention and no other: a file whose
			// first line ended CRLF would fail the SPDX prefix check too, and
			// the row would pass on the wrong problem.
			WriteFile(t, filepath.Join(dir, "carriage.go"),
				[]byte(SPDXHeader+"package carriage\r\n"))
		},
		want: "CRLF",
	}, {
		// A `go.mod` with no header, which the walk used to answer for with the
		// module arm and return before it ever asked about SPDX.
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
			// Every fixture but "undocumented" is in the ledger, and every
			// fixture but "undriven" is named by a test, so that each row
			// breaks the one convention it is about and no other.
			ledger := "| `" + test.name + "/` | `" + corpusModulePrefix + test.name + "` | a specimen |\n"
			if test.name == "undocumented" {
				ledger = ""
			}
			WriteFile(t, filepath.Join(root, FixturesDir, corpusReadme), []byte(ledger))
			naming := "package p\n\nvar _ = testkit.Copy(t, " + strconv.Quote(test.name) + ")\n"
			if test.name == "undriven" {
				// Named twice, and driven by neither: once in a sentence about
				// it, and once as a bare literal of the kind a report field name
				// or a table row would be. Both satisfied the first version of
				// this rule, which is why the row is written this way rather
				// than as a file that never mentions the fixture at all.
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

// TestCorpusConformanceAcceptsAConformingFixture is the other half of the table
// above, and the one that keeps it honest.
//
// A scan that reported every fixture as broken would satisfy every row of
// [TestCorpusConformanceReportsEveryKindOfBreach] and would be useless. This
// builds the same shape with nothing wrong with it — a nested module included,
// which is the case a walk that stopped at the top level would miss — and
// requires silence.
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

// writeFixtureModule writes the go.mod of one corpus module, header and all.
func writeFixtureModule(t testing.TB, dir, path, directive string) {
	t.Helper()
	WriteFile(t, filepath.Join(dir, "go.mod"), []byte(SPDXHeader+"module "+path+"\n\ngo "+directive+"\n"))
}

// corpusProblems reports every way the corpus under root breaks the conventions
// fixtures/README.md states, one sentence per problem, sorted.
//
// toolchain is a `runtime.Version()` string — "go1.26.6", or "devel go1.27-…"
// on a development build — and is the only fact about the machine the scan
// consults. It is a parameter rather than a call because the rule it feeds is
// one of the rules under test: a scan that could not be shown a toolchain would
// have to be tested against whichever one the run happened to have.
//
// The toolchain is read from this process rather than from a `go version`
// child, and that is deliberate. This test file belongs in the tier a developer
// runs on every save; a child `go` command would put it in the integration tier
// or in internal/testkit/testdata/unit-toolchain-allowlist.txt, and the ledger
// is meant to shrink. `runtime.Version()` is the toolchain that compiled and is
// running this binary, which under GOTOOLCHAIN=local is the toolchain every
// child would report anyway.
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
		// The header check comes before the rest and asks about the file's name
		// rather than about what else was found in it, because the three kinds
		// of file that carry one are checked by three different arms below and
		// a header missing from a `go.mod` used to be reported by none of them:
		// the `go.mod` arm answered first and returned. Every rule here is
		// independent of every other, and a file that breaks two has to be told
		// about both.
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

// moduleProblems reads one fixture go.mod: its module path, its `go` directive,
// and the two directives a fixture may not have.
//
// The parse is the handful of lines it needs rather than golang.org/x/mod, for
// the reason every other read in this package is: the harness's import list
// holds nothing from this module, and outside the standard library only
// github.com/google/go-cmp.
func moduleProblems(corpus, rel string, data []byte, toolchain string) []string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	slashed := filepath.ToSlash(rel)

	var problems []string
	report := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	// `failing-baseline/` is `fixture.example/failingbaseline`: a module path
	// element may hold a hyphen, but no fixture's does, and one rule that
	// derives the path from the directory is worth more than a convention
	// nobody can check.
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

// goVersion matches the release token inside a `runtime.Version()` string,
// which is "go1.26.6" on a release build and "devel go1.27-a1b2c3d4" on a
// development one.
var goVersion = regexp.MustCompile(`go(\d+)\.(\d+)(?:\.(\d+))?`)

// directiveWithin reports whether a `go` directive is at most the release the
// toolchain string names.
//
// A toolchain string this cannot read answers true, which is the safe
// direction: a scan that refused every fixture because it did not recognise a
// development toolchain would be a gate nobody could run.
func directiveWithin(directive, toolchain string) bool {
	found := goVersion.FindStringSubmatch(toolchain)
	if found == nil {
		return true
	}
	return compareVersions(directive, strings.TrimPrefix(found[0], "go")) <= 0
}

// compareVersions orders two dotted numeric versions, treating a missing
// component as zero and stopping at the first component that is not a number —
// which is how "1.27rc1" compares equal to "1.27" here, the only reading a
// fixture's `go` directive ever needs.
func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(left), len(right)) {
		if diff := versionField(left, i) - versionField(right, i); diff != 0 {
			return diff
		}
	}
	return 0
}

// versionField reads one dotted component as a number, or zero.
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

// binaryFile reports whether a file in the corpus is a compiled artefact rather
// than source.
//
// Three questions, because one of them alone lets something through: the
// executable formats of the three platforms this runs on by their magic bytes,
// the two extensions the go command writes (`.test` from `go test -c`, `.exe`
// from a Windows `go build`), and a NUL byte, which no text file the corpus
// holds has.
func binaryFile(name string, data []byte) bool {
	if strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".exe") {
		return true
	}
	for _, magic := range [][]byte{
		[]byte("\x7fELF"),        // Linux
		[]byte("MZ"),             // Windows
		{0xcf, 0xfa, 0xed, 0xfe}, // macOS, 64-bit little-endian Mach-O
	} {
		if bytes.HasPrefix(data, magic) {
			return true
		}
	}
	return bytes.IndexByte(data, 0) >= 0
}

// ledgerFixtures reads the fixture names out of the corpus table in
// fixtures/README.md, sorted.
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

// fixtureHelpers are the calls that take a corpus name, by the last identifier
// before the parenthesis.
//
// It is a ledger of what the suites actually write, and it is deliberately not
// "any quoted word". The first version of this scan looked for `"tagged"`
// anywhere in any `_test.go`, and almost every fixture in the corpus was
// answered for by something that is not a driver at all: the word `coverage` is
// a field of every run report, `workspace` is a field of two, and a sentence in
// a comment naming a fixture satisfied the rule as well as a test that runs it.
// A gate that a comment can satisfy is a gate that says nothing about whether
// anything drives the module.
//
// Each entry is matched as the tail of the callee's name, which is what lets one
// short list cover the spellings the suites use: `testkit.Copy(t, …)`,
// `testkit.Fixture(t, …)`, `copyFixture(t, …)`, `copyFixtureTree(…)`,
// `prepareFixtureWith(…)`, `mutantkit.Snapshot(t, …)`,
// `NewModule(t).From(…)`, and internal/engine's own `options(t, …)`.
//
// It can go stale, and it fails loudly when it does: a fixture driven only
// through a helper nobody wrote down here is reported as driven by nothing, and
// the answer is to add the helper's name below rather than to loosen the match.
var fixtureHelpers = []string{
	"Copy", "Fixture", "FixtureTree", "FixtureWith", "Snapshot", "From", "options",
}

// fixtureCall matches a corpus name passed to one of [fixtureHelpers], with the
// `t` first argument the harness's helpers take and the fixture-first form the
// root package's own helpers use.
var fixtureCall = regexp.MustCompile(
	`(?:` + strings.Join(fixtureHelpers, "|") + `)\(\s*(?:t,\s*)?"([a-z][a-z0-9-]*)"`)

// fixturesNamedByTests lists the corpus names some `_test.go` in the repository
// hands to one of [fixtureHelpers], sorted.
//
// A text scan rather than a parse, for the reason every other read in this
// package is: `go/types` would need the whole module loaded, with a toolchain,
// in the tier this test belongs to. The corpus itself is not scanned, because a
// fixture's own test file naming its own directory would answer for it.
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

// statExists reports whether a path is there.
func statExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
