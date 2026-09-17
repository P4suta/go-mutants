// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	schemaDir          = "schema"
	contractsPage      = "docs/json-schema.md"
	registryFile       = "internal/schemas/schemas.go"
	documentTypePrefix = "go-mutants/"
)

var schemaFileName = regexp.MustCompile(`schema/([a-z0-9-]+\.schema\.json)`)

var nativeCount = regexp.MustCompile(`publishes ([a-z]+) native document types`)

var wholeCount = regexp.MustCompile(`The ([a-z]+) that are whole documents`)

var shippedCount = regexp.MustCompile(`\*\*Status: ([a-z]+) schemas shipped`)

var numberWords = map[int]string{
	1: "one", 2: "two", 3: "three", 4: "four", 5: "five", 6: "six",
	7: "seven", 8: "eight", 9: "nine", 10: "ten", 11: "eleven", 12: "twelve",
}

func TestTheContractsPageNamesEverySchemaFile(t *testing.T) {
	t.Parallel()

	root := Root(t)
	published := publishedSchemas(t, root)
	if len(published) < 4 {
		t.Fatalf("the scan found %d schemas under %s/, which is too few to be this repository", len(published), schemaDir)
	}
	named := schemasNamedOn(t, root)

	for _, file := range published {
		if !slices.Contains(named, file) {
			t.Errorf("%s does not name schema/%s;\n"+
				"\ta schema nothing documents is one a consumer has to find by listing the repository", contractsPage, file)
		}
	}
	for _, file := range named {
		if !slices.Contains(published, file) {
			t.Errorf("%s names schema/%s, which this repository does not publish;\n"+
				"\tthe published schemas are %v", contractsPage, file, published)
		}
	}
}

func TestTheContractsPageDescribesEveryDocumentType(t *testing.T) {
	t.Parallel()

	root := Root(t)
	declared := declaredDocumentTypes(t, root)
	if len(declared) < 4 {
		t.Fatalf("the scan found %d document types in %s, which is too few to be this repository", len(declared), registryFile)
	}
	described := documentTypeSections(t, root)

	for _, documentType := range declared {
		if !slices.Contains(described, documentType) {
			t.Errorf("%s has no `## `+\"`%s`\"+` v1` section;\n"+
				"\ta type a consumer branches on and cannot then look up is a branch into prose that does not exist",
				contractsPage, documentType)
		}
	}
	for _, documentType := range described {
		if !slices.Contains(declared, documentType) {
			t.Errorf("%s describes %q, which %s does not declare;\n"+
				"\tthe declared types are %v", contractsPage, documentType, registryFile, declared)
		}
	}
}

func TestTheContractsPageCountsWhatItDescribes(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readPage(t, filepath.Join(root, filepath.FromSlash(contractsPage)))
	published := publishedSchemas(t, root)
	whole := 0
	for _, file := range published {
		if discriminatedSchema(t, filepath.Join(root, schemaDir, file)) {
			whole++
		}
	}

	for _, check := range []struct {
		what    string
		pattern *regexp.Regexp
		want    int
	}{
		{"schemas shipped", shippedCount, len(published)},
		{"native document types", nativeCount, len(declaredDocumentTypes(t, root))},
		{"whole documents", wholeCount, whole},
	} {
		want, ok := numberWords[check.want]
		if !ok {
			t.Fatalf("%s: this repository has %d %s, which the test cannot spell", contractsPage, check.want, check.what)
		}
		found := check.pattern.FindStringSubmatch(page)
		if found == nil {
			t.Errorf("%s holds no sentence matching %s, so its count of %s is not pinned to anything",
				contractsPage, check.pattern, check.what)
			continue
		}
		if found[1] != want {
			t.Errorf("%s says %q %s; this repository has %s (%d)",
				contractsPage, found[1], check.what, want, check.want)
		}
	}
}

func publishedSchemas(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(root, schemaDir))
	if err != nil {
		t.Fatalf("listing %s/: %v", schemaDir, err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".schema.json") {
			files = append(files, entry.Name())
		}
	}
	slices.Sort(files)
	return files
}

func schemasNamedOn(t *testing.T, root string) []string {
	t.Helper()

	page := readPage(t, filepath.Join(root, filepath.FromSlash(contractsPage)))
	var named []string
	for _, match := range schemaFileName.FindAllStringSubmatch(page, -1) {
		named = append(named, match[1])
	}
	slices.Sort(named)
	return slices.Compact(named)
}

func declaredDocumentTypes(t *testing.T, root string) []string {
	t.Helper()

	var types []string
	for _, constant := range exportedStringConstants(t, filepath.Join(root, filepath.FromSlash(registryFile))) {
		if strings.HasPrefix(constant.Value, documentTypePrefix) {
			types = append(types, constant.Value)
		}
	}
	slices.Sort(types)
	return types
}

func documentTypeSections(t *testing.T, root string) []string {
	t.Helper()

	page := readPage(t, filepath.Join(root, filepath.FromSlash(contractsPage)))
	var described []string
	for _, line := range strings.Split(page, "\n") {
		rest, ok := strings.CutPrefix(line, "## `"+documentTypePrefix)
		if !ok {
			continue
		}
		name, ok := strings.CutSuffix(strings.TrimSpace(rest), "` v1")
		if !ok {
			continue
		}
		described = append(described, documentTypePrefix+name)
	}
	slices.Sort(described)
	return described
}

func discriminatedSchema(t *testing.T, path string) bool {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var document struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return slices.Contains(document.Required, "document_type") &&
		slices.Contains(document.Required, "schema_version")
}
