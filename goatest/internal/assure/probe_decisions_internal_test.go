// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
)

func TestProbeTargetsCountsRestoredFactsWithoutInventingCollections(t *testing.T) {
	targets := []TargetEvidence{
		probeEvidence("TestMeasured", goanalysis.KindTest, time.Second),
		probeEvidence("TestUnmeasured", goanalysis.KindTest, time.Second),
	}
	targets[0].Probed = true
	targets[0].ProbeDuration = time.Second
	suites := map[string]PackageProbeEvidence{
		"fixture.example/measured":   {Measured: true, Duration: time.Second, Infected: []uint32{0}},
		"fixture.example/unmeasured": {},
	}
	session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		t.Fatal("a restored fact was remeasured")
		return gomutants.ProbeResult{}, nil
	}}
	evaluation, err := ProbeTargets(t.Context(), session, targets[:1], ProbeOptions{
		Suites: suites, SuitePackages: []string{"fixture.example/measured"},
	})
	if err != nil || evaluation.Measured != 1 || evaluation.Unmeasured != 0 ||
		evaluation.SuitesMeasured != 1 || evaluation.SuitesUnmeasured != 1 || !reflect.DeepEqual(evaluation.Suites, suites) {
		t.Fatalf("restored evaluation = (%+v, %v)", evaluation, err)
	}
	suites["fixture.example/measured"] = PackageProbeEvidence{}
	if !evaluation.Suites["fixture.example/measured"].Measured {
		t.Fatal("evaluation aliases restored suites")
	}
	evaluation, err = ProbeTargets(t.Context(), session, nil, ProbeOptions{})
	if err != nil || evaluation.Suites != nil {
		t.Fatalf("empty evaluation = (%+v, %v), want nil suites", evaluation, err)
	}
}

func TestProbeTargetsSelectsFallbackAndPendingSuitesExactly(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "accepted", Accepted: true, Package: "fixture.example/module"},
		{ID: "rejected", Package: "fixture.example/rejected"},
		{ID: "empty", Accepted: true},
	}}
	for _, test := range []struct {
		name           string
		options        ProbeOptions
		wantPackages   []string
		wantMeasured   int
		wantUnmeasured int
	}{
		{name: "disabled fallback"},
		{name: "enabled fallback", options: ProbeOptions{PackageSuites: true}, wantPackages: []string{"fixture.example/module"}, wantMeasured: 1},
		{
			name: "measured explicit suite is retained",
			options: ProbeOptions{
				SuitePackages: []string{"fixture.example/module"},
				Suites:        map[string]PackageProbeEvidence{"fixture.example/module": {Measured: true}},
			},
			wantMeasured: 1,
		},
		{
			name: "unmeasured explicit suite is retried",
			options: ProbeOptions{
				SuitePackages: []string{"fixture.example/module"},
				Suites: map[string]PackageProbeEvidence{
					"fixture.example/module": {},
					"fixture.example/saved":  {Measured: true},
				},
			},
			wantPackages: []string{"fixture.example/module"}, wantMeasured: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var packages []string
			session := &mutationUnitSession{catalog: catalog, probe: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				packages = append(packages, request.Package)
				return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, nil
			}}
			evaluation, err := ProbeTargets(t.Context(), session, nil, test.options)
			if err != nil || !slices.Equal(packages, test.wantPackages) ||
				evaluation.SuitesMeasured != test.wantMeasured || evaluation.SuitesUnmeasured != test.wantUnmeasured {
				t.Fatalf("evaluation = (%+v, %v), packages=%q", evaluation, err, packages)
			}
		})
	}
	if got := probeSuitePackages(catalog); !slices.Equal(got, []string{"fixture.example/module"}) {
		t.Fatalf("probe suite packages = %q", got)
	}
}

func TestProbeTargetsDoesNotRecordFatalWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	recording, recorder := newProbeRecording()
	session := &mutationUnitSession{catalog: probeCatalog(), probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		t.Fatal("canceled work reached the session")
		return gomutants.ProbeResult{}, nil
	}}
	evaluation, err := ProbeTargets(ctx, session, []TargetEvidence{probeEvidence("TestValue", goanalysis.KindTest, time.Second)}, ProbeOptions{Trace: recorder})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(evaluation, ProbeEvaluation{}) || len(probeRecords(t, recording)) != 0 {
		t.Fatalf("canceled evaluation = (%+v, %v), records=%+v", evaluation, err, probeRecords(t, recording))
	}
	target := probeMeasurement{recorded: true, record: probeRequestRecord("target", false, gomutants.ProbeRequest{})}
	suiteFatal := errors.New("suite fatal")
	selected := (probeWorkResult{target: target, suite: probeSuiteMeasurement{probeMeasurement: probeMeasurement{fatal: suiteFatal}}}).measurement()
	if !errors.Is(selected.fatal, suiteFatal) || selected.recorded {
		t.Fatalf("selected measurement = %+v", selected)
	}
}

func TestProbeResultRecordDistinguishesOutcomesUnknownsAndEmptyInfections(t *testing.T) {
	request := gomutants.ProbeRequest{Package: "fixture.example/module", Args: []string{"-test.run=^TestValue$"}}
	identities := map[uint32]string{0: "mutant-a", 2: "mutant-c"}
	for _, test := range []struct {
		name        string
		result      gomutants.ProbeResult
		measured    bool
		infected    []uint32
		recordIDs   []string
		wantOutcome string
		wantError   string
	}{
		{name: "timed out", result: gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut}, wantOutcome: string(gomutants.ProbeTimedOut)},
		{name: "empty measurement", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{}}, measured: true, infected: []uint32{}, recordIDs: []string{}, wantOutcome: string(gomutants.ProbeMeasured)},
		{name: "known measurement", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{2, 0, 2}}, measured: true, infected: []uint32{0, 2}, recordIDs: []string{"mutant-a", "mutant-c"}, wantOutcome: string(gomutants.ProbeMeasured)},
		{name: "unknown measurement", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{1}}, wantError: "unknown mutant index 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, infected, measured := probeResultRecord("target", false, request, test.result, identities)
			if measured != test.measured || !reflect.DeepEqual(infected, test.infected) || !reflect.DeepEqual(record.Infected, test.recordIDs) ||
				record.Outcome != test.wantOutcome || !strings.Contains(record.Error, test.wantError) {
				t.Fatalf("probe result = (%+v, %v, %t)", record, infected, measured)
			}
		})
	}
}

func TestProbeTargetDecidesOrdinaryFatalAndCanceledErrors(t *testing.T) {
	target := probeEvidence("TestValue", goanalysis.KindTest, time.Second)
	identities := probeMutantIdentities(probeCatalog())
	for _, test := range []struct {
		name     string
		handler  func(context.CancelFunc) func(gomutants.ProbeRequest) (gomutants.ProbeResult, error)
		fatal    bool
		recorded bool
	}{
		{
			name: "ordinary", recorded: true,
			handler: func(context.CancelFunc) func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
					return gomutants.ProbeResult{}, errors.New("scratch failed")
				}
			},
		},
		{
			name: "unprepared", fatal: true,
			handler: func(context.CancelFunc) func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
					return gomutants.ProbeResult{}, gomutants.ErrProbeNotPrepared
				}
			},
		},
		{
			name: "canceled during call", fatal: true,
			handler: func(cancel context.CancelFunc) func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
					cancel()
					return gomutants.ProbeResult{}, context.Canceled
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			session := &mutationUnitSession{probe: test.handler(cancel)}
			measurement := probeTarget(ctx, session, target, identities, ProbeOptions{})
			if (measurement.fatal != nil) != test.fatal || measurement.recorded != test.recorded {
				t.Fatalf("target measurement = %+v", measurement)
			}
		})
	}
}

func TestProbeSuiteDecidesCancellationErrorsObservationAndMeasurement(t *testing.T) {
	const pkg = "fixture.example/module"
	identities := map[uint32]string{0: "mutant-a"}
	for _, test := range []struct {
		name      string
		context   func(*testing.T) context.Context
		answer    func(gomutants.ProbeRequest) (gomutants.ProbeResult, error)
		observer  func(*testing.T) *RepositoryObserver
		fatal     bool
		recorded  bool
		measured  bool
		wholeTree bool
	}{
		{
			name: "already canceled", fatal: true,
			context: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			answer: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				t.Fatal("canceled suite called the session")
				return gomutants.ProbeResult{}, nil
			},
		},
		{name: "ordinary error", answer: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
			return gomutants.ProbeResult{}, errors.New("scratch failed")
		}, recorded: true},
		{name: "unprepared error", answer: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
			return gomutants.ProbeResult{}, gomutants.ErrProbeNotPrepared
		}, fatal: true},
		{
			name: "observation error", fatal: true, recorded: true,
			observer: func(t *testing.T) *RepositoryObserver {
				return newRepositoryObserver(t.TempDir(), t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{pkg: {}}, targetKeySources{model: baselineModel()})
			},
			answer: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				path, _ := repositoryTestLogPath(request.Args)
				return gomutants.ProbeResult{Output: []byte("testing: open " + path + ": denied")}, nil
			},
		},
		{
			name: "whole tree measurement", measured: true, recorded: true, wholeTree: true,
			observer: func(t *testing.T) *RepositoryObserver {
				return newRepositoryObserver(t.TempDir(), t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{pkg: {Unobservable: true}}, targetKeySources{})
			},
			answer: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{0}}, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.context != nil {
				ctx = test.context(t)
			}
			var observer *RepositoryObserver
			if test.observer != nil {
				observer = test.observer(t)
			}
			session := &mutationUnitSession{probe: test.answer}
			measurement := probeSuite(ctx, session, pkg, time.Second, identities, ProbeOptions{RepositoryObserver: observer})
			if (measurement.fatal != nil) != test.fatal || measurement.recorded != test.recorded ||
				measurement.measured != test.measured || measurement.wholeTree != test.wholeTree {
				t.Fatalf("suite measurement = %+v", measurement)
			}
			if measurement.recorded && !measurement.record.Suite {
				t.Fatalf("suite record lost its role: %+v", measurement.record)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	session := &mutationUnitSession{probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		cancel()
		return gomutants.ProbeResult{}, context.Canceled
	}}
	measurement := probeSuite(ctx, session, pkg, 0, identities, ProbeOptions{})
	if !errors.Is(measurement.fatal, context.Canceled) || measurement.recorded {
		t.Fatalf("suite canceled during call = %+v", measurement)
	}
}

func TestProbeDurationInputsSelectOnlyTheirPackageAndMeasuredSuite(t *testing.T) {
	targets := []TargetEvidence{
		{Target: goanalysis.Target{Package: "a"}, Duration: 2 * time.Second},
		{Target: goanalysis.Target{Package: "b"}, Duration: 4 * time.Second},
		{Target: goanalysis.Target{Package: "a"}, Duration: 8 * time.Second},
	}
	if got := packageSuiteControlDuration(targets, "a"); got != 10*time.Second {
		t.Fatalf("package control duration = %v", got)
	}
	if got := suiteCoverageControlDuration(map[string]PackageSuiteCoverage{"a": {Duration: 3 * time.Second}}, "a"); got != 3*time.Second {
		t.Fatalf("measured suite duration = %v", got)
	}
	if got := suiteCoverageControlDuration(nil, "a"); got != 0 {
		t.Fatalf("missing suite duration = %v", got)
	}
}

func TestCheckpointMutationProbeStoresOnlyMeasuredFacts(t *testing.T) {
	targets := []TargetEvidence{
		{Target: goanalysis.Target{ID: "measured"}, Probed: true, ProbeDuration: time.Second, Infected: []uint32{2, 1, 2}},
		{Target: goanalysis.Target{ID: "unmeasured"}, ProbeDuration: 9 * time.Second, Infected: []uint32{9}},
	}
	evaluation := ProbeEvaluation{Targets: targets, Suites: map[string]PackageProbeEvidence{
		"measured":   {Measured: true, Duration: 2 * time.Second, Infected: []uint32{2, 1, 2}, WholeTree: true},
		"unmeasured": {Duration: 9 * time.Second, Infected: []uint32{9}, WholeTree: true},
	}}
	saved := checkpointMutationProbe(probeCatalog(), evaluation)
	if len(saved.Targets) != 2 || !saved.Targets[0].Measured || saved.Targets[0].DurationNS != int64(time.Second) || !slices.Equal(saved.Targets[0].Infected, []uint32{1, 2}) ||
		saved.Targets[1].Measured || saved.Targets[1].DurationNS != 0 || saved.Targets[1].Infected != nil {
		t.Fatalf("target checkpoint = %+v", saved.Targets)
	}
	if len(saved.Suites) != 2 || saved.Suites[0].Package != "measured" || !saved.Suites[0].Measured || !saved.Suites[0].WholeTree ||
		saved.Suites[1].Package != "unmeasured" || saved.Suites[1].Measured || saved.Suites[1].DurationNS != 0 || saved.Suites[1].Infected != nil || saved.Suites[1].WholeTree {
		t.Fatalf("suite checkpoint = %+v", saved.Suites)
	}
}

func TestRestoreMutationProbeRejectsEveryMalformedBoundary(t *testing.T) {
	catalog := probeCatalog()
	targets := []TargetEvidence{
		{Target: goanalysis.Target{ID: "target-a"}, Probed: true, ProbeDuration: 99, Infected: []uint32{3}},
		{Target: goanalysis.Target{ID: "target-b"}, Probed: true, ProbeDuration: 99, Infected: []uint32{3}},
	}
	packages := []string{"pkg-a", "pkg-b"}
	valid := func() checkpoint.MutationProbe {
		return checkpoint.MutationProbe{
			IndexFingerprint: mutationProbeIndexFingerprint(catalog),
			Targets: []checkpoint.TargetProbe{
				{ID: "target-a", Measured: true, DurationNS: 5, Infected: []uint32{0, 2}},
				{ID: "target-b"},
			},
			Suites: []checkpoint.SuiteProbe{
				{Package: "pkg-a", Measured: true, DurationNS: 7, Infected: []uint32{1}, WholeTree: true},
				{Package: "pkg-b"},
			},
		}
	}
	restored, ok := restoreMutationProbe(catalog, targets, packages, valid())
	if !ok || restored.Measured != 1 || restored.Unmeasured != 1 || restored.SuitesMeasured != 1 || restored.SuitesUnmeasured != 1 ||
		!restored.Targets[0].Probed || restored.Targets[1].Probed || !restored.Suites["pkg-a"].Measured || restored.Suites["pkg-b"].Measured {
		t.Fatalf("valid restore = (%+v, %t)", restored, ok)
	}
	emptyTargets := slices.Clone(targets)
	emptyTargets[0].Target.ID = ""
	emptySaved := valid()
	emptySaved.Targets[0].ID = ""
	if evaluation, ok := restoreMutationProbe(catalog, emptyTargets, packages, emptySaved); ok || !reflect.DeepEqual(evaluation, ProbeEvaluation{}) {
		t.Fatalf("matching empty target identity restored = (%+v, %t)", evaluation, ok)
	}
	allMeasured := valid()
	allMeasured.Suites[1].Measured = true
	allMeasured.Suites[1].DurationNS = 1
	if evaluation, ok := restoreMutationProbe(catalog, targets, packages, allMeasured); !ok || evaluation.SuitesMeasured != 2 || evaluation.SuitesUnmeasured != 0 {
		t.Fatalf("all measured suites restored = (%+v, %t)", evaluation, ok)
	}
	for _, test := range []struct {
		name    string
		targets func([]TargetEvidence) []TargetEvidence
		change  func(*checkpoint.MutationProbe)
	}{
		{name: "empty input target id", targets: func(input []TargetEvidence) []TargetEvidence { input[0].Target.ID = ""; return input }},
		{name: "duplicate input target id", targets: func(input []TargetEvidence) []TargetEvidence { input[1].Target.ID = input[0].Target.ID; return input }},
		{name: "target count", change: func(saved *checkpoint.MutationProbe) { saved.Targets = saved.Targets[:1] }},
		{name: "unknown target", change: func(saved *checkpoint.MutationProbe) { saved.Targets[0].ID = "missing" }},
		{name: "duplicate saved target", change: func(saved *checkpoint.MutationProbe) { saved.Targets[1].ID = saved.Targets[0].ID }},
		{name: "invalid target fact", change: func(saved *checkpoint.MutationProbe) { saved.Targets[1].DurationNS = 1 }},
		{name: "unknown target infection", change: func(saved *checkpoint.MutationProbe) { saved.Targets[0].Infected = []uint32{99} }},
		{name: "package set", change: func(saved *checkpoint.MutationProbe) { saved.Suites[1].Package = "pkg-c" }},
		{name: "empty suite package", change: func(saved *checkpoint.MutationProbe) { saved.Suites[0].Package = "" }},
		{name: "invalid suite fact", change: func(saved *checkpoint.MutationProbe) { saved.Suites[1].WholeTree = true }},
		{name: "unknown suite infection", change: func(saved *checkpoint.MutationProbe) { saved.Suites[0].Infected = []uint32{99} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputTargets := slices.Clone(targets)
			if test.targets != nil {
				inputTargets = test.targets(inputTargets)
			}
			saved := valid()
			if test.change != nil {
				test.change(&saved)
			}
			if evaluation, ok := restoreMutationProbe(catalog, inputTargets, packages, saved); ok || !reflect.DeepEqual(evaluation, ProbeEvaluation{}) {
				t.Fatalf("invalid restore = (%+v, %t)", evaluation, ok)
			}
		})
	}
}
