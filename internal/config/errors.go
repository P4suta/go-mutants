// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type Code string

func (c Code) String() string { return string(c) }

const (
	CodeUnreadable         Code = "GOM3001"
	CodeInvalidTOML        Code = "GOM3002"
	CodeUnknownKey         Code = "GOM3003"
	CodeMissingVersion     Code = "GOM3004"
	CodeUnsupportedVersion Code = "GOM3005"

	CodeInvalidGlob            Code = "GOM3010"
	CodeUnknownOperator        Code = "GOM3011"
	CodeUnknownProfile         Code = "GOM3012"
	CodeInvalidExpectationID   Code = "GOM3013"
	CodeDuplicateExpectation   Code = "GOM3014"
	CodeEmptyExpectationReason Code = "GOM3015"
	CodeDuplicateOperator      Code = "GOM3016"

	CodeEmptyTestCommand       Code = "GOM3020"
	CodeInvalidDuration        Code = "GOM3021"
	CodeNonPositiveTimeout     Code = "GOM3022"
	CodeBaselineRunsOutOfRange Code = "GOM3023"
	CodeEmptyCommandName       Code = "GOM3024"
	CodeInvalidSize            Code = "GOM3025"
	CodeNonPositiveMemory      Code = "GOM3026"
	CodeUnknownNarrowing       Code = "GOM3027"
	CodeUnknownProbing         Code = "GOM3028"

	CodeJobsOutOfRange Code = "GOM3030"

	CodeUnknownCacheMode      Code = "GOM3040"
	CodeInvalidCacheDirectory Code = "GOM3041"

	CodeMinimumScoreOutOfRange Code = "GOM3050"

	CodeInvalidReportDirectory Code = "GOM3060"
	CodeUnknownReportFormat    Code = "GOM3061"
	CodeDuplicateReportFormat  Code = "GOM3062"
	CodeThresholdOutOfRange    Code = "GOM3063"
	CodeThresholdsInverted     Code = "GOM3064"
)

var codes = []Code{
	CodeUnreadable,
	CodeInvalidTOML,
	CodeUnknownKey,
	CodeMissingVersion,
	CodeUnsupportedVersion,
	CodeInvalidGlob,
	CodeUnknownOperator,
	CodeUnknownProfile,
	CodeInvalidExpectationID,
	CodeDuplicateExpectation,
	CodeEmptyExpectationReason,
	CodeDuplicateOperator,
	CodeEmptyTestCommand,
	CodeInvalidDuration,
	CodeNonPositiveTimeout,
	CodeBaselineRunsOutOfRange,
	CodeEmptyCommandName,
	CodeInvalidSize,
	CodeNonPositiveMemory,
	CodeUnknownNarrowing,
	CodeUnknownProbing,
	CodeJobsOutOfRange,
	CodeUnknownCacheMode,
	CodeInvalidCacheDirectory,
	CodeMinimumScoreOutOfRange,
	CodeInvalidReportDirectory,
	CodeUnknownReportFormat,
	CodeDuplicateReportFormat,
	CodeThresholdOutOfRange,
	CodeThresholdsInverted,
}

func Codes() []Code { return slices.Clone(codes) }

type Position struct {
	Line   int
	Column int
}

func (p Position) Known() bool { return p.Line > 0 }

func (p Position) String() string {
	if !p.Known() {
		return "-"
	}
	s := strconv.Itoa(p.Line)
	if p.Column > 0 {
		s += ":" + strconv.Itoa(p.Column)
	}
	return s
}

type Error struct {
	Code     Code
	File     string
	Position Position
	Key      string
	Message  string
	Err      error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": ")
	if e.File != "" {
		b.WriteString(e.File)
		if e.Position.Known() {
			b.WriteString(":")
			b.WriteString(e.Position.String())
		}
		b.WriteString(": ")
	}
	if e.Key != "" {
		b.WriteString(e.Key)
		b.WriteString(": ")
	}
	b.WriteString(e.Message)
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

type Origin uint8

const (
	OriginFile Origin = iota
	OriginFlag
)

var flagNames = map[string]string{
	"mutation.include":   "--include",
	"mutation.exclude":   "--exclude",
	"mutation.operators": "--operator",
	"mutation.profile":   "--profile",
	"test.command":       "-- <test argv>",
	"test.timeout":       "--timeout",
	"test.memory":        "--memory",
	"execution.jobs":     "--jobs",
	"execution.isolate":  "--isolate",
	"cache.mode":         "--cache",
	"policy.strict":      "--strict",
	"report.formats":     "--report",
}

func baseKey(key string) string {
	if i := strings.IndexByte(key, '['); i >= 0 {
		return key[:i]
	}
	return key
}

type reporter struct {
	origin    Origin
	file      string
	positions map[string]Position
}

func fileReporter(path string, positions map[string]Position) reporter {
	return reporter{origin: OriginFile, file: path, positions: positions}
}

func flagReporter() reporter { return reporter{origin: OriginFlag} }

func mergedReporter() reporter { return reporter{origin: OriginFile} }

func (r reporter) name(key string) string {
	if r.origin != OriginFlag {
		return key
	}
	if flag, ok := flagNames[key]; ok {
		return flag
	}
	if flag, ok := flagNames[baseKey(key)]; ok {
		return flag
	}
	return key
}

func (r reporter) at(key string) Position { return r.positions[key] }

func (r reporter) errorf(code Code, key, format string, args ...any) *Error {
	return &Error{
		Code:     code,
		File:     r.file,
		Position: r.at(key),
		Key:      r.name(key),
		Message:  fmt.Sprintf(format, args...),
	}
}

func (r reporter) wrapf(code Code, key string, cause error, format string, args ...any) *Error {
	e := r.errorf(code, key, format, args...)
	e.Err = cause
	return e
}

func join(errs []error) error {
	kept := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			kept = append(kept, err)
		}
	}
	switch len(kept) {
	case 0:
		return nil
	case 1:
		return kept[0]
	default:
		return &multiError{errs: kept}
	}
}

type multiError struct {
	errs []error
}

func (m *multiError) Error() string {
	lines := make([]string, 0, len(m.errs))
	for _, err := range m.errs {
		lines = append(lines, err.Error())
	}
	return strings.Join(lines, "\n")
}

func (m *multiError) Unwrap() []error { return slices.Clone(m.errs) }
