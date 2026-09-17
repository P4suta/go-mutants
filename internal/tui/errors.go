// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import "slices"

type Code string

const (
	CodeProgram Code = "GOM7701"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeProgram,
}

func Codes() []Code { return slices.Clone(codes) }

type Error struct {
	Code    Code
	Message string
	Err     error
}

func (e *Error) Error() string {
	s := string(e.Code) + ": " + e.Message
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }
