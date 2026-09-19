// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/report"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
)

const (
	branchBodyStartLine   = 10
	branchBodyStartColumn = 2
	branchBodyEndLine     = 12
	branchBodyEndColumn   = 16
	branchMutantLine      = 9
	branchMutantColumn    = 4
	timeoutSample         = 2 * time.Second
	timeoutLimit          = 3 * time.Second

	anotherMutantIndex         = 3
	selectedWithoutDisposition = 5
)

func branchMutant(change func(*gomutants.Mutant)) gomutants.Mutant {
	mutant := gomutants.Mutant{
		ID: "m-1", Line: branchMutantLine, Column: branchMutantColumn,
		Branch: &gomutants.BranchProof{
			BodyStartLine: branchBodyStartLine, BodyStartColumn: branchBodyStartColumn,
			BodyEndLine: branchBodyEndLine, BodyEndColumn: branchBodyEndColumn,
		},
	}
	if change != nil {
		change(&mutant)
	}
	return mutant
}

func TestANarrowedBranchSpanIsTheBodyAMutantOpensAheadOfIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		change   func(*gomutants.Mutant)
		narrowed bool
	}{
		{name: "a mutant a line above its body", narrowed: true},
		{
			name: "a mutant a column before its body",
			change: func(m *gomutants.Mutant) {
				m.Line, m.Column = branchBodyStartLine, branchBodyStartColumn-1
			},
			narrowed: true,
		},
		{name: "a mutant that gates no branch", change: func(m *gomutants.Mutant) { m.Branch = nil }},
		{
			name:   "a body that starts on line zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyStartLine = 0 },
		},
		{
			name:   "a body that starts in column zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyStartColumn = 0 },
		},
		{
			name:   "a body that ends on line zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyEndLine = 0 },
		},
		{
			name:   "a body that ends in column zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyEndColumn = 0 },
		},
		{
			name:   "a body that ends on an earlier line",
			change: func(m *gomutants.Mutant) { m.Branch.BodyEndLine = branchBodyStartLine - 1 },
		},
		{
			name: "a body that ends in an earlier column of its line",
			change: func(m *gomutants.Mutant) {
				m.Branch.BodyEndLine = branchBodyStartLine
				m.Branch.BodyEndColumn = branchBodyStartColumn - 1
			},
		},
		{
			name: "a body that opens and closes on one line",
			change: func(m *gomutants.Mutant) {
				m.Branch.BodyEndLine = branchBodyStartLine
				m.Branch.BodyEndColumn = branchBodyStartColumn
			},
			narrowed: true,
		},
		{
			name:   "a mutant past the line its body starts on",
			change: func(m *gomutants.Mutant) { m.Line = branchBodyStartLine + 1 },
		},
		{
			name: "a mutant where its body starts",
			change: func(m *gomutants.Mutant) {
				m.Line, m.Column = branchBodyStartLine, branchBodyStartColumn
			},
		},
		{
			name: "a mutant past the column its body starts in",
			change: func(m *gomutants.Mutant) {
				m.Line, m.Column = branchBodyStartLine, branchBodyStartColumn+1
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			span, narrowed := narrowedBranchSpan(branchMutant(test.change))
			if narrowed != test.narrowed {
				t.Fatalf("narrowedBranchSpan = (%+v, %t), want %t", span, narrowed, test.narrowed)
			}
			if !narrowed {
				if span != (goanalysis.CoverageSpan{}) {
					t.Errorf("a mutant it refused answered with %+v, want no span", span)
				}
				return
			}
			if span.StartLine != branchBodyStartLine || span.StartColumn != branchBodyStartColumn {
				t.Fatalf("narrowedBranchSpan = %+v, want the body the mutant gates", span)
			}
		})
	}
}

func TestAMutationExecutionTimeoutAddsItsSamplesUpToItsLimit(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		limit   time.Duration
		samples []time.Duration
		want    time.Duration
	}{
		{name: "no sample at all", limit: timeoutLimit},
		{name: "one sample under the limit", limit: timeoutLimit, samples: []time.Duration{time.Second}, want: time.Second},
		{
			name: "samples that reach the limit exactly", limit: timeoutLimit,
			samples: []time.Duration{time.Second, timeoutSample}, want: timeoutLimit,
		},
		{
			name: "samples that pass the limit", limit: timeoutLimit,
			samples: []time.Duration{timeoutSample, timeoutSample}, want: timeoutLimit,
		},
		{
			name:    "no limit at all",
			samples: []time.Duration{timeoutSample, timeoutSample}, want: 2 * timeoutSample,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationExecutionTimeout(test.limit, test.samples...); got != test.want {
				t.Fatalf("mutationExecutionTimeout(%s, %v) = %s, want %s",
					test.limit, test.samples, got, test.want)
			}
		})
	}
}

func TestAControlExecutionTimeoutFallsBackToTheLimitWhenNothingWasSampled(t *testing.T) {
	t.Parallel()
	if got := controlExecutionTimeout(timeoutLimit); got != timeoutLimit {
		t.Fatalf("controlExecutionTimeout with no sample = %s, want the limit %s", got, timeoutLimit)
	}
	if got := controlExecutionTimeout(timeoutLimit, time.Second); got != time.Second {
		t.Fatalf("controlExecutionTimeout with one sample = %s, want %s", got, time.Second)
	}
	if got := controlExecutionTimeout(0); got != 0 {
		t.Fatalf("controlExecutionTimeout with no limit and no sample = %s, want none", got)
	}
}

func TestValidatingAMutationCatalogRefusesEveryShapeItCannotAccountFor(t *testing.T) {
	t.Parallel()
	sound := gomutants.Catalog{
		Mutants: []gomutants.Mutant{
			{ID: "m-1", Accepted: true},
			{ID: "m-2"},
		},
		Rejections: []gomutants.Rejection{{ID: "m-2", Diagnostic: "does not compile"}},
	}
	for _, test := range []struct {
		name    string
		change  func(*gomutants.Catalog)
		refuses string
	}{
		{name: "a catalog that accounts for every mutant"},
		{
			name:   "a mutant with no identity",
			change: func(c *gomutants.Catalog) { c.Mutants[0].ID = "" }, refuses: "empty mutant ID",
		},
		{
			name:    "one identity used twice",
			change:  func(c *gomutants.Catalog) { c.Mutants[1].ID = "m-1" },
			refuses: "duplicate mutant m-1",
		},
		{
			name:    "a rejection of a mutant nothing catalogued",
			change:  func(c *gomutants.Catalog) { c.Rejections[0].ID = "m-3" },
			refuses: "absent from the mutation catalog",
		},
		{
			name: "one rejection written twice",
			change: func(c *gomutants.Catalog) {
				c.Rejections = append(c.Rejections, c.Rejections[0])
			},
			refuses: "duplicate rejection m-2",
		},
		{
			name:    "a mutant that is both executable and rejected",
			change:  func(c *gomutants.Catalog) { c.Mutants[1].Accepted = true },
			refuses: "both executable and compile-rejected",
		},
		{
			name:    "a mutant that is neither executable nor rejected",
			change:  func(c *gomutants.Catalog) { c.Rejections = nil },
			refuses: "has no compile rejection",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog := gomutants.Catalog{
				Mutants:    append([]gomutants.Mutant(nil), sound.Mutants...),
				Rejections: append([]gomutants.Rejection(nil), sound.Rejections...),
			}
			if test.change != nil {
				test.change(&catalog)
			}
			err := validateMutationCatalog(catalog)
			switch {
			case test.refuses == "" && err != nil:
				t.Fatalf("validateMutationCatalog refused a sound catalog: %v", err)
			case test.refuses != "" && (err == nil || !strings.Contains(err.Error(), test.refuses)):
				t.Fatalf("validateMutationCatalog reported %v, want it to say %q", err, test.refuses)
			}
		})
	}
}

func TestAValidCheckpointProbeFactCarriesOnlyWhatAMeasurementCan(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		measured  bool
		duration  int64
		infected  []uint32
		wholeTree bool
		want      bool
	}{
		{name: "a measurement of nothing", measured: true, want: true},
		{
			name: "a measurement that took time and saw infections", measured: true,
			duration: 1, infected: []uint32{1, 2}, want: true,
		},
		{name: "a measurement that claims the whole tree", measured: true, wholeTree: true, want: true},
		{name: "no measurement at all", want: true},
		{name: "a duration below zero", measured: true, duration: -1},
		{name: "no measurement that took time", duration: 1},
		{name: "no measurement that saw an infection", infected: []uint32{1}},
		{name: "no measurement that claims the whole tree", wholeTree: true},
		{name: "infections that repeat", measured: true, infected: []uint32{1, 1}},
		{name: "infections out of order", measured: true, infected: []uint32{2, 1}},
		{
			name: "an ascending run that dips at the end", measured: true,
			infected: []uint32{1, 2, 3, 2},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := validCheckpointProbeFact(test.measured, test.duration, test.infected, test.wholeTree)
			if got != test.want {
				t.Fatalf("validCheckpointProbeFact(%t, %d, %v, %t) = %t, want %t",
					test.measured, test.duration, test.infected, test.wholeTree, got, test.want)
			}
		})
	}
}

func TestAMutationProbeIndexFingerprintChangesWithEveryFactItCovers(t *testing.T) {
	t.Parallel()
	base := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{Index: 1, ID: "m-1", Accepted: true, Probed: true},
		{Index: 2, ID: "m-2"},
	}}
	fingerprint := mutationProbeIndexFingerprint(base)
	if fingerprint != "ad0fd21fcf1d13be7b86cba193c071487c16e1eb6a966d37b4297c89547daf24" {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	for _, test := range []struct {
		name   string
		change func(*gomutants.Catalog)
	}{
		{name: "another index", change: func(c *gomutants.Catalog) { c.Mutants[0].Index = anotherMutantIndex }},
		{name: "another identity", change: func(c *gomutants.Catalog) { c.Mutants[0].ID = "m-3" }},
		{name: "one fewer mutant", change: func(c *gomutants.Catalog) { c.Mutants = c.Mutants[:1] }},
		{name: "a mutant nothing accepts", change: func(c *gomutants.Catalog) { c.Mutants[0].Accepted = false }},
		{name: "a mutant nothing probed", change: func(c *gomutants.Catalog) { c.Mutants[0].Probed = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog := gomutants.Catalog{Mutants: append([]gomutants.Mutant(nil), base.Mutants...)}
			test.change(&catalog)
			if mutationProbeIndexFingerprint(catalog) == fingerprint {
				t.Fatalf("changing %s left the fingerprint at %q", test.name, fingerprint)
			}
		})
	}
	reordered := gomutants.Catalog{Mutants: []gomutants.Mutant{base.Mutants[1], base.Mutants[0]}}
	if mutationProbeIndexFingerprint(reordered) != fingerprint {
		t.Fatal("the fingerprint moved when the same mutants were listed in another order")
	}
}

func accountingCatalog() gomutants.Catalog {
	return gomutants.Catalog{
		Mutants: []gomutants.Mutant{
			{ID: "m-killed", Accepted: true, Path: "a.go", Line: 1},
			{ID: "m-survived", Accepted: true, Path: "a.go", Line: 2},
			{ID: "m-inconclusive", Accepted: true, Path: "a.go", Line: 3},
			{ID: "m-accepted", Accepted: true, Path: "a.go", Line: 4},
			{ID: "m-rejected", Path: "a.go", Line: 5},
			{ID: "m-outside", Path: "a.go", Line: 6},
		},
		Rejections: []gomutants.Rejection{{ID: "m-rejected", Diagnostic: "does not compile"}},
	}
}

func accountingEvaluation() MutationEvaluation {
	return MutationEvaluation{
		Evidence: []report.Evidence{
			{Kind: "mutation", ID: "m-killed", Status: "killed", Detail: "TestOne"},
			{Kind: "mutation", ID: "m-rejected", Status: "compile-rejected", Detail: "does not compile"},
			{Kind: "mutation", ID: "m-accepted", Status: "accepted", Detail: "finding-a"},
			{Kind: "baseline", ID: "m-killed", Status: "passed"},
		},
		Findings: []report.Finding{
			{ID: "f1", Kind: "surviving-mutant", MutantID: "m-survived", Summary: "survived"},
			{ID: "f2", Kind: "mutation-timeout", MutantID: "m-inconclusive", Summary: "timed out"},
		},
	}
}

func TestMutantAccountingCountsEachDispositionOnceAndNamesEveryMutant(t *testing.T) {
	t.Parallel()
	accounting, dispositions := mutationAccounting(
		accountingCatalog(), "", accountingEvaluation(), &MutationEvidence{}, nil)

	want := report.MutantAccounting{
		Discovered: 6, Selected: 5, Executed: 3,
		Killed: 1, Survived: 1, Inconclusive: 1,
		CompileRejected: 1, Accepted: 1, OutOfScope: 1,
	}
	if accounting != want {
		t.Fatalf("mutationAccounting = %+v, want %+v", accounting, want)
	}
	if len(dispositions) != len(accountingCatalog().Mutants) {
		t.Fatalf("the inventory names %d mutants, want every catalogued one", len(dispositions))
	}
	byID := make(map[string]report.MutantDisposition, len(dispositions))
	for _, disposition := range dispositions {
		byID[disposition.ID] = disposition
	}
	for id, status := range map[string]report.MutantStatus{
		"m-killed": report.MutantKilled, "m-survived": report.MutantSurvived,
		"m-inconclusive": report.MutantInconclusive, "m-accepted": report.MutantAccepted,
		"m-rejected": report.MutantCompileRejected, "m-outside": report.MutantOutOfScope,
	} {
		if byID[id].Status != status {
			t.Errorf("mutant %s is %q, want %q", id, byID[id].Status, status)
		}
	}
	if byID["m-outside"].Detail != "outside the resolved mutation scope" {
		t.Errorf("a mutant outside the scope says %q", byID["m-outside"].Detail)
	}
	if byID["m-killed"].Detail != "TestOne" {
		t.Errorf("a killed mutant says %q, want the target that killed it", byID["m-killed"].Detail)
	}
}

func TestMutantAccountingNamesAnUnexplainedMutantRatherThanGuessing(t *testing.T) {
	t.Parallel()
	accounting, dispositions := mutationAccounting(
		accountingCatalog(), "", MutationEvaluation{}, &MutationEvidence{}, nil)

	if accounting.Unknown != selectedWithoutDisposition {
		t.Fatalf("an evaluation that concluded nothing counted %d unknown, want %d",
			accounting.Unknown, selectedWithoutDisposition)
	}
	for _, disposition := range dispositions {
		if disposition.Status == report.MutantUnknown &&
			disposition.Detail != "selected mutant has no terminal disposition" {
			t.Errorf("an unexplained mutant says %q", disposition.Detail)
		}
	}
}

func TestMutantAccountingNarrowsToTheMutantAReplayNames(t *testing.T) {
	t.Parallel()
	accounting, dispositions := mutationAccounting(
		accountingCatalog(), "m-killed", accountingEvaluation(), &MutationEvidence{}, nil)

	if accounting.Selected != 1 || accounting.Killed != 1 || accounting.Executed != 1 {
		t.Fatalf("a replay selected %+v, want one mutant killed", accounting)
	}
	if accounting.OutOfScope != len(accountingCatalog().Mutants)-1 {
		t.Fatalf("a replay left %d mutants out of scope, want every other one", accounting.OutOfScope)
	}
	for _, disposition := range dispositions {
		if disposition.ID != "m-killed" && disposition.Status != report.MutantOutOfScope {
			t.Errorf("mutant %s is %q, want it out of the replay scope", disposition.ID, disposition.Status)
		}
	}
}

func TestMutantAccountingCountsReuseAgainstTheDispositionItReused(t *testing.T) {
	t.Parallel()
	accounting, dispositions := mutationAccounting(
		accountingCatalog(), "", accountingEvaluation(), &MutationEvidence{},
		map[string]string{"m-killed": "run-before", "m-survived": "run-before"})

	if accounting.ReusedKilled != 1 || accounting.ReusedSurvived != 1 {
		t.Fatalf("mutationAccounting counted %d reused kills and %d reused survivals, want one of each",
			accounting.ReusedKilled, accounting.ReusedSurvived)
	}
	for _, disposition := range dispositions {
		reused := disposition.ID == "m-killed" || disposition.ID == "m-survived"
		if disposition.Reused != reused {
			t.Errorf("mutant %s is reused %t, want %t", disposition.ID, disposition.Reused, reused)
		}
		if reused && disposition.Provenance != "run-before" {
			t.Errorf("mutant %s names the provenance %q", disposition.ID, disposition.Provenance)
		}
	}
}

func comparableTarget(pkg, name string, kind goanalysis.TargetKind, id string) TargetEvidence {
	return TargetEvidence{Target: goanalysis.Target{Package: pkg, Name: name, Kind: kind, ID: id}}
}

func TestMutationGroupTargetsAreOrderedByPackageThenNameThenKindThenIdentity(t *testing.T) {
	t.Parallel()
	base := comparableTarget("b", "TestB", goanalysis.KindTest, "t2")
	for _, test := range []struct {
		name  string
		first TargetEvidence
		want  int
	}{
		{name: "the same target", first: base},
		{
			name:  "an earlier package",
			first: comparableTarget("a", "TestZ", goanalysis.KindTest, "t9"), want: -1,
		},
		{
			name:  "a later package",
			first: comparableTarget("c", "TestA", goanalysis.KindTest, "t1"), want: 1,
		},
		{
			name:  "an earlier name",
			first: comparableTarget("b", "TestA", goanalysis.KindTest, "t9"), want: -1,
		},
		{
			name:  "a later name",
			first: comparableTarget("b", "TestC", goanalysis.KindTest, "t1"), want: 1,
		},
		{
			name:  "an earlier kind",
			first: comparableTarget("b", "TestB", goanalysis.KindExample, "t9"), want: -1,
		},
		{
			name:  "an earlier identity",
			first: comparableTarget("b", "TestB", goanalysis.KindTest, "t1"), want: -1,
		},
		{
			name:  "a later identity",
			first: comparableTarget("b", "TestB", goanalysis.KindTest, "t3"), want: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := compareMutationGroupTargets(test.first, base)
			if (got < 0) != (test.want < 0) || (got > 0) != (test.want > 0) {
				t.Fatalf("compareMutationGroupTargets = %d, want the sign of %d", got, test.want)
			}
		})
	}
}

func TestAPlanningDurationPrefersWhatTheProbeMeasured(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		target TargetEvidence
		want   time.Duration
	}{
		{name: "a target nothing probed", target: TargetEvidence{Duration: time.Second}, want: time.Second},
		{
			name:   "a target a probe measured",
			target: TargetEvidence{Duration: time.Second, ProbeDuration: timeoutSample}, want: timeoutSample,
		},
		{
			name:   "a probe that measured no time",
			target: TargetEvidence{Duration: time.Second, ProbeDuration: 0}, want: time.Second,
		},
		{
			name:   "a probe that measured less than no time",
			target: TargetEvidence{Duration: time.Second, ProbeDuration: -time.Second}, want: time.Second,
		},
		{name: "a target nothing measured at all"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationPlanningDuration(test.target); got != test.want {
				t.Fatalf("mutationPlanningDuration = %s, want %s", got, test.want)
			}
		})
	}
	group := []TargetEvidence{{Duration: time.Second}, {ProbeDuration: timeoutSample}}
	if got := mutationGroupPlanningDuration(group); got != time.Second+timeoutSample {
		t.Fatalf("mutationGroupPlanningDuration = %s, want the sum of what each names", got)
	}
	if got := mutationGroupPlanningDuration(nil); got != 0 {
		t.Fatalf("a group of no target planned %s, want none", got)
	}
}

func timedTarget(pkg string, baseline, probe time.Duration) TargetEvidence {
	return TargetEvidence{
		Target:   goanalysis.Target{Package: pkg, Name: "TestOne", Kind: goanalysis.KindTest, ID: "t1"},
		Duration: baseline, ProbeDuration: probe,
	}
}

func TestAnAggregateTimeoutAddsEveryControlItHasAndStopsAtTheLimit(t *testing.T) {
	t.Parallel()
	const pkg = "example.test/fixture"
	for _, test := range []struct {
		name    string
		targets []TargetEvidence
		options MutationOptions
		want    time.Duration
	}{
		{
			name:    "one target and nothing else",
			targets: []TargetEvidence{timedTarget(pkg, time.Second, 0)},
			want:    time.Second,
		},
		{
			name:    "one target a probe also measured",
			targets: []TargetEvidence{timedTarget(pkg, time.Second, time.Second)},
			want:    2 * time.Second,
		},
		{
			name: "two targets in one batch",
			targets: []TargetEvidence{
				timedTarget(pkg, time.Second, 0), timedTarget(pkg, time.Second, 0),
			},
			want: 2 * time.Second,
		},
		{
			name:    "a package suite that was measured",
			targets: []TargetEvidence{timedTarget(pkg, time.Second, 0)},
			options: MutationOptions{
				SuiteCoverage: map[string]PackageSuiteCoverage{pkg: {Duration: timeoutSample}},
			},
			want: time.Second + timeoutSample,
		},
		{
			name:    "a package suite that took no time",
			targets: []TargetEvidence{timedTarget(pkg, time.Second, 0)},
			options: MutationOptions{SuiteCoverage: map[string]PackageSuiteCoverage{pkg: {}}},
			want:    time.Second,
		},
		{
			name:    "a package suite a probe measured",
			targets: []TargetEvidence{timedTarget(pkg, time.Second, 0)},
			options: MutationOptions{
				SuiteProbes: map[string]PackageProbeEvidence{pkg: {Measured: true, Duration: timeoutSample}},
			},
			want: time.Second + timeoutSample,
		},
		{
			name:    "a package suite a probe did not measure",
			targets: []TargetEvidence{timedTarget(pkg, time.Second, 0)},
			options: MutationOptions{
				SuiteProbes: map[string]PackageProbeEvidence{pkg: {Duration: timeoutSample}},
			},
			want: time.Second,
		},
		{
			name:    "a package suite of another package",
			targets: []TargetEvidence{timedTarget(pkg, time.Second, 0)},
			options: MutationOptions{
				SuiteCoverage: map[string]PackageSuiteCoverage{"example.test/other": {Duration: timeoutSample}},
			},
			want: time.Second,
		},
		{
			name:    "a limit the controls reach",
			targets: []TargetEvidence{timedTarget(pkg, timeoutSample, timeoutSample)},
			options: MutationOptions{Timeout: timeoutLimit},
			want:    timeoutLimit,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := aggregateMutationTimeout(test.targets, test.options); got != test.want {
				t.Fatalf("aggregateMutationTimeout = %s, want %s", got, test.want)
			}
		})
	}
}

func TestASaturatingSumStopsAtTheLongestDurationThereIs(t *testing.T) {
	t.Parallel()
	maximum := time.Duration(math.MaxInt64)
	for _, test := range []struct {
		name     string
		total    time.Duration
		duration time.Duration
		want     time.Duration
	}{
		{name: "two ordinary durations", total: time.Second, duration: time.Second, want: 2 * time.Second},
		{name: "a duration of no time", total: time.Second, want: time.Second},
		{name: "a duration below zero", total: time.Second, duration: -time.Second, want: time.Second},
		{name: "a sum that would overflow", total: maximum - 1, duration: timeoutSample, want: maximum},
		{
			name:  "a sum that reaches the longest there is exactly",
			total: maximum - timeoutSample, duration: timeoutSample, want: maximum,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := saturatingDurationSum(test.total, test.duration); got != test.want {
				t.Fatalf("saturatingDurationSum(%s, %s) = %s, want %s",
					test.total, test.duration, got, test.want)
			}
		})
	}
}

func witnessTarget(probed bool, infected []uint32, duration time.Duration) TargetEvidence {
	return TargetEvidence{
		Target: goanalysis.Target{ID: "t1", Name: "TestOne", Package: "example.test/fixture"},
		Probed: probed, Infected: infected, Duration: duration,
	}
}

func TestMutationWitnessesPreferWhatAProbeSawAndThenWhatIsQuickest(t *testing.T) {
	t.Parallel()
	probed := gomutants.Mutant{ID: "m-1", Probed: true}
	unprobed := gomutants.Mutant{ID: "m-1"}
	for _, test := range []struct {
		name   string
		mutant gomutants.Mutant
		first  TargetEvidence
		second TargetEvidence
		want   int
	}{
		{
			name: "a probed witness ahead of one nothing probed", mutant: probed,
			first: witnessTarget(true, nil, time.Second), second: witnessTarget(false, nil, 0), want: -1,
		},
		{
			name: "a witness nothing probed behind one it did", mutant: probed,
			first: witnessTarget(false, nil, 0), second: witnessTarget(true, nil, time.Second), want: 1,
		},
		{
			name: "two probed witnesses, the narrower first", mutant: probed,
			first:  witnessTarget(true, []uint32{1}, time.Second),
			second: witnessTarget(true, []uint32{1, 2}, time.Second), want: -1,
		},
		{
			name: "two probed witnesses of one width, the quicker first", mutant: probed,
			first:  witnessTarget(true, []uint32{1}, time.Second),
			second: witnessTarget(true, []uint32{1}, timeoutSample), want: -1,
		},
		{
			name: "a mutant nothing probed, the quicker first", mutant: unprobed,
			first:  witnessTarget(true, []uint32{1, 2}, time.Second),
			second: witnessTarget(false, nil, timeoutSample), want: -1,
		},
		{
			name: "two witnesses nothing tells apart", mutant: probed,
			first:  witnessTarget(true, []uint32{1}, time.Second),
			second: witnessTarget(true, []uint32{2}, time.Second), want: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := compareMutationWitnesses(test.mutant, test.first, test.second)
			if (got < 0) != (test.want < 0) || (got > 0) != (test.want > 0) {
				t.Fatalf("compareMutationWitnesses = %d, want the sign of %d", got, test.want)
			}
		})
	}
}
