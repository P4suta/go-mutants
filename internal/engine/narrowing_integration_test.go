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
		name: "coverage off",
		apply: func(cfg *config.Config) {
			cfg.Test.Command = []string{"go", "test", "-count=1", "./..."}
		},
		widest: true,
	},
}

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

var differentialCorpus = []string{"coverage", "families", "killable"}

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

func verdictsOf(rep *report.Report) map[string]report.Outcome {
	out := make(map[string]report.Outcome, len(rep.Mutants))
	for _, mutant := range rep.Mutants {
		out[mutant.ID] = mutant.Outcome
	}
	return out
}

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
