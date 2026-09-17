// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

func TestARefusalNamesTheMutantByItsShortID(t *testing.T) {
	t.Parallel()

	t.Run("a catalogued id is shortened", func(t *testing.T) {
		t.Parallel()
		opts := fixtureOptions(t)
		full := opts.Results[0].ID
		opts.Results = append(slices.Clone(opts.Results), opts.Results[0])

		_, err := report.Build(opts)
		if got := report.CodeOf(err); got != report.CodeDuplicateEntry {
			t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeDuplicateEntry)
		}
		if want := "mutant " + full[:mutation.DisplayIDLength] + " has more than one result"; !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
		if strings.Contains(err.Error(), full) {
			t.Errorf("the refusal quotes all %d characters of the id: %v", len(full), err)
		}
	})

	t.Run("an id shorter than the display length is left alone", func(t *testing.T) {
		t.Parallel()
		opts := fixtureOptions(t)
		short := report.MutantResult{ID: "abc", Outcome: mutation.OutcomeKilled}
		opts.Results = append(slices.Clone(opts.Results), short, short)

		_, err := report.Build(opts)
		if got := report.CodeOf(err); got != report.CodeDuplicateEntry {
			t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeDuplicateEntry)
		}
		if want := "mutant abc has more than one result"; !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	})

	t.Run("a rejection claimed twice", func(t *testing.T) {
		t.Parallel()
		opts := fixtureOptions(t)
		full := opts.Rejections[0].ID
		opts.Rejections = append(slices.Clone(opts.Rejections), opts.Rejections[0])

		_, err := report.Build(opts)
		if want := "mutant " + full[:mutation.DisplayIDLength] + " has more than one rejection"; !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	})
}

func TestARowForAMutantNobodyCataloguedIsNamed(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		break_ func(o *report.Options)
		says   string
	}{
		"a result": {
			break_: func(o *report.Options) {
				o.Results = append(slices.Clone(o.Results), report.MutantResult{
					ID: staleID, Outcome: mutation.OutcomeKilled,
				})
			},
			says: "the result for mutant " + staleID[:mutation.DisplayIDLength] + " names an id that is not in this run's catalogue",
		},
		"a rejection": {
			break_: func(o *report.Options) {
				o.Rejections = append(slices.Clone(o.Rejections), report.Rejection{
					ID: staleID, Diagnostic: "does not compile",
				})
			},
			says: "the rejection for mutant " + staleID[:mutation.DisplayIDLength] + " names an id that is not in this run's catalogue",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := fixtureOptions(t)
			tc.break_(&opts)

			_, err := report.Build(opts)
			if got := report.CodeOf(err); got != report.CodeUnknownMutant {
				t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeUnknownMutant)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

func TestBuildSaysWhichOutcomeItCouldNotWrite(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	results := slices.Clone(opts.Results)
	results[0].Outcome = mutation.Outcome(42)
	opts.Results = results

	_, err := report.Build(opts)
	if got := report.CodeOf(err); got != report.CodeInvalidOutcome {
		t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidOutcome)
	}
	if !strings.Contains(err.Error(), "is not an outcome this report can write") {
		t.Errorf("the refusal is not the translation's: %v", err)
	}
}

func TestARefusalCountsInWordsAUserCanRead(t *testing.T) {
	t.Parallel()

	pass := report.Execution{Outcome: report.OutcomeSurvived, DurationMS: 5}
	for name, tc := range map[string]struct {
		executions []report.Execution
		attempts   int
		says       string
	}{
		"one against none": {
			executions: []report.Execution{pass}, attempts: 0,
			says: "reports 0 attempts and 1 execution: the two are the same fact at two resolutions",
		},
		"two against one": {
			executions: []report.Execution{pass, pass}, attempts: 1,
			says: "reports 1 attempt and 2 executions: the two are the same fact at two resolutions",
		},
		"three against two": {
			executions: []report.Execution{pass, pass, pass}, attempts: 2,
			says: "reports 2 attempts and 3 executions: the two are the same fact at two resolutions",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := fixtureOptions(t)
			results := slices.Clone(opts.Results)
			results[1].Executions = tc.executions
			results[1].Attempts = tc.attempts
			opts.Results = results

			_, err := report.Build(opts)
			if got := report.CodeOf(err); got != report.CodeInvalidExecutions {
				t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidExecutions)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}

	opts := fixtureOptions(t)
	results := slices.Clone(opts.Results)
	results[0].Executions = []report.Execution{pass}
	opts.Results = results
	_, err := report.Build(opts)
	if want := "is marked cached and carries 1 execution"; !strings.Contains(errText(err), want) {
		t.Errorf("the refusal does not say %q: %v", want, err)
	}
}

func TestARefusalAboutOneAttemptNamesTheAttempt(t *testing.T) {
	t.Parallel()

	pass := report.Execution{Outcome: report.OutcomeSurvived, DurationMS: 5}
	for name, tc := range map[string]struct {
		executions []report.Execution
		says       string
	}{
		"a verdict where an observation belongs": {
			executions: []report.Execution{{Outcome: report.OutcomeInconclusive, DurationMS: 5}},
			says:       "attempt 1 of mutant ",
		},
		"a verdict on the second pass": {
			executions: []report.Execution{pass, {Outcome: report.OutcomeNotRun, DurationMS: 5}},
			says:       "attempt 2 of mutant ",
		},
		"a memory kill that is not a kill": {
			executions: []report.Execution{pass, {Outcome: report.OutcomeSurvived, DurationMS: 5, MemoryExceeded: true}},
			says:       "attempt 2 of mutant ",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := fixtureOptions(t)
			results := slices.Clone(opts.Results)
			results[1].Executions = tc.executions
			results[1].Attempts = len(tc.executions)
			opts.Results = results

			_, err := report.Build(opts)
			if got := report.CodeOf(err); got != report.CodeInvalidExecutions {
				t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidExecutions)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

func TestARefusalListsTheValuesItWouldHaveAccepted(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		break_ func(t *testing.T, o *report.Options)
		code   report.Code
		says   string
	}{
		"the outcomes one pass can observe": {
			break_: func(t *testing.T, o *report.Options) {
				t.Helper()
				results := slices.Clone(o.Results)
				results[1].Executions = []report.Execution{{Outcome: report.OutcomeNotRun, DurationMS: 5}}
				results[1].Attempts = 1
				o.Results = results
			},
			code: report.CodeInvalidExecutions,
			says: "expected one of killed, survived, timed-out, errored",
		},
		"the reasons a mutant can be not-run for": {
			break_: func(t *testing.T, o *report.Options) {
				t.Helper()
				results := slices.Clone(o.Results)
				results[6].NotRunReason = ""
				o.Results = results
			},
			code: report.CodeInvalidNotRunReason,
			says: "pass one of interrupted, out-of-selection, other-shard",
		},
		"a reason outside that list": {
			break_: func(t *testing.T, o *report.Options) {
				t.Helper()
				results := slices.Clone(o.Results)
				results[6].NotRunReason = report.NotRunReason("bored")
				o.Results = results
			},
			code: report.CodeInvalidNotRunReason,
			says: `"bored" is not a reason a mutant can be not-run for: expected one of interrupted, out-of-selection, other-shard`,
		},
		"the selection modes": {
			break_: func(t *testing.T, o *report.Options) {
				t.Helper()
				o.Mode = report.SelectionMode("everything")
			},
			code: report.CodeInvalidSelection,
			says: `"everything" is not a selection mode: expected one of all, mutant, changed, shard`,
		},
		"where a memory bound can come from": {
			break_: func(t *testing.T, o *report.Options) {
				t.Helper()
				o.MemorySource = ""
			},
			code: report.CodeInvalidMemory,
			says: "does not say where it came from: expected one of explicit, derived, unavailable",
		},
		"a memory source outside that list": {
			break_: func(t *testing.T, o *report.Options) {
				t.Helper()
				o.MemorySource = report.MemorySource("guessed")
			},
			code: report.CodeInvalidMemory,
			says: `came from "guessed", which is not a source: expected one of explicit, derived, unavailable`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := fixtureOptions(t)
			tc.break_(t, &opts)

			_, err := report.Build(opts)
			if got := report.CodeOf(err); got != tc.code {
				t.Fatalf("Build = %v (code %q), want %s", err, got, tc.code)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

func TestBuildSurvivesARunThatCataloguedOnlyRejections(t *testing.T) {
	t.Parallel()

	located, catalog := located(t, []candidate{
		{
			path: alphaFile, pkg: alphaPackage, rule: "eq-to-neq",
			start: 100, original: "==", replacement: "!=", line: 12, column: 9, rejected: true,
		},
		{
			path: alphaFile, pkg: alphaPackage, rule: "lt-to-le",
			start: 200, original: "<", replacement: "<=", line: 24, column: 7, rejected: true,
		},
	})
	opts := fixtureOptions(t)
	opts.Catalog = catalog
	opts.Located = located
	opts.Results = nil
	opts.Selected = 0
	opts.CacheMisses, opts.CacheWrites = 0, 0
	opts.Config.Mutation.Expect = nil
	opts.Rejections = nil
	for _, m := range catalog.Mutants() {
		opts.Rejections = append(opts.Rejections, report.Rejection{ID: m.ID, Diagnostic: fixtureDiagnostic})
	}

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(r.Mutants) != 0 || len(r.Rejected) != 2 {
		t.Fatalf("the document holds %d mutants and %d rejections, want 0 and 2", len(r.Mutants), len(r.Rejected))
	}
	if r.Summary.Total != 0 {
		t.Errorf("summary.total = %d, want 0 — nothing was measured", r.Summary.Total)
	}
	if r.Summary.ScorePercent != nil {
		t.Errorf("a score of %v was computed for a run that measured nothing", *r.Summary.ScorePercent)
	}
	if len(r.Expectations) != 0 {
		t.Errorf("expectations = %v, want none", r.Expectations)
	}
}

func TestACoverageGuidedRunThatProfiledNoBinaries(t *testing.T) {
	t.Parallel()

	opts := coverageOptions(t)
	opts.CoverageBinaries = 0

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Coverage.Binaries == nil {
		t.Fatal("a coverage-guided run states no binary count")
	}
	if *r.Coverage.Binaries != 0 {
		t.Errorf("coverage.binaries = %d, want 0", *r.Coverage.Binaries)
	}
	if r.Coverage.Mode != report.CoveragePackage {
		t.Errorf("coverage.mode = %q, want %q", r.Coverage.Mode, report.CoveragePackage)
	}
}

func TestBuildRefusesACoverageBinaryCountThatIsNotOne(t *testing.T) {
	t.Parallel()

	opts := coverageOptions(t)
	opts.CoverageBinaries = -1

	_, err := report.Build(opts)
	if got := report.CodeOf(err); got != report.CodeInvalidCoverage {
		t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidCoverage)
	}
	if !strings.Contains(err.Error(), "the coverage pass reports -1 test binaries") {
		t.Errorf("the refusal does not say what it was told: %v", err)
	}
}

func TestBuildRefusesTestFactsOutsideTestMode(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		mode report.CoverageMode
		edit func(*report.MutantResult)
		want string
	}{
		"covering tests in package mode": {
			mode: report.CoveragePackage,
			edit: func(r *report.MutantResult) {
				r.CoveringTests = []report.TestRef{{Package: corePackage, Name: "TestClamp"}}
			},
			want: "names 1 covering test in a run whose coverage mode is \"package\"",
		},
		"a narrowed pass in package mode": {
			mode: report.CoveragePackage,
			edit: func(r *report.MutantResult) {
				r.Executions[0].Tests = []report.TestRef{{Package: corePackage, Name: "TestClamp"}}
			},
			want: "attempt 1 of mutant",
		},
		"covering tests with coverage off": {
			mode: report.CoverageOff,
			edit: func(r *report.MutantResult) {
				r.CoveringTests = []report.TestRef{{Package: corePackage, Name: "TestClamp"}}
			},
			want: "in a run whose coverage mode is \"off\"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := testCoverageOptions(t)
			opts.CoverageMode = tc.mode
			for i := range opts.Results {
				opts.Results[i].CoveringTests = nil
				opts.Results[i].Uncovered = opts.Results[i].Uncovered && tc.mode.Narrowed()
				for j := range opts.Results[i].Executions {
					opts.Results[i].Executions[j].Tests = nil
				}
			}
			tc.edit(&opts.Results[0])

			_, err := report.Build(opts)
			if got := report.CodeOf(err); got != report.CodeInvalidCoverage {
				t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidCoverage)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say what was wrong: %v", err)
			}
		})
	}
}

func TestATestNarrowedRunThatProfiledNoTests(t *testing.T) {
	t.Parallel()

	opts := testCoverageOptions(t)
	opts.CoverageTests = 0

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Coverage.Tests == nil {
		t.Fatal("a test-narrowed run states no test count")
	}
	if *r.Coverage.Tests != 0 {
		t.Errorf("coverage.tests = %d, want 0", *r.Coverage.Tests)
	}
	if r.Coverage.Mode != report.CoverageTest {
		t.Errorf("coverage.mode = %q, want %q", r.Coverage.Mode, report.CoverageTest)
	}
}

func TestBuildRefusesANegativeTestCount(t *testing.T) {
	t.Parallel()

	opts := testCoverageOptions(t)
	opts.CoverageTests = -1

	_, err := report.Build(opts)
	if got := report.CodeOf(err); got != report.CodeInvalidCoverage {
		t.Fatalf("Build = %v (code %q), want %s", err, got, report.CodeInvalidCoverage)
	}
	if !strings.Contains(err.Error(), "the coverage pass reports -1 tests") {
		t.Errorf("the refusal does not say what it was told: %v", err)
	}
}

func TestTheToolchainBlockIsWrittenOnlyWhenSomethingIsKnown(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		toolchain   *report.ToolchainFacts
		wantBlock   bool
		wantGoBin   string
		wantVersion string
	}{
		"nothing at all": {toolchain: nil},
		"an empty block": {toolchain: &report.ToolchainFacts{}},
		"a binary and no version": {
			toolchain: &report.ToolchainFacts{GoBin: fixtureGoBin},
			wantBlock: true, wantGoBin: fixtureGoBin, wantVersion: "unknown",
		},
		"a version and no binary": {
			toolchain: &report.ToolchainFacts{Version: fixtureGoVersionLine},
			wantBlock: true, wantGoBin: "unknown", wantVersion: fixtureGoVersionLine,
		},
		"both": {
			toolchain: &report.ToolchainFacts{GoBin: fixtureGoBin, Version: fixtureGoVersionLine},
			wantBlock: true, wantGoBin: fixtureGoBin, wantVersion: fixtureGoVersionLine,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := fixtureOptions(t)
			opts.Toolchain = tc.toolchain

			r, err := report.Build(opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !tc.wantBlock {
				if r.Test.Toolchain != nil {
					t.Errorf("a toolchain block was written for a run that knows nothing about one: %+v", *r.Test.Toolchain)
				}
				return
			}
			if r.Test.Toolchain == nil {
				t.Fatal("no toolchain block was written for a run that knows half of one")
			}
			if r.Test.Toolchain.GoBin != tc.wantGoBin {
				t.Errorf("go_bin = %q, want %q", r.Test.Toolchain.GoBin, tc.wantGoBin)
			}
			if r.Test.Toolchain.Version != tc.wantVersion {
				t.Errorf("version = %q, want %q", r.Test.Toolchain.Version, tc.wantVersion)
			}
		})
	}
}

func TestAMutantIsToldTheBoundWasReachedByItsOwnRows(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	results := slices.Clone(opts.Results)
	results[1].Outcome = mutation.OutcomeKilled
	results[1].KilledBy = alphaPackage
	results[1].Attempts = 2
	results[1].Executions = []report.Execution{
		{Outcome: report.OutcomeSurvived, DurationMS: 5, PeakMemoryBytes: 41_943_040},
		{Outcome: report.OutcomeKilled, DurationMS: 1_500, MemoryExceeded: true, PeakMemoryBytes: 1_181_116_006},
	}
	opts.Results = results

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	m := r.Mutants[1]
	if !m.MemoryExceeded {
		t.Error("a mutant one of whose passes was stopped by the bound does not say so")
	}
	if m.PeakMemoryBytes != 1_181_116_006 {
		t.Errorf("peak_memory_bytes = %d, want the highest of its rows, 1181116006", m.PeakMemoryBytes)
	}
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
