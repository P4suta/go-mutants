// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"errors"
	"fmt"
	"strings"
)

type Code string

const (
	CodeInvalidOptions Code = "GOM7001"

	CodeSourceRoot Code = "GOM7002"

	CodeWalk Code = "GOM7003"

	CodeSymlink Code = "GOM7004"

	CodeReparsePoint Code = "GOM7005"

	CodeIrregular Code = "GOM7006"

	CodeUnsupportedName Code = "GOM7007"

	CodeDestination Code = "GOM7008"

	CodeCopy Code = "GOM7009"

	CodeCleanupRefused Code = "GOM7010"

	CodeCleanupFailed Code = "GOM7011"

	CodeRestoreFailed Code = "GOM7012"
)

type Error struct {
	Code    Code
	Path    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": snapshot: ")
	b.WriteString(e.Message)
	if e.Path != "" {
		fmt.Fprintf(&b, ": %q", e.Path)
	}
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
