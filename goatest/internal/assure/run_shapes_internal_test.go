// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/config"
	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func TestAssuranceInputsResolveBothBuildIdentitiesInOrder(t *testing.T) {
	t.Parallel()
	engineFailure := errors.New("engine identity")
	buildFailure := errors.New("executable identity")
	for _, test := range []struct {
		name        string
		engine      func() (string, error)
		build       func() (string, error)
		want        error
		engineValue string
		buildValue  string
		buildCalls  int
	}{
		{
			name:   "engine failure",
			engine: func() (string, error) { return "", engineFailure },
			build:  func() (string, error) { return "unreached", nil },
			want:   engineFailure,
		},
		{
			name:   "executable failure",
			engine: func() (string, error) { return "engine-a", nil },
			build:  func() (string, error) { return "", buildFailure },
			want:   buildFailure, buildCalls: 1,
		},
		{
			name:        "both identities",
			engine:      func() (string, error) { return "engine-a", nil },
			build:       func() (string, error) { return "build-a", nil },
			engineValue: "engine-a", buildValue: "build-a", buildCalls: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeRunHelperFile(t, root, "value.go", "package fixture\n")
			calls := 0
			build := func() (string, error) {
				calls++
				return test.build()
			}
			inputs, digest, err := assuranceInputsWithIdentityResolvers(
				root, "standard-v1", Options{Environment: []string{}}, config.Config{},
				roundMetadata{toolchain: "go version go1.26.6"}, test.engine, build,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("assurance inputs error = %v, want %v", err, test.want)
			}
			if calls != test.buildCalls {
				t.Fatalf("build identity calls = %d, want %d", calls, test.buildCalls)
			}
			if test.want != nil {
				if !reflect.DeepEqual(inputs, evidence.Inputs{}) || digest != "" {
					t.Fatalf("failed inputs = %+v, digest %q", inputs, digest)
				}
				return
			}
			if inputs.GoMutantsVersion != test.engineValue || inputs.GoatestBuild != test.buildValue || digest == "" {
				t.Fatalf("inputs = %+v, digest %q", inputs, digest)
			}
		})
	}
}

func TestBuildTagsAreAddedToEveryEnvironmentShape(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		environment []string
		tags        []string
		want        []string
	}{
		{name: "no tags", environment: []string{"OTHER=value", "GOFLAGS=-trimpath"}, want: []string{"OTHER=value", "GOFLAGS=-trimpath"}},
		{name: "nonempty flags", environment: []string{"OTHER=value", "GOFLAGS=-trimpath"}, tags: []string{"integration", "race"}, want: []string{"GOFLAGS=-trimpath -tags=integration,race", "OTHER=value"}},
		{name: "empty flags", environment: []string{"OTHER=value", "GOFLAGS="}, tags: []string{"integration"}, want: []string{"GOFLAGS=-tags=integration", "OTHER=value"}},
		{name: "case folded flags", environment: []string{"OTHER=value", "goflags=-trimpath"}, tags: []string{"integration"}, want: []string{"OTHER=value", "goflags=-trimpath -tags=integration"}},
		{name: "missing flags", environment: []string{"OTHER=value", "BROKEN"}, tags: []string{"integration"}, want: []string{"BROKEN", "GOFLAGS=-tags=integration", "OTHER=value"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			before := slices.Clone(test.environment)
			got := environmentWithBuildTags(test.environment, test.tags)
			if !slices.Equal(got, test.want) {
				t.Fatalf("environment = %q, want %q", got, test.want)
			}
			if !slices.Equal(test.environment, before) {
				t.Fatalf("input changed from %q to %q", before, test.environment)
			}
		})
	}
	got := mutationEnvironment([]string{"A=1", "GOFLAGS=-trimpath"}, []string{"integration"})
	if !containsEnvironment(got, "GOFLAGS", "-trimpath -buildvcs=false -tags=integration") {
		t.Fatalf("mutation environment = %q", got)
	}
	caseFolded := executionEnvironment([]string{"goflags=-trimpath"})
	if !slices.Contains(caseFolded, "goflags=-trimpath -buildvcs=false") || slices.Contains(caseFolded, "GOFLAGS=-trimpath -buildvcs=false") {
		t.Fatalf("case-folded execution environment = %q", caseFolded)
	}
}

func TestReportScopeDistinguishesEveryRequestedAndResolvedShape(t *testing.T) {
	t.Parallel()
	model := goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{
		{ImportPath: "fixture.example/module/z"}, {ImportPath: "fixture.example/module/a"},
	}}
	all := []string{"fixture.example/module/a", "fixture.example/module/z"}
	for _, test := range []struct {
		name      string
		options   Options
		selection impactSelection
		requested report.ScopeSpec
		resolved  report.ScopeSpec
	}{
		{
			name: "full project", options: Options{Packages: []string{"./..."}},
			requested: report.ScopeSpec{Kind: "full", Project: ".", Modules: []string{model.ModulePath}, Packages: all},
			resolved:  report.ScopeSpec{Kind: "full", Project: ".", Modules: []string{model.ModulePath}, Packages: all},
		},
		{
			name: "package", options: Options{Packages: []string{"./pkg"}, PackageScope: true},
			requested: report.ScopeSpec{Kind: "package", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}},
			resolved:  report.ScopeSpec{Kind: "package", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}},
		},
		{
			name: "finding replay", options: Options{Packages: []string{"./pkg"}, ReplayFindingID: "finding-a"},
			requested: report.ScopeSpec{Kind: "replay", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}},
			resolved:  report.ScopeSpec{Kind: "replay", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}},
		},
		{
			name: "mutant replay", options: Options{Packages: []string{"./pkg"}, ReplayMutantID: "mutant-a"},
			requested: report.ScopeSpec{Kind: "replay", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}},
			resolved:  report.ScopeSpec{Kind: "replay", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}},
		},
		{
			name: "narrow changeset", options: Options{Packages: []string{"./pkg"}, Changed: true, ChangedRef: "main"},
			selection: impactSelection{changed: []string{"pkg/value.go"}},
			requested: report.ScopeSpec{Kind: "changeset", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}, Files: []string{"pkg/value.go"}, Ref: "main"},
			resolved:  report.ScopeSpec{Kind: "changeset", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}, Files: []string{"pkg/value.go"}, Ref: "main"},
		},
		{
			name: "broad changeset", options: Options{Packages: []string{"./pkg"}, Changed: true, PackageScope: true, ChangedRef: "main"},
			selection: impactSelection{changed: []string{"pkg/value.go"}, broad: true},
			requested: report.ScopeSpec{Kind: "changeset", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}, Files: []string{"pkg/value.go"}, Ref: "main"},
			resolved:  report.ScopeSpec{Kind: "full", Project: ".", Modules: []string{model.ModulePath}, Packages: all, Ref: "main"},
		},
		{
			name: "replayed changeset", options: Options{Packages: []string{"./pkg"}, Changed: true, ReplayFindingID: "finding-a"},
			selection: impactSelection{changed: []string{"pkg/value.go"}},
			requested: report.ScopeSpec{Kind: "replay", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}, Files: []string{"pkg/value.go"}},
			resolved:  report.ScopeSpec{Kind: "replay", Project: ".", Modules: []string{model.ModulePath}, Packages: []string{"./pkg"}, Files: []string{"pkg/value.go"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := reportScope(test.options, model, test.selection)
			if !reflect.DeepEqual(got.Requested, test.requested) || !reflect.DeepEqual(got.Resolved, test.resolved) {
				t.Fatalf("scope = %+v, want requested %+v resolved %+v", got, test.requested, test.resolved)
			}
		})
	}
}

func TestWorkspaceModuleGraphAcceptsExactlyOneMatchingMainModule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		data   []byte
		module string
		want   string
	}{
		{name: "malformed", data: []byte("{"), module: "fixture.example/module", want: "decode module graph"},
		{name: "empty path", data: moduleGraphJSON(t, listedModule{Main: true}), module: "fixture.example/module", want: "empty path"},
		{name: "no main", data: moduleGraphJSON(t, listedModule{Path: "example.com/dependency"}), module: "fixture.example/module", want: "no main module"},
		{name: "different main", data: moduleGraphJSON(t, listedModule{Path: "fixture.example/other", Main: true}), module: "fixture.example/module", want: "does not match"},
		{name: "multiple mains", data: moduleGraphJSON(t, listedModule{Path: "fixture.example/a", Main: true}, listedModule{Path: "fixture.example/b", Main: true}), module: "fixture.example/module", want: "multiple main modules"},
		{name: "matching main", data: moduleGraphJSON(t, listedModule{Path: "fixture.example/module", Main: true}), module: "fixture.example/module"},
		{name: "duplicate matching main", data: moduleGraphJSON(t, listedModule{Path: "fixture.example/module", Main: true}, listedModule{Path: "fixture.example/module", Main: true}), module: "fixture.example/module"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateWorkspaceModuleGraph(test.data, test.module)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("module graph error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProjectExcludesRecognizeEverySupportedPatternShape(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		candidate string
		patterns  []string
		excluded  bool
	}{
		{name: "no patterns", candidate: "pkg/value.go"},
		{name: "whole tree", candidate: "pkg/value.go", patterns: []string{"**"}, excluded: true},
		{name: "directory itself", candidate: "generated", patterns: []string{"generated/**"}, excluded: true},
		{name: "directory child", candidate: "generated/pkg/value.go", patterns: []string{"generated/**"}, excluded: true},
		{name: "directory sibling", candidate: "generated-other/value.go", patterns: []string{"generated/**"}},
		{name: "suffix at root", candidate: "value_generated.go", patterns: []string{"**/*_generated.go"}, excluded: true},
		{name: "suffix nested", candidate: "pkg/value_generated.go", patterns: []string{"**/*_generated.go"}, excluded: true},
		{name: "suffix mismatch", candidate: "pkg/value.go", patterns: []string{"**/*_generated.go"}},
		{name: "ordinary glob", candidate: "pkg/value.go", patterns: []string{"pkg/*.go"}, excluded: true},
		{name: "ordinary glob is not a directory prefix", candidate: "pkg/*.go/child", patterns: []string{"pkg/*.go"}},
		{name: "ordinary glob is not a recursive suffix", candidate: "root/pkg/value.go", patterns: []string{"pkg/*.go"}},
		{name: "normalized path", candidate: `./pkg\value.go`, patterns: []string{"./pkg/*.go"}, excluded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := projectPathExcluded(test.candidate, test.patterns); got != test.excluded {
				t.Fatalf("projectPathExcluded(%q, %q) = %t, want %t", test.candidate, test.patterns, got, test.excluded)
			}
		})
	}
	targets := []goanalysis.Target{{ID: "a"}, {ID: "b"}}
	got := includedProjectTargets(targets, nil)
	got[0].ID = "changed"
	if targets[0].ID != "a" || len(got) != len(targets) {
		t.Fatalf("included targets = %+v, input = %+v", got, targets)
	}
}

func TestAcceptanceMetadataAndCachedReportsUseOnlyCurrentEntries(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.FixedZone("offset", 9*60*60))
	loaded := config.Config{Acceptance: []config.Acceptance{
		{ID: "z-active", Reason: "z", Expires: now.Add(time.Hour), Owner: "owner-z", Ticket: "Z-1"},
		{ID: "equal", Expires: now},
		{ID: "expired", Expires: now.Add(-time.Nanosecond)},
		{ID: "a-active", Reason: "a", Expires: now.Add(2 * time.Hour), Owner: "owner-a", Ticket: "A-1"},
	}}
	got := activeAcceptanceMetadata(loaded, now)
	if len(got) != 2 || got[0].ID != "a-active" || got[1].ID != "z-active" || got[0].Reason != "a" || got[0].Owner != "owner-a" || got[0].Ticket != "A-1" || got[0].Expires != loaded.Acceptance[3].Expires.UTC().Format(time.RFC3339) {
		t.Fatalf("active acceptance metadata = %+v", got)
	}
	for _, test := range []struct {
		name     string
		report   report.Report
		accepted map[string]bool
		reusable bool
	}{
		{name: "plain report", reusable: true},
		{name: "current metadata", report: report.Report{Acceptances: []report.Acceptance{{ID: "active"}}}, accepted: map[string]bool{"active": true}, reusable: true},
		{name: "expired metadata", report: report.Report{Acceptances: []report.Acceptance{{ID: "expired"}}}, accepted: map[string]bool{"active": true}},
		{name: "later phases absent", report: report.Report{Limitations: []report.Limitation{{Code: report.LimitationLaterPhasesNotRun}}}},
		{name: "unrelated limitation", report: report.Report{Limitations: []report.Limitation{{Code: report.LimitationPlanCostEstimate}}}, reusable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := cachedReportReusable(test.report, test.accepted); got != test.reusable {
				t.Fatalf("cachedReportReusable = %t, want %t", got, test.reusable)
			}
		})
	}
	if countedNoun(0, "target", "targets") != "0 targets" || countedNoun(1, "target", "targets") != "1 target" || countedNoun(2, "target", "targets") != "2 targets" {
		t.Fatal("counted noun did not distinguish zero, one and many")
	}
}
