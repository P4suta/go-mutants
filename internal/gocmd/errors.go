// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import "errors"

// Stable error codes for this package.
//
// They share the GOM72xx process-layer block with internal/runner and do not
// overlap with the codes defined there. Each code is defined exactly once, and
// each names a failure a user can act on differently: install a toolchain, fix
// a path, or report a toolchain that answers `go version` with something no
// released Go has ever printed.
const (
	// CodeToolchainNotFound reports that no `go` executable could be located —
	// nothing on PATH, nothing at the explicitly configured path, or something
	// found only relative to a working directory this process can no longer
	// read, which leaves it just as unreachable.
	CodeToolchainNotFound = "GOM7210"
	// CodeVersionProbeFailed reports that the executable was found but
	// `go version` could not be run, timed out, or exited non-zero. The path
	// exists; whether it is a Go toolchain is exactly what is in doubt.
	CodeVersionProbeFailed = "GOM7211"
	// CodeVersionUnparsable reports that `go version` ran and printed
	// something this package cannot read as a version line. It is kept
	// separate from a failed probe because the remedy is different: this one
	// is either not the Go toolchain or a format change worth a bug report.
	CodeVersionUnparsable = "GOM7212"
)

// Error is a toolchain failure carrying a stable GOM#### code.
//
// It mirrors internal/runner's error type rather than importing it. The two
// packages share an error *shape* so that a future common renderer can treat
// them alike, but not an error *identity*: a caller matching on a toolchain
// failure should not have to reason about whether the process layer might
// produce the same value.
type Error struct {
	// Code is the stable GOM#### identifier.
	Code string
	// Message is a one-line explanation. Where it names a remedy it names a
	// concrete one, because the audience for these errors is somebody whose
	// first go-mutants run has just failed.
	Message string
	// Err is the underlying cause, if any.
	Err error
}

// Error renders the code, the message, and the cause.
func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the GOM#### code carried by err, or "" if err did not come
// from this package.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
