// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of `list`. It snapshots a real module, loads it
// with a real `go` command, and asserts the catalogue that comes out — which is
// the only way to test this command at all: every interesting thing about it
// (which expressions the type checker says are the universe's `true`, which
// bytes a span covers, what a workspace digest is) is exactly what a mock would
// have to invent.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
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
)

// fixtureModule is the module path of the corpus module these tests list.
const fixtureModule = "fixture.example/discovery"

// A listedMutant is one row of the expected catalogue, reduced to what a human
// would check by opening the file: where it is, which rule proposed it, and
// what the edit is. The id is deliberately not here — it is a digest of the
// bytes, so pinning it in a table would make every whitespace change in the
// fixture a test edit — and determinism of the ids is asserted separately, by
// running the command twice.
type listedMutant struct {
	path        string
	line        int
	column      int
	family      string
	rule        string
	original    string
	replacement string
}

// wantMutants is every candidate fixtures/discovery holds, in catalogue order:
// path, then span, then registry position.
//
// The table is exhaustive rather than a sample. A missing candidate and an
// extra one are the two ways this phase can be wrong, and only an exact
// comparison catches both — a "contains" assertion would pass a discovery that
// also mutated the const block next door.
var wantMutants = []listedMutant{
	// compare/compare.go is the control group: every rule that fires on an
	// ordinary comparison and on the statement around it, in ordinary statement
	// context. The condition-negation and return-replacement rows are here
	// because the operator catalogue reaches further than the comparison family
	// alone; the table is the whole of what discovery found, not a sample of it.
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
	{"suppressed/suppressed.go", 77, 6, "condition-negation", "negate-condition", "ok == true", "!(ok == true)"},
	{"suppressed/suppressed.go", 77, 9, "comparison", "eq-to-neq", "==", "!="},
	{"suppressed/suppressed.go", 77, 12, "boolean-literal", "true-to-false", "true", "false"},
	{"suppressed/suppressed.go", 78, 11, "return-replacement", "return-empty-string", "\"equal and ok\"", "\"\""},
	{"suppressed/suppressed.go", 81, 10, "return-replacement", "return-empty-string", "\"not ok\"", "\"\""},
	{"suppressed/suppressed.go", 85, 6, "condition-negation", "negate-condition", "v > b", "!(v > b)"},
	{"suppressed/suppressed.go", 85, 8, "comparison", "gt-to-ge", ">", ">="},
	{"suppressed/suppressed.go", 86, 11, "return-replacement", "return-empty-string", "\"greater\"", "\"\""},
	{"suppressed/suppressed.go", 89, 10, "return-replacement", "return-empty-string", "v", "\"\""},
	{"suppressed/suppressed.go", 91, 9, "return-replacement", "return-empty-string", "\"none\"", "\"\""},
	{"suppressed/suppressed.go", 98, 10, "return-replacement", "return-empty-string", "\"sent\"", "\"\""},
	{"suppressed/suppressed.go", 100, 6, "condition-negation", "negate-condition", "v == true", "!(v == true)"},
	{"suppressed/suppressed.go", 100, 8, "comparison", "eq-to-neq", "==", "!="},
	{"suppressed/suppressed.go", 100, 11, "boolean-literal", "true-to-false", "true", "false"},
	{"suppressed/suppressed.go", 101, 11, "return-replacement", "return-empty-string", "\"received\"", "\"\""},
	{"suppressed/suppressed.go", 104, 9, "return-replacement", "return-empty-string", "\"none\"", "\"\""},

	// A boolean literal used as a map key is value code: the type-argument
	// suppression must not reach an ordinary index expression — that is the
	// `m[true]` row above. The body of a generic function is ordinary code too,
	// however many type parameters and constraints surround it, which is the
	// generics/generics.go block. The universe `false` in a package that
	// declares its own `true` is the one shadow/shadow.go boolean row: every
	// mention of the shadowing name is absent, which is the whole point of that
	// package. And suppressed/suppressed.go contributes the live side of each
	// suppressed context — an expression switch's case body, a type switch's,
	// and a select's — while their labels contribute nothing.
}

// wantSkips is every reason discovery recorded, in (path, reason) order.
//
// The counts are candidates, not sites: four suppressed expressions inside
// const declarations, five inside type parameter lists and type arguments, and
// so on. The generated file counts one, because it was never opened.
var wantSkips = []catalogSkip{
	{Path: "generated/generated.go", Reason: "generated", Count: 1},
	{Path: "generics/generics.go", Reason: "type-param", Count: 5},
	{Path: "suppressed/suppressed.go", Reason: "array-length", Count: 2},
	{Path: "suppressed/suppressed.go", Reason: "case-label", Count: 4},
	{Path: "suppressed/suppressed.go", Reason: "const-decl", Count: 4},
	{Path: "suppressed/suppressed.go", Reason: "package-var-init", Count: 5},
}

// inFixture points the process at the discovery fixture for the length of one
// test, with its own temporary directory.
//
// The temporary directory is redirected so that "the snapshot was removed" is
// an assertion rather than a guess: the machine's shared temporary directory
// has other packages writing into it, and one leak there is indistinguishable
// from another. It returns the redirected directory.
func inFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "discovery"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("fixtures/discovery is not a module: %v", err)
	}
	temp := t.TempDir()
	// os.TempDir reads TMPDIR on POSIX and TMP then TEMP on Windows, so all
	// three are set rather than guessing which platform is reading.
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	t.Chdir(root)
	return temp
}

// list runs the command in process and returns its streams. The exit status is
// checked here, because every test below wants a listing rather than a
// diagnostic.
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

// decodeCatalog parses a catalogue document, refusing anything the schema would
// refuse first.
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

// listed reduces a document's mutants to the table form.
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

// diffMutants renders two tables side by side, since a slice of structs in a
// failure message is unreadable otherwise.
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

	// Neither the generated file nor the shadowed `true` may contribute
	// anything at all. Both are already implied by the table above; they are
	// spelled out because a future edit to the table is far more likely than a
	// deliberate decision to start mutating generated code.
	for _, m := range doc.Mutants {
		if strings.HasPrefix(m.Path, "generated/") {
			t.Errorf("%s:%d is a candidate in a generated file", m.Path, m.Line)
		}
		// The shadowing package's own `true` is an int constant and a local
		// variable, and the ordinary integer code around both is mutated like
		// any other. What may never happen is a boolean-literal candidate on
		// one of those names: the family is about the universe constants, and
		// only the `false` on line 34 is one.
		if m.Path == "shadow/shadow.go" && m.Family == "boolean-literal" && m.Line != 34 {
			t.Errorf("shadow/shadow.go:%d is a boolean-literal candidate; only the universe `false` on line 34 may be one", m.Line)
		}
	}

	// Identity is the catalogue's job, and the document is where it becomes
	// visible: full ids are what a report and an expectation ledger carry, and
	// the short form has to be a prefix of the full one or `--mutant` would
	// resolve two different alphabets.
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

		// The package is the import path a user would type, derived from the
		// directory rather than restated per row.
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
	// The default selection: the balanced profile, no named operators, and the
	// default include. Empty lists are lists, never null.
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

// TestListJSONIsByteIdenticalBetweenRuns is the determinism contract. It covers
// the mutant ids as well as the ordering: an id is a digest of the path, the
// rule, the span, and the bytes, so two runs agreeing byte for byte means the
// whole recipe — including the snapshot the bytes were read from — is stable.
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

// TestListTextListingMatchesTheDocument proves the two renderings are one
// selection. Both are composed from the same document, and this is what says so
// out loud: a line per mutant, in the same order, with the same coordinates.
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

	// Quiet drops the header and nothing else: the mutants, the counts, and the
	// skip breakdown are all findings about the code, and the run console draws
	// the same line — it silences progress and keeps results.
	quietOut, _ := list(t, "--quiet")
	quiet := strings.Split(strings.TrimSuffix(quietOut, "\n"), "\n")
	if !slices.Equal(quiet, want[1:]) {
		t.Errorf("--quiet printed:\n%s\nwant the listing without its header line", quietOut)
	}
}

// TestListNarrowsTheSelection covers the three flags that change which mutants
// are listed, each against the same fixture so that the difference is the flag.
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
		// This used to be the "not discovered yet" case: bitwise was a `strong`
		// family the phase did not implement, so an empty listing came with a
		// GOM1006 warning saying why. Every family in the registry is
		// discovered now, so the empty listing here is a fact about the fixture
		// — it holds no bitwise operators — and there is nothing to warn about.
		// Saying so would be worse than silence: it would tell the user their
		// build cannot find something it can.
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
		// The partial case, which is the one a user actually types. Both halves
		// are discoverable, so what comes back is every comparison candidate
		// and no diagnostic at all.
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
		// An excluded file is one skip, whatever it holds: it was never opened,
		// so counting candidates in it would mean guessing.
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
		// The filter narrows the listing and not the pass: the skips still
		// describe every file discovery looked at.
		if !slices.Equal(doc.Skips, full.Skips) {
			t.Errorf("skips = %+v, want the whole pass's %+v", doc.Skips, full.Skips)
		}
	})
}

// TestListWorkspaceDigestIgnoresTheSelection pins, for `list`, the invariant
// internal/engine pins for `run`.
//
// Both commands copy the workspace with the same two-line block —
// `snapshot.Create(root, snapshot.Options{ReportDir: ...})` and a deferred
// cleanup — and today the two agree only because a comment in each says they
// must. `TestMutationExcludeChangesNeitherTheSnapshotNorItsDigest` holds the
// engine's copy in place; nothing held this one, so an edit that threaded
// `Exclude:` into list.go's Options would shrink the tree this command copies,
// move the workspace digest the ids are minted against, and produce a different
// id from `run` for the same code — with every existing test still green.
//
// The digest is the strongest available spelling of "the same tree was copied":
// it is taken over the snapshot manifest, so equal digests mean equal file sets
// and equal bytes. Every flag that selects rather than describes is tried, since
// a selection setting is exactly the kind of thing that gets routed into the
// copy by mistake.
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

// scratchModule writes a throwaway module and points the process at it for the
// length of one test.
//
// The fixture module cannot be used for anything involving .go-mutants.toml: a
// configuration file there would change the resolved configuration of every
// other test in this file, several of which assert the built-in defaults.
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
	// The snapshot is made under the temporary directory, which must not be the
	// module being snapshotted.
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	t.Chdir(root)
}

// TestListReportsAProfileTheConfigurationFileMadeInert covers the one way the
// documented precedence can invert.
//
// `list --help` promises that flags override the file. A profile is a tier and
// a named operator is looked up in the whole catalogue, so the two do not
// combine: whenever any operator is named the profile selects nothing. When the
// operators came from .go-mutants.toml and the profile came from a flag, that
// means the file overrides the flag — the opposite of what was promised, and
// previously with nothing on standard error to say so.
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

	// The two invocations that must stay silent: the same file with no profile
	// flag at all — nothing was overridden — and a command line that names the
	// operators itself, where the user can see both decisions in front of them.
	if _, quiet := list(t, "--json"); quiet != "" {
		t.Errorf("stderr = %q on a listing that set no profile, want nothing", quiet)
	}
	if _, quiet := list(t, "--json", "--profile", "all", "--operator", "comparison"); quiet != "" {
		t.Errorf("stderr = %q when both were typed, want nothing", quiet)
	}
}

// TestListWarningsComeOutInOneOrder is the diffability contract for standard
// error.
//
// The listing itself is deliberately unpadded and unsorted-by-data so that two
// runs can be diffed; a diagnostic stream whose lines swap places between runs
// would undo that.
//
// It used to assert two lines in one order — what was asked for, then what
// could be found of it — and it cannot any more: the second family, GOM1006,
// became unreachable through a real selection when the last operator family
// landed in discovery, because every rule the registry names is now discovered.
// Its wording is kept under test by
// TestWarnUnimplementedStillSaysWhyAnEmptyListingIsEmpty, which drives the
// writer directly. What survives here is the half that can still happen, and it
// is asserted exactly: one line, the profile warning, and nothing else — a
// second line appearing would mean a registry rule landed ahead of its
// discovery, which is the situation the order was pinned for.
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

// TestListRemovesItsSnapshot is the read-only promise, checked from the outside:
// after a listing there is nothing left in the temporary directory the snapshot
// was created in, and the user's own tree is untouched.
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

// treeDigest renders a directory tree as sorted "path size" lines, which is
// enough to notice a file that was written, added, or removed.
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

// TestListMapsCancellationToTheInterruptExitCode covers the signal path. A
// listing copies a workspace and starts a `go list` under it, so a Ctrl-C has
// to unwind the pipeline — which is what leaves nothing behind — rather than
// killing the process where it stands, and the documented answer for a
// cancelled command is 130 and not "an infrastructure failure".
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

// TestListCommandLineEndToEnd builds cmd/go-mutants and runs it, which is the
// only test that covers the wiring as a user meets it: a real process, a real
// working directory, and a document on standard output with nothing else mixed
// into it.
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
	// The child inherits this process's environment, which is what puts the
	// toolchain manager's `go` on its PATH; only the temporary directory and
	// the colour decision are overridden. os/exec keeps the last of duplicate
	// keys, so appending is enough.
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
