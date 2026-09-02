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

// The toolchain-backed half of `list` — the snapshot, the loader, the
// catalogue it produces — lives in list_integration_test.go. Everything here
// runs without a Go toolchain, which is what keeps the argument checking, the
// selection algebra, and the rendering rules cheap enough to assert
// exhaustively.

func TestListRefusesJSONWithQuiet(t *testing.T) {
	// The rejection has to happen before anything is discovered, or a user who
	// typed two contradictory flags waits for a snapshot first.
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
	// The message has to say what to type instead, since a path argument is
	// what somebody coming from another mutation tester will reach for first.
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

// TestSelectRulesFollowsTheProfileUntilAnOperatorIsNamed pins the one place
// where `--operator` and `--profile` could disagree.
//
// A named operator is looked up in the whole catalogue, so a family the profile
// does not reach is still honoured. The alternative — intersecting the two —
// answers "run the bitwise family" with an empty listing and no explanation.
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

	// bitwise is a `strong` family, so the balanced profile does not select it.
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

	// A family and one of its own rules overlap; the result is the family, once,
	// in registry order rather than in the order the names were written.
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

// TestImplementedRulesIsASubsetOfTheSelection proves the warning that says a
// selection found nothing is asking the right question: it is discovery's own
// statement about which rules it implements, not a list kept here.
func TestImplementedRulesIsASubsetOfTheSelection(t *testing.T) {
	registry := mutation.CanonicalRegistry()

	implemented := implementedRules(registry.SelectTier(mutation.TierAll))
	if len(implemented) != len(discover.SupportedRules()) {
		t.Errorf("the whole catalogue implements %d rules, discovery reports %d",
			len(implemented), len(discover.SupportedRules()))
	}
	// Discovery implements the whole catalogue today, so the filter is the
	// identity over any selection. That is a fact about discovery and not about
	// this filter, which is exactly why it is asserted through
	// [discover.SupportedRules] rather than through a family named here: the
	// day a v2 rule lands in the registry ahead of discovery, this keeps
	// answering correctly without being edited.
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

// TestWarnUnimplementedIsSilentForTheWholeCatalogue is what the
// partial-selection contract became when discovery finished the catalogue.
//
// The contract was one warning line per name the selection dropped, so that
// `--operator comparison --operator bitwise` could not list the comparison half
// and drop the other one without a word — leaving the user with exactly the
// wrong conclusion, "my code has no bitwise operators in it". Every rule in the
// catalogue is discovered now, so no name can be dropped and the per-name
// branch has no reachable input.
//
// [warnUnimplemented] is kept anyway, for the same reason discovery keeps the
// branch that ignores a rule it has not implemented: a v2 rule lands in the
// registry before it lands in discovery, and the first thing it must not do is
// print an empty listing without a word. This test pins the state that makes
// the warning unreachable — every catalogue name, family and rule alike, is
// silent — so the day one of them stops being discovered is the day this fails
// and the per-name assertions have to come back with it.
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
				// The same name twice, which is also the deduplication case:
				// one dropped name never earned two lines.
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

// TestWarnUnimplementedStillSaysWhyAnEmptyListingIsEmpty covers the message
// itself, which the case above can no longer reach through a real selection.
//
// The aggregate form is the one a profile takes: a tier is not a list of names
// the user chose between, so naming its unimplemented members would be a wall
// of text about a decision they did not make. Driving it directly with a
// selection that resolved to nothing discoverable keeps the wording, the code,
// and the "implemented so far" list under test while nothing in the catalogue
// can produce that selection.
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

// TestWarnInertProfileFiresOnlyOnThePrecedenceInversion pins which of the four
// ways a profile can go unused is worth a word.
//
// A named operator always beats a profile — that is the documented rule, and it
// is fine when the user typed both on one command line, where both are in front
// of them. It is not fine when the operators came from .go-mutants.toml and the
// profile came from a flag: the help text promises that flags override the file,
// and there the file wins over something typed for this invocation.
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
			// The user cannot act on "your profile did nothing": the line has to
			// name the tier that lost, the file that won, the key it won with,
			// and the two ways out.
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

// TestOperatorRulesAnswersTheSameQuestionAsTheSelection keeps the diagnostic and
// the selection reading one catalogue. A warning built from a second, drifting
// resolution of the same names is a warning that describes a selection nobody
// made.
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
	// The catalogue schema requires a non-empty version, so there is no state in
	// which the field may be left blank.
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

// TestBranchProofAppearsInTheCatalogDocument carries the branch proof through
// the same join and into the published document.
//
// The absence is asserted on the encoded bytes rather than on the struct,
// because `branch` is an optional property: a mutant discovery proved nothing
// about has to carry no key at all, and a `null` would be a different document
// to everybody's decoder.
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

// branchProofDiscovery builds a discovery result holding two candidates in one
// file: one whose condition discovery proved a body span for, and one it proved
// nothing about.
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
		result:          result,
		catalog:         catalog,
		workspaceDigest: strings.Repeat("ab", 32),
	}
}

// TestCatalogDocumentJoinsCoordinatesOntoTheCatalogue covers the one piece of
// bookkeeping between discovery and the document: the catalogue knows a mutant's
// identity and its bytes, and only the discovery result knows which line, column,
// and package a human would look for it at.
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

	// The filter is a prefix of the full id, which the display id is itself a
	// prefix of, so both spellings of an id select the same mutant.
	for _, prefix := range []string{m.ID, m.DisplayID, m.ID[:mutation.MinPrefixLength]} {
		filtered, filterErr := found.document(config.Defaults(), prefix)
		if filterErr != nil {
			t.Fatalf("filtering by %q: %v", prefix, filterErr)
		}
		if len(filtered.Mutants) != 1 {
			t.Errorf("filtering by %q listed %d mutants, want 1", prefix, len(filtered.Mutants))
		}
	}

	// A prefix that matches nothing is an empty listing, never an error: the
	// flag is a filter, and "no mutant here" is an answer.
	empty, err := found.document(config.Defaults(), strings.Repeat("f", mutation.IDHexLength))
	if err != nil {
		t.Fatalf("filtering by an unmatched prefix: %v", err)
	}
	if len(empty.Mutants) != 0 {
		t.Errorf("an unmatched prefix listed %d mutants, want none", len(empty.Mutants))
	}
	// The skips describe the discovery pass, not the filtered listing, so they
	// survive a filter that removed every mutant.
	if len(empty.Skips) != len(doc.Skips) {
		t.Errorf("filtering changed the skips from %d rows to %d", len(doc.Skips), len(empty.Skips))
	}
}

// TestCatalogDocumentRefusesAMutantItCannotLocate is the invariant behind that
// join. It cannot happen through the command line — the catalogue is built from
// the same candidates — and it is checked because the alternative is a document
// whose coordinates point at nothing.
func TestCatalogDocumentRefusesAMutantItCannotLocate(t *testing.T) {
	found := oneMutantDiscovery(t)
	found.result.Candidates = nil

	_, err := found.document(config.Defaults(), "")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeCatalogMismatch {
		t.Fatalf("document() = %v, want %s", err, CodeCatalogMismatch)
	}
}

// oneMutantDiscovery builds a discovery result holding a single candidate,
// catalogued the way the pipeline catalogues one.
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
		result:          result,
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
		// The two decisions a user cannot guess: an operator name is honoured
		// whatever the profile says, and --mutant filters rather than selects.
		"rather than from the profile",
		"a filter, not a selector",
		// The consequence of the first, and the one place the precedence a user
		// typed can be overturned by a file: it is a diagnostic, not silence.
		string(CodeInertProfile),
		// The counts under a filtered listing are the filtered counts; only the
		// skip breakdown describes the whole pass. The help said the opposite of
		// this for one release, which is the kind of sentence a test has to hold
		// in place because nothing else does.
		"describe the filtered listing",
		"the whole discovery",
		"Exit codes:",
	} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("`list --help` does not document %q:\n%s", needle, stdout)
		}
	}
	// Help output has to be diffable between two machines, so no flag may print
	// a default that depends on the host.
	if strings.Contains(stdout, "(default ") {
		t.Errorf("`list --help` prints a pflag default, which may vary by machine:\n%s", stdout)
	}
}
