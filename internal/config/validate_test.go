// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/mutation"
)

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
		{"memory", Overlay{Memory: Explicit(int64(-1))}, CodeNonPositiveMemory, "--memory"},
		{"include", Overlay{Include: Explicit([]string{"bad/"})}, CodeInvalidGlob, "--include"},
		{"exclude", Overlay{Exclude: Explicit([]string{""})}, CodeInvalidGlob, "--exclude"},
		{"operator", Overlay{Operators: Explicit([]string{"nonsense"})}, CodeUnknownOperator, "--operator"},
		{"report format", Overlay{ReportFormats: Explicit([]ReportFormat{"xml"})}, CodeUnknownReportFormat, "--report"},
		{"test command", Overlay{TestCommand: Explicit([]string{})}, CodeEmptyTestCommand, "-- <test argv>"},
		{"baseline runs", Overlay{BaselineRuns: Explicit(0)}, CodeBaselineRunsOutOfRange, "test.baseline_runs"},
		{"narrowing", Overlay{Narrowing: Explicit(Narrowing("binary"))}, CodeUnknownNarrowing, "test.narrowing"},
		{"probing", Overlay{Probing: Explicit(Probing("maybe"))}, CodeUnknownProbing, "test.probing"},
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
		Memory:          Explicit(int64(1)),
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

func TestDiagnosticsSpellOutTheVocabularyTheyOffer(t *testing.T) {
	for _, test := range []struct {
		name    string
		overlay Overlay
		code    Code
		want    string
	}{
		{
			name:    "an unknown profile lists the tiers",
			overlay: Overlay{Profile: Explicit(mutation.Tier(9))},
			code:    CodeUnknownProfile,
			want:    `unknown profile "tier(9)": expected "balanced", "strong", "all"`,
		},
		{
			name:    "an unknown cache mode lists the modes",
			overlay: Overlay{CacheMode: Explicit(CacheMode("maybe"))},
			code:    CodeUnknownCacheMode,
			want:    `unknown cache mode "maybe": expected "auto", "on", "off"`,
		},
		{
			name:    "an unknown narrowing lists the narrowings",
			overlay: Overlay{Narrowing: Explicit(Narrowing("binary"))},
			code:    CodeUnknownNarrowing,
			want:    `unknown narrowing "binary": expected "test", "package"`,
		},
		{
			name:    "an unknown probing mode lists the modes",
			overlay: Overlay{Probing: Explicit(Probing("maybe"))},
			code:    CodeUnknownProbing,
			want:    `unknown probing mode "maybe": expected "off", "on"`,
		},
		{
			name:    "an unknown report format lists the formats",
			overlay: Overlay{ReportFormats: Explicit([]ReportFormat{"xml"})},
			code:    CodeUnknownReportFormat,
			want:    `unknown report format "xml": expected "json", "html"`,
		},
		{
			name:    "an absolute cache directory ends with the rule",
			overlay: Overlay{CacheDirectory: Explicit("/abs")},
			code:    CodeInvalidCacheDirectory,
			want: `"/abs" is not usable as a cache directory: ` +
				"give a relative path that stays inside the tree it is resolved against",
		},
		{
			name:    "an escaping report directory ends with the same rule",
			overlay: Overlay{ReportDirectory: Explicit("../out")},
			code:    CodeInvalidReportDirectory,
			want: `"../out" is not usable as a report directory: ` +
				"give a relative path that stays inside the tree it is resolved against",
		},
		{
			name:    "a score floor is printed the way the exit policy prints it",
			overlay: Overlay{MinimumScore: Explicit(101.0)},
			code:    CodeMinimumScoreOutOfRange,
			want:    "a minimum score of 101 is outside 0..100",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.overlay.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", test.overlay)
			}
			got := only(t, err)
			if got.Code != test.code {
				t.Errorf("code = %s, want %s", got.Code, test.code)
			}
			if got.Message != test.want {
				t.Errorf("message = %q, want %q", got.Message, test.want)
			}
		})
	}
}

func TestUnknownOperatorNamesEveryFamilyInTheCatalogue(t *testing.T) {
	err := (Overlay{Operators: Explicit([]string{"telepathy"})}).Validate()
	if err == nil {
		t.Fatalf("Validate accepted an operator that is neither a family nor a rule")
	}
	got := only(t, err)
	if got.Code != CodeUnknownOperator {
		t.Fatalf("code = %s, want %s", got.Code, CodeUnknownOperator)
	}
	if !strings.HasPrefix(got.Message, `unknown operator "telepathy": `) {
		t.Errorf("the message does not quote what was written: %q", got.Message)
	}

	registry := mutation.CanonicalRegistry()
	families := registry.Families()
	if len(families) == 0 {
		t.Fatalf("the catalogue reports no families at all")
	}
	for _, family := range families {
		if !strings.Contains(got.Message, string(family)) {
			t.Errorf("the message does not offer the %q family: %q", string(family), got.Message)
		}
	}
	if want := fmt.Sprintf("%d families", len(families)); !strings.Contains(got.Message, want) {
		t.Errorf("the message does not say %q: %q", want, got.Message)
	}
	if want := fmt.Sprintf("%d rule names", registry.Len()); !strings.Contains(got.Message, want) {
		t.Errorf("the message does not say %q: %q", want, got.Message)
	}
}

func TestInvalidPatternQuotesTheMatcherWithoutItsWrapper(t *testing.T) {
	err := (Overlay{Include: Explicit([]string{"a//b"})}).Validate()
	if err == nil {
		t.Fatalf("Validate accepted a pattern that does not compile")
	}
	got := only(t, err)
	if got.Code != CodeInvalidGlob {
		t.Fatalf("code = %s, want %s", got.Code, CodeInvalidGlob)
	}
	want := `invalid pattern "a//b": empty path element between two '/' (at character 3)`
	if got.Message != want {
		t.Errorf("message = %q, want %q", got.Message, want)
	}

	var syntax *glob.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("errors.As did not reach the *glob.SyntaxError: %v", err)
	}
	if strings.Contains(got.Message, syntax.Error()) {
		t.Errorf("the message repeats the wrapper it was supposed to unwrap: %q", got.Message)
	}
}

func TestMinimumScoreRejectsNaN(t *testing.T) {
	err := (Overlay{MinimumScore: Explicit(math.NaN())}).Validate()
	if err == nil {
		t.Fatalf("Validate accepted NaN as a minimum score")
	}
	if got := only(t, err); got.Code != CodeMinimumScoreOutOfRange {
		t.Errorf("code = %s, want %s", got.Code, CodeMinimumScoreOutOfRange)
	}
}

func TestThresholdsAreCheckedAfterMerging(t *testing.T) {
	file, err := Parse(FileName, []byte("version = 1\n[report]\nlow = 70\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
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

	equal := Merge(Defaults(), FileConfig{}, Overlay{ReportHigh: Explicit(60), ReportLow: Explicit(60)})
	if err := equal.Validate(); err != nil {
		t.Errorf("equal thresholds were rejected: %v", err)
	}
}

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
	both, err := Parse(FileName, []byte("version = 1\n[report]\nhigh = 0\nlow = 0\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Merge(Defaults(), both, Overlay{}).Validate(); err != nil {
		t.Errorf("a file that sets both thresholds was still refused: %v", err)
	}
}

func TestInvertedThresholdsAreNotReportedTwice(t *testing.T) {
	resolved := Merge(Defaults(), FileConfig{}, Overlay{ReportHigh: Explicit(-5)})
	got := codesOf(problems(t, resolved.Validate()))
	if diff := cmp.Diff([]Code{CodeThresholdOutOfRange}, got); diff != "" {
		t.Errorf("codes (-want +got):\n%s", diff)
	}
}

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
		{"memory", func(c *Config) { c.Test.Memory = -1 }, CodeNonPositiveMemory, "test.memory"},
		{"baseline runs", func(c *Config) { c.Test.BaselineRuns = 42 }, CodeBaselineRunsOutOfRange, "test.baseline_runs"},
		{"narrowing", func(c *Config) { c.Test.Narrowing = "" }, CodeUnknownNarrowing, "test.narrowing"},
		{"probing", func(c *Config) { c.Test.Probing = "" }, CodeUnknownProbing, "test.probing"},
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
			if got.File != "" || got.Position.Known() {
				t.Errorf("a merged error claimed a file position: %s", got)
			}
		})
	}
}

func TestZeroMeansUnsetForDerivedSettings(t *testing.T) {
	resolved := Defaults()
	resolved.Test.Timeout = 0
	resolved.Test.Memory = 0
	resolved.Cache.Directory = ""
	if err := resolved.Validate(); err != nil {
		t.Errorf("a derived budget or a default cache directory was rejected: %v", err)
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

	if _, flagErr := Load(path, Overlay{Jobs: Explicit(0)}); flagErr == nil {
		t.Errorf("Load accepted --jobs 0")
	} else if got := only(t, flagErr); got.Key != "--jobs" {
		t.Errorf("key = %q, want --jobs", got.Key)
	}

	resolved, err = Load(filepath.Join(dir, "absent.toml"), Overlay{})
	if err != nil {
		t.Fatalf("Load of an absent file: %v", err)
	}
	if diff := cmp.Diff(Defaults(), resolved); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestLoadStopsAtEveryStepThatFails(t *testing.T) {
	dir := t.TempDir()

	unknown := filepath.Join(dir, "unknown.toml")
	if err := os.WriteFile(unknown, []byte("version = 1\nflavour = \"vanilla\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	resolved, err := Load(unknown, Overlay{})
	if err == nil {
		t.Fatalf("Load accepted a file with an unknown key")
	}
	if got := only(t, err); got.Code != CodeUnknownKey {
		t.Errorf("code = %s, want %s (%v)", got.Code, CodeUnknownKey, err)
	}
	if diff := cmp.Diff(Config{}, resolved); diff != "" {
		t.Errorf("a failed Load handed back a configuration (-want +got):\n%s", diff)
	}

	inverted := filepath.Join(dir, "inverted.toml")
	if writeErr := os.WriteFile(inverted, []byte("version = 1\n[report]\nlow = 90\n"), 0o600); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	if file, fileErr := LoadFile(inverted); fileErr != nil {
		t.Fatalf("the file is not the problem: %v", fileErr)
	} else if !file.Present {
		t.Fatalf("Present = false for a file that was written")
	}
	resolved, err = Load(inverted, Overlay{})
	if err == nil {
		t.Fatalf("Load accepted a low threshold above the default high")
	}
	if got := only(t, err); got.Code != CodeThresholdsInverted {
		t.Errorf("code = %s, want %s (%v)", got.Code, CodeThresholdsInverted, err)
	}
	if diff := cmp.Diff(Config{}, resolved); diff != "" {
		t.Errorf("a failed Load handed back a configuration (-want +got):\n%s", diff)
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
