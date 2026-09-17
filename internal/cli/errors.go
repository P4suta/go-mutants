// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
)

type Code string

const (
	CodeUsage                  Code = "GOM1001"
	CodeTestArgv               Code = "GOM1002"
	CodeWorkingDirectory       Code = "GOM1003"
	CodeConflictingFlags       Code = "GOM1004"
	CodeInvalidMutantPrefix    Code = "GOM1005"
	CodeUnimplementedOperators Code = "GOM1006"
	CodeCatalogMismatch        Code = "GOM1007"
	CodeInertProfile           Code = "GOM1008"
	CodeMutantUnresolved       Code = "GOM1009"
	CodeUnreadableReport       Code = "GOM1010"
	CodeInvalidReportDocument  Code = "GOM1011"
	CodeGitHubSummary          Code = "GOM1012"
	CodeTraceUnavailable       Code = "GOM1013"
	CodeDiagnosticsUnavailable Code = "GOM1014"
)

const (
	CodeEnvironmentUnusable Code = "GOM8001"
)

const (
	CodeConfigurationExists     Code = "GOM8101"
	CodeConfigurationUnreadable Code = "GOM8102"
	CodeConfigurationNotWritten Code = "GOM8103"
	CodeConfigurationStale      Code = "GOM8104"
)

const (
	CodeNotAModuleRoot Code = "GOM8201"
	CodeNoStoredRun    Code = "GOM8202"
)

const (
	CodeNoTraceRecorded Code = "GOM8301"
	CodeUnreadableTrace Code = "GOM8302"
	CodeTraceNotRemoved Code = "GOM8303"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeUsage,
	CodeTestArgv,
	CodeWorkingDirectory,
	CodeConflictingFlags,
	CodeInvalidMutantPrefix,
	CodeUnimplementedOperators,
	CodeCatalogMismatch,
	CodeInertProfile,
	CodeMutantUnresolved,
	CodeUnreadableReport,
	CodeInvalidReportDocument,
	CodeGitHubSummary,
	CodeTraceUnavailable,
	CodeDiagnosticsUnavailable,
	CodeEnvironmentUnusable,
	CodeConfigurationExists,
	CodeConfigurationUnreadable,
	CodeConfigurationNotWritten,
	CodeConfigurationStale,
	CodeNotAModuleRoot,
	CodeNoStoredRun,
	CodeNoTraceRecorded,
	CodeUnreadableTrace,
	CodeTraceNotRemoved,
}

func Codes() []Code { return slices.Clone(codes) }

type Error struct {
	Code    Code
	Message string
	Hint    string
	Err     error
}

func (e *Error) Error() string {
	msg := string(e.Code) + ": " + e.Message
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

func usagef(format string, args ...any) *Error {
	return &Error{
		Code:    CodeUsage,
		Message: fmt.Sprintf(format, args...),
		Hint:    "run `go-mutants --help` to see the commands and flags",
	}
}

type exitError struct {
	code   mutation.ExitCode
	err    error
	silent bool
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func ExitCode(err error) mutation.ExitCode {
	if err == nil {
		return mutation.ExitOK
	}
	var decided *exitError
	if errors.As(err, &decided) {
		return decided.code
	}
	return mutation.ExitInfrastructure
}

func RenderError(w io.Writer, err error) {
	if err == nil {
		return
	}
	var decided *exitError
	if errors.As(err, &decided) && decided.silent {
		return
	}
	var b strings.Builder
	text := strings.TrimRight(err.Error(), "\n")
	if _, _, coded := splitCode(firstLine(text)); coded {
		current := string(CodeUsage)
		for _, raw := range strings.Split(text, "\n") {
			line := strings.TrimRight(raw, "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			code, rest, ok := splitCode(line)
			if !ok {
				code, rest = current, line
			}
			current = code
			fmt.Fprintf(&b, "error %s: %s\n", code, rest)
		}
	} else {
		for _, raw := range strings.Split(text, "\n") {
			fmt.Fprintf(&b, "error %s: %s\n", CodeUsage, strings.TrimRight(raw, "\r"))
		}
	}

	var cliErr *Error
	if errors.As(err, &cliErr) && cliErr.Hint != "" {
		b.WriteString("hint: " + cliErr.Hint + "\n")
	}
	if command := commandOf(err); command != nil && len(command.Argv) > 0 {
		b.WriteString("    command: " + renderArgv(command.Argv) + "\n")
		if command.Dir != "" {
			b.WriteString("    dir: " + command.Dir + "\n")
		}
	}
	if output := outputOf(err); output != "" {
		for _, line := range strings.Split(output, "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
	if directory := diagnosticsOf(err); directory != "" {
		b.WriteString("diagnostics: " + directory + "\n")
	}
	_, _ = io.WriteString(w, b.String())
}

type diagnosticsError struct {
	err       error
	directory string
}

func (e *diagnosticsError) Error() string { return e.err.Error() }
func (e *diagnosticsError) Unwrap() error { return e.err }

func (e *diagnosticsError) DiagnosticsDirectory() string { return e.directory }

type diagnosticsCarrier interface{ DiagnosticsDirectory() string }

func diagnosticsOf(err error) string {
	var found string
	walkCauses(err, func(e error) bool {
		carrier, ok := e.(diagnosticsCarrier)
		if !ok {
			return false
		}
		found = carrier.DiagnosticsDirectory()
		return found != ""
	})
	return found
}

func renderWarning(w io.Writer, err error) {
	var coded *Error
	if !errors.As(err, &coded) {
		return
	}
	line := "warning " + coded.Error() + "\n"
	if coded.Hint != "" {
		line += "hint: " + coded.Hint + "\n"
	}
	_, _ = io.WriteString(w, line)
}

type outputCarrier interface{ RetainedOutput() string }

type commandCarrier interface{ Command() *runner.Invocation }

func outputOf(err error) string {
	var found string
	walkCauses(err, func(e error) bool {
		carrier, ok := e.(outputCarrier)
		if !ok {
			return false
		}
		found = carrier.RetainedOutput()
		return found != ""
	})
	return found
}

func commandOf(err error) *runner.Invocation {
	var found *runner.Invocation
	walkCauses(err, func(e error) bool {
		carrier, ok := e.(commandCarrier)
		if !ok {
			return false
		}
		found = carrier.Command()
		return found != nil
	})
	return found
}

func walkCauses(err error, visit func(error) bool) bool {
	for err != nil {
		if visit(err) {
			return true
		}
		switch cause := err.(type) {
		case interface{ Unwrap() error }:
			err = cause.Unwrap()
		case interface{ Unwrap() []error }:
			for _, branch := range cause.Unwrap() {
				if walkCauses(branch, visit) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

func renderArgv(argv []string) string {
	rendered := make([]string, len(argv))
	for i, arg := range argv {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\v\f\r\"") {
			rendered[i] = arg
			continue
		}
		rendered[i] = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
	}
	return strings.Join(rendered, " ")
}

func splitCode(line string) (code, rest string, ok bool) {
	const width = len("GOM0000")
	if len(line) < width+2 || !strings.HasPrefix(line, "GOM") {
		return "", "", false
	}
	for i := 3; i < width; i++ {
		if line[i] < '0' || line[i] > '9' {
			return "", "", false
		}
	}
	if line[width] != ':' || line[width+1] != ' ' {
		return "", "", false
	}
	return line[:width], line[width+2:], true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
