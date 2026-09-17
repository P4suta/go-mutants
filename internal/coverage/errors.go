// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"errors"
	"slices"
	"strings"
)

type Code string

const (
	CodeMalformedProfile Code = "GOM7600"

	CodeCustomTestCommand Code = "GOM7601"

	CodeUnavailable Code = "GOM7602"

	CodeOrderDependentTests Code = "GOM7603"

	CodeUnreliableTestSet Code = "GOM7604"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeMalformedProfile,
	CodeCustomTestCommand,
	CodeUnavailable,
	CodeOrderDependentTests,
	CodeUnreliableTestSet,
}

func Codes() []Code { return slices.Clone(codes) }

type Error struct {
	Code    Code
	Message string
	Err     error
}

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
