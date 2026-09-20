// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const (
	savingsLine       = 20
	savingsColumn     = 4
	savingsBodyLine   = 20
	savingsBodyColumn = 15
	savingsBodyEnd    = 22
	savingsBodyEndCol = 3
	savingsRule       = "eq-to-neq"
)

func savingsAuditor(t *testing.T, layers []layer, route trace.RouteRecord) *auditor {
	t.Helper()
	recorded := recordedEvidence(t, map[string][]string{
		killerTarget: {linked(savingsBodyLine, savingsBodyColumn, savingsBodyEnd, savingsBodyEndCol)},
	})
	catalog := fixtureCatalog(cataloguedMutant(firstMutant, savingsLine, savingsColumn,
		gatedBody(savingsBodyLine, savingsBodyColumn, savingsBodyEnd, savingsBodyEndCol)))
	audit := newAuditor(recorded, catalog, layers)
	audit.routes[route.MutantID] = route
	return audit
}

func savingsRoute(change func(*trace.RouteRecord)) trace.RouteRecord {
	route := trace.RouteRecord{
		MutantID: firstMutant, Rule: savingsRule, Path: subjectPath,
		Line: savingsLine, Column: savingsColumn, Granularity: trace.GranularityBlock,
		Probed: true, ReachingTargets: []string{killerTarget},
	}
	if change != nil {
		change(&route)
	}
	return route
}

func TestTheBranchLayerMeasuresOnlyTheRoutesItCouldHaveDecided(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*trace.RouteRecord)
		want   dischargeSavings
	}{
		{
			name: "a block route of a mutant the catalog proves",
			want: dischargeSavings{routes: 1, reaching: 1, discharged: 1, emptied: 1},
		},
		{
			name:   "a route routed by file",
			change: func(route *trace.RouteRecord) { route.Granularity = trace.GranularityFile },
		},
		{
			name: "a route that fell back",
			change: func(route *trace.RouteRecord) {
				route.Fallback = trace.FallbackOutsideBlocks
			},
		},
		{
			name:   "a route of a mutant the catalog does not list",
			change: func(route *trace.RouteRecord) { route.MutantID = secondMutant },
		},
		{
			name:   "a route reaching nothing at all",
			change: func(route *trace.RouteRecord) { route.ReachingTargets = nil },
			want:   dischargeSavings{routes: 1},
		},
		{
			name:   "a route reaching a target no profile measured",
			change: func(route *trace.RouteRecord) { route.ReachingTargets = []string{secondTarget} },
			want:   dischargeSavings{routes: 1, reaching: 1},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			audit := savingsAuditor(t, []layer{reachLayer(), branchLayer(nil)}, savingsRoute(test.change))
			if got := audit.measureBranchSavings(); got != test.want {
				t.Fatalf("the branch layer measured %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestTheInfectionLayerMeasuresOnlyTheRoutesAProbePassCovered(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*trace.RouteRecord)
		want   dischargeSavings
	}{
		{
			name: "a probed block route",
			want: dischargeSavings{routes: 1, reaching: 1},
		},
		{
			name:   "a route nothing probed",
			change: func(route *trace.RouteRecord) { route.Probed = false },
		},
		{
			name:   "a route routed by file",
			change: func(route *trace.RouteRecord) { route.Granularity = trace.GranularityFile },
		},
		{
			name: "a route that fell back",
			change: func(route *trace.RouteRecord) {
				route.Fallback = trace.FallbackPositionUnknown
			},
		},
		{
			name:   "a route reaching nothing at all",
			change: func(route *trace.RouteRecord) { route.ReachingTargets = nil },
			want:   dischargeSavings{routes: 1},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			audit := savingsAuditor(t, []layer{reachLayer(), infectionLayer()}, savingsRoute(test.change))
			if got := audit.measureInfectionSavings(); got != test.want {
				t.Fatalf("the infection layer measured %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestTheInfectionLayerMeasuresTheTargetsAProbeSawNothingIn(t *testing.T) {
	t.Parallel()
	audit := savingsAuditor(t, []layer{reachLayer(), infectionLayer()}, savingsRoute(nil))
	audit.probes[killerTarget] = &probeFacts{
		outcome: trace.ProbeOutcomeMeasured, infected: map[string]struct{}{},
	}
	want := dischargeSavings{routes: 1, reaching: 1, discharged: 1, emptied: 1}
	if got := audit.measureInfectionSavings(); got != want {
		t.Fatalf("the infection layer measured %+v, want %+v", got, want)
	}
}
