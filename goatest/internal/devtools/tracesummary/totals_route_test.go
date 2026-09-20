// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const (
	routesUnderASuiteControl  = 3
	routesThatReachedTheSuite = 2
	probedTargets             = 3
	barrenProbedTargets       = 2
	probedSuites              = 3

	dispositionsReached = 2
	mutantsKilled       = 2
)

func routedMutant(change func(*trace.RouteRecord)) trace.Event {
	record := trace.RouteRecord{
		MutantID: "m-1", Rule: "eq-to-neq", Path: "a.go", Line: 1, Column: 1,
		Granularity: trace.GranularityBlock, Reason: trace.ReasonCoverageReaching,
	}
	if change != nil {
		change(&record)
	}
	return trace.Event{Type: trace.TypeRoute, Route: &record}
}

func TestRouteTotalsCountAFallbackOnlyWhereOneWasRecorded(t *testing.T) {
	t.Parallel()
	total := routeTotals([]trace.Event{
		routedMutant(nil),
		routedMutant(func(r *trace.RouteRecord) {
			r.MutantID, r.Fallback = "m-2", trace.FallbackOutsideBlocks
		}),
	})
	rendered := formatLabelCounts(total.fallbacks)
	if !strings.Contains(rendered, trace.FallbackOutsideBlocks+" 1") {
		t.Fatalf("the fallback tally reads %q, want the one route that fell back", rendered)
	}
	if strings.Contains(rendered, " 2") {
		t.Fatalf("the fallback tally reads %q, want the route that did not fall back left out", rendered)
	}
}

func TestRouteTotalsTellASuiteItReachedFromOneItDidNot(t *testing.T) {
	t.Parallel()
	controlled := func(id string, reached bool) trace.Event {
		return routedMutant(func(r *trace.RouteRecord) {
			r.MutantID = id
			r.SuiteCoverage, r.SuiteReached = "package-suite-coverage:example.com/app", reached
		})
	}
	total := routeTotals([]trace.Event{
		controlled("m-1", true),
		controlled("m-2", true),
		controlled("m-3", false),
		routedMutant(func(r *trace.RouteRecord) { r.MutantID = "m-4" }),
	})
	if total.suiteCoverage != routesUnderASuiteControl {
		t.Fatalf("the totals counted %d suite controls, want the %d routes that named one",
			total.suiteCoverage, routesUnderASuiteControl)
	}
	if total.suiteReached != routesThatReachedTheSuite || total.suiteUnreached != 1 {
		t.Fatalf("the totals counted %d reached and %d unreached, want %d and one",
			total.suiteReached, total.suiteUnreached, routesThatReachedTheSuite)
	}
}

func TestProbeTotalsCountATargetNothingInfectedAsBarren(t *testing.T) {
	t.Parallel()
	probe := func(target string, suite bool, infected ...string) trace.Event {
		return trace.Event{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
			Target: target, Package: "example.com/app", Suite: suite,
			Outcome: trace.ProbeOutcomeMeasured, Infected: infected,
		}}
	}
	suite := func(pkg string, infected ...string) trace.Event {
		return probe(trace.PackageSuiteProbePrefix+pkg, true, infected...)
	}
	total := probeTotals([]trace.Event{
		probe("TestOne", false, "m-1"),
		probe("TestTwo", false),
		probe("TestThree", false),
		suite("example.com/app", "m-1"),
		suite("example.com/inner", "m-2"),
		suite("example.com/other"),
	})
	if total.targets != probedTargets || total.barrenTargets != barrenProbedTargets {
		t.Fatalf("the totals counted %d targets and %d barren, want %d and %d",
			total.targets, total.barrenTargets, probedTargets, barrenProbedTargets)
	}
	if total.suites != probedSuites || total.barrenSuites != 1 {
		t.Fatalf("the totals counted %d suites and %d barren, want %d and one",
			total.suites, total.barrenSuites, probedSuites)
	}
}

func TestTheProbeBlockNamesPackageSuitesOnlyWhereThereWereSome(t *testing.T) {
	t.Parallel()
	withSuites := probeBlock([]trace.Event{
		{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
			Target: "TestOne", Package: "example.com/app",
			Outcome: trace.ProbeOutcomeMeasured, WholeTree: true, WholeTreeReason: trace.WholeTreeDirectoryAccess,
		}},
		{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
			Target: trace.PackageSuiteProbePrefix + "example.com/app", Package: "example.com/app", Suite: true,
			Outcome: trace.ProbeOutcomeMeasured,
		}},
	})
	if !strings.Contains(strings.Join(withSuites, "\n"), "package suites") {
		t.Fatalf("a recording that probed a package suite rendered %q", withSuites)
	}

	withoutSuites := probeBlock([]trace.Event{
		{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
			Target: "TestOne", Package: "example.com/app",
			Outcome: trace.ProbeOutcomeMeasured, WholeTree: true, WholeTreeReason: trace.WholeTreeDirectoryAccess,
		}},
	})
	rendered := strings.Join(withoutSuites, "\n")
	if !strings.Contains(rendered, "whole-tree keys:") {
		t.Fatalf("a recording that widened a target rendered %q", withoutSuites)
	}
	if strings.Contains(rendered, "package suites") {
		t.Fatalf("a recording with no package suite still named them:\n%s", rendered)
	}
}

func TestDispositionTotalsHoldOneRowPerOutcomeHoweverManyMutantsReachedIt(t *testing.T) {
	t.Parallel()
	mutant := func(id, outcome string) trace.Event {
		return trace.Event{Type: trace.TypeMutantExec, Mutant: &trace.MutantRecord{
			ID: id, DisplayID: id, Package: "example.com/app", Outcome: outcome, DurationMS: 1,
		}}
	}
	totals := dispositionTotals([]trace.Event{
		mutant("m-1", "killed"), mutant("m-2", "killed"), mutant("m-3", "survived"),
	})
	if len(totals) != dispositionsReached {
		t.Fatalf("three mutants of two outcomes totalled %d rows, want two: %+v", len(totals), totals)
	}
	for _, total := range totals {
		switch total.disposition {
		case "killed":
			if total.mutants != mutantsKilled {
				t.Errorf("the killed row counts %d mutants, want two", total.mutants)
			}
		case "survived":
			if total.mutants != 1 {
				t.Errorf("the survived row counts %d mutants, want one", total.mutants)
			}
		}
	}
}
