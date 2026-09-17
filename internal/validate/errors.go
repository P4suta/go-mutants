// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"errors"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/runner"
)

type Code string

const (
	CodeOptions          Code = "GOM7401"
	CodeSourceUnreadable Code = "GOM7402"

	CodeBuildFailed   Code = "GOM7410"
	CodeBuildTimedOut Code = "GOM7411"
	CodeInterrupted   Code = "GOM7412"

	CodeNotMutantInduced Code = "GOM7420"
	CodeStillFailing     Code = "GOM7421"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeOptions,
	CodeSourceUnreadable,
	CodeBuildFailed,
	CodeBuildTimedOut,
	CodeInterrupted,
	CodeNotMutantInduced,
	CodeStillFailing,
}

func Codes() []Code { return slices.Clone(codes) }

type Error struct {
	Code    Code
	Message string
	Output  string
	Err     error

	Invocation *runner.Invocation

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
