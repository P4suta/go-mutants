// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
)

const fixtureModule = "fixture.example/discovery"

type listedMutant struct {
	path        string
	line        int
	column      int
	family      string
	rule        string
	original    string
	replacement string
}

var wantMutants = []listedMutant{
	{"compare/compare.go", 16, 5, "condition-negation", "negate-condition", "a == b", "!(a == b)"},
	{"compare/compare.go", 16, 7, "comparison", "eq-to-neq", "==", "!="},
	{"compare/compare.go", 17, 10, "return-replacement", "return-empty-string", "\"eq\"", "\"\""},
	{"compare/compare.go", 19, 5, "condition-negation", "negate-condition", "a != b", "!(a != b)"},
	{"compare/compare.go", 19, 7, "comparison", "neq-to-eq", "!=", "=="},
	{"compare/compare.go", 20, 10, "return-replacement", "return-empty-string", "\"ne\"", "\"\""},
	{"compare/compare.go", 22, 5, "condition-negation", "negate-condition", "a < b", "!(a < b)"},
	{"compare/compare.go", 22, 7, "comparison", "lt-to-le", "<", "<="},
	{"compare/compare.go", 23, 10, "return-replacement", "return-empty-string", "\"lt\"", "\"\""},
	{"compare/compare.go", 25, 5, "condition-negation", "negate-condition", "a <= b", "!(a <= b)"},
	{"compare/compare.go", 25, 7, "comparison", "le-to-lt", "<=", "<"},
	{"compare/compare.go", 26, 10, "return-replacement", "return-empty-string", "\"le\"", "\"\""},
	{"compare/compare.go", 28, 5, "condition-negation", "negate-condition", "a > b", "!(a > b)"},
	{"compare/compare.go", 28, 7, "comparison", "gt-to-ge", ">", ">="},
	{"compare/compare.go", 29, 10, "return-replacement", "return-empty-string", "\"gt\"", "\"\""},
	{"compare/compare.go", 31, 5, "condition-negation", "negate-condition", "a >= b", "!(a >= b)"},
	{"compare/compare.go", 31, 7, "comparison", "ge-to-gt", ">=", ">"},
	{"compare/compare.go", 32, 10, "return-replacement", "return-empty-string", "\"ge\"", "\"\""},
	{"compare/compare.go", 34, 9, "return-replacement", "return-empty-string", "\"none\"", "\"\""},
	{"compare/compare.go", 39, 8, "boolean-literal", "true-to-false", "true", "false"},
	{"compare/compare.go", 40, 9, "boolean-literal", "false-to-true", "false", "true"},
	{"compare/compare.go", 41, 9, "return-replacement", "return-true", "on", "true"},
	{"compare/compare.go", 41, 9, "return-replacement", "return-false", "on", "false"},
	{"compare/compare.go", 41, 13, "return-replacement", "return-true", "off", "true"},
	{"compare/compare.go", 41, 13, "return-replacement", "return-false", "off", "false"},
	{"compare/compare.go", 48, 9, "return-replacement", "return-zero-numeric", "m[true]", "0"},
	{"compare/compare.go", 48, 11, "boolean-literal", "true-to-false", "true", "false"},
	{"generics/generics.go", 16, 5, "condition-negation", "negate-condition", "a > b", "!(a > b)"},
	{"generics/generics.go", 16, 7, "comparison", "gt-to-ge", ">", ">="},
	{"generics/generics.go", 31, 9, "return-replacement", "return-zero-numeric", "sized[[len([1]bool{false})]byte](v)[0]", "0"},
	{"generics/generics.go", 42, 44, "return-replacement", "return-zero-numeric", "b.v[0]", "0"},
	{"generics/generics.go", 56, 9, "return-replacement", "return-zero-numeric", "len(p.key) + len(p.value)", "0"},
	{"generics/generics.go", 56, 20, "integer-arithmetic", "add-to-sub", "+", "-"},
	{"shadow/shadow.go", 23, 27, "return-replacement", "return-zero-numeric", "true", "0"},
	{"shadow/shadow.go", 28, 12, "integer-arithmetic", "add-to-sub", "+", "-"},
	{"shadow/shadow.go", 29, 9, "return-replacement", "return-zero-numeric", "true", "0"},
	{"shadow/shadow.go", 34, 32, "boolean-literal", "false-to-true", "false", "true"},
	{"suppressed/suppressed.go", 36, 26, "return-replacement", "return-zero-numeric", "len(Buffer{})", "0"},
	{"suppressed/suppressed.go", 59, 33, "return-replacement", "return-empty-string", "Data", "\"\""},
	{"suppressed/suppressed.go", 66, 5, "condition-negation", "negate-condition", "limit", "!(limit)"},
	{"suppressed/suppressed.go", 67, 10, "return-replacement", "return-zero-numeric", "a", "0"},
	{"suppressed/suppressed.go", 81, 9, "comparison", "eq-to-neq", "==", "!="},
	{"suppressed/suppressed.go", 82, 6, "condition-negation", "negate-condition", "ok == true", "!(ok == true)"},
	{"suppressed/suppressed.go", 82, 9, "comparison", "eq-to-neq", "==", "!="},
	{"suppressed/suppressed.go", 82, 12, "boolean-literal", "true-to-false", "true", "false"},
	{"suppressed/suppressed.go", 83, 11, "return-replacement", "return-empty-string", "\"equal and ok\"", "\"\""},
	{"suppressed/suppressed.go", 85, 10, "comparison", "eq-to-neq", "==", "!="},
	{"suppressed/suppressed.go", 85, 13, "boolean-literal", "false-to-true", "false", "true"},
	{"suppressed/suppressed.go", 86, 10, "return-replacement", "return-empty-string", "\"not ok\"", "\"\""},
	{"suppressed/suppressed.go", 89, 9, "integer-arithmetic", "add-to-sub", "+", "-"},
	{"suppressed/suppressed.go", 90, 10, "return-replacement", "return-empty-string", "\"one more\"", "\"\""},
	{"suppressed/suppressed.go", 91, 9, "integer-arithmetic", "mul-to-div", "*", "/"},
	{"suppressed/suppressed.go", 92, 10, "return-replacement", "return-empty-string", "\"twice\"", "\"\""},
	{"suppressed/suppressed.go", 96, 6, "condition-negation", "negate-condition", "v > b", "!(v > b)"},
	{"suppressed/suppressed.go", 96, 8, "comparison", "gt-to-ge", ">", ">="},
	{"suppressed/suppressed.go", 97, 11, "return-replacement", "return-empty-string", "\"greater\"", "\"\""},
	{"suppressed/suppressed.go", 100, 10, "return-replacement", "return-empty-string", "v", "\"\""},
	{"suppressed/suppressed.go", 102, 9, "return-replacement", "return-empty-string", "\"none\"", "\"\""},
	{"suppressed/suppressed.go", 114, 16, "comparison", "lt-to-le", "<", "<="},
	{"suppressed/suppressed.go", 115, 10, "return-replacement", "return-empty-string", "\"sent\"", "\"\""},
	{"suppressed/suppressed.go", 117, 6, "condition-negation", "negate-condition", "v == true", "!(v == true)"},
	{"suppressed/suppressed.go", 117, 8, "comparison", "eq-to-neq", "==", "!="},
	{"suppressed/suppressed.go", 117, 11, "boolean-literal", "true-to-false", "true", "false"},
	{"suppressed/suppressed.go", 118, 11, "return-replacement", "return-empty-string", "\"received\"", "\"\""},
	{"suppressed/suppressed.go", 121, 9, "return-replacement", "return-empty-string", "\"none\"", "\"\""},
}

var wantSkips = []catalogSkip{
	{Path: "generated/generated.go", Reason: "generated", Count: 1},
	{Path: "generics/generics.go", Reason: "type-param", Count: 5},
	{Path: "suppressed/suppressed.go", Reason: "array-length", Count: 2},
	{Path: "suppressed/suppressed.go", Reason: "const-decl", Count: 4},
	{Path: "suppressed/suppressed.go", Reason: "package-var-init", Count: 4},
}

func inFixture(t *testing.T) string {
	t.Helper()
	root := testkit.Copy(t, "discovery")
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	t.Chdir(root)
	return temp
}

func list(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := ExecuteContext(t.Context(), append([]string{"list"}, args...), &out, &errOut)
	if code != 0 {
		t.Fatalf("`go-mutants list %s` exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

func decodeCatalog(t *testing.T, data []byte) catalogDocument {
	t.Helper()
	if err := schemas.Validate(schemas.CatalogV1, data); err != nil {
		t.Fatalf("the document does not satisfy %s: %v\n%s", schemas.CatalogV1, err, data)
	}
	var doc catalogDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		t.Fatalf("decoding the document: %v\n%s", err, data)
	}
	return doc
}

func listed(doc catalogDocument) []listedMutant {
	out := make([]listedMutant, 0, len(doc.Mutants))
	for _, m := range doc.Mutants {
		out = append(out, listedMutant{
			path:        m.Path,
			line:        m.Line,
			column:      m.Column,
			family:      m.Family,
			rule:        m.Rule,
			original:    m.Original,
			replacement: m.Replacement,
		})
	}
	return out
}

func diffMutants(got, want []listedMutant) string {
	var b strings.Builder
	b.WriteString("got:\n")
	for _, m := range got {
		fmt.Fprintf(&b, "  %s:%d:%d  %s/%s  %s -> %s\n", m.path, m.line, m.column, m.family, m.rule, m.original, m.replacement)
	}
	b.WriteString("want:\n")
	for _, m := range want {
		fmt.Fprintf(&b, "  %s:%d:%d  %s/%s  %s -> %s\n", m.path, m.line, m.column, m.family, m.rule, m.original, m.replacement)
	}
	return b.String()
}

func TestListDiscoversExactlyTheFixtureCandidates(t *testing.T) {
	inFixture(t)
	stdout, stderr := list(t, "--json")
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing on a clean listing", stderr)
	}
	doc := decodeCatalog(t, []byte(stdout))

	if got := listed(doc); !slices.Equal(got, wantMutants) {
		t.Fatalf("the catalogue is not the expected one:\n%s", diffMutants(got, wantMutants))
	}

	for _, m := range doc.Mutants {
		if strings.HasPrefix(m.Path, "generated/") {
			t.Errorf("%s:%d is a candidate in a generated file", m.Path, m.Line)
		}
		if m.Path == "shadow/shadow.go" && m.Family == "boolean-literal" && m.Line != 34 {
			t.Errorf("shadow/shadow.go:%d is a boolean-literal candidate; only the universe `false` on line 34 may be one", m.Line)
		}
	}

	seen := make(map[string]bool, len(doc.Mutants))
	for _, m := range doc.Mutants {
		if len(m.ID) != 64 || !isLowerHex(m.ID) {
			t.Errorf("mutant id %q is not 64 lowercase hex characters", m.ID)
		}
		if len(m.DisplayID) != 20 || !strings.HasPrefix(m.ID, m.DisplayID) {
			t.Errorf("display id %q is not the 20 character prefix of %q", m.DisplayID, m.ID)
		}
		if seen[m.ID] {
			t.Errorf("mutant id %s appears twice", m.ID)
		}
		seen[m.ID] = true

		want := fixtureModule + "/" + path.Dir(m.Path)
		if m.Package != want {
			t.Errorf("%s is in package %q, want %q", m.Path, m.Package, want)
		}
		if m.EndByte-m.StartByte != uint32(len(m.Original)) {
			t.Errorf("%s:%d spans %d bytes for the %d byte original %q",
				m.Path, m.Line, m.EndByte-m.StartByte, len(m.Original), m.Original)
		}
	}
}

func TestListRecordsEverySuppressedContext(t *testing.T) {
	inFixture(t)
	stdout, _ := list(t, "--json")
	doc := decodeCatalog(t, []byte(stdout))

	if !slices.Equal(doc.Skips, wantSkips) {
		t.Errorf("skips = %+v, want %+v", doc.Skips, wantSkips)
	}
}

func TestListDescribesTheWorkspaceAndTheSelection(t *testing.T) {
	inFixture(t)
	stdout, _ := list(t, "--json")
	doc := decodeCatalog(t, []byte(stdout))

	if doc.DocumentType != schemas.CatalogV1 {
		t.Errorf("document_type = %q, want the type internal/schemas validates (%q)",
			doc.DocumentType, schemas.CatalogV1)
	}
	if doc.SchemaVersion != 1 || doc.ToolVersion != Version {
		t.Errorf("schema_version/tool_version = %d/%q, want 1/%q", doc.SchemaVersion, doc.ToolVersion, Version)
	}
	if doc.Workspace.ModulePath != fixtureModule {
		t.Errorf("module_path = %q, want %q", doc.Workspace.ModulePath, fixtureModule)
	}
	if doc.Workspace.GoVersion == "" {
		t.Error("go_version is empty, which the schema refuses")
	}
	if len(doc.Workspace.WorkspaceDigest) != 64 || !isLowerHex(doc.Workspace.WorkspaceDigest) {
		t.Errorf("workspace_digest = %q, want the snapshot manifest's 64 hex character digest", doc.Workspace.WorkspaceDigest)
	}
	if doc.Workspace.Platform.OS != runtime.GOOS || doc.Workspace.Platform.Arch != runtime.GOARCH {
		t.Errorf("platform = %+v, want %s/%s", doc.Workspace.Platform, runtime.GOOS, runtime.GOARCH)
	}
	if doc.Selection.Profile != "balanced" {
		t.Errorf("profile = %q, want balanced", doc.Selection.Profile)
	}
	if doc.Selection.Operators == nil || len(doc.Selection.Operators) != 0 {
		t.Errorf("operators = %v, want an empty list", doc.Selection.Operators)
	}
	if !slices.Equal(doc.Selection.Include, []string{"**/*.go"}) {
		t.Errorf("include = %v, want the default [**/*.go]", doc.Selection.Include)
	}
	if doc.Selection.Exclude == nil || len(doc.Selection.Exclude) != 0 {
		t.Errorf("exclude = %v, want an empty list", doc.Selection.Exclude)
	}
}

func TestListJSONIsByteIdenticalBetweenRuns(t *testing.T) {
	inFixture(t)
	first, _ := list(t, "--json")
	second, _ := list(t, "--json")
	if first != second {
		t.Errorf("two listings of one workspace differ:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.HasSuffix(first, "}\n") {
		t.Errorf("the document does not end in a newline: %q", first[max(0, len(first)-16):])
	}
}

func TestListTextListingMatchesTheDocument(t *testing.T) {
	inFixture(t)
	jsonOut, _ := list(t, "--json")
	doc := decodeCatalog(t, []byte(jsonOut))
	textOut, stderr := list(t)
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing on a clean listing", stderr)
	}
	if strings.Contains(textOut, "\x1b") {
		t.Error("the listing carries escape sequences; a buffer is not a terminal")
	}

	lines := strings.Split(strings.TrimSuffix(textOut, "\n"), "\n")
	want := []string{"go-mutants " + Version + " (list)"}
	for _, m := range doc.Mutants {
		want = append(want, fmt.Sprintf("%s  %s:%d:%d  %s/%s  %s -> %s",
			m.DisplayID[:listIDWidth], m.Path, m.Line, m.Column, m.Family, m.Rule, m.Original, m.Replacement))
	}
	want = append(want, fmt.Sprintf("mutants %d  files %d  skips %d", len(doc.Mutants), 4, skipTotal(doc.Skips)))
	for _, reason := range skipsByReason(doc.Skips) {
		want = append(want, "skip "+reason.reason+" "+strconv.Itoa(reason.count))
	}
	if !slices.Equal(lines, want) {
		t.Errorf("the listing is not the document:\ngot:\n%s\nwant:\n%s",
			strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	quietOut, _ := list(t, "--quiet")
	quiet := strings.Split(strings.TrimSuffix(quietOut, "\n"), "\n")
	if !slices.Equal(quiet, want[1:]) {
		t.Errorf("--quiet printed:\n%s\nwant the listing without its header line", quietOut)
	}
}

func TestListNarrowsTheSelection(t *testing.T) {
	inFixture(t)
	all, _ := list(t, "--json")
	full := decodeCatalog(t, []byte(all))

	t.Run("operator", func(t *testing.T) {
		stdout, _ := list(t, "--json", "--operator", "comparison")
		doc := decodeCatalog(t, []byte(stdout))
		if len(doc.Mutants) == 0 || len(doc.Mutants) >= len(full.Mutants) {
			t.Fatalf("--operator comparison listed %d of %d mutants, want a proper subset",
				len(doc.Mutants), len(full.Mutants))
		}
		for _, m := range doc.Mutants {
			if m.Family != "comparison" {
				t.Errorf("--operator comparison listed a %s mutant at %s:%d", m.Family, m.Path, m.Line)
			}
		}
		if !slices.Equal(doc.Selection.Operators, []string{"comparison"}) {
			t.Errorf("selection.operators = %v, want [comparison]", doc.Selection.Operators)
		}
	})

	t.Run("a family the fixture has no operators of", func(t *testing.T) {
		stdout, stderr := list(t, "--json", "--operator", "bitwise")
		doc := decodeCatalog(t, []byte(stdout))
		if len(doc.Mutants) != 0 {
			t.Errorf("--operator bitwise listed %d mutants, want none: the fixture has no bitwise operators", len(doc.Mutants))
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing: bitwise is discovered, the fixture simply has none", stderr)
		}
	})

	t.Run("two families, one of which the fixture has none of", func(t *testing.T) {
		stdout, stderr := list(t, "--json", "--operator", "comparison", "--operator", "bitwise")
		doc := decodeCatalog(t, []byte(stdout))
		if len(doc.Mutants) == 0 {
			t.Error("the comparison half of the selection listed nothing")
		}
		for _, m := range doc.Mutants {
			if m.Family != "comparison" {
				t.Errorf("listed a %s mutant at %s:%d", m.Family, m.Path, m.Line)
			}
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing: both families are discovered", stderr)
		}
	})

	t.Run("exclude", func(t *testing.T) {
		stdout, _ := list(t, "--json", "--exclude", "suppressed/**")
		doc := decodeCatalog(t, []byte(stdout))
		for _, m := range doc.Mutants {
			if strings.HasPrefix(m.Path, "suppressed/") {
				t.Errorf("%s:%d survived --exclude suppressed/**", m.Path, m.Line)
			}
		}
		want := catalogSkip{Path: "suppressed/suppressed.go", Reason: "excluded", Count: 1}
		if !slices.Contains(doc.Skips, want) {
			t.Errorf("skips = %+v, want it to record %+v", doc.Skips, want)
		}
		for _, skip := range doc.Skips {
			if strings.HasPrefix(skip.Path, "suppressed/") && skip.Reason != "excluded" {
				t.Errorf("excluded file still reports %s x%d, which means it was walked", skip.Reason, skip.Count)
			}
		}
	})

	t.Run("include", func(t *testing.T) {
		stdout, _ := list(t, "--json", "--include", "compare/**")
		doc := decodeCatalog(t, []byte(stdout))
		if len(doc.Mutants) != 27 {
			t.Errorf("--include compare/** listed %d mutants, want the 27 in compare/compare.go", len(doc.Mutants))
		}
		for _, m := range doc.Mutants {
			if !strings.HasPrefix(m.Path, "compare/") {
				t.Errorf("%s:%d is outside the include pattern", m.Path, m.Line)
			}
		}
	})

	t.Run("mutant prefix", func(t *testing.T) {
		target := full.Mutants[0]
		stdout, _ := list(t, "--json", "--mutant", target.DisplayID[:listIDWidth])
		doc := decodeCatalog(t, []byte(stdout))
		if len(doc.Mutants) != 1 || doc.Mutants[0].ID != target.ID {
			t.Fatalf("--mutant listed %d mutants, want exactly %s", len(doc.Mutants), target.ID)
		}
		if !slices.Equal(doc.Skips, full.Skips) {
			t.Errorf("skips = %+v, want the whole pass's %+v", doc.Skips, full.Skips)
		}
	})
}

func TestListWorkspaceDigestIgnoresTheSelection(t *testing.T) {
	inFixture(t)
	base, _ := list(t, "--json")
	full := decodeCatalog(t, []byte(base))
	want := full.Workspace.WorkspaceDigest
	if len(full.Mutants) == 0 {
		t.Fatal("the fixture listed no mutants, so --mutant cannot be exercised")
	}

	selections := [][]string{
		{"--json", "--exclude", "suppressed/**"},
		{"--json", "--include", "compare/**"},
		{"--json", "--operator", "comparison"},
		{"--json", "--mutant", full.Mutants[0].DisplayID[:listIDWidth]},
		{"--json", "--profile", "all"},
	}
	for _, args := range selections {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			stdout, _ := list(t, args...)
			doc := decodeCatalog(t, []byte(stdout))
			if got := doc.Workspace.WorkspaceDigest; got != want {
				t.Errorf("workspace_digest = %s, want the unfiltered listing's %s: a selection setting must not reach the snapshot walk",
					got, want)
			}
		})
	}
}

func scratchModule(t *testing.T, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	t.Chdir(root)
}

func TestListReportsAProfileTheConfigurationFileMadeInert(t *testing.T) {
	scratchModule(t, map[string]string{
		"go.mod":           "module scratch.example/inert\n\ngo 1.24\n",
		"a/a.go":           "package a\n\n// Eq reports whether x and y are equal.\nfunc Eq(x, y int) bool { return x == y }\n",
		".go-mutants.toml": "version = 1\n\n[mutation]\noperators = [\"comparison\"]\n",
	})

	stdout, stderr := list(t, "--json", "--profile", "all")
	doc := decodeCatalog(t, []byte(stdout))
	if !slices.Equal(doc.Selection.Operators, []string{"comparison"}) {
		t.Errorf("selection.operators = %v, want the file's [comparison]", doc.Selection.Operators)
	}
	if doc.Selection.Profile != "all" {
		t.Errorf("selection.profile = %q, want the tier the flag set", doc.Selection.Profile)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stderr = %q, want exactly one warning line", stderr)
	}
	for _, needle := range []string{string(CodeInertProfile), "--profile all", ".go-mutants.toml", "mutation.operators"} {
		if !strings.Contains(lines[0], needle) {
			t.Errorf("stderr = %q, want it to mention %q", stderr, needle)
		}
	}

	if _, quiet := list(t, "--json"); quiet != "" {
		t.Errorf("stderr = %q on a listing that set no profile, want nothing", quiet)
	}
	if _, quiet := list(t, "--json", "--profile", "all", "--operator", "comparison"); quiet != "" {
		t.Errorf("stderr = %q when both were typed, want nothing", quiet)
	}
}

func TestListWarningsComeOutInOneOrder(t *testing.T) {
	scratchModule(t, map[string]string{
		"go.mod":           "module scratch.example/ordered\n\ngo 1.24\n",
		"a/a.go":           "package a\n\n// Eq reports whether x and y are equal.\nfunc Eq(x, y int) bool { return x == y }\n",
		".go-mutants.toml": "version = 1\n\n[mutation]\noperators = [\"bitwise\", \"comparison\"]\n",
	})

	_, stderr := list(t, "--json", "--profile", "all")
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stderr = %q, want exactly the one warning line that is still reachable", stderr)
	}
	if !strings.HasPrefix(lines[0], "warning "+string(CodeInertProfile)+": ") {
		t.Errorf("the line = %q, want the %s warning about what was asked for", lines[0], CodeInertProfile)
	}
}

func TestListRemovesItsSnapshot(t *testing.T) {
	temp := inFixture(t)
	before := treeDigest(t, ".")
	list(t, "--json")

	left, err := os.ReadDir(temp)
	if err != nil {
		t.Fatalf("reading %s: %v", temp, err)
	}
	if len(left) != 0 {
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("the listing left %v behind in %s", names, temp)
	}
	if after := treeDigest(t, "."); after != before {
		t.Error("the fixture changed while it was being listed; the workspace is meant to be read-only")
	}
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		lines = append(lines, filepath.ToSlash(p)+" "+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func TestListMapsCancellationToTheInterruptExitCode(t *testing.T) {
	inFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var out, errOut bytes.Buffer
	code := ExecuteContext(ctx, []string{"list", "--json"}, &out, &errOut)
	if code != int(mutation.ExitInterrupted) {
		t.Errorf("exit = %d, want %d\nstderr:\n%s", code, mutation.ExitInterrupted, errOut.String())
	}
	if out.String() != "" {
		t.Errorf("a cancelled listing wrote %q to standard output, want nothing", out.String())
	}
}

func TestListCommandLineEndToEnd(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go executable on PATH: %v", err)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	fixture, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "discovery"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "go-mutants")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), goBin, "build", "-o", binary, "./cmd/go-mutants")
	build.Dir = repo
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("building cmd/go-mutants: %v\n%s", buildErr, out)
	}

	run := exec.CommandContext(t.Context(), binary, "list", "--json")
	run.Dir = fixture
	temp := t.TempDir()
	run.Env = append(os.Environ(), "NO_COLOR=1", "TMPDIR="+temp, "TMP="+temp, "TEMP="+temp)

	var stdout, stderr bytes.Buffer
	run.Stdout = &stdout
	run.Stderr = &stderr
	if err := run.Run(); err != nil {
		t.Fatalf("go-mutants list --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if code := run.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing on a clean listing", stderr.String())
	}

	doc := decodeCatalog(t, stdout.Bytes())
	if got := listed(doc); !slices.Equal(got, wantMutants) {
		t.Errorf("the built binary lists a different catalogue:\n%s", diffMutants(got, wantMutants))
	}
	if !slices.Equal(doc.Skips, wantSkips) {
		t.Errorf("skips = %+v, want %+v", doc.Skips, wantSkips)
	}
	if left, err := os.ReadDir(temp); err == nil && len(left) != 0 {
		t.Errorf("the run left %d entries behind in its temporary directory", len(left))
	}
}

func TestListAtAWorkspaceRootListsEveryModule(t *testing.T) {
	root := testkit.Copy(t, "workspace")
	t.Chdir(root)

	var out, errOut bytes.Buffer
	if code := ExecuteContext(t.Context(), []string{"list", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("`go-mutants list` at a go.work root exited %d\nstderr:\n%s", code, errOut.String())
	}
	var doc struct {
		Workspace struct {
			ModulePath string `json:"module_path"`
			Modules    []struct {
				Dir        string `json:"dir"`
				ModulePath string `json:"module_path"`
			} `json:"modules"`
		} `json:"workspace"`
		Mutants []struct {
			Path       string `json:"path"`
			ModulePath string `json:"module_path"`
		} `json:"mutants"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("decoding the listing: %v", err)
	}
	if doc.Workspace.ModulePath != "" {
		t.Errorf("workspace.module_path = %q; a workspace has no single answer for it",
			doc.Workspace.ModulePath)
	}
	want := []string{
		"fixture.example/workspace/app",
		"fixture.example/workspace/cross",
		"fixture.example/workspace/lib",
	}
	var got []string
	for _, module := range doc.Workspace.Modules {
		got = append(got, module.ModulePath)
	}
	if !slices.Equal(got, want) {
		t.Errorf("workspace.modules = %v, want %v in `use` order", got, want)
	}
	if len(doc.Mutants) != 11 {
		t.Errorf("the listing holds %d mutants, want the workspace's eleven", len(doc.Mutants))
	}
	for _, m := range doc.Mutants {
		if m.ModulePath == "" {
			t.Errorf("the mutant at %s names no module", m.Path)
		}
	}

	out.Reset()
	if code := ExecuteContext(t.Context(), []string{"list"}, &out, &errOut); code != 0 {
		t.Fatalf("`go-mutants list` exited %d\nstderr:\n%s", code, errOut.String())
	}
	for _, phrase := range []string{"app/app.go:", "cross/cross.go:", "lib/lib.go:"} {
		if !strings.Contains(out.String(), phrase) {
			t.Errorf("the listing does not name %q:\n%s", phrase, out.String())
		}
	}
}
