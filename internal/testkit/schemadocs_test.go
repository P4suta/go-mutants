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

// docs/json-schema.md is the page a consumer reads before decoding anything
// this tool writes, and it enumerates three sets the repository also
// enumerates: the schema files, the document types, and how many of each there
// are. Nothing pinned it. A schema could be published, registered, embedded and
// validated against with the page still describing the set it belonged to
// before -- which is what had happened: the page counted four native document
// types after a fifth arrived.
//
// The counts are pinned as words rather than left to prose because the words
// are the part that goes stale silently. A section heading that is missing is
// visible to anybody reading the page; "four" where five are described reads
// perfectly and is simply false.
//
// The scan is source-level, as every ledger in this package is: the harness may
// import nothing from this module (see env_test.go), so the document types are
// read out of internal/schemas/schemas.go with go/ast and the schema files out
// of the directory.

const (
	// schemaDir holds every schema this repository publishes.
	schemaDir = "schema"
	// contractsPage is the page that describes them.
	contractsPage = "docs/json-schema.md"
	// registryFile is where the document types are declared.
	registryFile = "internal/schemas/schemas.go"
	// documentTypePrefix is the namespace a native document type is in. The
	// vendored Stryker schema is somebody else's contract and is in none.
	documentTypePrefix = "go-mutants/"
)

// schemaFileName matches a schema this repository publishes, as the page spells
// one: always with its directory, so that a bare filename in prose is not
// mistaken for a claim about a file.
var schemaFileName = regexp.MustCompile(`schema/([a-z0-9-]+\.schema\.json)`)

// nativeCount matches the sentence that says how many document types there are.
var nativeCount = regexp.MustCompile(`publishes ([a-z]+) native document types`)

// wholeCount matches the sentence that says how many of them are whole
// documents discriminated by the two fields.
var wholeCount = regexp.MustCompile(`The ([a-z]+) that are whole documents`)

// shippedCount matches the Status line's count of schemas.
var shippedCount = regexp.MustCompile(`\*\*Status: ([a-z]+) schemas shipped`)

// numberWords spells the counts this page can honestly hold. A repository with
// more than a dozen schemas has a different page, and a test that cannot spell
// the number it read fails rather than passing quietly.
var numberWords = map[int]string{
	1: "one", 2: "two", 3: "three", 4: "four", 5: "five", 6: "six",
	7: "seven", 8: "eight", 9: "nine", 10: "ten", 11: "eleven", 12: "twelve",
}

// TestTheContractsPageNamesEverySchemaFile keeps the page and the directory
// equal, in both directions.
//
// A published schema the page does not name is one a consumer has no way to
// find; a schema the page names and the directory does not hold is a path
// somebody will try to fetch.
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

// TestTheContractsPageDescribesEveryDocumentType keeps the page's sections and
// the declared document types equal, in both directions.
//
// The heading is the unit rather than a mention, because a document type named
// only in passing is one whose fields are nowhere: the page's promise is that a
// consumer branching on `document_type` can find out what follows the branch.
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

// TestTheContractsPageCountsWhatItDescribes turns the page's three counts from
// prose into claims.
//
// All three are derived rather than listed. The schemas are the files; the
// native types are the constants; and which of them are *whole documents* is
// read out of each schema's own `required` list -- a document discriminated by
// `document_type` and `schema_version` requires both of them, and the one that
// is a stream of lines rather than a document requires neither.
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

// publishedSchemas is every schema file this repository publishes, sorted.
//
// The top level of schema/ only: the vendored third-party schemas live in a
// subdirectory behind an embed of their own, for the reason internal/schemas
// gives -- they are somebody else's contract, and this page describes what
// go-mutants defines.
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

// schemasNamedOn is every schema file the contracts page names, sorted and
// without repeats.
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

// declaredDocumentTypes is every native document type internal/schemas
// declares, sorted.
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

// documentTypeSections is every document type the contracts page gives a
// section of its own, sorted.
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

// discriminatedSchema reports whether a schema describes a whole document
// discriminated by the pair of fields the page tells a consumer to check.
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
