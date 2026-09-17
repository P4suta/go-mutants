// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import (
	"errors"

	"github.com/P4suta/go-mutants/internal/runner"
)

const (
	CodeToolchainNotFound  = "GOM7210"
	CodeVersionProbeFailed = "GOM7211"
	CodeVersionUnparsable  = "GOM7212"
)

type Error struct {
	Code    string
	Message string
	Err     error

	Invocation *runner.Invocation

	Output string
}

func (e *Error) RetainedOutput() string { return e.Output }

func (e *Error) Command() *runner.Invocation { return e.Invocation }

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
