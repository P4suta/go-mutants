// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
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
	if len(implementedRules(registry.FamilyRules(mutation.FamilyBitwise))) != 0 {
		t.Error("the bitwise family is reported as implemented; this phase discovers comparisons and boolean literals only")
	}
	families := implementedFamilies()
	for _, rule := range discover.SupportedRules() {
		if !strings.Contains(families, string(rule.Family)) {
			t.Errorf("the implemented families %q do not name %s", families, rule.Family)
		}
	}
}

// TestWarnUnimplementedNamesEveryOperatorItDropped is the partial-selection
// contract.
//
// Warning only when the whole selection is unimplemented leaves the commonest
// case of all silent: `--operator comparison --operator bitwise` lists the
// comparison half and drops the other one without a word, which is exactly the
// wrong conclusion — "my code has no bitwise operators in it" — that this
// warning exists to make unreachable.
func TestWarnUnimplementedNamesEveryOperatorItDropped(t *testing.T) {
	// quoted is how the warning spells a name, which is what makes "does not
	// mention comparison" assertable: the implemented-families list at the end
	// of every line names the comparison family unquoted.
	quoted := func(name string) string { return `"` + name + `"` }

	tests := []struct {
		name      string
		operators []string
		want      []string
	}{
		{"a profile names nothing", nil, nil},
		{"one implemented family", []string{"comparison"}, nil},
		{"one implemented rule", []string{"eq-to-neq"}, nil},
		{"one unimplemented family", []string{"bitwise"}, []string{"bitwise"}},
		{"one unimplemented rule", []string{"shl-to-shr"}, []string{"shl-to-shr"}},
		{"the partial selection", []string{"comparison", "bitwise"}, []string{"bitwise"}},
		{"two unimplemented families", []string{"bitwise", "statement-deletion"}, []string{"bitwise", "statement-deletion"}},
		{"a name written twice", []string{"bitwise", "bitwise"}, []string{"bitwise"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Mutation.Operators = test.operators
			rules, err := selectRules(cfg)
			if err != nil {
				t.Fatalf("selectRules(%v): %v", test.operators, err)
			}

			var b strings.Builder
			warnUnimplemented(&b, cfg, rules)
			got := b.String()
			if len(test.want) == 0 {
				if got != "" {
					t.Fatalf("warnUnimplemented wrote %q, want nothing for %v", got, test.operators)
				}
				return
			}

			lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			if len(lines) != len(test.want) {
				t.Fatalf("warnUnimplemented wrote %d lines, want %d:\n%s", len(lines), len(test.want), got)
			}
			// One line per dropped name, in the order the names were written:
			// a diagnostic that reorders itself is one nobody can diff.
			for i, name := range test.want {
				if !strings.Contains(lines[i], quoted(name)) {
					t.Errorf("line %d = %q, want it to name %s", i, lines[i], quoted(name))
				}
				if !strings.Contains(lines[i], string(CodeUnimplementedOperators)) {
					t.Errorf("line %d = %q, want the %s code", i, lines[i], CodeUnimplementedOperators)
				}
			}
			// The implemented half is listed, so it must not be reported as
			// dropped as well.
			if strings.Contains(got, quoted("comparison")) {
				t.Errorf("warnUnimplemented reported the comparison family as undiscoverable:\n%s", got)
			}
		})
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
