// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"errors"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/runner"
)

type Code string

const (
	CodeOptions         Code = "GOM7501"
	CodeBinDir          Code = "GOM7502"
	CodeListFailed      Code = "GOM7503"
	CodeListUnreadable  Code = "GOM7504"
	CodeTestBuildFailed Code = "GOM7505"
)

const (
	CodeNoTestBinaries     Code = "GOM7510"
	CodeMutantInvalid      Code = "GOM7511"
	CodeScratchDir         Code = "GOM7512"
	CodeMutantStart        Code = "GOM7513"
	CodeStaleCatalog       Code = "GOM7514"
	CodeProbeInvalid       Code = "GOM7515"
	CodeProbeStart         Code = "GOM7516"
	CodeProbeLog           Code = "GOM7517"
	CodeControlInvalid     Code = "GOM7518"
	CodeControlStart       Code = "GOM7519"
	CodeInterrupted        Code = "GOM7520"
	CodeTestLogUnsupported Code = "GOM7521"
)

const (
	CodeCoverageDir    Code = "GOM7530"
	CodeCoverageFailed Code = "GOM7531"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeOptions,
	CodeBinDir,
	CodeListFailed,
	CodeListUnreadable,
	CodeTestBuildFailed,
	CodeNoTestBinaries,
	CodeMutantInvalid,
	CodeScratchDir,
	CodeMutantStart,
	CodeStaleCatalog,
	CodeProbeInvalid,
	CodeProbeStart,
	CodeProbeLog,
	CodeControlInvalid,
	CodeControlStart,
	CodeInterrupted,
	CodeTestLogUnsupported,
	CodeCoverageDir,
	CodeCoverageFailed,
}

func Codes() []Code { return slices.Clone(codes) }

const OutputTailLines = 50

type Error struct {
	Code    Code
	Message string
	Output  string
	Err     error

	Invocation *runner.Invocation

	Package  string
	ExitCode int
	TimedOut bool
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
