// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package schemas

import (
	"errors"
	"slices"
)

// A Code is a stable, user-facing diagnostic code. Codes are part of the
// command line interface: they are printed, searched for, and quoted in bug
// reports, so a code is allocated once and never reused for a different
// meaning.
//
// This package owns GOM5001 through GOM5009 inside the GOM50xx schema and
// report block. The rest of the block belongs to internal/report, which writes
// the documents this package checks.
type Code string

// The schema diagnostic codes.
const (
	// CodeUnknownDocument reports a document type this build has no schema
	// for. It is a failure rather than a pass: the caller asked for a document
	// to be checked and it was not checked.
	CodeUnknownDocument Code = "GOM5001"

	// CodeMalformedJSON reports bytes that are not a JSON document at all, so
	// there is nothing to validate. It is kept apart from
	// [CodeInvalidDocument] because the remedy is different in kind: this one
	// means the encoder produced garbage, not that a field is wrong.
	CodeMalformedJSON Code = "GOM5002"

	// CodeInvalidDocument reports a well formed JSON document that the schema
	// rejects. The [Error] carries the JSON pointer of the value to look at.
	CodeInvalidDocument Code = "GOM5003"

	// CodeSchemaUnusable reports an embedded schema that could not be read or
	// compiled. It is a bug in this repository rather than in anything a user
	// did, and it is reported instead of panicking so that a broken schema
	// takes down one report format rather than the whole run.
	CodeSchemaUnusable Code = "GOM5004"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the block this
// package owns.
var codes = []Code{
	CodeUnknownDocument,
	CodeMalformedJSON,
	CodeInvalidDocument,
	CodeSchemaUnusable,
}

// Codes returns every diagnostic code this package can report, in numeric
// order.
func Codes() []Code { return slices.Clone(codes) }

// An Error is every failure this package returns, so a caller can always reach
// the [Code] and the [Error.Pointer] with errors.As rather than by matching on
// message text.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// DocumentType is the document type the caller asked to validate. It is
	// present on every error, including [CodeUnknownDocument], where it is the
	// unknown name itself.
	DocumentType string
	// Pointer is the RFC 6901 JSON pointer of the value that failed, chosen
	// deterministically when a document breaks the schema in several places at
	// once. It is the empty pointer for a failure at the document root, and it
	// is empty for failures that are not about a location in a document — an
	// unknown document type, unparsable bytes, an unusable schema.
	Pointer string
	// Message states the problem on one line, without the code. It already
	// names the pointer and the underlying complaint where there is one, so a
	// caller can print it alone.
	Message string
	// Err is the underlying cause: the JSON syntax error, the validator's full
	// error tree, or the failure that made a schema unusable.
	Err error
}

// Error renders "GOM5003: schemas: <message>".
//
// The cause is deliberately not appended. For a validation failure it is a
// multi-line tree listing every violation at every depth, and pasting that
// after the colon would break the one-error-one-line shape the rest of
// go-mutants renders — the summary in Message names the first violation, and
// the tree stays reachable through errors.As for a caller that wants all of
// them.
func (e *Error) Error() string { return string(e.Code) + ": schemas: " + e.Message }

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the [Code] carried by err, or the empty Code if err did not
// come from this package.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// PointerOf returns the JSON pointer carried by err, and whether err came from
// this package at all. The distinction matters because the empty pointer is a
// real answer: it names the document root.
func PointerOf(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Pointer, true
	}
	return "", false
}
