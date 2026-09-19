// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/P4suta/go-mutants/goatest/internal/config"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/mutationbridge"
	"github.com/P4suta/go-mutants/goatest/internal/report"
	enginetrace "github.com/P4suta/go-mutants/trace"
)

const runDecisionCacheBytes = 41

func TestRunCoordinatorRejectsOwnedTestBinaryFlagsBeforeOpeningScratch(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	result, err := harness.run(Options{TestArgs: []string{"-test.timeout=1s"}})
	if err == nil || !strings.Contains(err.Error(), "assurance-owned flag") || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("run = (%+v, %v)", result, err)
	}
	if harness.openCalls != 0 || harness.runScratch != "" || len(harness.sweepParents) != 0 {
		t.Fatalf("startup continued: opens=%d scratch=%q sweeps=%q", harness.openCalls, harness.runScratch, harness.sweepParents)
	}
}

func TestEngineRecordingRequiresBothAReceiverAndEvents(t *testing.T) {
	t.Parallel()
	events := []enginetrace.Event{{Seq: 7}}
	var received [][]enginetrace.Event
	receiver := func(recording []enginetrace.Event) { received = append(received, recording) }
	recordEngineEvents(nil, events)
	recordEngineEvents(receiver, nil)
	recordEngineEvents(receiver, events)
	if len(received) != 1 || !reflect.DeepEqual(received[0], events) {
		t.Fatalf("received recordings = %+v", received)
	}
}

func TestRunCoordinatorReportsEveryBuildCacheLifecycleFailure(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	openFailure := errors.New("cache open failed")
	releaseFailure := errors.New("cache release failed")
	cacheScratch := t.TempDir()
	harness.dependencies.openBuildCache = func(program, base, source string, scratch runScratch, maxBytes int64) (runBuildCache, error) {
		if program != "cache-program" || base != "cache-base" || source != "cache-source" || scratch.dir == "" || maxBytes != runDecisionCacheBytes {
			t.Fatalf("open cache = %q %q %q %+v %d", program, base, source, scratch, maxBytes)
		}
		return runBuildCache{plain: "plain", persisting: "persisting", scratch: cacheScratch}, openFailure
	}
	collected, released := false, false
	harness.dependencies.collectBuildCache = func(_ Options, _ config.Config, cache runBuildCache, _ time.Time) {
		collected = cache.scratch == cacheScratch
	}
	harness.dependencies.releaseBuildCache = func(_ Options, cache runBuildCache, scratch runScratch, _ time.Time) error {
		released = cache.scratch == cacheScratch && scratch.dir != ""
		return releaseFailure
	}
	harness.loaded.Cache.BuildMaxBytes = runDecisionCacheBytes
	result, err := harness.run(Options{
		BuildCacheProgram: "cache-program", BuildCacheDir: "cache-base", BuildCacheNativeSource: "cache-source",
	})
	if err != nil || result.Verdict != report.VerdictAssured || !collected || !released {
		t.Fatalf("run = (%+v, %v), collected=%t released=%t", result, err, collected, released)
	}
	for _, want := range []struct{ kind, detail string }{
		{kind: "build-cache-unavailable", detail: openFailure.Error()},
		{kind: "build-cache-unavailable", detail: releaseFailure.Error()},
	} {
		if !slices.ContainsFunc(harness.events, func(event Event) bool { return event.Kind == want.kind && event.Detail == want.detail }) {
			t.Fatalf("events = %+v, want %+v", harness.events, want)
		}
	}
	if !slices.ContainsFunc(harness.events, func(event Event) bool {
		return event.Kind == "build-cache-summary" && strings.HasPrefix(event.Detail, "gets=0 ")
	}) {
		t.Fatalf("events = %+v, want build-cache-summary", harness.events)
	}
}

func TestRunCoordinatorChoosesTheExactMutationPreparationScope(t *testing.T) {
	for _, test := range []struct {
		name         string
		options      Options
		selection    *impactSelection
		wantInclude  []string
		wantPackages []string
	}{
		{name: "full project"},
		{
			name: "explicit package", options: Options{Packages: []string{"./fixture"}},
			wantInclude: []string{"*.go", "other/*.go"}, wantPackages: []string{"./fixture"},
		},
		{
			name: "changed explicit package", options: Options{Changed: true, Packages: []string{"./fixture"}},
			selection:   &impactSelection{changed: []string{"value.go"}},
			wantInclude: []string{"value.go"}, wantPackages: []string{"."},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			if test.selection != nil {
				selection := *test.selection
				harness.dependencies.selectImpact = func(_ context.Context, _ string, _ goanalysis.Model, targets []goanalysis.Target, _ Options) impactSelection {
					selection.targets = slices.Clone(targets)
					return selection
				}
			}
			if _, err := harness.run(test.options); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(harness.preparedOptions.Include, test.wantInclude) || !slices.Equal(harness.preparedOptions.Packages, test.wantPackages) {
				t.Fatalf("prepared scope = include %q packages %q", harness.preparedOptions.Include, harness.preparedOptions.Packages)
			}
		})
	}
}

func TestRunCoordinatorCreatesRepositoryObserversOnlyInsideTheReusableEvidenceBoundary(t *testing.T) {
	for _, test := range []struct {
		name         string
		options      Options
		emptyModel   bool
		wantObserver bool
	}{
		{name: "full run", wantObserver: true},
		{name: "changeset", options: Options{Changed: true}},
		{name: "empty package model", emptyModel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newRunCoordinatorHarness(t)
			if test.emptyModel {
				harness.metadata.model.Packages = nil
			}
			if _, err := harness.run(test.options); err != nil {
				t.Fatal(err)
			}
			if got := harness.baselineOptions.RepositoryObserver != nil; got != test.wantObserver {
				t.Fatalf("repository observer present = %t, want %t", got, test.wantObserver)
			}
		})
	}
}

func TestRunCoordinatorReportsAnUnavailableRepositoryObservationDirectory(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	acquire := harness.dependencies.acquireResources
	harness.dependencies.acquireResources = func(ctx context.Context, loaded config.Config, targets []goanalysis.Target, environment []string) (runRoundCloser, []BaselineTarget, []report.Evidence, []string, error) {
		manager, baseline, evidenceItems, resourceEnvironment, err := acquire(ctx, loaded, targets, environment)
		if removeErr := os.RemoveAll(harness.runScratch); removeErr != nil {
			t.Fatal(removeErr)
		}
		return manager, baseline, evidenceItems, resourceEnvironment, err
	}
	result, err := harness.run(Options{})
	if err != nil || result.Verdict != report.VerdictAssured || harness.baselineOptions.RepositoryObserver == nil {
		t.Fatalf("run = (%+v, %v), observer=%#v", result, err, harness.baselineOptions.RepositoryObserver)
	}
	if !slices.ContainsFunc(harness.events, func(event Event) bool {
		return event.Kind == "repository-observation-unavailable" && event.Detail != ""
	}) {
		t.Fatalf("events = %+v, want repository observation failure", harness.events)
	}
}

func TestRunCoordinatorRejectsAnEmptyPreparedSession(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.dependencies.prepareSession = func(context.Context, *mutationbridge.Workspace, mutationbridge.PrepareOptions) (MutationSession, error) {
		return nil, nil
	}
	result, err := harness.run(Options{})
	if err == nil || err.Error() != "goatest: mutation session was not prepared" || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("run = (%+v, %v)", result, err)
	}
	if harness.manager.calls != 1 || harness.workspaceCloses != 1 || harness.raceCalls != 0 {
		t.Fatalf("cleanup = manager %d workspace %d race %d", harness.manager.calls, harness.workspaceCloses, harness.raceCalls)
	}
}

func TestRunCoordinatorPassesAnExistingBaselineCheckpointIntoBothCollections(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	saved := checkpoint.Baseline{
		BuildVetComplete: true,
		Evidence:         []report.Evidence{{Kind: "baseline", ID: "saved", Status: "passed"}},
	}
	harness.cache.checkpoint = checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: harness.digest, Attempts: 2, Baseline: saved,
	}
	harness.cache.checkpointFound = true
	var resumes []*checkpoint.Baseline
	harness.dependencies.collectBaseline = func(_ context.Context, _ CommandWorkspace, _ goanalysis.Model, _ []BaselineTarget, options BaselineOptions) (BaselineResult, error) {
		harness.baselineCalls++
		if options.Resume == nil {
			resumes = append(resumes, nil)
		} else {
			copy := *options.Resume
			resumes = append(resumes, &copy)
		}
		if options.StopAfterChecks {
			return BaselineResult{}, nil
		}
		return harness.baseline, nil
	}
	if _, err := harness.run(Options{}); err != nil {
		t.Fatal(err)
	}
	if len(resumes) != 2 || !reflect.DeepEqual(resumes[0], &saved) || !reflect.DeepEqual(resumes[1], &saved) {
		t.Fatalf("baseline resumes = %+v, want saved state twice", resumes)
	}
	if !slices.ContainsFunc(harness.events, func(event Event) bool {
		return event.Kind == "resume-baseline" && event.Detail == "0 targets"
	}) {
		t.Fatalf("events = %+v, want resume-baseline", harness.events)
	}
}

func TestRunCoordinatorReportsEveryBaselineLimitationAndNoInventedResourceLimit(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.baseline.Targets[0].WholeTree = true
	harness.baseline.UnmeasuredSuites = map[string]gomutants.ProbeOutcome{
		"fixture.example/module": gomutants.ProbeTimedOut,
	}
	result, err := harness.run(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{
		report.LimitationWholeTreeBehaviourKeys,
		report.LimitationPackageSuiteUnmeasured,
		report.LimitationRaceScopeStaticEstimate,
	} {
		if !slices.ContainsFunc(result.Limitations, func(item report.Limitation) bool { return item.Code == code }) {
			t.Fatalf("limitations = %+v, want %q", result.Limitations, code)
		}
	}
	if slices.ContainsFunc(result.Limitations, func(item report.Limitation) bool {
		return item.Code == report.LimitationResourceCacheDisabled
	}) {
		t.Fatalf("limitations = %+v, resource cache was not disabled", result.Limitations)
	}
	static := result.Limitations[slices.IndexFunc(result.Limitations, func(item report.Limitation) bool {
		return item.Code == report.LimitationRaceScopeStaticEstimate
	})]
	if !static.Estimated {
		t.Fatalf("race limitation = %+v", static)
	}
}

func TestRunCoordinatorCountsExcludedTargetsFromTheDiscoveredInventory(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.targets[0].Path = "value_test.go"
	excluded := harness.targets[0]
	excluded.ID, excluded.Name, excluded.Path = "target-b", "TestGenerated", "generated/value_test.go"
	harness.targets = append(harness.targets, excluded)
	harness.loaded.Project.Exclude = []string{"generated/**"}
	result, err := harness.run(Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := report.CountAccounting{Discovered: 2, Selected: 1, Excluded: 1}
	if result.Accounting.Targets.Discovered != want.Discovered || result.Accounting.Targets.Selected != want.Selected || result.Accounting.Targets.Excluded != want.Excluded {
		t.Fatalf("target accounting = %+v, want %+v", result.Accounting.Targets, want)
	}
}

func TestRunCoordinatorDoesNotCheckpointRunsWithRuntimeResources(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.loaded.Resources = map[string]config.Resource{"db": {Command: []string{"provider"}}}
	result, err := harness.run(Options{})
	if err != nil || result.Resume != nil || harness.cache.checkpointFound {
		t.Fatalf("run = (%+v, %v), checkpoint=%+v found=%t", result, err, harness.cache.checkpoint, harness.cache.checkpointFound)
	}
}

func TestRunCoordinatorReusesAnExactRaceCheckpoint(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	savedEvidence := []report.Evidence{{Kind: "race", ID: "saved-race", Status: "passed"}}
	harness.cache.checkpoint = checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: harness.digest, Attempts: 1,
		Race: &checkpoint.Race{Complete: true, Packages: []string{"fixture.example/module"}, Evidence: savedEvidence},
	}
	harness.cache.checkpointFound = true
	result, err := harness.run(Options{})
	if err != nil || harness.raceCalls != 0 || !slices.ContainsFunc(result.Evidence, func(item report.Evidence) bool {
		return item.ID == "saved-race"
	}) {
		t.Fatalf("run = (%+v, %v), race calls=%d", result, err, harness.raceCalls)
	}
	if !slices.ContainsFunc(harness.events, func(event Event) bool {
		return event.Kind == "resume-race" && event.Detail == "1 packages"
	}) {
		t.Fatalf("events = %+v, want resume-race", harness.events)
	}
}

func TestRunCoordinatorNamesPluralMutationWork(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.catalog.Mutants = append(harness.catalog.Mutants, gomutants.Mutant{ID: "mutant-b", Accepted: true})
	if _, err := harness.run(Options{}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(harness.events, func(event Event) bool {
		return event.Kind == "mutation-target" && event.Detail == "2 mutants"
	}) {
		t.Fatalf("events = %+v, want plural mutation target", harness.events)
	}
}

func TestRunCoordinatorDiscardsMutationResumeWhenTheProbeInventoryIsInvalid(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.cache.checkpoint = checkpoint.State{
		Schema: checkpoint.SchemaV1, InputDigest: harness.digest, Attempts: 1,
		Mutation: &checkpoint.Mutation{
			CatalogFingerprint: MutationCatalogFingerprint(harness.catalog),
			Results:            []checkpoint.MutationResult{{ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "saved"}}}},
			Probe:              &checkpoint.MutationProbe{IndexFingerprint: "stale"},
		},
	}
	harness.cache.checkpointFound = true
	if _, err := harness.run(Options{}); err != nil {
		t.Fatal(err)
	}
	if harness.mutationOptions.Resume != nil {
		t.Fatalf("mutation resume = %+v, want invalid work discarded", harness.mutationOptions.Resume)
	}
	if !slices.ContainsFunc(harness.events, func(event Event) bool {
		return event.Kind == "checkpoint-warning" && strings.Contains(event.Detail, "probe inventory changed")
	}) {
		t.Fatalf("events = %+v, want invalid probe warning", harness.events)
	}
}

func TestRunCoordinatorClosesTheRoundWhenProbingFails(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	cause := errors.New("probe failed")
	harness.dependencies.probeTargets = func(context.Context, MutationSession, []TargetEvidence, ProbeOptions) (ProbeEvaluation, error) {
		return ProbeEvaluation{}, cause
	}
	result, err := harness.run(Options{})
	if !errors.Is(err, cause) || !reflect.DeepEqual(result, report.Report{}) || harness.mutationCalls != 0 || harness.manager.calls != 1 || harness.workspaceCloses != 1 {
		t.Fatalf("run = (%+v, %v), mutation=%d manager=%d workspace=%d", result, err, harness.mutationCalls, harness.manager.calls, harness.workspaceCloses)
	}
}

func TestRunCoordinatorTurnsUnknownMutationAccountingIntoAnErrorVerdict(t *testing.T) {
	harness := newRunCoordinatorHarness(t)
	harness.mutation.Accounting = report.MutantAccounting{Discovered: 1, Selected: 1, Unknown: 1}
	result, err := harness.run(Options{})
	if err != nil || result.Verdict != report.VerdictError || result.Accounting.Mutants.Unknown != 1 {
		t.Fatalf("run = (%+v, %v)", result, err)
	}
	if !slices.ContainsFunc(result.Findings, func(finding report.Finding) bool {
		return finding.Kind == "mutation-accounting" && strings.Contains(finding.Summary, "no auditable disposition")
	}) {
		t.Fatalf("findings = %+v, want mutation-accounting", result.Findings)
	}
}

func TestBoundedProgressUsesCeilingSizedStepsAndSuppressesDuplicates(t *testing.T) {
	t.Parallel()
	var events []Event
	progress := boundedProgress(Options{Progress: func(event Event) { events = append(events, event) }}, "work")
	for _, completed := range []int{0, 1, 2, 3, 3, 200, 201} {
		progress(completed, 201)
	}
	want := []Event{
		{Kind: "work", Detail: "0/201"},
		{Kind: "work", Detail: "1/201"},
		{Kind: "work", Detail: "3/201"},
		{Kind: "work", Detail: "201/201"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("progress events = %+v, want %+v", events, want)
	}
}
