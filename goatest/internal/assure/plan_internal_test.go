// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/config"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
)

const (
	configuredTimeout    = 90 * time.Second
	pinnedTimeout        = 30 * time.Second
	configuredJobs       = 6
	pinnedJobs           = 3
	oneExclusiveJob      = 1
	excludedPatternCount = 2
)

func configuredDefaults() config.Config {
	return config.Config{
		Project: config.Project{Packages: []string{"./configured/..."}},
		Execution: config.Execution{
			TestBinaryArgs: []string{"-configured"},
			BuildTags:      []string{"configured"},
			Timeout:        configuredTimeout,
			Jobs:           configuredJobs,
		},
	}
}

func TestExecutionDefaultsFillOnlyWhatTheRequestLeftOpen(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		options Options
		want    Options
	}{
		{
			name:    "a request that names nothing",
			options: Options{},
			want: Options{
				Packages: []string{"./configured/..."}, TestArgs: []string{"-configured"},
				BuildTags: []string{"configured"}, CommandTimeout: configuredTimeout,
				TargetTimeout: configuredTimeout, MutationJobs: configuredJobs,
			},
		},
		{
			name:    "a request that names its own packages",
			options: Options{Packages: []string{"./asked/..."}},
			want: Options{
				Packages: []string{"./asked/..."}, TestArgs: []string{"-configured"},
				BuildTags: []string{"configured"}, CommandTimeout: configuredTimeout,
				TargetTimeout: configuredTimeout, MutationJobs: configuredJobs,
			},
		},
		{
			name: "a request that pinned its execution",
			options: Options{
				ExecutionPinned: true, TestArgs: []string{"-asked"}, CommandTimeout: pinnedTimeout,
			},
			want: Options{
				Packages: []string{"./configured/..."}, TestArgs: []string{"-asked"},
				CommandTimeout: pinnedTimeout, ExecutionPinned: true,
			},
		},
		{
			name: "a request that named every execution setting",
			options: Options{
				Packages: []string{"./asked/..."}, TestArgs: []string{"-asked"},
				BuildTags: []string{"asked"}, CommandTimeout: pinnedTimeout,
				TargetTimeout: pinnedTimeout, MutationJobs: pinnedJobs,
			},
			want: Options{
				Packages: []string{"./asked/..."}, TestArgs: []string{"-asked"},
				BuildTags: []string{"asked"}, CommandTimeout: pinnedTimeout,
				TargetTimeout: pinnedTimeout, MutationJobs: pinnedJobs,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := test.options
			applyExecutionDefaults(&options, configuredDefaults())
			if !slices.Equal(options.Packages, test.want.Packages) {
				t.Errorf("packages = %q, want %q", options.Packages, test.want.Packages)
			}
			if !slices.Equal(options.TestArgs, test.want.TestArgs) {
				t.Errorf("test arguments = %q, want %q", options.TestArgs, test.want.TestArgs)
			}
			if !slices.Equal(options.BuildTags, test.want.BuildTags) {
				t.Errorf("build tags = %q, want %q", options.BuildTags, test.want.BuildTags)
			}
			if options.CommandTimeout != test.want.CommandTimeout {
				t.Errorf("command timeout = %s, want %s", options.CommandTimeout, test.want.CommandTimeout)
			}
			if options.TargetTimeout != test.want.TargetTimeout {
				t.Errorf("target timeout = %s, want %s", options.TargetTimeout, test.want.TargetTimeout)
			}
			if options.MutationJobs != test.want.MutationJobs {
				t.Errorf("mutation jobs = %d, want %d", options.MutationJobs, test.want.MutationJobs)
			}
		})
	}
}

func TestPlannedResourcesNameEveryCapabilityOnceAndRefuseAnUnconfiguredOne(t *testing.T) {
	t.Parallel()
	loaded := config.Config{Resources: map[string]config.Resource{
		"postgres": {Command: []string{"pg"}},
		"redis":    {Command: []string{"redis"}},
	}}
	for _, test := range []struct {
		name    string
		targets []goanalysis.Target
		want    []string
		refuses string
	}{
		{name: "targets that need nothing"},
		{
			name:    "one target that needs one resource",
			targets: []goanalysis.Target{{Name: "TestOne", Capabilities: []string{"postgres"}}},
			want:    []string{"postgres"},
		},
		{
			name: "two targets that need the same resource",
			targets: []goanalysis.Target{
				{Name: "TestOne", Capabilities: []string{"postgres"}},
				{Name: "TestTwo", Capabilities: []string{"postgres"}},
			},
			want: []string{"postgres"},
		},
		{
			name: "targets that need two resources, named out of order",
			targets: []goanalysis.Target{
				{Name: "TestOne", Capabilities: []string{"redis"}},
				{Name: "TestTwo", Capabilities: []string{"postgres"}},
			},
			want: []string{"postgres", "redis"},
		},
		{
			name:    "a target that needs a resource nobody configured",
			targets: []goanalysis.Target{{Name: "TestOne", Capabilities: []string{"kafka"}}},
			refuses: `target TestOne requires unconfigured resource "kafka"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resources, err := plannedResources(test.targets, loaded)
			if test.refuses != "" {
				if err == nil || !strings.Contains(err.Error(), test.refuses) {
					t.Fatalf("plannedResources reported %v, want it to say %q", err, test.refuses)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(resources, test.want) {
				t.Fatalf("plannedResources = %q, want %q", resources, test.want)
			}
		})
	}
}

func TestTheMutationJobLimitYieldsToAnExclusiveResource(t *testing.T) {
	t.Parallel()
	exclusive := config.Config{Resources: map[string]config.Resource{
		"shared":    {Command: []string{"one"}},
		"exclusive": {Command: []string{"two"}, Exclusive: true},
	}}
	shared := config.Config{Resources: map[string]config.Resource{"shared": {Command: []string{"one"}}}}
	for _, test := range []struct {
		name   string
		loaded config.Config
		jobs   int
		want   int
	}{
		{name: "an exclusive resource", loaded: exclusive, jobs: configuredJobs, want: oneExclusiveJob},
		{name: "an exclusive resource and no request", loaded: exclusive, want: oneExclusiveJob},
		{name: "a request against shared resources", loaded: shared, jobs: pinnedJobs, want: pinnedJobs},
		{
			name: "no request against shared resources", loaded: shared,
			want: defaultMutationJobLimit(),
		},
		{
			name: "a request of no job at all", loaded: shared, jobs: 0,
			want: defaultMutationJobLimit(),
		},
		{
			name: "a request of fewer than no job", loaded: shared, jobs: -1,
			want: defaultMutationJobLimit(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationJobLimit(Options{MutationJobs: test.jobs}, test.loaded); got != test.want {
				t.Fatalf("mutationJobLimit = %d, want %d", got, test.want)
			}
		})
	}
	if got := defaultMutationJobLimit(); got < 1 || got > defaultMutationJobCap {
		t.Fatalf("the default job limit is %d, want it between 1 and %d", got, defaultMutationJobCap)
	}
}

func TestDefaultPackagePatternsRecognizeOnlyTheWholeProject(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		patterns []string
		want     bool
	}{
		{name: "no pattern at all", want: true},
		{name: "the whole tree", patterns: []string{"./..."}, want: true},
		{name: "one package", patterns: []string{"./internal/app"}},
		{name: "the whole tree beside another", patterns: []string{"./...", "./internal/app"}},
		{name: "the whole tree of a subdirectory", patterns: []string{"./internal/..."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := defaultPackagePatterns(test.patterns); got != test.want {
				t.Fatalf("defaultPackagePatterns(%q) = %t, want %t", test.patterns, got, test.want)
			}
		})
	}
}

func TestProjectExcludeLimitationsNameEveryPatternTheBoundaryLeavesOut(t *testing.T) {
	t.Parallel()
	if got := projectExcludeLimitations(nil); len(got) != 0 {
		t.Fatalf("a project that excludes nothing stated %+v, want no limitation", got)
	}
	limitations := projectExcludeLimitations([]string{"vendor/**", "third_party/**"})
	if len(limitations) != excludedPatternCount {
		t.Fatalf("two excluded patterns stated %d limitations, want two", len(limitations))
	}
	for index, pattern := range []string{"vendor/**", "third_party/**"} {
		if !strings.Contains(limitations[index].Summary, pattern) {
			t.Errorf("limitation %d says %q, want it to name %q", index, limitations[index].Summary, pattern)
		}
	}
}

func TestModelPackagePathsNameEveryPackageOnceInOrder(t *testing.T) {
	t.Parallel()
	model := goanalysis.Model{Packages: []goanalysis.Package{
		{ImportPath: "example.test/z"},
		{ImportPath: "example.test/a"},
		{ImportPath: "example.test/a"},
	}}
	got := modelPackagePaths(model)
	want := []string{"example.test/a", "example.test/z"}
	if !slices.Equal(got, want) {
		t.Fatalf("modelPackagePaths = %q, want %q", got, want)
	}
	if got := modelPackagePaths(goanalysis.Model{}); len(got) != 0 {
		t.Fatalf("a model of no package named %q, want none", got)
	}
}

func TestAModeIdentityChangesWithEverySettingThatChangesTheRun(t *testing.T) {
	t.Parallel()
	base := Options{}
	for _, test := range []struct {
		name   string
		change func(*Options)
	}{
		{name: "applying the repair", change: func(o *Options) { o.NoApply = true }},
		{name: "narrowing to a changeset", change: func(o *Options) { o.Changed = true }},
		{name: "the reference it compares against", change: func(o *Options) { o.ChangedRef = "main" }},
		{name: "the mutant it replays", change: func(o *Options) { o.ReplayMutantID = "m-1" }},
		{name: "the finding it replays", change: func(o *Options) { o.ReplayFindingID = "f-1" }},
		{name: "the packages it was pointed at", change: func(o *Options) { o.Packages = []string{"./pkg"} }},
		{name: "scoping the verdict to a package", change: func(o *Options) { o.PackageScope = true }},
		{name: "the arguments it gives a test binary", change: func(o *Options) { o.TestArgs = []string{"-short"} }},
		{name: "the tags it builds with", change: func(o *Options) { o.BuildTags = []string{"integration"} }},
		{
			name:   "the operators it mutates with",
			change: func(o *Options) { o.MutationOperators = []string{"comparison"} },
		},
		{name: "how many mutants it runs at once", change: func(o *Options) { o.MutationJobs = pinnedJobs }},
		{name: "how long a command may take", change: func(o *Options) { o.CommandTimeout = pinnedTimeout }},
		{name: "how long a target may take", change: func(o *Options) { o.TargetTimeout = pinnedTimeout }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := base
			test.change(&changed)
			if modeIdentity(changed) == modeIdentity(base) {
				t.Fatalf("changing %s left the identity at %q", test.name, modeIdentity(base))
			}
		})
	}
}

func TestAModeIdentityIgnoresWhatDoesNotChangeTheRun(t *testing.T) {
	t.Parallel()
	base := Options{}
	for _, test := range []struct {
		name   string
		change func(*Options)
	}{
		{name: "where the run keeps its temporaries", change: func(o *Options) { o.TempDirectory = "/tmp/elsewhere" }},
		{name: "keeping the temporaries at all", change: func(o *Options) { o.KeepTemp = true }},
		{name: "the root it was pointed at", change: func(o *Options) { o.Root = "/elsewhere" }},
		{name: "the go binary it runs", change: func(o *Options) { o.GoBinary = "/usr/local/bin/go" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := base
			test.change(&changed)
			if modeIdentity(changed) != modeIdentity(base) {
				t.Fatalf("changing %s moved the identity from %q to %q",
					test.name, modeIdentity(base), modeIdentity(changed))
			}
		})
	}
}

func TestEmitTellsBothTheProgressCallbackAndTheRecording(t *testing.T) {
	t.Parallel()
	var seen []Event
	options := Options{Progress: func(event Event) { seen = append(seen, event) }}
	emit(options, "build-cache-unavailable", "no room")
	if len(seen) != 1 || seen[0].Kind != "build-cache-unavailable" || seen[0].Detail != "no room" {
		t.Fatalf("the progress callback saw %+v, want one build-cache-unavailable event saying why", seen)
	}
	emit(Options{}, "build-cache-unavailable", "no room")
}
