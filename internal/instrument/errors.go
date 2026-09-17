// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"errors"
	"slices"
	"strings"
)

type Code string

const (
	CodeUntokenizable       Code = "GOM7301"
	CodeRawStringConversion Code = "GOM7302"
	CodeNotIdentical        Code = "GOM7303"
	CodeNotFlat             Code = "GOM7304"

	CodeSpliceSpan     Code = "GOM7310"
	CodeSpliceMismatch Code = "GOM7311"
	CodeSpliceOverlap  Code = "GOM7312"
	CodeSpanStraddles  Code = "GOM7313"

	CodeOptions          Code = "GOM7320"
	CodeSourceUnreadable Code = "GOM7321"
	CodeUnparsable       Code = "GOM7322"
	CodeUnsupportedGuard Code = "GOM7323"
	CodeSiteNotFound     Code = "GOM7324"
	CodeSiteConflict     Code = "GOM7325"
	CodeLineDrift        Code = "GOM7326"
	CodeImportInjection  Code = "GOM7327"
	CodeWriteFailed      Code = "GOM7328"
	CodeMissingGuard     Code = "GOM7329"
	CodeInfectionLog     Code = "GOM7330"
	CodeLoopCensus       Code = "GOM7331"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeUntokenizable,
	CodeRawStringConversion,
	CodeNotIdentical,
	CodeNotFlat,
	CodeSpliceSpan,
	CodeSpliceMismatch,
	CodeSpliceOverlap,
	CodeSpanStraddles,
	CodeOptions,
	CodeSourceUnreadable,
	CodeUnparsable,
	CodeUnsupportedGuard,
	CodeSiteNotFound,
	CodeSiteConflict,
	CodeLineDrift,
	CodeImportInjection,
	CodeWriteFailed,
	CodeMissingGuard,
	CodeInfectionLog,
	CodeLoopCensus,
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
