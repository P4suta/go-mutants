// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The developer documentation these tests pin, relative to the module root.
//
// docs/development.md is the one page that says how the developer
// infrastructure fits together, and every fact in it is a fact about a file in
// this repository. Prose drifts silently: a variable renamed, a task added, an
// ADR written and never linked are all changes nobody would think to grep the
// documentation for. So the parts that *can* be derived are derived, and the
// page fails the build when it stops naming them.
const (
	developmentDoc = "docs/development.md"
	adrDirectory   = "docs/adr"
	adrIndex       = "README.md"
	miseFile       = "mise.toml"
)

// harnessPackages are the two halves of the test harness, relative to the
// module root. Both are scanned for the variables a developer types, because a
// variable that only one of them declares is still one somebody has to look up.
var harnessPackages = []string{HarnessDir, HarnessDir + "/mutantkit"}

// harnessEnvPrefixes are the namespaces the harness owns.
//
// GO_MUTANTS_TEST_ is the harness's half of the tool's own namespace — it is
// read before [Env] strips that prefix from a child — and TESTKIT_ is what the
// helper protocol uses precisely because it must *survive* that stripping. A
// third prefix would be a third thing to look up, which is why the list is
// short and why it is written down rather than inferred.
var harnessEnvPrefixes = []string{"GO_MUTANTS_TEST_", "TESTKIT_"}

// miseTaskPrefixes are the task names a developer runs to test, measure or
// regenerate something. Everything else in mise.toml — bootstrap, build, fmt,
// lint, check, hooks, package — is either a gate documented in CONTRIBUTING.md
// or a step of one, and belongs there rather than here.
var miseTaskPrefixes = []string{"test", "cover", "bench", "golden", "dogfood"}

// miseTaskLine matches a task heading in mise.toml.
var miseTaskLine = regexp.MustCompile(`^\[tasks\.([A-Za-z0-9_.-]+)\]\s*$`)

// adrFileName matches an ADR's file name: four digits, a dash, a slug.
var adrFileName = regexp.MustCompile(`^[0-9]{4}-[a-z0-9-]+\.md$`)

// TestDevelopmentDocNamesEveryTestkitEnvironmentVariable keeps the page that
// tells a contributor how to drive the harness from omitting a way to drive it.
//
// Every one of these variables exists because somebody could not otherwise get
// at something — the evidence a failed test had, the tools a job must have, the
// cache a suite fills — and a variable nobody can find is a variable nobody
// uses. The constants are read out of the source rather than listed here, so
// that a seventh one added tomorrow fails this test on the day it is added
// rather than on the day somebody needs it.
func TestDevelopmentDocNamesEveryTestkitEnvironmentVariable(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, developmentDoc))
	variables := harnessEnvironmentVariables(t, root)

	// A scan that stopped finding anything would pass this test in silence, and
	// the page would then be pinned to nothing at all. The floor is the count
	// at the time of writing: four for the keep policy, two the rest of the
	// harness reads, and the helper protocol's cover root.
	if len(variables) < 7 {
		t.Fatalf("found only %d harness environment variables (%v); the scan has stopped seeing them",
			len(variables), variables)
	}
	for _, name := range variables {
		if !strings.Contains(page, name) {
			t.Errorf("%s never names %s, so a developer who needs it has nowhere to read what it does",
				developmentDoc, name)
		}
	}
}

// TestDevelopmentDocNamesEveryMiseTestTask keeps the same page from omitting a
// command.
//
// mise.toml is where the tasks are defined and where the reasoning behind each
// one is written down, but nobody reads a build file to find out what to run.
// A task that exists and is documented nowhere is a task that gets reinvented
// as a hand-typed `go test` with the wrong flags — which is exactly how the
// suites came to fill the developer's own build cache.
func TestDevelopmentDocNamesEveryMiseTestTask(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, developmentDoc))
	tasks := miseTasks(t, root)

	var named []string
	for _, task := range tasks {
		if !hasPrefixIn(task, miseTaskPrefixes) {
			continue
		}
		named = append(named, task)
		if !mentionsTask(page, task) {
			t.Errorf("%s never says `mise run %s`, so the task is defined in %s and documented nowhere",
				developmentDoc, task, miseFile)
		}
	}
	if len(named) < 10 {
		t.Fatalf("found only %d testing tasks in %s (%v); the scan has stopped seeing them",
			len(named), miseFile, named)
	}
}

// commandPages are the pages whose `console` blocks are read back.
//
// A page that tells a contributor what to type is a page whose commands have to
// exist. There are three, and they are named rather than globbed so that adding
// one is a decision: the development guide, the CI page, and the working
// protocol at the repository root.
var commandPages = []string{developmentDoc, "docs/ci.md", "CLAUDE.md"}

// documentedPrograms are the programs a reader of those pages is told to run.
//
// `go-mutants` is the tool itself, reached through `go run ./cmd/go-mutants` or
// from a build; `git` is how a contributor inspects what a run left and what CI
// refuses to let them commit; `du` reads the size of a directory the harness
// owns.
var documentedPrograms = []string{"mise", "go", "go-mutants", "git", "du"}

// TestEveryDocumentedCommandIsAMiseTaskOrAGoCommand reads the pages' own
// command blocks back.
//
// A documented command that does not exist is worse than no command: it costs
// the reader the time to type it, the time to read the error, and the trust
// they had in the rest of the page. Every `mise run` in a `console` block has
// to name a real task, and every other command has to be one of the programs
// this repository documents — so a task renamed in mise.toml, or a paste from
// somebody's shell history, fails here.
func TestEveryDocumentedCommandIsAMiseTaskOrAGoCommand(t *testing.T) {
	t.Parallel()

	root := Root(t)
	tasks := miseTasks(t, root)

	for _, doc := range commandPages {
		page := readDoc(t, filepath.Join(root, filepath.FromSlash(doc)))
		commands := consoleCommands(page)
		if len(commands) == 0 {
			t.Errorf("%s holds no ```console block, so it is pinning nothing", doc)
			continue
		}
		for _, command := range commands {
			fields := strings.Fields(command)
			program := fields[0]
			if !slices.Contains(documentedPrograms, program) {
				t.Errorf("%s documents a command starting with %q, which is none of %v:\n\t%s",
					doc, program, documentedPrograms, command)
				continue
			}
			if program != "mise" {
				continue
			}
			if len(fields) < 3 || fields[1] != "run" {
				t.Errorf("%s documents %q; the only mise invocation this repository uses is `mise run <task>`",
					doc, command)
				continue
			}
			if !slices.Contains(tasks, fields[2]) {
				t.Errorf("%s documents `mise run %s`, which is not a task in %s: %v",
					doc, fields[2], miseFile, tasks)
			}
		}
	}
}

// TestADRIndexListsEveryADRFile keeps the record index and the records
// together, in both directions.
//
// docs/adr/README.md says a decision record is read in order, as the history of
// the design — which is only true if the index is the whole set. An ADR written
// and never linked is a decision nobody finds; a row pointing at a file that
// was renamed is a link that 404s in a repository browser and says nothing
// about why.
func TestADRIndexListsEveryADRFile(t *testing.T) {
	t.Parallel()

	root := Root(t)
	directory := filepath.Join(root, adrDirectory)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("reading %s: %v", adrDirectory, err)
	}
	index := readDoc(t, filepath.Join(directory, adrIndex))

	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == adrIndex {
			continue
		}
		if !adrFileName.MatchString(name) {
			t.Errorf("%s/%s is not named like an ADR (NNNN-slug.md)", adrDirectory, name)
			continue
		}
		files = append(files, name)
		if !strings.Contains(index, "("+name+")") {
			t.Errorf("%s/%s links to no ADR named %s, so the record is unreachable from the index",
				adrDirectory, adrIndex, name)
		}
	}
	if len(files) < 2 {
		t.Fatalf("found %d ADRs under %s; the scan has stopped seeing them", len(files), adrDirectory)
	}
	for _, target := range indexLinks(index) {
		if !slices.Contains(files, target) {
			t.Errorf("%s/%s links to %s, which does not exist", adrDirectory, adrIndex, target)
		}
	}
}

// harnessEnvironmentVariables is every variable the harness declares as an
// exported string constant, sorted.
//
// It reads the source rather than the constants themselves for one reason that
// decides the shape of this file: internal/testkit/mutantkit imports this
// package, so a test *in* this package cannot import it back. Parsing both
// trees is what lets one rule cover both halves of the harness, and it is the
// same technique the import gate in this package already uses.
func harnessEnvironmentVariables(t *testing.T, root string) []string {
	t.Helper()

	var names []string
	for _, pkg := range harnessPackages {
		directory := filepath.Join(root, filepath.FromSlash(pkg))
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatalf("reading %s: %v", pkg, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			names = append(names, constantsIn(t, filepath.Join(directory, name))...)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// A sourceConstant is one exported string constant as the source declares it:
// its name, its value, the documentation above it, and the file it is in.
//
// The doc comment is the part that makes this worth having rather than a list
// of values. A ledger over environment variables needs only the value; a ledger
// over diagnostic codes needs the sentence beside each one, and that sentence
// exists nowhere but the source -- it cannot be reached from a running program.
type sourceConstant struct {
	Name  string
	Value string
	Doc   string
	File  string
}

// exportedStringConstants is every exported string constant one file declares,
// with the documentation a reader would find above it.
//
// Parsed with parser.ParseComments so that Doc is populated, and the comment
// taken from the ValueSpec when it has one and from the enclosing GenDecl
// otherwise -- which is how a single-spec `const ( // doc \n Name = "v" )` and a
// block of documented specs both read the way a reader reads them.
func exportedStringConstants(t *testing.T, path string) []sourceConstant {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var found []sourceConstant
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			doc := value.Doc
			if doc == nil && len(general.Specs) == 1 {
				doc = general.Doc
			}
			for index, name := range value.Names {
				if !name.IsExported() || index >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					continue
				}
				found = append(found, sourceConstant{
					Name:  name.Name,
					Value: unquoted,
					Doc:   doc.Text(),
					File:  path,
				})
			}
		}
	}
	return found
}

// constantsIn is every exported string constant in one file whose value names a
// variable in one of the harness's namespaces.
func constantsIn(t *testing.T, path string) []string {
	t.Helper()

	var names []string
	for _, constant := range exportedStringConstants(t, path) {
		if hasPrefixIn(constant.Value, harnessEnvPrefixes) {
			names = append(names, constant.Value)
		}
	}
	return names
}

// miseTasks is every task name mise.toml defines, in file order.
func miseTasks(t *testing.T, root string) []string {
	t.Helper()

	var tasks []string
	for _, line := range strings.Split(readDoc(t, filepath.Join(root, miseFile)), "\n") {
		if match := miseTaskLine.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			tasks = append(tasks, match[1])
		}
	}
	if len(tasks) == 0 {
		t.Fatalf("%s defines no tasks; the scan has stopped seeing them", miseFile)
	}
	return tasks
}

// mentionsTask reports whether the page names `mise run <task>` as a whole
// word.
//
// The boundary is the whole point. `mise run test` is a prefix of `mise run
// test-race`, so a plain substring search would report the page as naming the
// unit tier when all it ever mentions is the race job.
func mentionsTask(page, task string) bool {
	needle := "mise run " + task
	for offset := 0; ; {
		index := strings.Index(page[offset:], needle)
		if index < 0 {
			return false
		}
		after := offset + index + len(needle)
		if after == len(page) || !isTaskNameByte(page[after]) {
			return true
		}
		offset = after
	}
}

// isTaskNameByte reports whether a byte could continue a task name.
func isTaskNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '.':
		return true
	}
	return false
}

// consoleCommands is every command inside a ```console block.
//
// Only that fence, because it is the one whose contents are meant to be typed:
// a ```text block is a quoted transcript and a ```toml one is a file. A line
// ending in a backslash is joined to the one below it, so a command wrapped for
// the page's width is read as the one command it is rather than as a line
// starting with `\`.
func consoleCommands(page string) []string {
	var commands []string
	inConsole, pending := false, ""
	for _, line := range strings.Split(page, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inConsole = trimmed == "```console"
			pending = ""
			continue
		}
		if !inConsole || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if continuation := strings.TrimSuffix(trimmed, `\`); continuation != trimmed {
			pending += strings.TrimSpace(continuation) + " "
			continue
		}
		command := stripAssignments(strings.TrimSpace(pending + trimmed))
		pending = ""
		if command != "" {
			commands = append(commands, command)
		}
	}
	return commands
}

// stripAssignments removes the leading `NAME=VALUE` words of a command line, so
// that `GO_MUTANTS_TEST_KEEP=1 go test ...` is reported as a `go` command.
func stripAssignments(line string) string {
	fields := strings.Fields(line)
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
		fields = fields[1:]
	}
	return strings.Join(fields, " ")
}

// indexLinks is every ADR file name the index links to.
func indexLinks(index string) []string {
	var targets []string
	for _, match := range regexp.MustCompile(`\(([0-9]{4}-[a-z0-9-]+\.md)\)`).FindAllStringSubmatch(index, -1) {
		targets = append(targets, match[1])
	}
	return targets
}

// hasPrefixIn reports whether s starts with any of the prefixes.
func hasPrefixIn(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// readDoc reads one documentation file, failing the test when it is missing.
//
// A missing page is a failure rather than a skip: these tests exist because the
// page is part of the contract, and a suite that quietly passed when somebody
// deleted it would be pinning nothing.
func readDoc(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path built from the module root
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// claudeDoc is the working protocol at the repository root.
const claudeDoc = "CLAUDE.md"

// ledgerHeading opens its table of which test pins which page.
const ledgerHeading = "## The documentation ledger"

// TestTheDocumentationLedgerNamesFilesThatExist keeps the index of ledgers from
// becoming one more thing to go stale.
//
// The table in CLAUDE.md is what makes the discipline transmissible: it is
// where somebody learns that a page which enumerates something gets a test in
// the same change. A table naming a test file that has been renamed teaches the
// opposite, and does it to the reader most likely to believe it.
//
// The check is that every file it names is there -- both columns, the pages and
// the tests. What it cannot check is the other direction, because "this test
// pins a page" is not a property a scan can see; that half is what the rule in
// the paragraph under the table is for.
func TestTheDocumentationLedgerNamesFilesThatExist(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, claudeDoc))
	start := strings.Index(page, ledgerHeading)
	if start < 0 {
		t.Fatalf("%s has no %q section", claudeDoc, ledgerHeading)
	}
	section := page[start:]
	if end := strings.Index(section[len(ledgerHeading):], "\n## "); end >= 0 {
		section = section[:len(ledgerHeading)+end]
	}

	named := 0
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		for _, token := range backtickedTokens(trimmed) {
			if !strings.HasSuffix(token, ".go") && !strings.HasSuffix(token, ".md") &&
				!strings.HasSuffix(token, ".toml") {
				continue
			}
			named++
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(token))); err != nil {
				t.Errorf("%s's ledger names %s, which is not there: %v", claudeDoc, token, err)
			}
		}
	}
	if named < 20 {
		t.Fatalf("the ledger names %d files, which is too few to be the table; the parser has stopped seeing it", named)
	}
}
