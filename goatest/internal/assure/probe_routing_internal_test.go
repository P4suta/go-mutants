// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const (
	routedMutantIndex = 7
	otherMutantIndex  = 9
	routedSuiteMS     = 250 * time.Millisecond
)

func routedMutant(change func(*gomutants.Mutant)) gomutants.Mutant {
	mutant := gomutants.Mutant{
		ID: "m-1", Package: "example.test/fixture", Index: routedMutantIndex, Probed: true,
	}
	if change != nil {
		change(&mutant)
	}
	return mutant
}

func routedTarget(id string, probed bool, infected ...uint32) TargetEvidence {
	return TargetEvidence{
		Target:   goanalysis.Target{ID: id, Name: "Test" + id, Package: "example.test/fixture"},
		Probed:   probed,
		Infected: infected,
	}
}

func TestProbeRoutingAddsATargetOnlyWhenTheProbeSawTheMutantInfectIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		mutant  gomutants.Mutant
		targets []TargetEvidence
		route   mutationRoute
		want    []string
	}{
		{
			name:    "a target a probe saw it infect",
			mutant:  routedMutant(nil),
			targets: []TargetEvidence{routedTarget("t1", true, routedMutantIndex)},
			want:    []string{"t1"},
		},
		{
			name:    "a target a probe saw another mutant infect",
			mutant:  routedMutant(nil),
			targets: []TargetEvidence{routedTarget("t1", true, otherMutantIndex)},
		},
		{
			name:    "a target nothing probed",
			mutant:  routedMutant(nil),
			targets: []TargetEvidence{routedTarget("t1", false, routedMutantIndex)},
		},
		{
			name:    "a mutant nothing probed",
			mutant:  routedMutant(func(m *gomutants.Mutant) { m.Probed = false }),
			targets: []TargetEvidence{routedTarget("t1", true, routedMutantIndex)},
		},
		{
			name:    "a target coverage already reaches",
			mutant:  routedMutant(nil),
			targets: []TargetEvidence{routedTarget("t1", true, routedMutantIndex)},
			route:   mutationRoute{reaching: []TargetEvidence{routedTarget("t1", true, routedMutantIndex)}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			routed := applyProbeRouting(test.mutant, test.targets, test.route, nil)
			got := make([]string, 0, len(routed.probeReaching))
			got = append(got, routed.probeReaching...)
			if len(got) != len(test.want) {
				t.Fatalf("probe routing recovered %q, want %q", got, test.want)
			}
			for index, want := range test.want {
				if got[index] != want {
					t.Fatalf("probe routing recovered %q, want %q", got, test.want)
				}
			}
		})
	}
}

func TestProbeRoutingReadsThePackageSuiteOnlyWhenCoverageFoundNothing(t *testing.T) {
	t.Parallel()
	suites := map[string]PackageProbeEvidence{
		"example.test/fixture": {Measured: true, Infected: []uint32{routedMutantIndex}, Duration: routedSuiteMS},
	}
	for _, test := range []struct {
		name      string
		mutant    gomutants.Mutant
		route     mutationRoute
		suites    map[string]PackageProbeEvidence
		wantProbe string
		infected  bool
	}{
		{
			name:   "coverage found nothing and the suite saw it infect",
			mutant: routedMutant(nil), suites: suites,
			wantProbe: "package-suite:example.test/fixture", infected: true,
		},
		{
			name:   "coverage that already reaches a target",
			mutant: routedMutant(nil), suites: suites,
			route: mutationRoute{reaching: []TargetEvidence{routedTarget("t1", false)}},
		},
		{
			name:   "coverage that already discharged a target",
			mutant: routedMutant(nil), suites: suites,
			route: mutationRoute{discharged: []trace.Discharge{{Target: "t1", Reason: "never-infected"}}},
		},
		{
			name:   "a mutant nothing probed",
			mutant: routedMutant(func(m *gomutants.Mutant) { m.Probed = false }), suites: suites,
		},
		{
			name:   "a package suite nothing measured",
			mutant: routedMutant(nil),
			suites: map[string]PackageProbeEvidence{"example.test/fixture": {Infected: []uint32{routedMutantIndex}}},
		},
		{
			name:   "a package suite of another package",
			mutant: routedMutant(nil),
			suites: map[string]PackageProbeEvidence{"example.test/other": {Measured: true}},
		},
		{
			name:   "a package suite that saw another mutant infect",
			mutant: routedMutant(nil),
			suites: map[string]PackageProbeEvidence{
				"example.test/fixture": {Measured: true, Infected: []uint32{otherMutantIndex}},
			},
			wantProbe: "package-suite:example.test/fixture",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			routed := applyProbeRouting(test.mutant, nil, test.route, test.suites)
			if routed.suiteProbe != test.wantProbe {
				t.Fatalf("probe routing named the suite %q, want %q", routed.suiteProbe, test.wantProbe)
			}
			if routed.suiteInfected != test.infected {
				t.Fatalf("probe routing says the suite saw it infect: %t, want %t",
					routed.suiteInfected, test.infected)
			}
			if test.wantProbe != "" && routed.suiteDuration != routedSuiteMS && test.suites != nil {
				if _, measured := test.suites["example.test/fixture"]; measured &&
					test.suites["example.test/fixture"].Duration != 0 {
					t.Errorf("probe routing recorded %s, want the suite duration", routed.suiteDuration)
				}
			}
		})
	}
}

func TestProbeRoutingDropsASuiteCoverageControlAPositiveProbeContradicts(t *testing.T) {
	t.Parallel()
	route := mutationRoute{
		suiteCoverage: "package-suite-coverage:example.test/fixture",
		suiteReached:  false, suiteInfected: true,
	}
	routed := applyProbeRouting(routedMutant(nil), nil, route, nil)
	if routed.suiteCoverage != "" {
		t.Fatalf("probe routing kept the coverage control %q that the probe contradicts", routed.suiteCoverage)
	}

	agreeing := mutationRoute{
		suiteCoverage: "package-suite-coverage:example.test/fixture",
		suiteReached:  true, suiteInfected: true,
	}
	if kept := applyProbeRouting(routedMutant(nil), nil, agreeing, nil); kept.suiteCoverage == "" {
		t.Fatal("probe routing dropped a coverage control the probe agrees with")
	}
}

func TestPackageProbeInfectsNamesOnlyTheIndexItRecorded(t *testing.T) {
	t.Parallel()
	evidence := PackageProbeEvidence{Infected: []uint32{1, routedMutantIndex}}
	if !packageProbeInfects(evidence, routedMutantIndex) {
		t.Error("an index the probe recorded was said not to infect")
	}
	if packageProbeInfects(evidence, otherMutantIndex) {
		t.Error("an index the probe never recorded was said to infect")
	}
	if packageProbeInfects(PackageProbeEvidence{}, routedMutantIndex) {
		t.Error("a probe that recorded nothing was said to infect")
	}
}
