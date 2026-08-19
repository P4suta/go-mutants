// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// Validate checks a flag overlay and names the flags that carry the problems.
//
// The CLI calls it before merging, so that `--jobs 99` is refused as `--jobs`
// rather than as `execution.jobs`, which would send the user editing a file
// they never touched.
func (o Overlay) Validate() error { return validateOverlay(o, flagReporter()) }

// Validate checks a resolved configuration: every per-value rule again, plus
// the rules that can only be judged once the layers are merged.
//
// Re-checking values that were already checked in their own layer is
// deliberate. A Config can be built by hand, and the merged value is what the
// run will actually use; a validator that trusted its inputs would be a
// validator that only runs when it is not needed.
//
// Errors name TOML keys, because the file is where a value can be corrected
// for good, and carry no position: after merging, a value has no single place
// in any file to point at.
func (c Config) Validate() error {
	report := mergedReporter()
	problems := []error{validateOverlay(c.overlay(), report)}

	// Cross-field rules go here and only here. This one cannot live with
	// either threshold: low may come from the file and high from a flag, so
	// neither layer can see the pair.
	//
	// It is skipped when either end is already out of range, so a file with
	// `high = 120` gets the one error that explains it rather than a second,
	// derived one about an ordering that was never the real problem.
	if inPercentRange(c.Report.Low) && inPercentRange(c.Report.High) && c.Report.Low > c.Report.High {
		problems = append(problems, report.errorf(CodeThresholdsInverted, "report.low",
			"report.low %d is above report.high %d: the low threshold marks the bottom of the range, not the top",
			c.Report.Low, c.Report.High))
	}

	return join(problems)
}

// validateOverlay checks every value a layer set, in document order, and
// reports all of the problems rather than the first.
//
// One rule per setting, one place. Both entry points — a parsed file and a
// flag overlay — and the post-merge check all come through here, so a rule
// cannot be enforced in one path and forgotten in another.
func validateOverlay(o Overlay, report reporter) error {
	var problems []error

	if version, ok := o.Version.Get(); ok && version != Version {
		problems = append(problems, report.errorf(CodeUnsupportedVersion, "version",
			"configuration version %d is not supported: this build reads version %d", version, Version))
	}

	if patterns, ok := o.Include.Get(); ok {
		problems = append(problems, validatePatterns(patterns, "mutation.include", report)...)
	}
	if patterns, ok := o.Exclude.Get(); ok {
		problems = append(problems, validatePatterns(patterns, "mutation.exclude", report)...)
	}
	if operators, ok := o.Operators.Get(); ok {
		problems = append(problems, validateOperators(operators, report)...)
	}
	if profile, ok := o.Profile.Get(); ok && !profile.Valid() {
		problems = append(problems, report.errorf(CodeUnknownProfile, "mutation.profile",
			"unknown profile %q: expected %s", profile.String(), tierList()))
	}
	if expectations, ok := o.Expect.Get(); ok {
		problems = append(problems, validateExpectations(expectations, report)...)
	}

	if command, ok := o.TestCommand.Get(); ok {
		problems = append(problems, validateCommand(command, report)...)
	}
	if timeout, ok := o.Timeout.Get(); ok && timeout <= 0 {
		problems = append(problems, report.errorf(CodeNonPositiveTimeout, "test.timeout",
			"a timeout of %s cannot be waited for: omit the key to derive max(10s, slowest baseline × 5)",
			timeout))
	}
	if runs, ok := o.BaselineRuns.Get(); ok && (runs < MinBaselineRuns || runs > MaxBaselineRuns) {
		problems = append(problems, report.errorf(CodeBaselineRunsOutOfRange, "test.baseline_runs",
			"%d baseline runs is outside %d..%d", runs, MinBaselineRuns, MaxBaselineRuns))
	}

	if jobs, ok := o.Jobs.Get(); ok && (jobs < MinJobs || jobs > MaxJobs) {
		problems = append(problems, report.errorf(CodeJobsOutOfRange, "execution.jobs",
			"%d workers is outside %d..%d", jobs, MinJobs, MaxJobs))
	}

	if mode, ok := o.CacheMode.Get(); ok && !mode.Valid() {
		problems = append(problems, report.errorf(CodeUnknownCacheMode, "cache.mode",
			"unknown cache mode %q: expected %s", mode.String(), cacheModeList()))
	}
	if directory, ok := o.CacheDirectory.Get(); ok {
		if _, err := relativeDirectory(directory); err != nil {
			problems = append(problems, report.wrapf(CodeInvalidCacheDirectory, "cache.directory", err,
				"%q is not usable as a cache directory: %s", directory, directoryRule()))
		}
	}

	if score, ok := o.MinimumScore.Get(); ok && !inPercentRangeFloat(score) {
		problems = append(problems, report.errorf(CodeMinimumScoreOutOfRange, "policy.minimum_score",
			"a minimum score of %s is outside %d..%d", formatScore(score), MinPercent, MaxPercent))
	}

	if directory, ok := o.ReportDirectory.Get(); ok {
		if _, err := relativeDirectory(directory); err != nil {
			problems = append(problems, report.wrapf(CodeInvalidReportDirectory, "report.directory", err,
				"%q is not usable as a report directory: %s", directory, directoryRule()))
		}
	}
	if formats, ok := o.ReportFormats.Get(); ok {
		problems = append(problems, validateFormats(formats, report)...)
	}
	if high, ok := o.ReportHigh.Get(); ok && !inPercentRange(high) {
		problems = append(problems, report.errorf(CodeThresholdOutOfRange, "report.high",
			"a high threshold of %d is outside %d..%d", high, MinPercent, MaxPercent))
	}
	if low, ok := o.ReportLow.Get(); ok && !inPercentRange(low) {
		problems = append(problems, report.errorf(CodeThresholdOutOfRange, "report.low",
			"a low threshold of %d is outside %d..%d", low, MinPercent, MaxPercent))
	}

	return join(problems)
}

// validatePatterns compiles every glob so that a pattern which cannot match
// anything is refused where it was written rather than silently selecting
// nothing.
func validatePatterns(patterns []string, key string, report reporter) []error {
	var problems []error
	for i, pattern := range patterns {
		if _, err := glob.Compile(pattern); err != nil {
			var syntax *glob.SyntaxError
			detail := err.Error()
			if errors.As(err, &syntax) {
				detail = fmt.Sprintf("%s (at character %d)", syntax.Message, syntax.Column)
			}
			problems = append(problems, report.wrapf(CodeInvalidGlob, elementKey(key, i), err,
				"invalid pattern %q: %s", pattern, detail))
		}
	}
	return problems
}

// validateOperators checks each name against the frozen catalogue.
//
// Both a family name and a rule name are accepted. docs/configuration.md
// documents families only, and families are what `init` writes, but a name is
// unambiguous either way — no rule in the v1 table shares a name with a family
// — and refusing "eq-to-neq" while accepting "comparison" would be an
// arbitrary distinction to explain.
func validateOperators(operators []string, report reporter) []error {
	registry := mutation.CanonicalRegistry()
	var problems []error
	seen := make(map[string]int, len(operators))
	for i, name := range operators {
		key := elementKey("mutation.operators", i)
		if first, duplicate := seen[name]; duplicate {
			problems = append(problems, report.errorf(CodeDuplicateOperator, key,
				"operator %q is already selected at %s", name, elementKey("mutation.operators", first)))
			continue
		}
		seen[name] = i

		if _, ok := registry.FamilyPosition(mutation.Family(name)); ok {
			continue
		}
		if _, ok := registry.Lookup(name); ok {
			continue
		}
		problems = append(problems, report.errorf(CodeUnknownOperator, key,
			"unknown operator %q: expected one of the %d families (%s) or one of the %d rule names in the v1 catalogue",
			name, len(registry.Families()), familyList(registry), registry.Len()))
	}
	return problems
}

// validateExpectations checks the ledger rows.
//
// A prefix is refused even though `--mutant` accepts one: a ledger outlives
// the run that produced it, and a prefix that resolves uniquely today can
// become ambiguous after a single commit, at which point the row would either
// silence the wrong mutant or fail for a reason nobody could read off the file.
func validateExpectations(expectations []Expectation, report reporter) []error {
	var problems []error
	seen := make(map[string]int, len(expectations))
	for i, expectation := range expectations {
		idKey := fmt.Sprintf("mutation.expect[%d].id", i)
		switch {
		case expectation.ID == "":
			problems = append(problems, report.errorf(CodeInvalidExpectationID, idKey,
				"an expectation needs an id: the full %d character mutant id, as printed in the JSON report",
				mutation.IDHexLength))
		case !mutation.IsID(expectation.ID):
			problems = append(problems, report.errorf(CodeInvalidExpectationID, idKey,
				"%q is not a mutant id: expected exactly %d lowercase hex characters, not a display prefix",
				expectation.ID, mutation.IDHexLength))
		default:
			if first, duplicate := seen[expectation.ID]; duplicate {
				problems = append(problems, report.errorf(CodeDuplicateExpectation, idKey,
					"mutant %s is already expected at mutation.expect[%d]: one mutant has one reason",
					expectation.ID, first))
			} else {
				seen[expectation.ID] = i
			}
		}
		if strings.TrimSpace(expectation.Reason) == "" {
			problems = append(problems, report.errorf(CodeEmptyExpectationReason,
				fmt.Sprintf("mutation.expect[%d].reason", i),
				"an expectation needs a reason: say why this mutant is expected to survive, for whoever reads the ledger next"))
		}
	}
	return problems
}

// validateCommand checks the test argv vector.
//
// Only the program name is required to be non-blank. Later elements are passed
// through untouched and an empty one can be meaningful — `-run ""` selects
// every test — so rejecting every empty element would refuse a legitimate
// command to catch a typo that the program name check already catches.
func validateCommand(command []string, report reporter) []error {
	if len(command) == 0 {
		return []error{report.errorf(CodeEmptyTestCommand, "test.command",
			`the test command is empty: give the argv vector that runs the tests, such as ["go", "test", "./..."]`)}
	}
	if strings.TrimSpace(command[0]) == "" {
		return []error{report.errorf(CodeEmptyCommandName, elementKey("test.command", 0),
			"the first element of the test command names the program to run and cannot be blank; the vector is executed directly, never through a shell")}
	}
	return nil
}

// validateFormats checks the report formats, rejecting a repeat as well as an
// unknown name: writing one artefact twice is never what was meant.
func validateFormats(formats []ReportFormat, report reporter) []error {
	var problems []error
	seen := make(map[ReportFormat]int, len(formats))
	for i, format := range formats {
		key := elementKey("report.formats", i)
		if !format.Valid() {
			problems = append(problems, report.errorf(CodeUnknownReportFormat, key,
				"unknown report format %q: expected %s", format.String(), formatList()))
			continue
		}
		if first, duplicate := seen[format]; duplicate {
			problems = append(problems, report.errorf(CodeDuplicateReportFormat, key,
				"report format %q is already listed at %s", format.String(), elementKey("report.formats", first)))
			continue
		}
		seen[format] = i
	}
	return problems
}

// relativeDirectory canonicalises a configured directory and refuses anything
// that is not a relative path staying inside the tree it is resolved against.
//
// It is [mutation.NormalizePath], which is also what mutant identities are
// built from, so "reports\mutation" and "reports/mutation" are one directory
// here for exactly the same reason they are one file there.
func relativeDirectory(directory string) (string, error) {
	return mutation.NormalizePath(directory)
}

// directoryRule is the one sentence every directory diagnostic ends with.
func directoryRule() string {
	return "give a relative path that stays inside the tree it is resolved against"
}

// elementKey names one element of an array setting.
func elementKey(key string, index int) string {
	return key + "[" + strconv.Itoa(index) + "]"
}

// inPercentRange reports whether an integer percentage is in range.
func inPercentRange(v int) bool { return v >= MinPercent && v <= MaxPercent }

// inPercentRangeFloat reports whether a fractional percentage is in range. The
// comparison is written so that a NaN, which compares false against
// everything, is rejected rather than accepted.
func inPercentRangeFloat(v float64) bool { return v >= MinPercent && v <= MaxPercent }

// formatScore renders a score floor the way the exit policy renders it, so the
// number in a configuration error matches the number in a failure message.
func formatScore(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// tierList renders the profile names for a diagnostic.
func tierList() string {
	names := make([]string, 0, len(mutation.Tiers()))
	for _, tier := range mutation.Tiers() {
		names = append(names, strconv.Quote(tier.String()))
	}
	return strings.Join(names, ", ")
}

// cacheModeList renders the cache modes for a diagnostic.
func cacheModeList() string {
	names := make([]string, 0, len(CacheModes()))
	for _, mode := range CacheModes() {
		names = append(names, strconv.Quote(mode.String()))
	}
	return strings.Join(names, ", ")
}

// formatList renders the report formats for a diagnostic.
func formatList() string {
	names := make([]string, 0, len(ReportFormats()))
	for _, format := range ReportFormats() {
		names = append(names, strconv.Quote(format.String()))
	}
	return strings.Join(names, ", ")
}

// familyList renders the operator families for a diagnostic.
func familyList(registry *mutation.Registry) string {
	families := registry.Families()
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, string(family))
	}
	return strings.Join(names, ", ")
}

// The Parse* helpers turn the text a flag carries into the typed value an
// [Overlay] field holds, failing with this package's codes so that a bad
// `--profile` and a bad `mutation.profile` are the same diagnostic with the
// same code. They name flags, since a flag is the only way their input
// arrives; the file path through [Parse] does the same conversions against
// TOML keys.

// ParseProfile resolves a profile name to its tier.
func ParseProfile(name string) (mutation.Tier, error) {
	tier, err := mutation.ParseTier(name)
	if err != nil {
		return 0, &Error{
			Code:    CodeUnknownProfile,
			Key:     flagNames["mutation.profile"],
			Message: fmt.Sprintf("unknown profile %q: expected %s", name, tierList()),
			Err:     err,
		}
	}
	return tier, nil
}

// ParseCacheMode resolves a cache mode name.
func ParseCacheMode(name string) (CacheMode, error) {
	mode := CacheMode(name)
	if !mode.Valid() {
		return "", &Error{
			Code:    CodeUnknownCacheMode,
			Key:     flagNames["cache.mode"],
			Message: fmt.Sprintf("unknown cache mode %q: expected %s", name, cacheModeList()),
		}
	}
	return mode, nil
}

// ParseReportFormats resolves the value of `--report`: a comma-separated list
// of formats, or "none" for no project reports at all.
//
// "none" exists because an empty flag value is indistinguishable from an
// unset one on a command line, while `formats = []` says the same thing
// unambiguously in a file. The returned slice is non-nil and empty for "none",
// which is what makes it an explicit choice that beats the default rather than
// an absence that does not.
func ParseReportFormats(value string) ([]ReportFormat, error) {
	if strings.TrimSpace(value) == "none" {
		return []ReportFormat{}, nil
	}
	parts := strings.Split(value, ",")
	formats := make([]ReportFormat, 0, len(parts))
	for _, part := range parts {
		formats = append(formats, ReportFormat(strings.TrimSpace(part)))
	}
	if err := join(validateFormats(formats, flagReporter())); err != nil {
		return nil, err
	}
	return formats, nil
}

// ParseTimeout resolves the value of `--timeout`.
func ParseTimeout(value string) (time.Duration, error) {
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, &Error{
			Code:    CodeInvalidDuration,
			Key:     flagNames["test.timeout"],
			Message: fmt.Sprintf("%q is not a duration: write a Go duration such as \"90s\", \"2m\", or \"1m30s\"", value),
			Err:     err,
		}
	}
	if timeout <= 0 {
		return 0, &Error{
			Code:    CodeNonPositiveTimeout,
			Key:     flagNames["test.timeout"],
			Message: fmt.Sprintf("a timeout of %s cannot be waited for: omit the flag to derive max(10s, slowest baseline × 5)", timeout),
		}
	}
	return timeout, nil
}
