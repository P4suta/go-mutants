// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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

func constantsOfType(t *testing.T, typeName string) []string {
	t.Helper()

	return constantsOfTypeIn(t, "api.go", nil, typeName)
}

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

func sameNames(declared, enumerated []string) bool {
	return slices.Equal(declared, enumerated)
}

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
