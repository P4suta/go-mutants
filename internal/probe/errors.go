// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probe

import "errors"

type Code string

const (
	CodeUnavailable Code = "GOM7101"

	CodeNothingToProbe Code = "GOM7102"

	CodeInconsistent Code = "GOM7103"
)

func Codes() []Code {
	return []Code{CodeUnavailable, CodeNothingToProbe, CodeInconsistent}
}

var ErrInconsistent = errors.New("probe: an infection log names a mutant this catalogue does not hold")

type Error struct {
	Code    Code
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return string(e.Code) + ": " + e.Message + ": " + e.Err.Error()
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
