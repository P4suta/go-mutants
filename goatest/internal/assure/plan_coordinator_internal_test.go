// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/config"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/mutationbridge"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const planTestMutantCount = 5

type planTestSession struct{ catalog gomutants.Catalog }

func (session planTestSession) Catalog() gomutants.Catalog { return session.catalog }

type planTestWorkspace struct {
	catalog        gomutants.Catalog
	toolchain      string
	swept          gomutants.SweepResult
	prepareErr     error
	closeErr       error
	commands       []gomutants.Command
	prepared       []mutationbridge.PrepareOptions
	closeCalls     int
	prepareStarted bool
}

func (workspace *planTestWorkspace) Exec(
	_ context.Context, command gomutants.Command,
) (gomutants.CommandResult, error) {
	workspace.commands = append(workspace.commands, command)
	return gomutants.CommandResult{}, nil
}

func (workspace *planTestWorkspace) ToolchainVersion() string { return workspace.toolchain }

func (workspace *planTestWorkspace) Swept() gomutants.SweepResult { return workspace.swept }

func (workspace *planTestWorkspace) Close() error {
	workspace.closeCalls++
	return workspace.closeErr
}

func (workspace *planTestWorkspace) PreparePlan(
	_ context.Context, options mutationbridge.PrepareOptions,
) (planMutationSession, error) {
	workspace.prepareStarted = true
	workspace.prepared = append(workspace.prepared, options)
	if workspace.prepareErr != nil {
		return nil, workspace.prepareErr
	}
	return planTestSession{catalog: workspace.catalog}, nil
}

type planTestHarness struct {
	dependencies planDependencies
	workspace    *planTestWorkspace
	loaded       config.Config
	model        goanalysis.Model
	targets      []goanalysis.Target
	selection    impactSelection
	metadata     roundMetadata

	identityErr  error
	rootErr      error
	configErr    error
	normalizeErr error
	scratchErr   error
	cacheErr     error
	openErr      error
	inspectErr   error
	discoverErr  error
	releaseErr   error

	rootRequests        []string
	configRequests      []string
	normalizeRequests   [][]string
	sweepOptions        []Options
	scratchRequests     [][2]string
	cacheRequests       []runBuildCache
	workspaceRoots      []string
	workspaceOptions    []mutationbridge.Options
	inspectPackages     [][]string
	inspectTags         [][]string
	inspectTimeouts     []time.Duration
	discoverRoots       []string
	discoverModels      [][]goanalysis.Package
	impactOptions       []Options
	collectCalls        int
	releaseCacheCalls   int
	releaseScratchCalls int
}

func newPlanTestHarness() *planTestHarness {
	const timeout = 9 * time.Second
	harness := &planTestHarness{
		loaded: config.Config{
			Contract: "standard-v1",
			Project:  config.Project{Packages: []string{"./..."}, Exclude: []string{"vendor/**"}},
			Execution: config.Execution{
				TestBinaryArgs: []string{"-short"}, BuildTags: []string{"integration"},
				Timeout: timeout, Jobs: 2,
			},
			Resources: map[string]config.Resource{"postgres": {Command: []string{"postgres"}}},
		},
		model: goanalysis.Model{
			ModulePath: "fixture.example/module", ModuleDir: "/repo",
			Packages: []goanalysis.Package{
				{ImportPath: "fixture.example/module/pkg", RelativeDir: "pkg"},
				{ImportPath: "fixture.example/module/other", RelativeDir: "other"},
			},
		},
		targets: []goanalysis.Target{
			{ID: "target-a", Name: "TestA", Kind: goanalysis.KindTest, Package: "fixture.example/module/pkg", RelativeDir: "pkg", Capabilities: []string{"postgres"}},
			{ID: "target-b", Name: "FuzzB", Kind: goanalysis.KindFuzz, Package: "fixture.example/module/other", RelativeDir: "other"},
		},
	}
	harness.metadata = roundMetadata{model: harness.model, toolchain: "go version go1.26.6 darwin/arm64"}
	harness.selection = impactSelection{targets: slices.Clone(harness.targets), broad: true}
	harness.workspace = &planTestWorkspace{
		toolchain: "go1.26.6",
		swept:     gomutants.SweepResult{Removed: []string{"old"}, RemovedBytes: 7},
		catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{
			{ID: "selected-a", Path: "pkg/a.go", Line: 11, Rule: "comparison", Original: "<", Replacement: "<=", Accepted: true},
			{ID: "selected-b", Path: "pkg/b.go", Line: 12, Rule: "boolean", Original: "true", Replacement: "false", Accepted: true},
			{ID: "selected-c", Path: "other/c.go", Line: 13, Rule: "numeric", Original: "1", Replacement: "0", Accepted: true},
			{ID: "rejected-a", Path: "pkg/d.go", Line: 14, Rule: "statement", Original: "call()", Replacement: "", Accepted: false},
			{ID: "rejected-b", Path: "pkg/e.go", Line: 15, Rule: "statement", Original: "return", Replacement: "", Accepted: false},
		}, Rejections: []gomutants.Rejection{
			{ID: "rejected-a", Diagnostic: "does not compile"},
			{ID: "rejected-b"},
		}},
	}
	harness.dependencies = planDependencies{
		goMutantsIdentity: func() (string, error) {
			if harness.identityErr != nil {
				return "", harness.identityErr
			}
			return "v1.2.3", nil
		},
		repositoryRoot: func(root string) (string, error) {
			harness.rootRequests = append(harness.rootRequests, root)
			if harness.rootErr != nil {
				return "", harness.rootErr
			}
			return "/repo", nil
		},
		loadConfig: func(root string) (config.Config, error) {
			harness.configRequests = append(harness.configRequests, root)
			if harness.configErr != nil {
				return config.Config{}, harness.configErr
			}
			return harness.loaded, nil
		},
		normalizeTestArgs: func(arguments []string) ([]string, error) {
			harness.normalizeRequests = append(harness.normalizeRequests, slices.Clone(arguments))
			if harness.normalizeErr != nil {
				return nil, harness.normalizeErr
			}
			return slices.Clone(arguments), nil
		},
		sweepTemporaries: func(options Options, _ time.Time) {
			harness.sweepOptions = append(harness.sweepOptions, options)
		},
		openScratch: func(temporary, root string, _ time.Time) (runScratch, error) {
			harness.scratchRequests = append(harness.scratchRequests, [2]string{temporary, root})
			if harness.scratchErr != nil {
				return runScratch{}, harness.scratchErr
			}
			return runScratch{dir: "/scratch", root: root, id: "run-a"}, nil
		},
		releaseScratch: func(_ Options, _ runScratch, _ time.Time) {
			harness.releaseScratchCalls++
		},
		openBuildCache: func(_, _, _ string, _ runScratch, _ int64) (runBuildCache, error) {
			if harness.cacheErr != nil {
				return runBuildCache{}, harness.cacheErr
			}
			return runBuildCache{}, nil
		},
		collectBuildCache: func(_ Options, _ config.Config, cache runBuildCache, _ time.Time) {
			harness.cacheRequests = append(harness.cacheRequests, cache)
			harness.collectCalls++
		},
		releaseBuildCache: func(_ Options, _ runBuildCache, _ runScratch, _ time.Time) error {
			harness.releaseCacheCalls++
			return harness.releaseErr
		},
		openWorkspace: func(_ context.Context, root string, options mutationbridge.Options) (planWorkspace, error) {
			harness.workspaceRoots = append(harness.workspaceRoots, root)
			harness.workspaceOptions = append(harness.workspaceOptions, options)
			if harness.openErr != nil {
				return nil, harness.openErr
			}
			return harness.workspace, nil
		},
		inspectWorkspace: func(
			_ context.Context, _ CommandWorkspace, _ string, packages, tags []string, timeout time.Duration,
		) (roundMetadata, error) {
			harness.inspectPackages = append(harness.inspectPackages, slices.Clone(packages))
			harness.inspectTags = append(harness.inspectTags, slices.Clone(tags))
			harness.inspectTimeouts = append(harness.inspectTimeouts, timeout)
			if harness.inspectErr != nil {
				return roundMetadata{}, harness.inspectErr
			}
			return harness.metadata, nil
		},
		discoverTargets: func(root string, packages []goanalysis.Package) ([]goanalysis.Target, error) {
			harness.discoverRoots = append(harness.discoverRoots, root)
			harness.discoverModels = append(harness.discoverModels, slices.Clone(packages))
			if harness.discoverErr != nil {
				return nil, harness.discoverErr
			}
			return slices.Clone(harness.targets), nil
		},
		selectImpact: func(
			_ context.Context, _ string, _ goanalysis.Model, _ []goanalysis.Target, options Options,
		) impactSelection {
			harness.impactOptions = append(harness.impactOptions, options)
			return harness.selection
		},
	}
	return harness
}

func planOptions() Options {
	return Options{
		Root: ".", KeepTemp: true, TempDirectory: "/temporary", Environment: []string{"CUSTOM=ready"},
		MutationOperators: []string{"comparison"}, Now: func() time.Time { return time.Date(2026, 9, 19, 3, 4, 5, 0, time.UTC) },
	}
}

func evidenceByID(items []report.Evidence, id string) (report.Evidence, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return report.Evidence{}, false
}

func TestPlanCoordinatorPublishesTheExactWorkItWouldRun(t *testing.T) {
	t.Parallel()
	harness := newPlanTestHarness()
	var events []Event
	options := planOptions()
	options.Progress = func(event Event) { events = append(events, event) }

	got, err := planWithDependencies(t.Context(), options, harness.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != report.SchemaV1 || got.RunKind != report.RunOperation || got.Verdict != report.VerdictCompleted || got.Contract != "standard-v1" {
		t.Fatalf("plan identity = schema %q kind %q verdict %q contract %q", got.Schema, got.RunKind, got.Verdict, got.Contract)
	}
	if got.Repository.Module != harness.model.ModulePath || !slices.Equal(got.Repository.Packages, []string{"fixture.example/module/other", "fixture.example/module/pkg"}) {
		t.Fatalf("repository = %+v", got.Repository)
	}
	if got.Toolchain.Go != harness.metadata.toolchain || got.Toolchain.GoMutants != "v1.2.3" || got.Toolchain.OS == "" || got.Toolchain.Arch == "" {
		t.Fatalf("toolchain = %+v", got.Toolchain)
	}
	if !slices.Equal(got.Execution.TestArgs, []string{"-short"}) || !slices.Equal(got.Execution.BuildTags, []string{"integration"}) || got.Execution.MutationJobs != 2 {
		t.Fatalf("execution = %+v", got.Execution)
	}
	if len(harness.workspaceOptions) != 1 {
		t.Fatalf("workspace opens = %d", len(harness.workspaceOptions))
	}
	opened := harness.workspaceOptions[0]
	if opened.TempDirectory != "/scratch" || opened.ReportDirectory != internalOutputDirectory || opened.KeepTemp || !slices.Equal(opened.SnapshotExclude, []string{reportOutputDirectory, distributionOutputDirectory}) {
		t.Fatalf("workspace options = %+v", opened)
	}
	tagged := false
	for _, entry := range opened.Environment {
		tagged = tagged || strings.HasPrefix(entry, "GOFLAGS=") && strings.Contains(entry, "-tags=integration")
	}
	if !containsEnvironment(opened.Environment, "CUSTOM", "ready") || !tagged {
		t.Fatalf("workspace environment = %q", opened.Environment)
	}
	if len(harness.workspace.prepared) != 1 {
		t.Fatalf("prepare calls = %d", len(harness.workspace.prepared))
	}
	prepared := harness.workspace.prepared[0]
	if prepared.Contract != "standard-v1" || !prepared.SkipVerify || prepared.Jobs != 2 || prepared.BuildTimeout != 9*time.Second || prepared.MutantTimeout != 9*time.Second {
		t.Fatalf("prepare options = %+v", prepared)
	}
	if !slices.Equal(prepared.Operators, []string{"comparison"}) || !slices.Equal(prepared.Exclude, []string{"vendor/**"}) || prepared.Include != nil || prepared.Packages != nil || prepared.DiscoveryPackages != nil {
		t.Fatalf("prepare scope = %+v", prepared)
	}
	for _, test := range []struct {
		id, status, detail string
	}{
		{id: "target-a", status: "selected", detail: "test fixture.example/module/pkg/TestA"},
		{id: "postgres", status: "required"},
		{id: "selected-a", status: "selected", detail: "pkg/a.go:11 comparison: < -> <="},
		{id: "rejected-a", status: "compile-rejected", detail: "does not compile"},
		{id: "rejected-b", status: "compile-rejected", detail: "pkg/e.go:15 statement: return -> "},
	} {
		item, found := evidenceByID(got.Evidence, test.id)
		if !found || item.Status != test.status || test.detail != "" && item.Detail != test.detail {
			t.Errorf("evidence %q = %+v, found %t", test.id, item, found)
		}
	}
	summary, found := evidenceByID(got.Evidence, "summary")
	if !found || summary.Detail != "targets=2 mutants=3 compile-rejected=2 resources=1 jobs=2 estimated-mutation-waves=2" {
		t.Fatalf("summary = %+v, found %t", summary, found)
	}
	if len(got.Limitations) != 2 || got.Limitations[1].Code != report.LimitationPlanCostEstimate || !got.Limitations[1].Estimated {
		t.Fatalf("limitations = %+v", got.Limitations)
	}
	if harness.workspace.closeCalls != 1 || harness.collectCalls != 1 || harness.releaseCacheCalls != 1 || harness.releaseScratchCalls != 1 {
		t.Fatalf("cleanup = workspace %d collect %d cache %d scratch %d", harness.workspace.closeCalls, harness.collectCalls, harness.releaseCacheCalls, harness.releaseScratchCalls)
	}
	if len(events) != 1 || events[0].Kind != "mutation-temp-sweep" || !strings.Contains(events[0].Detail, "removed=1 bytes=7") {
		t.Fatalf("progress = %+v", events)
	}
}

func TestPlanCoordinatorReturnsEveryFailureAtItsBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		configure func(*planTestHarness, error)
		opened    bool
		cleanup   bool
	}{
		{name: "engine identity", configure: func(h *planTestHarness, err error) { h.identityErr = err }},
		{name: "repository root", configure: func(h *planTestHarness, err error) { h.rootErr = err }},
		{name: "configuration", configure: func(h *planTestHarness, err error) { h.configErr = err }},
		{name: "test arguments", configure: func(h *planTestHarness, err error) { h.normalizeErr = err }},
		{name: "run scratch", configure: func(h *planTestHarness, err error) { h.scratchErr = err }},
		{name: "workspace", configure: func(h *planTestHarness, err error) { h.openErr = err }, cleanup: true},
		{name: "inspection", configure: func(h *planTestHarness, err error) { h.inspectErr = err }, opened: true, cleanup: true},
		{name: "discovery", configure: func(h *planTestHarness, err error) { h.discoverErr = err }, opened: true, cleanup: true},
		{name: "resource", configure: func(h *planTestHarness, _ error) {
			h.targets[0].Capabilities = []string{"absent"}
			h.selection.targets = slices.Clone(h.targets)
		}, opened: true, cleanup: true},
		{name: "preparation", configure: func(h *planTestHarness, err error) { h.workspace.prepareErr = err }, opened: true, cleanup: true},
		{name: "catalog", configure: func(h *planTestHarness, _ error) {
			h.workspace.catalog = gomutants.Catalog{Mutants: []gomutants.Mutant{{Accepted: true}}}
		}, opened: true, cleanup: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			harness := newPlanTestHarness()
			failure := errors.New("plan " + test.name + " failure")
			test.configure(harness, failure)
			_, err := planWithDependencies(t.Context(), planOptions(), harness.dependencies)
			if err == nil {
				t.Fatal("plan returned no error")
			}
			if test.name != "resource" && test.name != "catalog" && !errors.Is(err, failure) {
				t.Fatalf("plan error = %v, want %v", err, failure)
			}
			if opened := harness.workspace.closeCalls != 0; opened != test.opened {
				t.Errorf("workspace closed = %t, want %t", opened, test.opened)
			}
			if cleaned := harness.releaseScratchCalls != 0; cleaned != test.cleanup {
				t.Errorf("scratch released = %t, want %t", cleaned, test.cleanup)
			}
		})
	}
}

func TestPlanCoordinatorSelectsContractsScopeAndMutationWaves(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		options   Options
		contract  string
		selection impactSelection
		catalog   gomutants.Catalog
		prepares  bool
		include   []string
		packages  []string
		discovery []string
		wantWaves string
	}{
		{
			name: "configured contract and broad default scope", options: planOptions(), contract: "standard-v1",
			selection: impactSelection{broad: true}, catalog: gomutants.Catalog{}, prepares: true,
			wantWaves: "estimated-mutation-waves=0",
		},
		{
			name: "explicit deep contract and package scope", options: func() Options {
				options := planOptions()
				options.Contract = "deep-v1"
				options.Packages = []string{"./asked/..."}
				return options
			}(), contract: "deep-v1", selection: impactSelection{broad: true}, catalog: gomutants.Catalog{}, prepares: true,
			packages: []string{"./asked/..."}, discovery: []string{"./asked/..."}, wantWaves: "estimated-mutation-waves=0",
		},
		{
			name: "known empty changeset", options: func() Options {
				options := planOptions()
				options.Changed = true
				return options
			}(), contract: "standard-v1", selection: impactSelection{changed: []string{}},
		},
		{
			name: "known source change", options: func() Options {
				options := planOptions()
				options.Changed = true
				return options
			}(), contract: "standard-v1",
			selection: impactSelection{
				targets: []goanalysis.Target{{ID: "target-a", Name: "TestA", Kind: goanalysis.KindTest, Package: "fixture.example/module/pkg", RelativeDir: "pkg"}},
				changed: []string{"pkg/value.go"},
			},
			catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{
				{ID: "one", Accepted: true}, {ID: "two", Accepted: true}, {ID: "three", Accepted: true},
			}}, prepares: true, include: []string{"pkg/value.go"}, packages: []string{"./pkg"}, discovery: []string{"./pkg"},
			wantWaves: "estimated-mutation-waves=2",
		},
		{
			name: "unknown changed impact stays broad", options: func() Options {
				options := planOptions()
				options.Changed = true
				return options
			}(), contract: "standard-v1", selection: impactSelection{broad: true}, catalog: gomutants.Catalog{}, prepares: true,
			wantWaves: "estimated-mutation-waves=0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			harness := newPlanTestHarness()
			harness.selection = test.selection
			if harness.selection.targets == nil && (test.selection.broad || !test.options.Changed) {
				harness.selection.targets = slices.Clone(harness.targets)
			}
			harness.workspace.catalog = test.catalog
			got, err := planWithDependencies(t.Context(), test.options, harness.dependencies)
			if err != nil {
				t.Fatal(err)
			}
			if got.Contract != test.contract {
				t.Errorf("contract = %q, want %q", got.Contract, test.contract)
			}
			if harness.workspace.prepareStarted != test.prepares {
				t.Fatalf("prepare started = %t, want %t", harness.workspace.prepareStarted, test.prepares)
			}
			if !test.prepares {
				return
			}
			prepared := harness.workspace.prepared[0]
			if !slices.Equal(prepared.Include, test.include) || !slices.Equal(prepared.Packages, test.packages) || !slices.Equal(prepared.DiscoveryPackages, test.discovery) {
				t.Errorf("scope = include %q packages %q discovery %q", prepared.Include, prepared.Packages, prepared.DiscoveryPackages)
			}
			summary, found := evidenceByID(got.Evidence, "summary")
			if !found || !strings.Contains(summary.Detail, test.wantWaves) {
				t.Errorf("summary = %+v, found %t", summary, found)
			}
		})
	}
}

func TestPlanCoordinatorRejectsUnknownContractsAndJoinsCleanupFailures(t *testing.T) {
	t.Parallel()
	t.Run("unknown contract", func(t *testing.T) {
		t.Parallel()
		harness := newPlanTestHarness()
		options := planOptions()
		options.Contract = "unknown-v1"
		_, err := planWithDependencies(t.Context(), options, harness.dependencies)
		if err == nil || !strings.Contains(err.Error(), `contract "unknown-v1" is unknown`) {
			t.Fatalf("plan error = %v", err)
		}
		if len(harness.sweepOptions) != 0 {
			t.Fatalf("unknown contract swept %d temporary roots", len(harness.sweepOptions))
		}
	})
	t.Run("build cache unavailable", func(t *testing.T) {
		t.Parallel()
		harness := newPlanTestHarness()
		harness.cacheErr = errors.New("cache unavailable")
		var events []Event
		options := planOptions()
		options.Progress = func(event Event) { events = append(events, event) }
		if _, err := planWithDependencies(t.Context(), options, harness.dependencies); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range events {
			found = found || event.Kind == "build-cache-unavailable" && event.Detail == "cache unavailable"
		}
		if !found || !harness.workspace.prepareStarted {
			t.Fatalf("events = %+v, prepare started = %t", events, harness.workspace.prepareStarted)
		}
	})
	t.Run("workspace close", func(t *testing.T) {
		t.Parallel()
		harness := newPlanTestHarness()
		failure := errors.New("close workspace")
		harness.workspace.closeErr = failure
		got, err := planWithDependencies(t.Context(), planOptions(), harness.dependencies)
		if !errors.Is(err, failure) || got.Verdict != report.VerdictCompleted {
			t.Fatalf("plan = verdict %q error %v", got.Verdict, err)
		}
	})
	t.Run("build cache release", func(t *testing.T) {
		t.Parallel()
		harness := newPlanTestHarness()
		failure := errors.New("release cache")
		harness.releaseErr = failure
		got, err := planWithDependencies(t.Context(), planOptions(), harness.dependencies)
		if !errors.Is(err, failure) || got.Verdict != report.VerdictCompleted {
			t.Fatalf("plan = verdict %q error %v", got.Verdict, err)
		}
	})
	if planTestMutantCount != len(newPlanTestHarness().workspace.catalog.Mutants) {
		t.Fatalf("plan fixture has %d mutants, want %d", len(newPlanTestHarness().workspace.catalog.Mutants), planTestMutantCount)
	}
}

func TestProductionPlanWorkspaceAdapterPreservesBothResults(t *testing.T) {
	t.Parallel()
	failure := errors.New("open workspace")
	failed, err := adaptProductionPlanWorkspace(nil, failure)
	if failed != nil || !errors.Is(err, failure) {
		t.Fatalf("failed adapter = %#v, %v", failed, err)
	}
	workspace := &mutationbridge.Workspace{}
	adapted, err := adaptProductionPlanWorkspace(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	production, ok := adapted.(productionPlanWorkspace)
	if !ok || production.Workspace != workspace {
		t.Fatalf("adapted workspace = %#v", adapted)
	}
}
