// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"errors"
	"slices"
	"strings"
)

type Code string

const (
	CodeGitUnavailable      Code = "GOM7710"
	CodeNotARepository      Code = "GOM7711"
	CodeNoUpstream          Code = "GOM7712"
	CodeUnknownRef          Code = "GOM7713"
	CodeDiffFailed          Code = "GOM7714"
	CodeMalformedDiff       Code = "GOM7715"
	CodeUntrackedUnreadable Code = "GOM7716"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeGitUnavailable,
	CodeNotARepository,
	CodeNoUpstream,
	CodeUnknownRef,
	CodeDiffFailed,
	CodeMalformedDiff,
	CodeUntrackedUnreadable,
}

func Codes() []Code { return slices.Clone(codes) }

type Error struct {
	Code    Code
	Message string
	Output  string
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

func OutputOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Output
	}
	return ""
}
