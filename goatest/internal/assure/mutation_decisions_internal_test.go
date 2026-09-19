// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const containmentTestLimit = 5 * time.Second

func TestEvaluateMutationsReturnsCatalogValidationFailure(t *testing.T) {
	t.Parallel()
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{Accepted: true}}},
		exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
			t.Fatal("invalid catalog executed")
			return gomutants.MutantResult{}, nil
		},
	}
	evaluation, err := EvaluateMutations(t.Context(), session, nil, MutationOptions{})
	if err == nil || !strings.Contains(err.Error(), "empty mutant ID") || !reflect.DeepEqual(evaluation, MutationEvaluation{}) {
		t.Fatalf("invalid catalog = (%+v, %v)", evaluation, err)
	}
}

func TestMutationSchedulerNeverCheckpointsAProtocolError(t *testing.T) {
	t.Parallel()
	mutant := internalMutation("mutant")
	checkpoints := 0
	session := &mutationUnitSession{
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
		exec: func(request gomutants.ExecRequest) (gomutants.MutantResult, error) {
			return gomutants.MutantResult{ID: request.Mutant, Outcome: gomutants.OutcomeErrored}, nil
		},
	}
	_, err := evaluateMutationsForTest(t.Context(), session, []TargetEvidence{
		internalTarget("TestValue", goanalysis.KindTest, time.Millisecond),
	}, MutationOptions{Checkpoint: func(string, MutationEvaluation) { checkpoints++ }})
	if err == nil || checkpoints != 0 {
		t.Fatalf("protocol error = %v, checkpoints = %d", err, checkpoints)
	}
}

func TestMutationSeedMarksEveryTerminalShortcutResolved(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("shortcut")
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := evidenceIdentity("TestValue", goanalysis.KindTest)
	key := digestText("target-key")
	index := evidenceIndex(
		[]evidence.MutationRecord{killedEvidenceRecord(mutant, identity, key)},
		map[targetIdentity]string{identity: key}, map[targetIdentity]bool{identity: true},
	)
	seed := evaluateMutationSeed(t.Context(), refusingSession(t, gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}}),
		mutant, []TargetEvidence{target}, MutationOptions{Evidence: index})
	if !seed.resolved || seed.err != nil || len(seed.evaluation.Evidence) != 1 {
		t.Fatalf("reused seed = %+v", seed)
	}

	unreached := evaluateMutationSeed(t.Context(), &mutationUnitSession{}, mutant, nil, MutationOptions{
		SuiteCoverage: map[string]PackageSuiteCoverage{mutant.Package: {
			Instrumented: blockRoutingInstrumentation(), Duration: time.Second,
		}},
	})
	if !unreached.resolved || unreached.err != nil || len(unreached.evaluation.Findings) != 1 {
		t.Fatalf("unreached suite seed = %+v", unreached)
	}

	mutant.Probed = true
	uninfected := evaluateMutationSeed(t.Context(), &mutationUnitSession{}, mutant, nil, MutationOptions{
		SuiteProbes: map[string]PackageProbeEvidence{mutant.Package: {Measured: true, Duration: time.Second}},
	})
	if !uninfected.resolved || uninfected.err != nil || len(uninfected.evaluation.Findings) != 1 {
		t.Fatalf("uninfected suite seed = %+v", uninfected)
	}
}

func TestMutationSeedStopsAtControlErrorsOnBothRoutes(t *testing.T) {
	t.Parallel()
	cause := errors.New("control failed")
	mutant := internalMutation("mutant")
	executions := 0
	session := &mutationUnitSession{exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		executions++
		return gomutants.MutantResult{Outcome: gomutants.OutcomeKilled}, nil
	}}
	options := MutationOptions{
		Timeout: time.Second,
		OriginalControl: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
			return gomutants.ControlResult{}, cause
		},
	}
	packageSeed := evaluateMutationSeed(t.Context(), session, mutant, nil, options)
	if packageSeed.err == nil || !errors.Is(packageSeed.err, cause) || executions != 0 {
		t.Fatalf("package control = (%+v, executions %d)", packageSeed, executions)
	}
	targetSeed := evaluateMutationSeed(t.Context(), session, mutant, []TargetEvidence{
		internalTarget("TestValue", goanalysis.KindTest, time.Millisecond),
	}, options)
	if targetSeed.err == nil || !errors.Is(targetSeed.err, cause) || executions != 0 {
		t.Fatalf("target control = (%+v, executions %d)", targetSeed, executions)
	}
}

func TestPrepareMutationRequestPreservesControlErrorsAndOutput(t *testing.T) {
	t.Parallel()
	mutant := internalMutation("mutant")
	request := gomutants.ExecRequest{Timeout: time.Second}
	cause := errors.New("control failed")
	gotRequest, finding, err := prepareMutationRequest(t.Context(), mutant, request, MutationOptions{
		Timeout: time.Second,
		OriginalControl: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
			return gomutants.ControlResult{}, cause
		},
	}, "failed", "control failed", "timed-out", "control timed out")
	if !errors.Is(err, cause) || !reflect.DeepEqual(gotRequest, gomutants.ExecRequest{}) || finding != (controlFinding{}) ||
		!strings.Contains(err.Error(), mutant.DisplayID) {
		t.Fatalf("control error = (%+v, %+v, %v)", gotRequest, finding, err)
	}

	for _, test := range []struct {
		name    string
		output  []byte
		summary string
	}{
		{name: "empty", summary: "control failed"},
		{name: "output", output: []byte(" useful detail\n"), summary: "control failed: useful detail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, got, err := prepareMutationRequest(t.Context(), mutant, request, MutationOptions{
				Timeout: time.Second,
				OriginalControl: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
					return gomutants.ControlResult{ExitCode: 1, Output: test.output}, nil
				},
			}, "failed", "control failed", "timed-out", "control timed out")
			if err != nil || got.kind != "failed" || got.summary != test.summary {
				t.Fatalf("control finding = (%+v, %v), want %q", got, err, test.summary)
			}
		})
	}
}

func TestMemoizedOriginalControlReturnsTheRememberedError(t *testing.T) {
	t.Parallel()
	cause := errors.New("control failed")
	calls := 0
	control := memoizedOriginalControl(func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
		calls++
		return gomutants.ControlResult{ExitCode: 7}, cause
	})
	request := gomutants.ExecRequest{Package: "pkg", Timeout: time.Second}
	for range 2 {
		result, err := control(t.Context(), request)
		if result.ExitCode != 7 || !errors.Is(err, cause) {
			t.Fatalf("memoized control = (%+v, %v)", result, err)
		}
	}
	if calls != 1 {
		t.Fatalf("control calls = %d, want 1", calls)
	}
}

func TestExecuteMutationUnderMeasuredBudgetReturnsExecutionErrorsDirectly(t *testing.T) {
	t.Parallel()
	cause := errors.New("execution failed")
	freshCalls := 0
	session := &mutationUnitSession{exec: func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{Outcome: gomutants.OutcomeTimedOut}, cause
	}}
	result, _, err := executeMutationUnderMeasuredBudget(t.Context(), session, gomutants.ExecRequest{Mutant: "mutant"}, MutationOptions{
		freshControl: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
			freshCalls++
			return gomutants.ControlResult{Duration: time.Second}, nil
		},
	})
	if result.Outcome != gomutants.OutcomeTimedOut || !errors.Is(err, cause) || freshCalls != 0 {
		t.Fatalf("execution error = (%+v, %v), fresh calls %d", result, err, freshCalls)
	}
}

func TestContainmentAfterHealthyControlRejectsEveryUnhealthyBoundary(t *testing.T) {
	t.Parallel()
	limit := containmentTestLimit
	healthy := gomutants.ControlResult{Duration: time.Second}
	cause := errors.New("control failed")
	for _, test := range []struct {
		name    string
		request time.Duration
		control OriginalControl
		want    bool
	}{
		{name: "no control", request: time.Second},
		{name: "no request budget", control: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) { return healthy, nil }},
		{name: "request at ceiling", request: limit, control: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) { return healthy, nil }},
		{name: "control error", request: time.Second, control: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
			return gomutants.ControlResult{}, cause
		}},
		{name: "control timeout", request: time.Second, control: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
			return gomutants.ControlResult{TimedOut: true, Duration: time.Second}, nil
		}},
		{name: "control failure", request: time.Second, control: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
			return gomutants.ControlResult{ExitCode: 1, Duration: time.Second}, nil
		}},
		{name: "control without duration", request: time.Second, control: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) {
			return gomutants.ControlResult{}, nil
		}},
		{name: "healthy", request: time.Second, control: func(context.Context, gomutants.ExecRequest) (gomutants.ControlResult, error) { return healthy, nil }, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ceiling, got := containmentAfterHealthyControl(t.Context(), gomutants.ExecRequest{Timeout: test.request}, MutationOptions{
				Timeout: limit, freshControl: test.control,
			})
			if got != test.want || got && ceiling != limit || !got && ceiling != 0 {
				t.Fatalf("containment = (%s, %t), want %t", ceiling, got, test.want)
			}
		})
	}
}

func TestMutationTargetGroupsBreakDurationTiesByEnvironment(t *testing.T) {
	t.Parallel()
	second := internalTarget("TestSecond", goanalysis.KindTest, time.Second)
	second.Environment = []string{"RESOURCE=second"}
	first := internalTarget("TestFirst", goanalysis.KindTest, time.Second)
	first.Environment = []string{"RESOURCE=first"}
	groups := mutationTargetGroups([]TargetEvidence{second, first})
	if len(groups) != 2 || groups[0][0].Target.Name != "TestFirst" || groups[1][0].Target.Name != "TestSecond" {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestAggregateMutationTimeoutUsesOnlyMeasuredPositiveSuiteFacts(t *testing.T) {
	t.Parallel()
	target := internalTarget("TestValue", goanalysis.KindTest, time.Second)
	target.ProbeDuration = timeoutSample
	options := MutationOptions{
		Timeout:       30 * time.Second,
		SuiteCoverage: map[string]PackageSuiteCoverage{target.Target.Package: {Duration: 4 * time.Second}},
		SuiteProbes:   map[string]PackageProbeEvidence{target.Target.Package: {Measured: true, Duration: 5 * time.Second}},
	}
	if got := aggregateMutationTimeout([]TargetEvidence{target}, options); got != 12*time.Second {
		t.Fatalf("aggregate timeout = %s, want 12s", got)
	}
	options.SuiteCoverage[target.Target.Package] = PackageSuiteCoverage{Duration: -time.Second}
	options.SuiteProbes[target.Target.Package] = PackageProbeEvidence{Duration: 20 * time.Second}
	if got := aggregateMutationTimeout([]TargetEvidence{target}, options); got != 3*time.Second {
		t.Fatalf("unmeasured timeout = %s, want 3s", got)
	}
}

func TestMutationDischargeSummariesDistinguishBranchInfectionAndMixedProofs(t *testing.T) {
	t.Parallel()
	branch := trace.Discharge{Target: "branch", Reason: trace.DischargeBranchNeverTaken}
	infection := trace.Discharge{Target: "infection", Reason: trace.DischargeNeverInfected}
	if got := (mutationDischargeCounts{branch: 1}).clause(); got != mutationBranchDischargeClause {
		t.Fatalf("branch clause = %q", got)
	}
	if got := mutationDischargedSummary([]trace.Discharge{branch}); got != mutationFullyDischargedSummary {
		t.Fatalf("branch summary = %q", got)
	}
	if got := mutationDischargedSummary([]trace.Discharge{infection}); !strings.Contains(got, mutationInfectionDischargeClause) {
		t.Fatalf("infection summary = %q", got)
	}
	if got := mutationDischargedSummary([]trace.Discharge{branch, infection}); !strings.Contains(got, "1 take no branch") || !strings.Contains(got, "1 never make") {
		t.Fatalf("mixed summary = %q", got)
	}
}

func TestMutationExecutionTimeoutClampsOnlyPositiveLimits(t *testing.T) {
	t.Parallel()
	if got := mutationExecutionTimeout(3*time.Second, time.Second, 2*time.Second, time.Second); got != 3*time.Second {
		t.Fatalf("limited timeout = %s", got)
	}
	if got := mutationExecutionTimeout(0, time.Second, 2*time.Second); got != 3*time.Second {
		t.Fatalf("unlimited timeout = %s", got)
	}
}

func TestSuiteCoverageRoutingLeavesAnsweredRoutesUntouched(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("answered-suite-route")
	suites := map[string]PackageSuiteCoverage{mutant.Package: {
		Instrumented: blockRoutingInstrumentation(), Covered: blockRoutingInstrumentation(),
		Duration: 9 * time.Second, WholeTree: true,
	}}
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Second)
	for _, route := range []mutationRoute{
		{reaching: []TargetEvidence{target}, suiteDuration: time.Second},
		{discharged: []trace.Discharge{{Target: target.Target.ID, Reason: trace.DischargeNeverInfected}}, suiteDuration: time.Second},
	} {
		got := applySuiteCoverageRouting(mutant, route, suites)
		if !reflect.DeepEqual(got, route) {
			t.Fatalf("answered route changed from %+v to %+v", route, got)
		}
	}
}

func TestSuiteCoverageRoutingRejectsEachZeroCoordinate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutant gomutants.Mutant
		block  goanalysis.CoverageBlock
	}{
		{
			name: "line", mutant: gomutants.Mutant{Path: "value.go", Package: evidenceModule, Line: 0, Column: 1},
			block: goanalysis.CoverageBlock{StartLine: 0, StartColumn: 1, EndLine: 0, EndColumn: 2},
		},
		{
			name: "column", mutant: gomutants.Mutant{Path: "value.go", Package: evidenceModule, Line: 1, Column: 0},
			block: goanalysis.CoverageBlock{StartLine: 1, StartColumn: 0, EndLine: 1, EndColumn: 1},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			route := applySuiteCoverageRouting(test.mutant, mutationRoute{}, map[string]PackageSuiteCoverage{
				test.mutant.Package: {
					Instrumented: []goanalysis.FileCoverage{{Path: test.mutant.Path, Blocks: []goanalysis.CoverageBlock{test.block}}},
					Covered:      []goanalysis.FileCoverage{{Path: test.mutant.Path, Blocks: []goanalysis.CoverageBlock{test.block}}},
					Duration:     time.Second,
				},
			})
			if route.suiteCoverage != "" || route.suiteReached || route.suiteDuration != time.Second {
				t.Fatalf("zero-coordinate route = %+v", route)
			}
		})
	}
}

func TestNeededProbeSuitesSkipEveryAlreadyAnsweredRoute(t *testing.T) {
	t.Parallel()
	mutant := probedMutant()
	for _, target := range []TargetEvidence{
		infectionTarget("TestInfects", time.Second, true, mutant.Index),
		infectionTarget("TestDischarged", time.Second, true, mutant.Index+1),
	} {
		got := neededProbeSuitePackages(gomutants.Catalog{Mutants: []gomutants.Mutant{mutant}},
			[]TargetEvidence{target}, blockRoutingInstrumentation(), nil)
		if len(got) != 0 {
			t.Fatalf("answered target %s requested suites %v", target.Target.ID, got)
		}
	}
}

func TestProbeRoutingPreservesAnEarlierSuiteDuration(t *testing.T) {
	t.Parallel()
	mutant := routedMutant(nil)
	route := applyProbeRouting(mutant, nil, mutationRoute{suiteDuration: time.Second}, map[string]PackageProbeEvidence{
		mutant.Package: {Measured: true, Duration: 2 * time.Second},
	})
	if route.suiteDuration != time.Second || route.suiteProbe == "" {
		t.Fatalf("suite route = %+v", route)
	}
}

func TestProbeRoutingDoesNotRecoverKnownOrDuplicateTargets(t *testing.T) {
	t.Parallel()
	mutant := routedMutant(nil)
	target := routedTarget("same", true, mutant.Index)
	for _, route := range []mutationRoute{
		{reaching: []TargetEvidence{target}},
		{discharged: []trace.Discharge{{Target: target.Target.ID, Reason: trace.DischargeNeverInfected}}},
	} {
		got := applyProbeRouting(mutant, []TargetEvidence{target}, route, nil)
		if !reflect.DeepEqual(got, route) {
			t.Fatalf("known route changed from %+v to %+v", route, got)
		}
	}

	got := applyProbeRouting(mutant, []TargetEvidence{target, target}, mutationRoute{}, nil)
	if len(got.reaching) != 1 || len(got.probeReaching) != 1 {
		t.Fatalf("duplicate target route = %+v", got)
	}
}

func TestProbeRoutingReordersOnlyAfterRecoveringATarget(t *testing.T) {
	t.Parallel()
	mutant := routedMutant(nil)
	long := routedTarget("long", true, mutant.Index)
	long.Duration = timeoutSample
	short := routedTarget("short", true, mutant.Index)
	short.Duration = time.Second

	recovered := applyProbeRouting(mutant, []TargetEvidence{short}, mutationRoute{reaching: []TargetEvidence{long}}, nil)
	if got := []string{recovered.reaching[0].Target.ID, recovered.reaching[1].Target.ID}; !reflect.DeepEqual(got, []string{short.Target.ID, long.Target.ID}) {
		t.Fatalf("recovered order = %v", got)
	}

	unchanged := applyProbeRouting(mutant, nil, mutationRoute{reaching: []TargetEvidence{long, short}}, nil)
	if got := []string{unchanged.reaching[0].Target.ID, unchanged.reaching[1].Target.ID}; !reflect.DeepEqual(got, []string{long.Target.ID, short.Target.ID}) {
		t.Fatalf("route without recovery reordered to %v", got)
	}
}

func TestProbeRoutingStopsOnlyForPositiveSuiteEvidence(t *testing.T) {
	t.Parallel()
	mutant := routedMutant(nil)
	target := routedTarget("positive", true, mutant.Index)
	for _, route := range []mutationRoute{
		{suiteCoverage: "coverage", suiteReached: true},
		{suiteInfected: true},
	} {
		got := applyProbeRouting(mutant, []TargetEvidence{target}, route, nil)
		if len(got.reaching) != 0 || len(got.probeReaching) != 0 {
			t.Fatalf("positive suite route continued to targets: %+v", got)
		}
	}

	for _, route := range []mutationRoute{
		{suiteCoverage: "coverage", suiteReached: true, suiteProbe: "probe"},
		{suiteReached: true},
		{suiteCoverage: "coverage"},
	} {
		got := applyProbeRouting(mutant, []TargetEvidence{target}, route, nil)
		if len(got.reaching) != 1 || len(got.probeReaching) != 1 {
			t.Fatalf("non-positive suite route did not recover target: %+v", got)
		}
	}
}

func TestDischargeHelpersPreserveNoProofAsNil(t *testing.T) {
	t.Parallel()
	target := infectionTarget("TestKept", time.Second, true, probedMutantIndex)
	reaching := []TargetEvidence{target}

	unprobed := probedMutant()
	unprobed.Probed = false
	kept, discharged := dischargeNeverInfected(unprobed, reaching)
	if !reflect.DeepEqual(kept, reaching) || discharged != nil {
		t.Fatalf("unprobed discharge = (%+v, %+v)", kept, discharged)
	}

	kept, discharged = dischargeNeverInfected(probedMutant(), reaching)
	if !reflect.DeepEqual(kept, reaching) || discharged != nil {
		t.Fatalf("fully infected discharge = (%+v, %+v)", kept, discharged)
	}

	for _, mutant := range []gomutants.Mutant{
		{},
		branchMutant(nil),
	} {
		kept, discharged = dischargeNarrowedBranch(mutant, reaching, goanalysis.FileCoverage{}, "value.go")
		if !reflect.DeepEqual(kept, reaching) || discharged != nil {
			t.Fatalf("branch without applicable proof = (%+v, %+v)", kept, discharged)
		}
	}
}

func TestNarrowedBranchSpanAcceptsOneBasedBoundaryCoordinates(t *testing.T) {
	t.Parallel()
	mutant := gomutants.Mutant{Line: 0, Column: 1, Branch: &gomutants.BranchProof{
		BodyStartLine: 1, BodyStartColumn: 1, BodyEndLine: 1, BodyEndColumn: 1,
	}}
	span, ok := narrowedBranchSpan(mutant)
	if !ok || span != (goanalysis.CoverageSpan{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 1}) {
		t.Fatalf("one-based span = (%+v, %t)", span, ok)
	}
}
