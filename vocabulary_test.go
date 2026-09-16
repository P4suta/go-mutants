// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The published result vocabularies against the ones the engine produces.
//
// A consumer switching on [gomutants.Outcome] has no compiler to tell it when a
// case is missing: Go's switch is exhaustive only by convention, so a value the
// engine starts returning reaches a `default` that was written for values that
// never arrive. The vocabularies are small and they are stable, and neither of
// those is a reason not to say what they are — a consumer that wants to be told
// at build time needs a list it can pin, and this is where that list has to
// agree with what the engine actually has.
package gomutants_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// TestKnownOutcomesIsTheEnginesOutcomeVocabulary pins the published list to the
// one the engine settles a mutant with.
//
// [gomutants.Session.Exec] spells its result by converting the engine's own
// outcome, so the two vocabularies are one vocabulary written twice. Being
// written twice is what makes this worth a test: the published list is the
// thing a consumer pins, and a consumer pinning a list the engine has already
// grown past learns nothing from having pinned it.
func TestKnownOutcomesIsTheEnginesOutcomeVocabulary(t *testing.T) {
	t.Parallel()

	var want []gomutants.Outcome
	for _, outcome := range mutation.Outcomes() {
		want = append(want, gomutants.Outcome(outcome.String()))
	}

	if diff := cmp.Diff(want, gomutants.KnownOutcomes()); diff != "" {
		t.Errorf("KnownOutcomes() is not what the engine settles a mutant with (-engine +published):\n%s", diff)
	}
}

// TestKnownProbeOutcomesIsTheEnginesProbeVocabulary pins the published list to
// the one a probe pass ends with.
func TestKnownProbeOutcomesIsTheEnginesProbeVocabulary(t *testing.T) {
	t.Parallel()

	var want []gomutants.ProbeOutcome
	for _, outcome := range execute.ProbeOutcomes() {
		want = append(want, gomutants.ProbeOutcome(outcome))
	}

	if diff := cmp.Diff(want, gomutants.KnownProbeOutcomes()); diff != "" {
		t.Errorf("KnownProbeOutcomes() is not what a probe pass ends with (-engine +published):\n%s", diff)
	}
}

// TestTheOutcomeVocabulariesAreFreshSlices refuses a list a caller can edit out
// from under the next one, which is the contract [gomutants.KnownPreparePhases]
// already states and these two have to match.
func TestTheOutcomeVocabulariesAreFreshSlices(t *testing.T) {
	t.Parallel()

	first := gomutants.KnownOutcomes()
	first[0] = gomutants.Outcome("edited")
	if gomutants.KnownOutcomes()[0] == gomutants.Outcome("edited") {
		t.Error("KnownOutcomes() handed out a slice a caller can edit for everybody")
	}

	probes := gomutants.KnownProbeOutcomes()
	probes[0] = gomutants.ProbeOutcome("edited")
	if gomutants.KnownProbeOutcomes()[0] == gomutants.ProbeOutcome("edited") {
		t.Error("KnownProbeOutcomes() handed out a slice a caller can edit for everybody")
	}
}

// TestTheOutcomeVocabulariesNameEveryConstantDeclaredBesideThem reads the
// declarations rather than trusting the lists.
//
// A list of constants and a list that enumerates them are two statements, and
// the second one is maintained by remembering. [TestOutcomeNames] in
// internal/mutation shows what that costs: it compares the enumeration against
// a map written in the test, so a seventh constant added to neither is a
// seventh constant nothing mentions. The declarations are the thing that cannot
// be forgotten, because adding one is the act itself — so they are what these
// lists are checked against.
func TestTheOutcomeVocabulariesNameEveryConstantDeclaredBesideThem(t *testing.T) {
	t.Parallel()

	for _, vocabulary := range []struct {
		typeName string
		declared func([]string) bool
	}{
		{"Outcome", func(names []string) bool { return sameNames(names, outcomeNames(gomutants.KnownOutcomes())) }},
		{"ProbeOutcome", func(names []string) bool { return sameNames(names, probeNames(gomutants.KnownProbeOutcomes())) }},
	} {
		declared := constantsOfType(t, vocabulary.typeName)
		if len(declared) == 0 {
			t.Fatalf("no %s constants found in api.go; the parser stopped seeing what it reads", vocabulary.typeName)
		}
		if !vocabulary.declared(declared) {
			t.Errorf("the %s constants declared in api.go are %v, and the list beside them names something else;\n"+
				"\ta constant nothing enumerates is one a consumer pinning the list will never be told about",
				vocabulary.typeName, declared)
		}
	}
}

// constantsOfType is every constant in api.go declared with the named type, in
// declaration order.
func constantsOfType(t *testing.T, typeName string) []string {
	t.Helper()

	return constantsOfTypeIn(t, "api.go", nil, typeName)
}

// constantsOfTypeIn is constantsOfType against named source, so that the reader
// can be shown a declaration this repository does not have.
func constantsOfTypeIn(t *testing.T, name string, src any, typeName string) []string {
	t.Helper()

	path := name
	if src == nil {
		path = filepath.Join(testkit.Root(t), name)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}

	var names []string
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := value.Type.(*ast.Ident)
			if !ok || ident.Name != typeName {
				continue
			}
			for _, name := range value.Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

// outcomeNames is the identifier each outcome is declared under. The wire name
// and the identifier differ — `not_run` against OutcomeNotRun — so the two
// lists are joined by construction here rather than by transforming one into
// the other, which would be this test deciding the spelling rule.
func outcomeNames(outcomes []gomutants.Outcome) []string {
	byValue := map[gomutants.Outcome]string{
		gomutants.OutcomeNotRun:       "OutcomeNotRun",
		gomutants.OutcomeKilled:       "OutcomeKilled",
		gomutants.OutcomeSurvived:     "OutcomeSurvived",
		gomutants.OutcomeTimedOut:     "OutcomeTimedOut",
		gomutants.OutcomeInconclusive: "OutcomeInconclusive",
		gomutants.OutcomeErrored:      "OutcomeErrored",
	}
	names := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		names = append(names, byValue[outcome])
	}
	return names
}

// probeNames is outcomeNames for the probe vocabulary.
func probeNames(outcomes []gomutants.ProbeOutcome) []string {
	byValue := map[gomutants.ProbeOutcome]string{
		gomutants.ProbeMeasured:    "ProbeMeasured",
		gomutants.ProbeTestFailed:  "ProbeTestFailed",
		gomutants.ProbeTimedOut:    "ProbeTimedOut",
		gomutants.ProbeUnavailable: "ProbeUnavailable",
	}
	names := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		names = append(names, byValue[outcome])
	}
	return names
}

// sameNames reports whether two identifier lists are equal, order included.
func sameNames(declared, enumerated []string) bool {
	return slices.Equal(declared, enumerated)
}

// TestTheDeclarationReaderSeesAConstantNoListNames is the other half of
// [TestTheOutcomeVocabulariesNameEveryConstantDeclaredBesideThem].
//
// That test passes when the declarations and the list agree, and it would pass
// just as quietly if the reader had stopped finding declarations at all — an
// empty list compared against an empty list is agreement. So the reader is
// shown a constant this repository does not declare, and has to find it. A
// gate whose failure nobody has observed is half a gate.
func TestTheDeclarationReaderSeesAConstantNoListNames(t *testing.T) {
	t.Parallel()

	const src = `package gomutants

type Outcome string

const (
	OutcomeKilled   Outcome = "killed"
	OutcomeInvented Outcome = "invented"
)
`

	got := constantsOfTypeIn(t, "invented.go", src, "Outcome")
	want := []string{"OutcomeKilled", "OutcomeInvented"}
	if !slices.Equal(got, want) {
		t.Errorf("the reader found %v in a file declaring %v;\n"+
			"\tthe other test compares what this finds against a list, and finding nothing "+
			"is how that comparison passes without looking", got, want)
	}
}
