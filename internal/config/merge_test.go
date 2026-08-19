// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// A precedenceCase pins one setting's whole precedence chain: what it is with
// nothing set, what the file makes it, and what a flag makes it on top of that
// file. Every overridable setting has a row, because "defaults, then file,
// then flags" is a promise per field and a rule that holds for sixteen of
// seventeen fields is a bug report waiting to be filed.
type precedenceCase struct {
	name string
	// document is a complete configuration file that sets this one setting.
	document string
	// flags is the overlay a command line would produce for it.
	flags Overlay
	// read pulls the setting out of a resolved configuration.
	read func(Config) any
	// The three expected values, one per layer.
	fromDefault any
	fromFile    any
	fromFlag    any
}

func precedenceCases() []precedenceCase {
	return []precedenceCase{
		{
			name:        "mutation.include",
			document:    "version = 1\n[mutation]\ninclude = [\"lib/**/*.go\"]\n",
			flags:       Overlay{Include: Explicit([]string{"cmd/**/*.go"})},
			read:        func(c Config) any { return c.Mutation.Include },
			fromDefault: []string{"**/*.go"},
			fromFile:    []string{"lib/**/*.go"},
			fromFlag:    []string{"cmd/**/*.go"},
		},
		{
			name:        "mutation.exclude",
			document:    "version = 1\n[mutation]\nexclude = [\"vendor/**\"]\n",
			flags:       Overlay{Exclude: Explicit([]string{"third_party/**"})},
			read:        func(c Config) any { return c.Mutation.Exclude },
			fromDefault: []string(nil),
			fromFile:    []string{"vendor/**"},
			fromFlag:    []string{"third_party/**"},
		},
		{
			name:        "mutation.operators",
			document:    "version = 1\n[mutation]\noperators = [\"comparison\"]\n",
			flags:       Overlay{Operators: Explicit([]string{"bitwise"})},
			read:        func(c Config) any { return c.Mutation.Operators },
			fromDefault: []string(nil),
			fromFile:    []string{"comparison"},
			fromFlag:    []string{"bitwise"},
		},
		{
			name:        "mutation.profile",
			document:    "version = 1\n[mutation]\nprofile = \"strong\"\n",
			flags:       Overlay{Profile: Explicit(mutation.TierAll)},
			read:        func(c Config) any { return c.Mutation.Profile },
			fromDefault: mutation.TierBalanced,
			fromFile:    mutation.TierStrong,
			fromFlag:    mutation.TierAll,
		},
		{
			name:        "mutation.expect",
			document:    "version = 1\n[[mutation.expect]]\nid = \"" + hexID("a") + "\"\nreason = \"equivalent\"\n",
			flags:       Overlay{Expect: Explicit([]Expectation{{ID: hexID("b"), Reason: "also equivalent"}})},
			read:        func(c Config) any { return c.Mutation.Expect },
			fromDefault: []Expectation(nil),
			fromFile:    []Expectation{{ID: hexID("a"), Reason: "equivalent"}},
			fromFlag:    []Expectation{{ID: hexID("b"), Reason: "also equivalent"}},
		},
		{
			name:        "test.command",
			document:    "version = 1\n[test]\ncommand = [\"go\", \"test\", \"./internal/...\"]\n",
			flags:       Overlay{TestCommand: Explicit([]string{"gotestsum", "--"})},
			read:        func(c Config) any { return c.Test.Command },
			fromDefault: []string{"go", "test", "./..."},
			fromFile:    []string{"go", "test", "./internal/..."},
			fromFlag:    []string{"gotestsum", "--"},
		},
		{
			name:        "test.timeout",
			document:    "version = 1\n[test]\ntimeout = \"45s\"\n",
			flags:       Overlay{Timeout: Explicit(2 * time.Minute)},
			read:        func(c Config) any { return c.Test.Timeout },
			fromDefault: time.Duration(0),
			fromFile:    45 * time.Second,
			fromFlag:    2 * time.Minute,
		},
		{
			name:        "test.baseline_runs",
			document:    "version = 1\n[test]\nbaseline_runs = 5\n",
			flags:       Overlay{BaselineRuns: Explicit(1)},
			read:        func(c Config) any { return c.Test.BaselineRuns },
			fromDefault: 3,
			fromFile:    5,
			fromFlag:    1,
		},
		{
			name:        "execution.jobs",
			document:    "version = 1\n[execution]\njobs = 2\n",
			flags:       Overlay{Jobs: Explicit(16)},
			read:        func(c Config) any { return c.Execution.Jobs },
			fromDefault: DefaultJobs(),
			fromFile:    2,
			fromFlag:    16,
		},
		{
			name:        "cache.mode",
			document:    "version = 1\n[cache]\nmode = \"on\"\n",
			flags:       Overlay{CacheMode: Explicit(CacheOff)},
			read:        func(c Config) any { return c.Cache.Mode },
			fromDefault: CacheAuto,
			fromFile:    CacheOn,
			fromFlag:    CacheOff,
		},
		{
			name:        "cache.directory",
			document:    "version = 1\n[cache]\ndirectory = \"team/cache\"\n",
			flags:       Overlay{CacheDirectory: Explicit("local/cache")},
			read:        func(c Config) any { return c.Cache.Directory },
			fromDefault: "",
			fromFile:    "team/cache",
			fromFlag:    "local/cache",
		},
		{
			name:        "policy.strict",
			document:    "version = 1\n[policy]\nstrict = true\n",
			flags:       Overlay{Strict: Explicit(false)},
			read:        func(c Config) any { return c.Policy.Strict },
			fromDefault: false,
			fromFile:    true,
			// --no-strict has to be able to turn off what the file turned on,
			// which is exactly the case a presence-free overlay would lose.
			fromFlag: false,
		},
		{
			name:        "policy.minimum_score",
			document:    "version = 1\n[policy]\nminimum_score = 80\n",
			flags:       Overlay{MinimumScore: Explicit(0.0)},
			read:        func(c Config) any { return c.Policy.MinimumScore },
			fromDefault: 0.0,
			fromFile:    80.0,
			fromFlag:    0.0,
		},
		{
			name:        "policy.require_mutants",
			document:    "version = 1\n[policy]\nrequire_mutants = false\n",
			flags:       Overlay{RequireMutants: Explicit(true)},
			read:        func(c Config) any { return c.Policy.RequireMutants },
			fromDefault: true,
			fromFile:    false,
			fromFlag:    true,
		},
		{
			name:        "report.directory",
			document:    "version = 1\n[report]\ndirectory = \"docs/mutation\"\n",
			flags:       Overlay{ReportDirectory: Explicit("out")},
			read:        func(c Config) any { return c.Report.Directory },
			fromDefault: "reports/mutation",
			fromFile:    "docs/mutation",
			fromFlag:    "out",
		},
		{
			name:        "report.formats",
			document:    "version = 1\n[report]\nformats = [\"json\"]\n",
			flags:       Overlay{ReportFormats: Explicit([]ReportFormat{})},
			read:        func(c Config) any { return c.Report.Formats },
			fromDefault: []ReportFormat{FormatJSON, FormatHTML},
			fromFile:    []ReportFormat{FormatJSON},
			// An explicitly empty list is a choice, not an absence: `--report
			// none` turns project reports off and must beat both layers below.
			fromFlag: []ReportFormat{},
		},
		{
			name:        "report.high",
			document:    "version = 1\n[report]\nhigh = 90\n",
			flags:       Overlay{ReportHigh: Explicit(70)},
			read:        func(c Config) any { return c.Report.High },
			fromDefault: 80,
			fromFile:    90,
			fromFlag:    70,
		},
		{
			name:        "report.low",
			document:    "version = 1\n[report]\nlow = 50\n",
			flags:       Overlay{ReportLow: Explicit(0)},
			read:        func(c Config) any { return c.Report.Low },
			fromDefault: 60,
			fromFile:    50,
			// Zero is a legitimate low threshold and has to survive the merge.
			fromFlag: 0,
		},
	}
}

func TestPrecedence(t *testing.T) {
	for _, test := range precedenceCases() {
		t.Run(test.name, func(t *testing.T) {
			file, err := Parse(FileName, []byte(test.document))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			absent := FileConfig{Path: FileName}

			t.Run("default", func(t *testing.T) {
				got := test.read(Merge(Defaults(), absent, Overlay{}))
				if diff := cmp.Diff(test.fromDefault, got); diff != "" {
					t.Errorf("(-want +got):\n%s", diff)
				}
			})
			t.Run("file over default", func(t *testing.T) {
				got := test.read(Merge(Defaults(), file, Overlay{}))
				if diff := cmp.Diff(test.fromFile, got); diff != "" {
					t.Errorf("(-want +got):\n%s", diff)
				}
			})
			t.Run("flag over file", func(t *testing.T) {
				got := test.read(Merge(Defaults(), file, test.flags))
				if diff := cmp.Diff(test.fromFlag, got); diff != "" {
					t.Errorf("(-want +got):\n%s", diff)
				}
			})
			t.Run("flag over default", func(t *testing.T) {
				got := test.read(Merge(Defaults(), absent, test.flags))
				if diff := cmp.Diff(test.fromFlag, got); diff != "" {
					t.Errorf("(-want +got):\n%s", diff)
				}
			})
			// A layer that set nothing must not disturb the layer below it,
			// which is the failure mode a plain-struct overlay has.
			t.Run("empty flags keep the file", func(t *testing.T) {
				got := test.read(Merge(Defaults(), file, Overlay{}))
				if diff := cmp.Diff(test.fromFile, got); diff != "" {
					t.Errorf("(-want +got):\n%s", diff)
				}
			})
		})
	}
}

// The matrix has to cover every field the overlay can carry. Version is the
// exception: it identifies the schema rather than configuring a run, and no
// flag sets it.
func TestPrecedenceCoversEveryOverridableSetting(t *testing.T) {
	covered := make(map[string]bool, len(precedenceCases()))
	for _, test := range precedenceCases() {
		covered[test.name] = true
	}
	want := []string{
		"mutation.include", "mutation.exclude", "mutation.operators", "mutation.profile", "mutation.expect",
		"test.command", "test.timeout", "test.baseline_runs",
		"execution.jobs",
		"cache.mode", "cache.directory",
		"policy.strict", "policy.minimum_score", "policy.require_mutants",
		"report.directory", "report.formats", "report.high", "report.low",
	}
	for _, name := range want {
		if !covered[name] {
			t.Errorf("no precedence row for %s", name)
		}
	}
	if len(covered) != len(want) {
		t.Errorf("the matrix has %d rows for %d settings", len(covered), len(want))
	}
}

// Arrays replace rather than accumulate. Appending would make it impossible to
// narrow a run from the command line, which is the direction people actually
// need.
func TestArraysReplaceRatherThanAppend(t *testing.T) {
	file, err := Parse(FileName, []byte("version = 1\n[mutation]\ninclude = [\"a/**\", \"b/**\"]\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := Merge(Defaults(), file, Overlay{Include: Explicit([]string{"c/**"})})
	if diff := cmp.Diff([]string{"c/**"}, got.Mutation.Include); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// Merge copies on the way out: editing the result must not reach back into the
// overlay, and editing the overlay afterwards must not reach the result.
func TestMergeSharesNoSlices(t *testing.T) {
	patterns := []string{"a/**"}
	flags := Overlay{Include: Explicit(patterns)}
	resolved := Merge(Defaults(), FileConfig{}, flags)

	resolved.Mutation.Include[0] = "tampered"
	if patterns[0] != "a/**" {
		t.Errorf("the merged config aliased the overlay's slice")
	}
	patterns[0] = "changed"
	if resolved.Mutation.Include[0] != "tampered" {
		t.Errorf("the overlay's slice reached back into the merged config")
	}
}

func TestMergeOverlaysAppliesInOrder(t *testing.T) {
	got := MergeOverlays(Defaults(),
		Overlay{Jobs: Explicit(2), Strict: Explicit(true)},
		Overlay{Jobs: Explicit(4)},
	)
	if got.Execution.Jobs != 4 {
		t.Errorf("jobs = %d, want 4", got.Execution.Jobs)
	}
	if !got.Policy.Strict {
		t.Errorf("a later layer that set nothing cleared strict")
	}
}
