// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/schemas"
)

func TestListRefusesJSONWithQuiet(t *testing.T) {
	code, stdout, stderr := execute(t, "list", "--json", "--quiet")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "error "+string(CodeConflictingFlags)) {
		t.Errorf("stderr = %q, want the conflicting-flags error", stderr)
	}
	if !strings.Contains(stderr, "hint: ") {
		t.Errorf("stderr = %q, want a hint naming the remedy", stderr)
	}
}

func TestListRefusesPositionalArguments(t *testing.T) {
	code, _, stderr := execute(t, "list", "internal/mutation")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "error "+string(CodeUsage)) {
		t.Errorf("stderr = %q, want a usage error", stderr)
	}
	for _, needle := range []string{"--include", "--operator", "--mutant"} {
		if !strings.Contains(stderr, needle) {
			t.Errorf("stderr = %q, want it to name %s", stderr, needle)
		}
	}
}

func TestListRefusesAnUnusableMutantPrefix(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"unset", "", true},
		{"display prefix", "60a73dea", true},
		{"shortest accepted", strings.Repeat("a", mutation.MinPrefixLength), true},
		{"full id", strings.Repeat("0", mutation.IDHexLength), true},
		{"too short", strings.Repeat("a", mutation.MinPrefixLength-1), false},
		{"too long", strings.Repeat("0", mutation.IDHexLength+1), false},
		{"uppercase", "60A73DEA", false},
		{"not hex", "zzzz", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := listPrefix(test.value)
			if test.want {
				if err != nil {
					t.Fatalf("listPrefix(%q) = %v, want it accepted", test.value, err)
				}
				if got != test.value {
					t.Errorf("listPrefix(%q) = %q, want the value unchanged", test.value, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("listPrefix(%q) was accepted, want a refusal", test.value)
			}
			var coded *Error
			if !errors.As(err, &coded) || coded.Code != CodeInvalidMutantPrefix {
				t.Errorf("listPrefix(%q) = %v, want %s", test.value, err, CodeInvalidMutantPrefix)
			}
		})
	}
}

func TestSelectRulesFollowsTheProfileUntilAnOperatorIsNamed(t *testing.T) {
	registry := mutation.CanonicalRegistry()

	balanced := config.Defaults()
	rules, err := selectRules(balanced)
	if err != nil {
		t.Fatalf("selectRules with the default profile: %v", err)
	}
	if !slices.Equal(rules, registry.SelectTier(mutation.TierBalanced)) {
		t.Errorf("the balanced profile selected %d rules, want the tier's own %d",
			len(rules), len(registry.SelectTier(mutation.TierBalanced)))
	}

	outside := config.Defaults()
	outside.Mutation.Operators = []string{"bitwise"}
	rules, err = selectRules(outside)
	if err != nil {
		t.Fatalf("selectRules with an out-of-profile family: %v", err)
	}
	want := registry.FamilyRules(mutation.FamilyBitwise)
	if !slices.Equal(rules, want) {
		t.Errorf("--operator bitwise selected %v, want the whole family %v", rules, want)
	}

	overlapping := config.Defaults()
	overlapping.Mutation.Operators = []string{"eq-to-neq", "comparison"}
	rules, err = selectRules(overlapping)
	if err != nil {
		t.Fatalf("selectRules with overlapping names: %v", err)
	}
	if !slices.Equal(rules, registry.FamilyRules(mutation.FamilyComparison)) {
		t.Errorf("overlapping names selected %v, want the comparison family once", rules)
	}

	unknown := config.Defaults()
	unknown.Mutation.Operators = []string{"telepathy"}
	if _, err := selectRules(unknown); err == nil {
		t.Error("selectRules accepted an operator the catalogue does not know")
	}
}

func TestImplementedRulesIsASubsetOfTheSelection(t *testing.T) {
	registry := mutation.CanonicalRegistry()

	implemented := implementedRules(registry.SelectTier(mutation.TierAll))
	if len(implemented) != len(discover.SupportedRules()) {
		t.Errorf("the whole catalogue implements %d rules, discovery reports %d",
			len(implemented), len(discover.SupportedRules()))
	}
	for _, family := range registry.Families() {
		rules := registry.FamilyRules(family)
		if got := len(implementedRules(rules)); got != len(rules) {
			t.Errorf("the %s family has %d rules and %d are reported as implemented", family, len(rules), got)
		}
	}
	families := implementedFamilies()
	for _, rule := range discover.SupportedRules() {
		if !strings.Contains(families, string(rule.Family)) {
			t.Errorf("the implemented families %q do not name %s", families, rule.Family)
		}
	}
}

func TestWarnUnimplementedIsSilentForTheWholeCatalogue(t *testing.T) {
	registry := mutation.CanonicalRegistry()

	names := []string{""}
	for _, family := range registry.Families() {
		names = append(names, string(family))
	}
	for _, rule := range registry.Rules() {
		names = append(names, rule.Name)
	}
	for _, name := range names {
		t.Run("operator "+name, func(t *testing.T) {
			cfg := config.Defaults()
			if name != "" {
				cfg.Mutation.Operators = []string{name, name}
			}
			rules, err := selectRules(cfg)
			if err != nil {
				t.Fatalf("selectRules(%v): %v", cfg.Mutation.Operators, err)
			}
			var b strings.Builder
			warnUnimplemented(&b, cfg, rules)
			if got := b.String(); got != "" {
				t.Errorf("warnUnimplemented wrote %q for %v, want nothing: every catalogue name is discovered",
					got, cfg.Mutation.Operators)
			}
		})
	}
}

func TestWarnUnimplementedStillSaysWhyAnEmptyListingIsEmpty(t *testing.T) {
	cfg := config.Defaults()
	var b strings.Builder
	warnUnimplemented(&b, cfg, nil)

	got := b.String()
	if !strings.Contains(got, string(CodeUnimplementedOperators)) {
		t.Errorf("warning = %q, want the %s code", got, CodeUnimplementedOperators)
	}
	if !strings.Contains(got, "the listing is empty") {
		t.Errorf("warning = %q, want it to say why the listing is empty", got)
	}
	if !strings.Contains(got, implementedFamilies()) {
		t.Errorf("warning = %q, want it to name the implemented families %q", got, implementedFamilies())
	}
	if lines := strings.Count(got, "\n"); lines != 1 {
		t.Errorf("warning spans %d lines, want 1:\n%s", lines, got)
	}
}

func TestWarnInertProfileFiresOnlyOnThePrecedenceInversion(t *testing.T) {
	tests := []struct {
		name          string
		profileTyped  bool
		operatorTyped bool
		operators     []string
		want          bool
	}{
		{"the file's operators against a typed profile", true, false, []string{"comparison"}, true},
		{"both typed on one command line", true, true, []string{"comparison"}, false},
		{"a profile with no operators anywhere", true, false, nil, false},
		{"the file's operators with no profile flag", false, false, []string{"comparison"}, false},
		{"nothing typed at all", false, false, nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var b strings.Builder
			warnInertProfile(&b, test.profileTyped, test.operatorTyped, "all", test.operators)
			got := b.String()
			if !test.want {
				if got != "" {
					t.Fatalf("warnInertProfile wrote %q, want nothing", got)
				}
				return
			}
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
				t.Errorf("warnInertProfile wrote %q, want exactly one line", got)
			}
			for _, needle := range []string{
				string(CodeInertProfile),
				"--profile all",
				config.FileName,
				"mutation.operators",
				"comparison",
				"--operator",
			} {
				if !strings.Contains(got, needle) {
					t.Errorf("warnInertProfile = %q, want it to mention %q", got, needle)
				}
			}
		})
	}
}

func TestOperatorRulesAnswersTheSameQuestionAsTheSelection(t *testing.T) {
	registry := mutation.CanonicalRegistry()
	for _, name := range []string{"comparison", "bitwise", "statement-deletion"} {
		got, ok := operatorRules(registry, name)
		if !ok {
			t.Fatalf("operatorRules(%q) did not resolve a family the catalogue lists", name)
		}
		if want := registry.FamilyRules(mutation.Family(name)); !slices.Equal(got, want) {
			t.Errorf("operatorRules(%q) = %v, want the whole family %v", name, got, want)
		}
	}
	rule, ok := registry.Lookup("eq-to-neq")
	if !ok {
		t.Fatal("the canonical registry does not know eq-to-neq")
	}
	got, ok := operatorRules(registry, "eq-to-neq")
	if !ok || !slices.Equal(got, []mutation.Rule{rule}) {
		t.Errorf("operatorRules(\"eq-to-neq\") = %v, %t, want just that rule", got, ok)
	}
	if _, ok := operatorRules(registry, "telepathy"); ok {
		t.Error("operatorRules resolved a name the catalogue does not know")
	}
}

func TestDisplayTextKeepsTheListingOneLinePerMutant(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"operator", "==", "=="},
		{"deletion", "", `""`},
		{"multi line", "a\nb", `"a\nb"`},
		{"trailing space", "x ", `"x "`},
		{"tab", "a\tb", `"a\tb"`},
		{"inner space", "a + b", "a + b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := console.FormatText(test.text); got != test.want {
				t.Errorf("console.FormatText(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}

func TestSkipsByReasonAggregatesAcrossFiles(t *testing.T) {
	skips := []catalogSkip{
		{Path: "b.go", Reason: "const-decl", Count: 2},
		{Path: "a.go", Reason: "type-param", Count: 1},
		{Path: "a.go", Reason: "const-decl", Count: 3},
	}
	want := []reasonCount{{reason: "const-decl", count: 5}, {reason: "type-param", count: 1}}
	if got := skipsByReason(skips); !slices.Equal(got, want) {
		t.Errorf("skipsByReason = %v, want %v", got, want)
	}
	if got := skipTotal(skips); got != 6 {
		t.Errorf("skipTotal = %d, want 6", got)
	}
}

func TestGoVersionFallsBackToTheToolchain(t *testing.T) {
	if got := goVersion("1.26", "go1.26.5"); got != "1.26" {
		t.Errorf("goVersion = %q, want the module's own directive", got)
	}
	if got := goVersion("", "go1.26.5"); got != "go1.26.5" {
		t.Errorf("goVersion = %q, want the toolchain release when the module declares none", got)
	}
	if got := goVersion("", ""); got == "" {
		t.Error("goVersion returned an empty string, which the catalogue schema refuses")
	}
}

func TestStringListNeverEncodesAsNull(t *testing.T) {
	if got := stringList(nil); got == nil || len(got) != 0 {
		t.Errorf("stringList(nil) = %v, want an empty non-nil slice", got)
	}
	source := []string{"a"}
	got := stringList(source)
	got[0] = "b"
	if source[0] != "a" {
		t.Error("stringList aliases its argument")
	}
}

func TestBranchProofAppearsInTheCatalogDocument(t *testing.T) {
	found := branchProofDiscovery(t)
	doc, err := found.document(config.Defaults(), "")
	if err != nil {
		t.Fatalf("building the document: %v", err)
	}
	if len(doc.Mutants) != 2 {
		t.Fatalf("document holds %d mutants, want 2", len(doc.Mutants))
	}
	proved, plain := doc.Mutants[0], doc.Mutants[1]
	if proved.Branch == nil {
		t.Fatalf("the proved mutant carries no branch: %+v", proved)
	}
	want := catalogBranch{
		Direction:       discover.BranchDecreasing,
		BodyStartLine:   3,
		BodyStartColumn: 12,
		BodyEndLine:     5,
		BodyEndColumn:   2,
	}
	if *proved.Branch != want {
		t.Errorf("branch = %+v, want %+v", *proved.Branch, want)
	}
	if plain.Branch != nil {
		t.Errorf("the unproved mutant carries a branch: %+v", *plain.Branch)
	}

	var buf bytes.Buffer
	if err := writeCatalogJSON(&buf, doc); err != nil {
		t.Fatalf("encoding the document: %v", err)
	}
	if err := schemas.Validate(schemas.CatalogV1, buf.Bytes()); err != nil {
		t.Fatalf("the document does not satisfy %s: %v\n%s", schemas.CatalogV1, err, buf.String())
	}
	var decoded struct {
		Mutants []map[string]any `json:"mutants"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	if _, ok := decoded.Mutants[0]["branch"]; !ok {
		t.Error("the proved mutant has no branch key")
	}
	if _, ok := decoded.Mutants[1]["branch"]; ok {
		t.Error("the unproved mutant has a branch key, want the property omitted")
	}
}

func branchProofDiscovery(t *testing.T) discovered {
	t.Helper()
	digest := mutation.DigestString("package a\n")
	located := func(name string, start, end uint32, original, replacement string, branch *discover.BranchProof) discover.Located {
		t.Helper()
		rule, ok := mutation.CanonicalRegistry().Lookup(name)
		if !ok {
			t.Fatalf("the canonical registry does not know %s", name)
		}
		span, err := mutation.NewSpan(start, end)
		if err != nil {
			t.Fatalf("building the span: %v", err)
		}
		return discover.Located{
			Candidate: mutation.Candidate{
				Path:         "a/a.go",
				Rule:         rule,
				Span:         span,
				Original:     original,
				Replacement:  replacement,
				SourceDigest: digest,
			},
			Line:    3,
			Column:  9,
			Package: "example.com/mini/a",
			Branch:  branch,
		}
	}
	result := discover.Result{
		Candidates: []discover.Located{
			located("le-to-lt", 20, 22, "<=", "<", &discover.BranchProof{
				Direction:       discover.BranchDecreasing,
				BodyStartLine:   3,
				BodyStartColumn: 12,
				BodyEndLine:     5,
				BodyEndColumn:   2,
			}),
			located("lt-to-le", 30, 31, "<", "<=", nil),
		},
		ModulePath: "example.com/mini",
		GoVersion:  "1.26",
	}
	catalog, err := discover.BuildCatalog(result)
	if err != nil {
		t.Fatalf("cataloguing: %v", err)
	}
	return discovered{
		modules:         []discover.WorkspaceModule{{Dir: ".", Path: result.ModulePath}},
		results:         []discover.Result{result},
		catalog:         catalog,
		workspaceDigest: strings.Repeat("ab", 32),
	}
}

func TestCatalogDocumentJoinsCoordinatesOntoTheCatalogue(t *testing.T) {
	found := oneMutantDiscovery(t)
	doc, err := found.document(config.Defaults(), "")
	if err != nil {
		t.Fatalf("building the document: %v", err)
	}
	if len(doc.Mutants) != 1 {
		t.Fatalf("document holds %d mutants, want 1", len(doc.Mutants))
	}
	m := doc.Mutants[0]
	if m.Line != 3 || m.Column != 9 || m.Package != "example.com/mini/a" {
		t.Errorf("mutant = %+v, want the discovered coordinates 3:9 in example.com/mini/a", m)
	}
	if m.Family != string(mutation.FamilyComparison) || m.Rule != "eq-to-neq" || m.RuleVersion != 1 {
		t.Errorf("mutant operator = %s/%s@%d, want comparison/eq-to-neq@1", m.Family, m.Rule, m.RuleVersion)
	}
	if m.StartByte != 20 || m.EndByte != 22 || m.Original != "==" || m.Replacement != "!=" {
		t.Errorf("mutant edit = %+v, want the candidate's own span and text", m)
	}
	if doc.DocumentType != catalogDocumentType || doc.SchemaVersion != catalogSchemaVersion || doc.ToolVersion != Version {
		t.Errorf("document identity = %s/%d/%s, want %s/%d/%s",
			doc.DocumentType, doc.SchemaVersion, doc.ToolVersion,
			catalogDocumentType, catalogSchemaVersion, Version)
	}

	for _, prefix := range []string{m.ID, m.DisplayID, m.ID[:mutation.MinPrefixLength]} {
		filtered, filterErr := found.document(config.Defaults(), prefix)
		if filterErr != nil {
			t.Fatalf("filtering by %q: %v", prefix, filterErr)
		}
		if len(filtered.Mutants) != 1 {
			t.Errorf("filtering by %q listed %d mutants, want 1", prefix, len(filtered.Mutants))
		}
	}

	empty, err := found.document(config.Defaults(), strings.Repeat("f", mutation.IDHexLength))
	if err != nil {
		t.Fatalf("filtering by an unmatched prefix: %v", err)
	}
	if len(empty.Mutants) != 0 {
		t.Errorf("an unmatched prefix listed %d mutants, want none", len(empty.Mutants))
	}
	if len(empty.Skips) != len(doc.Skips) {
		t.Errorf("filtering changed the skips from %d rows to %d", len(doc.Skips), len(empty.Skips))
	}
}

func TestCatalogDocumentRefusesAMutantItCannotLocate(t *testing.T) {
	found := oneMutantDiscovery(t)
	found.results[0].Candidates = nil

	_, err := found.document(config.Defaults(), "")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeCatalogMismatch {
		t.Fatalf("document() = %v, want %s", err, CodeCatalogMismatch)
	}
}

func oneMutantDiscovery(t *testing.T) discovered {
	t.Helper()
	rule, ok := mutation.CanonicalRegistry().Lookup("eq-to-neq")
	if !ok {
		t.Fatal("the canonical registry does not know eq-to-neq")
	}
	span, err := mutation.NewSpan(20, 22)
	if err != nil {
		t.Fatalf("building the span: %v", err)
	}
	located := discover.Located{
		Candidate: mutation.Candidate{
			Path:         "a/a.go",
			Rule:         rule,
			Span:         span,
			Original:     "==",
			Replacement:  "!=",
			SourceDigest: mutation.DigestString("package a\n"),
		},
		Line:    3,
		Column:  9,
		Package: "example.com/mini/a",
	}
	result := discover.Result{
		Candidates: []discover.Located{located},
		Skips:      []discover.Skip{{Path: "a/generated.go", Reason: discover.SkipGenerated, Count: 1}},
		ModulePath: "example.com/mini",
		GoVersion:  "1.26",
	}
	catalog, err := discover.BuildCatalog(result)
	if err != nil {
		t.Fatalf("cataloguing: %v", err)
	}
	return discovered{
		modules:         []discover.WorkspaceModule{{Dir: ".", Path: result.ModulePath}},
		results:         []discover.Result{result},
		catalog:         catalog,
		workspaceDigest: strings.Repeat("ab", 32),
	}
}

func TestListHelpDocumentsTheSelectionRules(t *testing.T) {
	code, stdout, _ := execute(t, "list", "--help")
	if code != int(mutation.ExitOK) {
		t.Errorf("exit = %d, want 0", code)
	}
	for _, needle := range []string{
		"--include",
		"--exclude",
		"--operator",
		"--profile",
		"--mutant",
		"--json",
		"rather than from the profile",
		"a filter, not a selector",
		string(CodeInertProfile),
		"describe the filtered listing",
		"the whole discovery",
		"Exit codes:",
	} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("`list --help` does not document %q:\n%s", needle, stdout)
		}
	}
	if strings.Contains(stdout, "(default ") {
		t.Errorf("`list --help` prints a pflag default, which may vary by machine:\n%s", stdout)
	}
}
