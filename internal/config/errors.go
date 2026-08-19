// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// A Code is a stable go-mutants diagnostic code.
//
// Codes are part of the user-facing contract: they appear in console output,
// in CI logs, and in issue reports, and they are what someone searches for
// when a message is not enough. A code therefore means exactly one thing
// forever. Wording may improve, the condition a code names may not change, and
// a retired code is never reused for something else.
//
// This package owns the GOM30xx block. Other packages own their own blocks so
// that two codes can never collide.
type Code string

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// The configuration codes.
//
// The block is grouped by the section of .go-mutants.toml a code belongs to,
// with gaps left inside each group so a new check can be added next to its
// relatives instead of at the end.
const (
	// CodeUnreadable reports a configuration file that exists but could not
	// be read. Not finding the file at all is not an error; see [LoadFile].
	CodeUnreadable Code = "GOM3001"
	// CodeInvalidTOML reports a document that is not well-formed TOML, or a
	// value whose type does not fit the key it was written under.
	CodeInvalidTOML Code = "GOM3002"
	// CodeUnknownKey reports a key that no version of this schema defines,
	// which is almost always a typo in a key that does exist.
	CodeUnknownKey Code = "GOM3003"
	// CodeMissingVersion reports a file with no `version` key.
	CodeMissingVersion Code = "GOM3004"
	// CodeUnsupportedVersion reports a `version` this build cannot read.
	CodeUnsupportedVersion Code = "GOM3005"

	// CodeInvalidGlob reports an include or exclude pattern that does not
	// compile.
	CodeInvalidGlob Code = "GOM3010"
	// CodeUnknownOperator reports an operator that is neither a family nor a
	// rule in the catalogue.
	CodeUnknownOperator Code = "GOM3011"
	// CodeUnknownProfile reports a profile that is not a tier.
	CodeUnknownProfile Code = "GOM3012"
	// CodeInvalidExpectationID reports an expectation whose id is not a full
	// mutant id.
	CodeInvalidExpectationID Code = "GOM3013"
	// CodeDuplicateExpectation reports one mutant id declared twice.
	CodeDuplicateExpectation Code = "GOM3014"
	// CodeEmptyExpectationReason reports an expectation with no reason.
	CodeEmptyExpectationReason Code = "GOM3015"
	// CodeDuplicateOperator reports one operator selected twice.
	CodeDuplicateOperator Code = "GOM3016"

	// CodeEmptyTestCommand reports an empty test argv vector.
	CodeEmptyTestCommand Code = "GOM3020"
	// CodeInvalidDuration reports a timeout that is not a Go duration.
	CodeInvalidDuration Code = "GOM3021"
	// CodeNonPositiveTimeout reports a timeout of zero or less.
	CodeNonPositiveTimeout Code = "GOM3022"
	// CodeBaselineRunsOutOfRange reports a baseline_runs outside its range.
	CodeBaselineRunsOutOfRange Code = "GOM3023"
	// CodeEmptyCommandName reports a test command whose first element, the
	// program to run, is empty.
	CodeEmptyCommandName Code = "GOM3024"

	// CodeJobsOutOfRange reports a worker count outside its range.
	CodeJobsOutOfRange Code = "GOM3030"

	// CodeUnknownCacheMode reports a cache mode that is not auto, on, or off.
	CodeUnknownCacheMode Code = "GOM3040"
	// CodeInvalidCacheDirectory reports a cache directory that is absolute or
	// escapes the workspace.
	CodeInvalidCacheDirectory Code = "GOM3041"

	// CodeMinimumScoreOutOfRange reports a score floor outside [0,100].
	CodeMinimumScoreOutOfRange Code = "GOM3050"

	// CodeInvalidReportDirectory reports a report directory that is absolute
	// or escapes the workspace.
	CodeInvalidReportDirectory Code = "GOM3060"
	// CodeUnknownReportFormat reports a format that is not json or html.
	CodeUnknownReportFormat Code = "GOM3061"
	// CodeDuplicateReportFormat reports one format listed twice.
	CodeDuplicateReportFormat Code = "GOM3062"
	// CodeThresholdOutOfRange reports an HTML threshold outside [0,100].
	CodeThresholdOutOfRange Code = "GOM3063"
	// CodeThresholdsInverted reports report.low above report.high.
	CodeThresholdsInverted Code = "GOM3064"
)

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM30xx block
// this package owns.
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

// Codes returns every diagnostic code this package can report, in numeric
// order. `doctor` prints it so that a code seen in a log can be looked up
// without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// A Position is a one-based line and column in a configuration file. The zero
// Position means "no position is known", which is the honest answer for a key
// that is missing, for a value that came from a flag, and for a cross-field
// rule whose two halves came from different layers.
type Position struct {
	// Line is the one-based line number, or zero when unknown.
	Line int
	// Column is the one-based byte column, or zero when unknown.
	Column int
}

// Known reports whether the position points at anything.
func (p Position) Known() bool { return p.Line > 0 }

// String renders "line:column", or "-" when the position is unknown.
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

// An Error is one configuration problem, located as precisely as the layer it
// came from allows.
//
// Every error this package returns is either an *Error or a join of *Errors,
// so errors.As always reaches one. Several problems in one file are reported
// together rather than one per edit-and-rerun cycle: the joined error's
// Unwrap() []error yields them in document order.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// File is the configuration file's path as the caller spelled it, or ""
	// when the value did not come from a file.
	File string
	// Position is where in File the offending value sits, when that is known.
	Position Position
	// Key names the setting: a TOML key path such as "report.low" or
	// "mutation.expect[2].id" for file and merged values, a flag such as
	// "--jobs" for flag values.
	Key string
	// Message states the problem in one line, without repeating the code, the
	// file, or the key.
	Message string
	// Err is the underlying cause when one exists, such as the glob or
	// duration error behind an invalid value. It is available to errors.Is
	// and errors.As and is not repeated in Message.
	Err error
}

// Error implements the error interface, rendering
// "GOM3063: .go-mutants.toml:55:7: report.low: <message>" with each locating
// part dropped when it is unknown.
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

// Unwrap returns the underlying cause, so that errors.Is reaches the glob,
// duration, or path error that explains a value.
func (e *Error) Unwrap() error { return e.Err }

// Origin says which vocabulary an error should use when it names a setting.
type Origin uint8

const (
	// OriginFile names TOML keys. It is also what a merged [Config] uses,
	// since the file is the durable place a value can be corrected.
	OriginFile Origin = iota
	// OriginFlag names command-line flags.
	OriginFlag
)

// flagNames maps a TOML key to the flag that overrides it. Settings with no
// flag are absent and fall back to their TOML key, which is the truthful
// answer: there is nowhere else to change them.
var flagNames = map[string]string{
	"mutation.include":   "--include",
	"mutation.exclude":   "--exclude",
	"mutation.operators": "--operator",
	"mutation.profile":   "--profile",
	"test.command":       "-- <test argv>",
	"test.timeout":       "--timeout",
	"execution.jobs":     "--jobs",
	"cache.mode":         "--cache",
	"policy.strict":      "--strict",
	"report.formats":     "--report",
}

// baseKey strips the array index and any field suffix from a key path, so
// that "mutation.include[3]" and "mutation.expect[1].id" can find the flag
// registered for "mutation.include" and "mutation.expect".
func baseKey(key string) string {
	if i := strings.IndexByte(key, '['); i >= 0 {
		return key[:i]
	}
	return key
}

// A reporter turns a key path into a located [Error]. One reporter describes
// one layer: a parsed file knows its path and its positions, a flag overlay
// knows neither and names flags instead, and a merged configuration names TOML
// keys with no position because its values no longer have one place to point.
type reporter struct {
	origin    Origin
	file      string
	positions map[string]Position
}

// fileReporter reports against a parsed configuration file.
func fileReporter(path string, positions map[string]Position) reporter {
	return reporter{origin: OriginFile, file: path, positions: positions}
}

// flagReporter reports against a flag overlay.
func flagReporter() reporter { return reporter{origin: OriginFlag} }

// mergedReporter reports against a merged configuration, where a value may
// have come from any layer.
func mergedReporter() reporter { return reporter{origin: OriginFile} }

// name renders a key path in this layer's vocabulary.
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

// at returns the recorded position of a key, if this layer has one.
func (r reporter) at(key string) Position { return r.positions[key] }

// errorf builds a located error for a key.
func (r reporter) errorf(code Code, key, format string, args ...any) *Error {
	return &Error{
		Code:     code,
		File:     r.file,
		Position: r.at(key),
		Key:      r.name(key),
		Message:  fmt.Sprintf(format, args...),
	}
}

// wrapf builds a located error that carries an underlying cause.
func (r reporter) wrapf(code Code, key string, cause error, format string, args ...any) *Error {
	e := r.errorf(code, key, format, args...)
	e.Err = cause
	return e
}

// join collapses a list of problems into one error, dropping the nils and
// preserving order. It returns nil when nothing went wrong and the single
// error itself when only one thing did, so that a caller printing one problem
// never sees a wrapper around it.
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

// A multiError is several configuration problems reported together. It exists
// instead of errors.Join so that the rendering is one problem per line with no
// leading blank, which is what the console prints verbatim.
type multiError struct {
	errs []error
}

// Error renders one problem per line, in document order.
func (m *multiError) Error() string {
	lines := make([]string, 0, len(m.errs))
	for _, err := range m.errs {
		lines = append(lines, err.Error())
	}
	return strings.Join(lines, "\n")
}

// Unwrap exposes the individual problems to errors.Is and errors.As.
func (m *multiError) Unwrap() []error { return slices.Clone(m.errs) }
