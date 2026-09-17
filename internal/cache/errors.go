// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"slices"
	"strings"
)

type Code string

const (
	CodeUnavailable          Code = "GOM7901"
	CodeExecutableUnreadable Code = "GOM7902"
	CodeInvalidContext       Code = "GOM7903"
	CodeCorruptEntry         Code = "GOM7904"
	CodeEntryNotWritten      Code = "GOM7905"

	CodeScanFailed Code = "GOM7910"
	CodeNotRemoved Code = "GOM7911"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeUnavailable,
	CodeExecutableUnreadable,
	CodeInvalidContext,
	CodeCorruptEntry,
	CodeEntryNotWritten,
	CodeScanFailed,
	CodeNotRemoved,
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
