// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// A flag overlay is checked with the same rules as a file, but a message that
// told someone to fix `execution.jobs` when they typed `--jobs` would send
// them to edit a file they never touched.
func TestOverlayValidateNamesFlags(t *testing.T) {
	tests := []struct {
		name    string
		overlay Overlay
		code    Code
		key     string
	}{
		{"jobs", Overlay{Jobs: Explicit(64)}, CodeJobsOutOfRange, "--jobs"},
		{"profile", Overlay{Profile: Explicit(mutation.Tier(9))}, CodeUnknownProfile, "--profile"},
		{"cache mode", Overlay{CacheMode: Explicit(CacheMode("maybe"))}, CodeUnknownCacheMode, "--cache"},
		{"timeout", Overlay{Timeout: Explicit(-time.Second)}, CodeNonPositiveTimeout, "--timeout"},
		{"include", Overlay{Include: Explicit([]string{"bad/"})}, CodeInvalidGlob, "--include"},
		{"exclude", Overlay{Exclude: Explicit([]string{""})}, CodeInvalidGlob, "--exclude"},
		{"operator", Overlay{Operators: Explicit([]string{"nonsense"})}, CodeUnknownOperator, "--operator"},
		{"report format", Overlay{ReportFormats: Explicit([]ReportFormat{"xml"})}, CodeUnknownReportFormat, "--report"},
		{"test command", Overlay{TestCommand: Explicit([]string{})}, CodeEmptyTestCommand, "-- <test argv>"},
		// A setting with no flag falls back to its TOML key, which is the
		// truthful answer: there is nowhere else to change it.
		{"baseline runs", Overlay{BaselineRuns: Explicit(0)}, CodeBaselineRunsOutOfRange, "test.baseline_runs"},
		{"minimum score", Overlay{MinimumScore: Explicit(101.0)}, CodeMinimumScoreOutOfRange, "policy.minimum_score"},
		{"report high", Overlay{ReportHigh: Explicit(-1)}, CodeThresholdOutOfRange, "report.high"},
		{"report directory", Overlay{ReportDirectory: Explicit("/tmp/out")}, CodeInvalidReportDirectory, "report.directory"},
		{"cache directory", Overlay{CacheDirectory: Explicit("../out")}, CodeInvalidCacheDirectory, "cache.directory"},
		{"expectation", Overlay{Expect: Explicit([]Expectation{{ID: "short", Reason: "r"}})}, CodeInvalidExpectationID, "mutation.expect[0].id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.overlay.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", test.overlay)
			}
			got := only(t, err)
			if got.Code != test.code {
				t.Errorf("code = %s, want %s", got.Code, test.code)
			}
			if got.Key != test.key {
				t.Errorf("key = %q, want %q", got.Key, test.key)
			}
			if got.File != "" || got.Position.Known() {
				t.Errorf("a flag error claimed a file position: %s", got)
			}
		})
	}
}

func TestOverlayValidateAcceptsTheEdges(t *testing.T) {
	overlay := Overlay{
		Version:         Explicit(1),
		Include:         Explicit([]string{}),
		Exclude:         Explicit([]string{"**"}),
		Operators:       Explicit([]string{"boolean-literal", "delete-call-statement"}),
		Profile:         Explicit(mutation.TierAll),
		Expect:          Explicit([]Expectation{}),
		TestCommand:     Explicit([]string{"go", ""}),
		Timeout:         Explicit(time.Nanosecond),
		BaselineRuns:    Explicit(MaxBaselineRuns),
		Jobs:            Explicit(MaxJobs),
		CacheMode:       Explicit(CacheOff),
		CacheDirectory:  Explicit("a"),
		Strict:          Explicit(true),
		MinimumScore:    Explicit(100.0),
		RequireMutants:  Explicit(false),
		ReportDirectory: Explicit("a/b/c"),
		ReportFormats:   Explicit([]ReportFormat{}),
		ReportHigh:      Explicit(0),
		ReportLow:       Explicit(0),
	}
	if err := overlay.Validate(); err != nil {
		t.Errorf("Validate rejected the edges of every range: %v", err)
	}
	if err := (Overlay{BaselineRuns: Explicit(MinBaselineRuns), Jobs: Explicit(MinJobs)}).Validate(); err != nil {
		t.Errorf("Validate rejected the bottom of every range: %v", err)
	}
}

// NaN is not a percentage. It is worth a row because it is the one value that
// slips through a naive `v > 100` check.
func TestMinimumScoreRejectsNaN(t *testing.T) {
	err := (Overlay{MinimumScore: Explicit(math.NaN())}).Validate()
	if err == nil {
		t.Fatalf("Validate accepted NaN as a minimum score")
	}
	if got := only(t, err); got.Code != CodeMinimumScoreOutOfRange {
		t.Errorf("code = %s, want %s", got.Code, CodeMinimumScoreOutOfRange)
	}
}

// Cross-field rules cannot be checked in either layer, because the two halves
// can arrive from different ones. This is the whole reason Validate runs after
// the merge as well as inside it.
func TestThresholdsAreCheckedAfterMerging(t *testing.T) {
	file, err := Parse(FileName, []byte("version = 1\n[report]\nlow = 70\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The file's low is fine on its own, and so is the flag's high; only the
	// merged pair is wrong.
	resolved := Merge(Defaults(), file, Overlay{ReportHigh: Explicit(50)})
	got := only(t, resolved.Validate())
	if got.Code != CodeThresholdsInverted {
		t.Errorf("code = %s, want %s", got.Code, CodeThresholdsInverted)
	}
	if got.Key != "report.low" {
		t.Errorf("key = %q, want report.low", got.Key)
	}
	if !strings.Contains(got.Message, "70") || !strings.Contains(got.Message, "50") {
		t.Errorf("message %q does not name both thresholds", got.Message)
	}

	// Equal thresholds are legal: they colour everything below the line red
	// and everything on it green, which is a coherent thing to ask for.
	equal := Merge(Defaults(), FileConfig{}, Overlay{ReportHigh: Explicit(60), ReportLow: Explicit(60)})
	if err := equal.Validate(); err != nil {
		t.Errorf("equal thresholds were rejected: %v", err)
	}
}

// A layer can be right on its own and still contradict a default, and saying
// so is the point of checking after the merge. `high = 0` is a legal threshold
// in isolation; against the shipped `low = 60` it describes a range that runs
// backwards, and the message names both halves so the half nobody wrote is
// visible too.
func TestAThresholdCanContradictADefault(t *testing.T) {
	file, err := Parse(FileName, []byte("version = 1\n[report]\nhigh = 0\n"))
	if err != nil {
		t.Fatalf("Parse rejected a file that is fine on its own: %v", err)
	}
	got := only(t, Merge(Defaults(), file, Overlay{}).Validate())
	if got.Code != CodeThresholdsInverted {
		t.Errorf("code = %s, want %s", got.Code, CodeThresholdsInverted)
	}
	for _, want := range []string{"report.low", "report.high", "60", "0"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("%q does not mention %q", got.Error(), want)
		}
	}
	// Setting the other half too resolves it, which is the fix the message
	// points at.
	both, err := Parse(FileName, []byte("version = 1\n[report]\nhigh = 0\nlow = 0\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Merge(Defaults(), both, Overlay{}).Validate(); err != nil {
		t.Errorf("a file that sets both thresholds was still refused: %v", err)
	}
}

// An out-of-range threshold gets the one error that explains it, not a second,
// derived complaint about an ordering that was never the real problem.
func TestInvertedThresholdsAreNotReportedTwice(t *testing.T) {
	resolved := Merge(Defaults(), FileConfig{}, Overlay{ReportHigh: Explicit(-5)})
	got := codesOf(problems(t, resolved.Validate()))
	if diff := cmp.Diff([]Code{CodeThresholdOutOfRange}, got); diff != "" {
		t.Errorf("codes (-want +got):\n%s", diff)
	}
}

// Config.Validate is the gate, so it has to re-check the per-value rules and
// not merely the cross-field one: a Config can be built by hand.
func TestConfigValidateChecksValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		code   Code
		key    string
	}{
		{"version", func(c *Config) { c.Version = 2 }, CodeUnsupportedVersion, "version"},
		{"include", func(c *Config) { c.Mutation.Include = []string{"/abs/**"} }, CodeInvalidGlob, "mutation.include[0]"},
		{"profile", func(c *Config) { c.Mutation.Profile = mutation.Tier(7) }, CodeUnknownProfile, "mutation.profile"},
		{"operators", func(c *Config) { c.Mutation.Operators = []string{"ghosts"} }, CodeUnknownOperator, "mutation.operators[0]"},
		{"expect id", func(c *Config) {
			c.Mutation.Expect = []Expectation{{ID: strings.Repeat("A", 64), Reason: "r"}}
		}, CodeInvalidExpectationID, "mutation.expect[0].id"},
		{"expect reason", func(c *Config) {
			c.Mutation.Expect = []Expectation{{ID: hexID("a"), Reason: ""}}
		}, CodeEmptyExpectationReason, "mutation.expect[0].reason"},
		{"command", func(c *Config) { c.Test.Command = nil }, CodeEmptyTestCommand, "test.command"},
		{"timeout", func(c *Config) { c.Test.Timeout = -1 }, CodeNonPositiveTimeout, "test.timeout"},
		{"baseline runs", func(c *Config) { c.Test.BaselineRuns = 42 }, CodeBaselineRunsOutOfRange, "test.baseline_runs"},
		{"jobs", func(c *Config) { c.Execution.Jobs = 0 }, CodeJobsOutOfRange, "execution.jobs"},
		{"cache mode", func(c *Config) { c.Cache.Mode = "" }, CodeUnknownCacheMode, "cache.mode"},
		{"cache directory", func(c *Config) { c.Cache.Directory = "/abs" }, CodeInvalidCacheDirectory, "cache.directory"},
		{"minimum score", func(c *Config) { c.Policy.MinimumScore = 100.5 }, CodeMinimumScoreOutOfRange, "policy.minimum_score"},
		{"report directory", func(c *Config) { c.Report.Directory = "../out" }, CodeInvalidReportDirectory, "report.directory"},
		{"report formats", func(c *Config) { c.Report.Formats = []ReportFormat{FormatJSON, FormatJSON} }, CodeDuplicateReportFormat, "report.formats[1]"},
		{"report low", func(c *Config) { c.Report.Low = 500 }, CodeThresholdOutOfRange, "report.low"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved := Defaults()
			test.mutate(&resolved)
			err := resolved.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", resolved)
			}
			got := only(t, err)
			if got.Code != test.code {
				t.Errorf("code = %s, want %s (%v)", got.Code, test.code, err)
			}
			if got.Key != test.key {
				t.Errorf("key = %q, want %q", got.Key, test.key)
			}
			// A merged value no longer has one place in a file to point at,
			// and claiming one would be a lie.
			if got.File != "" || got.Position.Known() {
				t.Errorf("a merged error claimed a file position: %s", got)
			}
		})
	}
}

// The two settings whose zero value means "unset" must not be validated as if
// somebody had asked for a zero.
func TestZeroMeansUnsetForDerivedSettings(t *testing.T) {
	resolved := Defaults()
	resolved.Test.Timeout = 0
	resolved.Cache.Directory = ""
	if err := resolved.Validate(); err != nil {
		t.Errorf("a derived timeout or a default cache directory was rejected: %v", err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("version = 1\n[execution]\njobs = 3\n[report]\nhigh = 90\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	resolved, err := Load(path, Overlay{Jobs: Explicit(5)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if resolved.Execution.Jobs != 5 {
		t.Errorf("jobs = %d, want 5 (the flag)", resolved.Execution.Jobs)
	}
	if resolved.Report.High != 90 {
		t.Errorf("high = %d, want 90 (the file)", resolved.Report.High)
	}
	if resolved.Test.BaselineRuns != DefaultBaselineRuns {
		t.Errorf("baseline_runs = %d, want the default", resolved.Test.BaselineRuns)
	}

	// A bad flag is refused before the file's own problems are considered, so
	// the message is about what the user just typed.
	if _, flagErr := Load(path, Overlay{Jobs: Explicit(0)}); flagErr == nil {
		t.Errorf("Load accepted --jobs 0")
	} else if got := only(t, flagErr); got.Key != "--jobs" {
		t.Errorf("key = %q, want --jobs", got.Key)
	}

	// Load of a path nobody configured is the defaults, not a failure.
	resolved, err = Load(filepath.Join(dir, "absent.toml"), Overlay{})
	if err != nil {
		t.Fatalf("Load of an absent file: %v", err)
	}
	if diff := cmp.Diff(Defaults(), resolved); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestParseHelpers(t *testing.T) {
	if got, err := ParseProfile("strong"); err != nil || got != mutation.TierStrong {
		t.Errorf("ParseProfile(strong) = %v, %v", got, err)
	}
	if _, err := ParseProfile("reckless"); err == nil {
		t.Errorf("ParseProfile accepted an unknown profile")
	} else if got := only(t, err); got.Code != CodeUnknownProfile || got.Key != "--profile" {
		t.Errorf("got %s", got)
	}

	if got, err := ParseCacheMode("off"); err != nil || got != CacheOff {
		t.Errorf("ParseCacheMode(off) = %v, %v", got, err)
	}
	if _, err := ParseCacheMode("perhaps"); err == nil {
		t.Errorf("ParseCacheMode accepted an unknown mode")
	}

	if got, err := ParseTimeout("90s"); err != nil || got != 90*time.Second {
		t.Errorf("ParseTimeout(90s) = %v, %v", got, err)
	}
	if _, err := ParseTimeout("soon"); err == nil {
		t.Errorf("ParseTimeout accepted a non-duration")
	} else if got := only(t, err); got.Code != CodeInvalidDuration {
		t.Errorf("code = %s", got.Code)
	}
	if _, err := ParseTimeout("0"); err == nil {
		t.Errorf("ParseTimeout accepted zero")
	} else if got := only(t, err); got.Code != CodeNonPositiveTimeout {
		t.Errorf("code = %s", got.Code)
	}

	formats, err := ParseReportFormats("json,html")
	if err != nil {
		t.Fatalf("ParseReportFormats: %v", err)
	}
	if diff := cmp.Diff([]ReportFormat{FormatJSON, FormatHTML}, formats); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	// "none" has to produce an empty-but-present slice: an absence would let
	// the default reports come back.
	none, err := ParseReportFormats("none")
	if err != nil {
		t.Fatalf("ParseReportFormats(none): %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("ParseReportFormats(none) = %#v, want an empty non-nil slice", none)
	}
	resolved := Merge(Defaults(), FileConfig{}, Overlay{ReportFormats: Explicit(none)})
	if len(resolved.Report.Formats) != 0 {
		t.Errorf("--report none did not turn reports off: %v", resolved.Report.Formats)
	}
	if _, err := ParseReportFormats("json,pdf"); err == nil {
		t.Errorf("ParseReportFormats accepted an unknown format")
	}
}

// Both spellings of an operator resolve, and the error for neither names the
// families so the reader can pick one.
func TestOperatorsAcceptFamiliesAndRules(t *testing.T) {
	registry := mutation.CanonicalRegistry()
	names := []string{}
	for _, family := range registry.Families() {
		names = append(names, string(family))
	}
	for _, rule := range registry.Rules() {
		names = append(names, rule.Name)
	}
	if err := (Overlay{Operators: Explicit(names)}).Validate(); err != nil {
		t.Errorf("Validate rejected a name from the catalogue: %v", err)
	}

	err := (Overlay{Operators: Explicit([]string{"comparisons"})}).Validate()
	got := only(t, err)
	if !strings.Contains(got.Message, "comparison") {
		t.Errorf("message %q does not list the families", got.Message)
	}
}

// The expectations ledger takes full ids only, whatever `--mutant` accepts.
func TestExpectationIDsAreFullIDs(t *testing.T) {
	for _, id := range []string{
		strings.Repeat("a", mutation.DisplayIDLength),
		strings.Repeat("a", mutation.IDHexLength-1),
		strings.Repeat("a", mutation.IDHexLength+1),
		strings.Repeat("A", mutation.IDHexLength),
		strings.Repeat("g", mutation.IDHexLength),
	} {
		err := (Overlay{Expect: Explicit([]Expectation{{ID: id, Reason: "r"}})}).Validate()
		if err == nil {
			t.Errorf("Validate accepted %q as a mutant id", id)
			continue
		}
		if got := only(t, err); got.Code != CodeInvalidExpectationID {
			t.Errorf("code = %s for %q", got.Code, id)
		}
	}
	if err := (Overlay{Expect: Explicit([]Expectation{{ID: hexID("0123456789abcdef"), Reason: "r"}})}).Validate(); err != nil {
		t.Errorf("Validate rejected a well-formed id: %v", err)
	}
}

// errors.As has to reach a *config.Error through everything this package
// returns, because the CLI maps a code to an exit status.
func TestEveryErrorCarriesACode(t *testing.T) {
	err := (Overlay{Jobs: Explicit(0), ReportHigh: Explicit(200)}).Validate()
	for _, problem := range problems(t, err) {
		if problem.Code == "" {
			t.Errorf("a problem carries no code: %v", problem)
		}
		if !errors.Is(err, error(problem)) {
			t.Errorf("errors.Is cannot reach %v", problem)
		}
	}
}
