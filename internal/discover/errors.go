// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"errors"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/glob"
)

// A Code is a stable, user-facing diagnostic code.
//
// This package owns the GOM41xx block. Like the orchestration codes it does
// not re-code the failures of the packages it uses: a toolchain that cannot be
// located is reported by internal/gocmd with its own code, because two
// identifiers for one condition means a user searching for the wrong one.
type Code string

// The discovery codes.
const (
	// CodeSnapshotRoot reports a snapshot root that is empty, cannot be
	// resolved, or is not a directory. It is a caller mistake rather than a
	// fact about the tree under test.
	CodeSnapshotRoot Code = "GOM4101"
	// CodeWorkspace reports a `go.work` file at the snapshot root. Multi-module
	// workspaces are not supported in v1: one module path, one set of
	// module-relative identities, one baseline. Saying so is the honest answer;
	// mutating the first module and quietly ignoring the rest is not.
	//
	// A workspace file outside the snapshot is neither reported nor obeyed. The
	// loader runs with GOWORK=off, so a snapshot that happens to sit below
	// somebody else's `go.work` — a temporary directory inside one, say — is
	// still discovered as the single module it contains.
	CodeWorkspace Code = "GOM4102"
	// CodePattern reports an include or exclude pattern that does not compile.
	// It is allocated here, and not in internal/engine, because which files are
	// worth mutating is discovery's question — the retired GOM4002 was the same
	// condition asked in the wrong place.
	CodePattern Code = "GOM4103"

	// CodeLoadFailed reports that the package loader itself could not run:
	// no `go` command reachable, a driver that failed, a cancelled context.
	// Nothing is known about the tree yet at this point.
	CodeLoadFailed Code = "GOM4110"
	// CodePackageErrors reports packages that failed to load or type-check.
	// Discovery requires a compiling tree; see the package documentation for
	// why this is an error and not a warning.
	CodePackageErrors Code = "GOM4111"
	// CodeModuleNotFound reports a snapshot root that no loaded package calls
	// its module root: an empty directory, a directory inside somebody else's
	// module, or a tree with no Go packages at all.
	CodeModuleNotFound Code = "GOM4112"

	// CodeUnknownRule reports a requested rule that the canonical registry does
	// not know, or knows with different metadata. Rules the registry knows but
	// this phase does not implement yet are ignored instead; see
	// [SupportedRules].
	CodeUnknownRule Code = "GOM4120"

	// CodeSpanMismatch reports that a candidate's byte span does not cover the
	// text the rule says it replaces. It is an internal invariant violation and
	// always a bug in this package: the alternative to failing loudly is
	// splicing the wrong bytes into somebody's source in a later phase.
	CodeSpanMismatch Code = "GOM4130"
	// CodeInvalidCandidate reports a candidate that internal/mutation refused.
	// Same category as [CodeSpanMismatch], caught one layer further down.
	CodeInvalidCandidate Code = "GOM4131"

	// CodeFileUnreadable reports a source file that could not be read, or that
	// is too large to address with the 32-bit span offsets identities use.
	CodeFileUnreadable Code = "GOM4140"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM41xx
// block.
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

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one discovery failure carrying a stable [Code].
//
// It mirrors the shape internal/engine and internal/gocmd use — code, one-line
// message, optional cause — so a single renderer can lay all three out the
// same way, without the three packages sharing an error identity.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code.
	Message string
	// Err is the underlying cause, or nil. It stays reachable through
	// errors.Is, which is how the command line recognises a cancellation.
	Err error
}

// Error renders "GOM4111: <message>", with the cause appended when there is
// one.
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

// CompilePatterns compiles include or exclude patterns, reporting the first
// one that does not parse as a [CodePattern] error.
//
// It exists so that every pattern a run uses is compiled in one place, with
// one code, whether it came from `.go-mutants.toml` or from a flag. The
// underlying *[glob.SyntaxError] stays reachable with errors.As, so a caller
// that wants to underline the offending byte still can.
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
