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

func (o Overlay) Validate() error { return validateOverlay(o, flagReporter()) }

func (c Config) Validate() error {
	report := mergedReporter()
	problems := []error{validateOverlay(c.overlay(), report)}

	if inPercentRange(c.Report.Low) && inPercentRange(c.Report.High) && c.Report.Low > c.Report.High {
		problems = append(problems, report.errorf(CodeThresholdsInverted, "report.low",
			"report.low %d is above report.high %d: the low threshold marks the bottom of the range, not the top",
			c.Report.Low, c.Report.High))
	}

	return join(problems)
}

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
	if memory, ok := o.Memory.Get(); ok && memory <= 0 {
		problems = append(problems, report.errorf(CodeNonPositiveMemory, "test.memory",
			"a memory bound of %s leaves no room for a test binary: omit the key to derive "+
				"max(1GiB, largest baseline peak × 4)", formatSize(memory)))
	}
	if runs, ok := o.BaselineRuns.Get(); ok && (runs < MinBaselineRuns || runs > MaxBaselineRuns) {
		problems = append(problems, report.errorf(CodeBaselineRunsOutOfRange, "test.baseline_runs",
			"%d baseline runs is outside %d..%d", runs, MinBaselineRuns, MaxBaselineRuns))
	}
	if probing, ok := o.Probing.Get(); ok && !probing.Valid() {
		problems = append(problems, report.errorf(CodeUnknownProbing, "test.probing",
			"unknown probing mode %q: expected %s", probing.String(), probingList()))
	}
	if narrowing, ok := o.Narrowing.Get(); ok && !narrowing.Valid() {
		problems = append(problems, report.errorf(CodeUnknownNarrowing, "test.narrowing",
			"unknown narrowing %q: expected %s", narrowing.String(), narrowingList()))
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

func relativeDirectory(directory string) (string, error) {
	return mutation.NormalizePath(directory)
}

func directoryRule() string {
	return "give a relative path that stays inside the tree it is resolved against"
}

func elementKey(key string, index int) string {
	return key + "[" + strconv.Itoa(index) + "]"
}

func inPercentRange(v int) bool { return v >= MinPercent && v <= MaxPercent }

func inPercentRangeFloat(v float64) bool { return v >= MinPercent && v <= MaxPercent }

func formatScore(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func tierList() string {
	names := make([]string, 0, len(mutation.Tiers()))
	for _, tier := range mutation.Tiers() {
		names = append(names, strconv.Quote(tier.String()))
	}
	return strings.Join(names, ", ")
}

func narrowingList() string {
	names := make([]string, 0, len(Narrowings()))
	for _, narrowing := range Narrowings() {
		names = append(names, strconv.Quote(narrowing.String()))
	}
	return strings.Join(names, ", ")
}

func probingList() string {
	names := make([]string, 0, len(Probings()))
	for _, probing := range Probings() {
		names = append(names, strconv.Quote(probing.String()))
	}
	return strings.Join(names, ", ")
}

func cacheModeList() string {
	names := make([]string, 0, len(CacheModes()))
	for _, mode := range CacheModes() {
		names = append(names, strconv.Quote(mode.String()))
	}
	return strings.Join(names, ", ")
}

func formatList() string {
	names := make([]string, 0, len(ReportFormats()))
	for _, format := range ReportFormats() {
		names = append(names, strconv.Quote(format.String()))
	}
	return strings.Join(names, ", ")
}

func familyList(registry *mutation.Registry) string {
	families := registry.Families()
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, string(family))
	}
	return strings.Join(names, ", ")
}

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

func ParseMemory(value string) (int64, error) {
	size, err := parseSize(value)
	if err != nil {
		return 0, &Error{
			Code:    CodeInvalidSize,
			Key:     flagNames["test.memory"],
			Message: err.Error(),
		}
	}
	if size <= 0 {
		return 0, &Error{
			Code: CodeNonPositiveMemory,
			Key:  flagNames["test.memory"],
			Message: fmt.Sprintf(
				"a memory bound of %s leaves no room for a test binary: omit the flag to derive max(1GiB, largest baseline peak × 4)",
				formatSize(size)),
		}
	}
	return size, nil
}

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
