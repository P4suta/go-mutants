// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"errors"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/glob"
)

type Code string

const (
	CodeSnapshotRoot Code = "GOM4101"
	CodeWorkspace    Code = "GOM4102"
	CodePattern      Code = "GOM4103"

	CodeLoadFailed     Code = "GOM4110"
	CodePackageErrors  Code = "GOM4111"
	CodeModuleNotFound Code = "GOM4112"

	CodeUnknownRule Code = "GOM4120"

	CodeSpanMismatch     Code = "GOM4130"
	CodeInvalidCandidate Code = "GOM4131"

	CodeFileUnreadable Code = "GOM4140"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeSnapshotRoot,
	CodeWorkspace,
	CodePattern,
	CodeLoadFailed,
	CodePackageErrors,
	CodeModuleNotFound,
	CodeUnknownRule,
	CodeSpanMismatch,
	CodeInvalidCandidate,
	CodeFileUnreadable,
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

func CompilePatterns(patterns []string) ([]glob.Pattern, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	compiled := make([]glob.Pattern, 0, len(patterns))
	for _, p := range patterns {
		pattern, err := glob.Compile(p)
		if err != nil {
			return nil, &Error{Code: CodePattern, Message: "invalid pattern", Err: err}
		}
		compiled = append(compiled, pattern)
	}
	return compiled, nil
}
