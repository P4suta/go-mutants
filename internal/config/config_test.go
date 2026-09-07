// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// problems flattens whatever this package returned into the individual
// diagnostics it carries. Every error path either is an *Error or joins
// *Errors, so a test that finds nothing here has found a violation of that
// contract rather than a missing case.
func problems(t *testing.T, err error) []*Error {
	t.Helper()
	if err == nil {
		return nil
	}
	var multi *multiError
	if errors.As(err, &multi) {
		out := make([]*Error, 0, len(multi.Unwrap()))
		for _, one := range multi.Unwrap() {
			var problem *Error
			if !errors.As(one, &problem) {
				t.Fatalf("joined error %v is not a *config.Error", one)
			}
			out = append(out, problem)
		}
		return out
	}
	var problem *Error
	if !errors.As(err, &problem) {
		t.Fatalf("error %v is not a *config.Error", err)
	}
	return []*Error{problem}
}

// only asserts that err carries exactly one diagnostic and returns it.
func only(t *testing.T, err error) *Error {
	t.Helper()
	got := problems(t, err)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 problem, got %d: %v", len(got), err)
	}
	return got[0]
}

// codesOf lists the codes of every diagnostic, in report order.
func codesOf(problems []*Error) []Code {
	out := make([]Code, 0, len(problems))
	for _, problem := range problems {
		out = append(out, problem.Code)
	}
	return out
}

func TestDefaults(t *testing.T) {
	got := Defaults()

	want := Config{
		Version: 1,
		Mutation: Mutation{
			Include:   []string{"**/*.go"},
			Exclude:   nil,
			Operators: nil,
			Profile:   mutation.TierBalanced,
			Expect:    nil,
		},
		Test: Test{
			Command:      []string{"go", "test", "./..."},
			Timeout:      0,
			BaselineRuns: 3,
			Narrowing:    NarrowingTest,
		},
		// The default worker count is a property of the machine, so it is
		// derived here the way the documentation states it rather than pinned
		// to whatever this test host happens to have.
		Execution: Execution{Jobs: min(runtime.NumCPU(), 8)},
		Cache:     Cache{Mode: CacheAuto, Directory: ""},
		Policy:    mutation.Policy{Strict: false, MinimumScore: 0, RequireMutants: true},
		Report: Report{
			Directory: "reports/mutation",
			Formats:   []ReportFormat{FormatJSON, FormatHTML},
			High:      80,
			Low:       60,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Defaults() mismatch (-want +got):\n%s", diff)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the built-in defaults do not validate: %v", err)
	}
}

// The defaults are handed out, not shared: a caller that edits what it got
// must not be able to change what the next caller gets.
func TestDefaultsAreFresh(t *testing.T) {
	first := Defaults()
	first.Mutation.Include[0] = "tampered"
	first.Report.Formats = append(first.Report.Formats[:0], FormatJSON)

	second := Defaults()
	if got := second.Mutation.Include[0]; got != "**/*.go" {
		t.Errorf("include[0] = %q after a caller edited an earlier Defaults()", got)
	}
	if got := len(second.Report.Formats); got != 2 {
		t.Errorf("len(formats) = %d after a caller edited an earlier Defaults()", got)
	}
}

func TestCloneSharesNothing(t *testing.T) {
	original := Defaults()
	original.Mutation.Exclude = []string{"vendor/**"}
	original.Mutation.Operators = []string{"comparison"}
	original.Mutation.Expect = []Expectation{{ID: strings.Repeat("a", 64), Reason: "why"}}

	clone := original.Clone()
	clone.Mutation.Include[0] = "x"
	clone.Mutation.Exclude[0] = "x"
	clone.Mutation.Operators[0] = "x"
	clone.Mutation.Expect[0].Reason = "x"
	clone.Test.Command[0] = "x"
	clone.Report.Formats[0] = "x"

	if original.Mutation.Include[0] != "**/*.go" ||
		original.Mutation.Exclude[0] != "vendor/**" ||
		original.Mutation.Operators[0] != "comparison" ||
		original.Mutation.Expect[0].Reason != "why" ||
		original.Test.Command[0] != "go" ||
		original.Report.Formats[0] != FormatJSON {
		t.Errorf("Clone() aliased a slice: %+v", original)
	}
}

// Codes are a user-facing contract: they have to be unique, and they have to
// stay inside the block this package owns, or two packages will eventually
// print the same code for different things.
func TestCodesAreUniqueAndOwned(t *testing.T) {
	seen := make(map[Code]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			t.Errorf("code %s is listed twice", code)
		}
		seen[code] = true
		if !strings.HasPrefix(string(code), "GOM30") || len(code) != 7 {
			t.Errorf("code %s is outside the GOM30xx block this package owns", code)
		}
	}
	if !slices.IsSortedFunc(Codes(), func(a, b Code) int { return strings.Compare(string(a), string(b)) }) {
		t.Errorf("Codes() is not in numeric order: %v", Codes())
	}
}

// Codes() is the list `doctor` prints so that a code seen in a log can be
// looked up without reading the source, and it is handed out rather than
// shared: a caller that sorts or truncates what it got must not be able to
// change what the next caller is told.
func TestCodesIsAFreshCopyOfTheWholeLedger(t *testing.T) {
	got := Codes()
	if diff := cmp.Diff(codes, got); diff != "" {
		t.Fatalf("Codes() is not the ledger (-want +got):\n%s", diff)
	}

	got[0] = "GOM3999"
	if again := Codes(); again[0] == "GOM3999" {
		t.Errorf("a caller's edit reached the next Codes()")
	}
}

// A code is printed on its own — in a console line, in a CI log, in an issue
// report — and String is how. Nothing else in this package reaches it, because
// Error.Error writes the field rather than calling it.
func TestCodeStringIsTheCodeItself(t *testing.T) {
	if got := CodeThresholdsInverted.String(); got != "GOM3064" {
		t.Errorf("CodeThresholdsInverted.String() = %q, want %q", got, "GOM3064")
	}
	for _, code := range codes {
		if got := code.String(); got != string(code) {
			t.Errorf("%s.String() = %q", string(code), got)
		}
	}
}

// A Position renders three ways, and only one of them is what an *Error
// carrying a located value prints. The other two are the honest answers for a
// key with no position and for a layer that could name the line but not a
// column inside it, and neither has anywhere else in this repository to be
// exercised.
func TestPositionString(t *testing.T) {
	for _, test := range []struct {
		name     string
		position Position
		known    bool
		want     string
	}{
		{"unknown", Position{}, false, "-"},
		{"line and column", Position{Line: 55, Column: 8}, true, "55:8"},
		// A line with no column is not "column zero": the column is dropped
		// rather than printed as a number no editor would accept.
		{"line only", Position{Line: 55}, true, "55"},
		// A column with no line points at nothing, which is what the zero
		// Position means.
		{"column only", Position{Column: 8}, false, "-"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.position.Known(); got != test.known {
				t.Errorf("Known() = %v, want %v", got, test.known)
			}
			if got := test.position.String(); got != test.want {
				t.Errorf("String() = %q, want %q", got, test.want)
			}
		})
	}
}

// baseKey is what lets a diagnostic about `mutation.include[3]` find the flag
// registered for `mutation.include`. It strips from the first '[' and does not
// look for a matching one, which is a decision rather than an oversight: every
// key the validators build puts the index last or last but one.
func TestBaseKeyStripsTheIndexAndWhatFollowsIt(t *testing.T) {
	for _, test := range []struct{ key, want string }{
		{"report.low", "report.low"},
		{"mutation.include[3]", "mutation.include"},
		{"mutation.expect[1].id", "mutation.expect"},
		// A key that is nothing but an index has the empty prefix in front of
		// it, and that — rather than the key itself — is what no flag is
		// registered under.
		{"[0]", ""},
		{"", ""},
	} {
		if got := baseKey(test.key); got != test.want {
			t.Errorf("baseKey(%q) = %q, want %q", test.key, got, test.want)
		}
	}
}

func TestSet(t *testing.T) {
	var unset Set[int]
	if unset.IsSet() {
		t.Errorf("the zero Set reports itself as set")
	}
	if got := unset.Or(7); got != 7 {
		t.Errorf("Or(7) on an unset Set = %d", got)
	}
	if got := unset.String(); got != "unset" {
		t.Errorf("String() on an unset Set = %q", got)
	}
	// A set value renders as the value, which is what makes "unset" mean
	// something rather than being one rendering among two that look alike.
	if got := Explicit(3).String(); got != "3" {
		t.Errorf("String() on Explicit(3) = %q, want %q", got, "3")
	}
	if got := Explicit([]string{"a", "b"}).String(); got != "[a b]" {
		t.Errorf("String() on a set slice = %q, want %q", got, "[a b]")
	}

	// A deliberate zero is a value: this is the whole reason the type exists.
	zero := Explicit(0)
	if !zero.IsSet() {
		t.Errorf("Explicit(0) is not set")
	}
	if got := zero.Or(7); got != 0 {
		t.Errorf("Or(7) on Explicit(0) = %d, want 0", got)
	}

	if got := When(false, 3); got.IsSet() {
		t.Errorf("When(false, 3) is set")
	}
	value, ok := When(true, 3).Get()
	if !ok || value != 3 {
		t.Errorf("When(true, 3).Get() = %d, %v", value, ok)
	}

	if !Unset[string]().Equal(Set[string]{}) {
		t.Errorf("Unset() differs from the zero Set")
	}
	if Explicit(1).Equal(Set[int]{}) {
		t.Errorf("a set value equals an unset one")
	}
	if !Explicit([]string{"a"}).Equal(Explicit([]string{"a"})) {
		t.Errorf("two Sets holding equal slices are not equal")
	}
	if Explicit([]string{"a"}).Equal(Explicit([]string{"b"})) {
		t.Errorf("two Sets holding different slices are equal")
	}
	// go-cmp has to use Equal rather than reaching for the unexported fields.
	if diff := cmp.Diff(Explicit(5), Explicit(5)); diff != "" {
		t.Errorf("cmp.Diff on equal Sets: %s", diff)
	}
}

func TestOverlayIsEmpty(t *testing.T) {
	var empty Overlay
	if !empty.IsEmpty() {
		t.Errorf("the zero Overlay is not empty")
	}
	// Every field must count, including the ones whose value is a zero.
	for name, overlay := range map[string]Overlay{
		"version":          {Version: Explicit(1)},
		"include":          {Include: Explicit([]string(nil))},
		"exclude":          {Exclude: Explicit([]string(nil))},
		"operators":        {Operators: Explicit([]string(nil))},
		"profile":          {Profile: Explicit(mutation.TierBalanced)},
		"expect":           {Expect: Explicit([]Expectation(nil))},
		"test_command":     {TestCommand: Explicit([]string(nil))},
		"timeout":          {Timeout: Explicit(time.Duration(0))},
		"memory":           {Memory: Explicit(int64(0))},
		"baseline_runs":    {BaselineRuns: Explicit(0)},
		"narrowing":        {Narrowing: Explicit(NarrowingTest)},
		"jobs":             {Jobs: Explicit(0)},
		"cache_mode":       {CacheMode: Explicit(CacheAuto)},
		"cache_directory":  {CacheDirectory: Explicit("")},
		"strict":           {Strict: Explicit(false)},
		"minimum_score":    {MinimumScore: Explicit(0.0)},
		"require_mutants":  {RequireMutants: Explicit(false)},
		"report_directory": {ReportDirectory: Explicit("")},
		"report_formats":   {ReportFormats: Explicit([]ReportFormat(nil))},
		"report_high":      {ReportHigh: Explicit(0)},
		"report_low":       {ReportLow: Explicit(0)},
	} {
		if overlay.IsEmpty() {
			t.Errorf("an Overlay setting %s reports itself as empty", name)
		}
	}
}

func TestErrorRendering(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{
			name: "fully located",
			err:  &Error{Code: CodeThresholdOutOfRange, File: ".go-mutants.toml", Position: Position{Line: 55, Column: 8}, Key: "report.high", Message: "out of range"},
			want: "GOM3063: .go-mutants.toml:55:8: report.high: out of range",
		},
		{
			name: "no position",
			err:  &Error{Code: CodeMissingVersion, File: ".go-mutants.toml", Key: "version", Message: "absent"},
			want: "GOM3004: .go-mutants.toml: version: absent",
		},
		{
			name: "from a flag",
			err:  &Error{Code: CodeJobsOutOfRange, Key: "--jobs", Message: "out of range"},
			want: "GOM3030: --jobs: out of range",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Error(); got != test.want {
				t.Errorf("Error() = %q, want %q", got, test.want)
			}
		})
	}
}

// A joined error prints one problem per line and stays reachable to errors.As
// and errors.Is.
func TestMultiErrorRendering(t *testing.T) {
	first := &Error{Code: CodeUnknownKey, File: "f.toml", Position: Position{Line: 1, Column: 1}, Key: "a", Message: "one"}
	second := &Error{Code: CodeUnknownKey, File: "f.toml", Position: Position{Line: 2, Column: 1}, Key: "b", Message: "two"}
	err := join([]error{nil, first, nil, second})

	want := "GOM3003: f.toml:1:1: a: one\nGOM3003: f.toml:2:1: b: two"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	var reached *Error
	if !errors.As(err, &reached) || reached != first {
		t.Errorf("errors.As did not reach the first problem")
	}
	if !errors.Is(err, error(second)) {
		t.Errorf("errors.Is did not reach the second problem")
	}
	// One problem is reported on its own, with no wrapper around it.
	if got := join([]error{first}); got != error(first) {
		t.Errorf("join of one error wrapped it: %T", got)
	}
	if got := join(nil); got != nil {
		t.Errorf("join of nothing = %v, want nil", got)
	}
}
