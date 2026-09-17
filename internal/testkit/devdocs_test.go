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

const (
	developmentDoc = "docs/development.md"
	adrDirectory   = "docs/adr"
	adrIndex       = "README.md"
	miseFile       = "mise.toml"
)

var harnessPackages = []string{HarnessDir, HarnessDir + "/mutantkit"}

var harnessEnvPrefixes = []string{"GO_MUTANTS_TEST_", "TESTKIT_"}

var miseTaskPrefixes = []string{"test", "cover", "bench", "golden", "dogfood"}

var miseTaskLine = regexp.MustCompile(`^\[tasks\.([A-Za-z0-9_.-]+)\]\s*$`)

var adrFileName = regexp.MustCompile(`^[0-9]{4}-[a-z0-9-]+\.md$`)

func TestDevelopmentDocNamesEveryTestkitEnvironmentVariable(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, developmentDoc))
	variables := harnessEnvironmentVariables(t, root)

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

var commandPages = []string{developmentDoc, "docs/ci.md", "CLAUDE.md"}

var documentedPrograms = []string{"mise", "go", "go-mutants", "git", "du"}

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

type sourceConstant struct {
	Name  string
	Value string
	Doc   string
	File  string
}

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

func isTaskNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '.':
		return true
	}
	return false
}

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

func stripAssignments(line string) string {
	fields := strings.Fields(line)
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
		fields = fields[1:]
	}
	return strings.Join(fields, " ")
}

func indexLinks(index string) []string {
	var targets []string
	for _, match := range regexp.MustCompile(`\(([0-9]{4}-[a-z0-9-]+\.md)\)`).FindAllStringSubmatch(index, -1) {
		targets = append(targets, match[1])
	}
	return targets
}

func hasPrefixIn(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path built from the module root
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

const claudeDoc = "CLAUDE.md"

const ledgerHeading = "## The documentation ledger"

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
