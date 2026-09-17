// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/runner"
)

type Code string

const (
	CodeWorkspaceRoot Code = "GOM4001"

	CodeTestCommand Code = "GOM4003"
	CodeScratchDir  Code = "GOM4004"
	CodeRunID       Code = "GOM4005"

	CodeBaselineBuildFailed        Code = "GOM4010"
	CodeBaselineTestFailed         Code = "GOM4011"
	CodeBaselineTimedOut           Code = "GOM4012"
	CodeInstrumentedBaselineFailed Code = "GOM4013"
	CodeWorkspaceDrift             Code = "GOM4014"
	CodeCoverageRender             Code = "GOM4015"

	CodeTimeoutTooSmall Code = "GOM4020"
	CodeUnknownOperator Code = "GOM4021"
	CodeTestScope       Code = "GOM4022"

	CodeInterrupted Code = "GOM4030"
)

const (
	CodeSnapshotNotRemoved     Code = "GOM4040"
	CodeScratchNotRemoved      Code = "GOM4041"
	CodeReportNotPublished     Code = "GOM4042"
	CodeSelectedMutantRejected Code = "GOM4043"
	CodeOrphanNotRemoved       Code = "GOM4044"
	CodeTemporaryNotKept       Code = "GOM4045"
	CodeDeadlineExceeded       Code = "GOM4046"
	CodeMemoryBoundUnavailable Code = "GOM4047"
	CodeBaselineFromTestCache  Code = "GOM4048"
	CodeLoopCensusUnusable     Code = "GOM4049"

	CodeChangedTestsUnaccounted Code = "GOM4050"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeWorkspaceRoot,
	CodeTestCommand,
	CodeScratchDir,
	CodeRunID,
	CodeBaselineBuildFailed,
	CodeBaselineTestFailed,
	CodeBaselineTimedOut,
	CodeInstrumentedBaselineFailed,
	CodeWorkspaceDrift,
	CodeCoverageRender,
	CodeTimeoutTooSmall,
	CodeUnknownOperator,
	CodeTestScope,
	CodeInterrupted,
	CodeSnapshotNotRemoved,
	CodeScratchNotRemoved,
	CodeReportNotPublished,
	CodeSelectedMutantRejected,
	CodeOrphanNotRemoved,
	CodeTemporaryNotKept,
	CodeDeadlineExceeded,
	CodeMemoryBoundUnavailable,
	CodeBaselineFromTestCache,
	CodeLoopCensusUnusable,
	CodeChangedTestsUnaccounted,
}

func Codes() []Code { return slices.Clone(codes) }

const OutputTailLines = 50

type Error struct {
	Code    Code
	Message string
	Output  string
	Err     error

	Invocation *runner.Invocation
}

func (e *Error) RetainedOutput() string { return e.Output }

func (e *Error) Command() *runner.Invocation { return e.Invocation }

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

type SelectionError struct {
	Prefix string
	Err    error
}

func (e *SelectionError) Error() string {
	return "--mutant " + strconv.Quote(e.Prefix) + " did not select one mutant: " + e.Err.Error()
}

func (e *SelectionError) Unwrap() error { return e.Err }

func OutputOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Output
	}
	return ""
}

func tail(output []byte) string {
	text := strings.TrimRight(string(output), "\r\n \t")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > OutputTailLines {
		lines = lines[len(lines)-OutputTailLines:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}
