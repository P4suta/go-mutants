// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// narrowingModes are the three ways a run decides which test binaries, and
// which tests inside them, a mutant is measured against.
//
// They are the three points of the claim ADR 0010 makes, and the reason to
// measure all three rather than the two the config enumerates: `package` and
// `test` are narrowings of different widths, and a custom test command turns
// coverage off entirely, which is the width of no narrowing at all. If the
// three ever disagreed, the narrower one would be the one reporting a survivor
// that is not one -- and the cache does not key on the mode, so a warm run
// could answer with whichever width happened to run first.
var narrowingModes = []struct {
	name   string
	apply  func(*config.Config)
	widest bool
}{
	{
		name: "test",
		apply: func(cfg *config.Config) {
			cfg.Test.Narrowing = config.NarrowingTest
		},
	},
	{
		name: "package",
		apply: func(cfg *config.Config) {
			cfg.Test.Narrowing = config.NarrowingPackage
		},
	},
	{
		// A command go-mutants cannot attribute to its own per-package
		// binaries. `-count=1` is chosen because it changes nothing about what
		// the suite asserts -- it only defeats the go test cache -- so the
		// programs compared are identical and only the narrowing differs.
		name: "coverage off",
		apply: func(cfg *config.Config) {
			cfg.Test.Command = []string{"go", "test", "-count=1", "./..."}
		},
		widest: true,
	},
}

// TestEveryNarrowingReachesTheSameVerdict is the evidence for ADR 0010.
//
// The record argues that narrowing a mutant to the tests whose coverage reaches
// it reaches the same verdicts as running its binaries: a survivor under the
// tests is a survivor under the binary, a kill is confirmed against a control,
// a test that fails alone is not used to narrow, and because the outcome does
// not depend on the mode the cache does not key on it.
//
// That last clause is what makes this test necessary rather than nice. The
// outcome cache is keyed on everything that could change a verdict, and the
// narrowing mode is deliberately not in the key -- so if the modes ever
// disagreed, a warm run would answer with whichever width wrote the entry, and
// nothing would say which one that was. The argument was written down and
// nothing measured it.
//
// The subject is the verdict of every mutant, not the score: two runs can reach
// the same percentage through different mutants, and a percentage is exactly
// the summary that would hide a disagreement.
func TestEveryNarrowingReachesTheSameVerdict(t *testing.T) {
	t.Parallel()

	root := testkit.NewModule(t).Module(narrowModule).
		Source("clean/clean.go", cleanSource).
		Source("clean/clean_test.go", cleanSuite).
		Source("dirty/dirty.go", dirtySource).
		Source("dirty/dirty_test.go", dirtySuite).
		Root()

	verdicts := map[string]map[string]report.Outcome{}
	for _, mode := range narrowingModes {
		opts := optionsAt(t, root)
		mode.apply(&opts.Config)

		outcome, _, err := collect(t, t.Context(), opts)
		if err != nil {
			t.Fatalf("running with narrowing %q: %v", mode.name, err)
		}
		if outcome.Report == nil {
			t.Fatalf("the %q run published no report", mode.name)
		}
		verdicts[mode.name] = verdictsOf(outcome.Report)
		if len(verdicts[mode.name]) == 0 {
			t.Fatalf("the %q run measured no mutants, so it settles nothing", mode.name)
		}
	}

	reference := narrowingModes[0].name
	for _, mode := range narrowingModes[1:] {
		for id, want := range verdicts[reference] {
			got, ok := verdicts[mode.name][id]
			if !ok {
				t.Errorf("%s catalogued %s and %s did not;\n"+
					"\tthe catalogue is the same tree either way, so a narrowing has changed what is measured"+
					" rather than how", reference, id, mode.name)
				continue
			}
			if got != want {
				t.Errorf("%s is %s under narrowing %q and %s under %q;\n"+
					"\tADR 0010 says a narrowing reaches the same verdict, and the outcome cache\n"+
					"\tdoes not key on the mode -- so a warm run would answer with whichever ran first",
					id, want, reference, got, mode.name)
			}
		}
		for id := range verdicts[mode.name] {
			if _, ok := verdicts[reference][id]; !ok {
				t.Errorf("%s catalogued %s and %s did not", mode.name, id, reference)
			}
		}
	}
}

// differentialCorpus are the corpus modules the three narrowings are compared
// over.
//
// `coverage` is the one that matters and the fixture README says why: it is
// "the one fixture where the *right* answer and the *fast* answer differ, so a
// mutant measured against the wrong binary would survive rather than merely
// cost time". If any narrowing is going to disagree with another, it is here.
//
// `families` is the breadth: at least one live candidate for every rule in the
// registry, so the comparison covers every operator rather than the handful a
// hand-written module happens to hold. `killable` is the control -- a module
// with a known split of kills and survivors, where a disagreement would show up
// as a changed tally as well as a changed verdict.
var differentialCorpus = []string{"coverage", "families", "killable"}

// TestEveryNarrowingReachesTheSameVerdictOnTheCorpus is the same claim as
// [TestEveryNarrowingReachesTheSameVerdict], measured over the modules that
// were built to make narrowing observable.
func TestEveryNarrowingReachesTheSameVerdictOnTheCorpus(t *testing.T) {
	t.Parallel()

	for _, name := range differentialCorpus {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := testkit.Copy(t, name)

			verdicts := map[string]map[string]report.Outcome{}
			for _, mode := range narrowingModes {
				opts := optionsAt(t, root)
				mode.apply(&opts.Config)
				outcome, _, err := collect(t, t.Context(), opts)
				if err != nil {
					t.Fatalf("running %s with narrowing %q: %v", name, mode.name, err)
				}
				if outcome.Report == nil {
					t.Fatalf("the %q run over %s published no report", mode.name, name)
				}
				verdicts[mode.name] = verdictsOf(outcome.Report)
			}

			reference := narrowingModes[0].name
			if len(verdicts[reference]) == 0 {
				t.Fatalf("%s catalogued no mutants, so it settles nothing", name)
			}
			// Equal maps of nothing are equal. Every module in this list has
			// mutants a suite kills, so a run that killed none measured
			// nothing, and three runs that measured nothing agree perfectly.
			killed := 0
			for _, outcome := range verdicts[reference] {
				if outcome == report.OutcomeKilled {
					killed++
				}
			}
			if killed == 0 {
				t.Fatalf("%s reports %d mutants and none killed, so the comparison is between three runs that measured nothing",
					name, len(verdicts[reference]))
			}
			t.Logf("%s: %d mutants, %d killed, identical under %d narrowings",
				name, len(verdicts[reference]), killed, len(narrowingModes))
			for _, mode := range narrowingModes[1:] {
				for id, want := range verdicts[reference] {
					got, ok := verdicts[mode.name][id]
					if !ok {
						t.Errorf("%s: %s catalogued %s and %s did not", name, reference, id, mode.name)
						continue
					}
					if got != want {
						t.Errorf("%s: %s is %s under %q and %s under %q", name, id, want, reference, got, mode.name)
					}
				}
			}
		})
	}
}

// TestTheWidestNarrowingMeasuresTheMostTests is the negative half.
//
// Equal verdicts alone would also be produced by three runs that all did the
// same thing, which is the one way this suite could pass while proving nothing.
// So the modes are required to *differ* where the design says they do: the
// test-level pass profiles tests and narrows a clean binary's mutant to one of
// them, and the coverage-off run narrows nothing at all.
func TestTheWidestNarrowingMeasuresTheMostTests(t *testing.T) {
	t.Parallel()

	root := testkit.NewModule(t).Module(narrowModule).
		Source("clean/clean.go", cleanSource).
		Source("clean/clean_test.go", cleanSuite).
		Source("dirty/dirty.go", dirtySource).
		Source("dirty/dirty_test.go", dirtySuite).
		Root()
	cleanPkg := narrowModule + "/clean"

	narrowed := runWithNarrowing(t, root, config.NarrowingTest)
	if narrowed.Coverage.Mode != report.CoverageTest {
		t.Fatalf("coverage mode = %q, want %q", narrowed.Coverage.Mode, report.CoverageTest)
	}
	positive := onlyMutant(t, narrowed, cleanPkg, "gt-to-ge")
	if len(positive.Executions) == 0 || len(positive.Executions[0].Tests) != 1 {
		t.Errorf("the test-level run did not narrow the clean mutant to one test: %+v", positive.Executions)
	}

	wide := runWithCommand(t, root, []string{"go", "test", "-count=1", "./..."})
	if wide.Coverage.Mode == report.CoverageTest {
		t.Errorf("a custom test command still narrowed by test, so the two modes are one")
	}
	widePositive := onlyMutant(t, wide, cleanPkg, "gt-to-ge")
	if len(widePositive.Executions) > 0 && len(widePositive.Executions[0].Tests) != 0 {
		t.Errorf("the coverage-off run named tests to run, so it narrowed: %+v", widePositive.Executions)
	}
	if len(wide.Warnings) == 0 {
		t.Errorf("a custom test command published no warning; the user is not told coverage guidance is off")
	}
	if !slices.ContainsFunc(wide.Warnings, func(w report.Warning) bool {
		return strings.HasPrefix(string(w.Code), "GOM76")
	}) {
		t.Errorf("no GOM76xx warning says why coverage guidance is off: %+v", wide.Warnings)
	}
}

// verdictsOf is every catalogued mutant's outcome, by full id.
func verdictsOf(rep *report.Report) map[string]report.Outcome {
	out := make(map[string]report.Outcome, len(rep.Mutants))
	for _, mutant := range rep.Mutants {
		out[mutant.ID] = mutant.Outcome
	}
	return out
}

// runWithNarrowing measures the module once at one narrowing.
func runWithNarrowing(t *testing.T, root string, narrowing config.Narrowing) *report.Report {
	t.Helper()
	opts := optionsAt(t, root)
	opts.Config.Test.Narrowing = narrowing
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("running with narrowing %q: %v", narrowing, err)
	}
	if outcome.Report == nil {
		t.Fatalf("the %q run published no report", narrowing)
	}
	return outcome.Report
}

// runWithCommand measures the module once with a test command of its own.
func runWithCommand(t *testing.T, root string, command []string) *report.Report {
	t.Helper()
	opts := optionsAt(t, root)
	opts.Config.Test.Command = command
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("running with %v: %v", command, err)
	}
	if outcome.Report == nil {
		t.Fatalf("the run with %v published no report", command)
	}
	return outcome.Report
}
