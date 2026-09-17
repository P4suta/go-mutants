// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import "errors"

const (
	CodeSupervisionUnavailable = "GOM7201"
	CodeProcessStartFailed     = "GOM7202"
	CodeSpecInvalid            = "GOM7203"
	CodeProcessWaitFailed      = "GOM7204"
)

type Error struct {
	Code    string
	Message string
	Err     error

	Invocation *Invocation

	Output string
}

func (e *Error) RetainedOutput() string { return e.Output }

func (e *Error) Command() *Invocation { return e.Invocation }

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
