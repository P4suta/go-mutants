// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package schemas

import (
	"errors"
	"slices"
)

type Code string

const (
	CodeUnknownDocument Code = "GOM5001"

	CodeMalformedJSON Code = "GOM5002"

	CodeInvalidDocument Code = "GOM5003"

	CodeSchemaUnusable Code = "GOM5004"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeUnknownDocument,
	CodeMalformedJSON,
	CodeInvalidDocument,
	CodeSchemaUnusable,
}

func Codes() []Code { return slices.Clone(codes) }

type Error struct {
	Code         Code
	DocumentType string
	Pointer      string
	Message      string
	Err          error
}

func (e *Error) Error() string { return string(e.Code) + ": schemas: " + e.Message }

func (e *Error) Unwrap() error { return e.Err }

func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func PointerOf(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Pointer, true
	}
	return "", false
}
