// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// A Code is a stable, user-facing diagnostic code. This package owns the
// GOM10xx block: mistakes in how go-mutants was invoked, as opposed to mistakes
// in what it was asked to do.
type Code string

// The command line codes.
const (
	// CodeUsage reports an invocation the command line could not accept: an
	// unknown command, an unknown flag, a flag with an unusable value, a
	// positional argument where none belongs. It is also the code an error
	// that carries no code of its own is reported under, since cobra and pflag
	// produce plain errors.
	CodeUsage Code = "GOM1001"
	// CodeTestArgv reports a `--` passthrough that cannot be run: nothing after
	// the separator, or a blank program name. It is separate from
	// [CodeUsage] because the remedy is specific and the mistake is easy to
	// make in a shell script, where an unset variable expands to nothing.
	CodeTestArgv Code = "GOM1002"
	// CodeWorkingDirectory reports a working directory that cannot be read.
	// The workspace root is the directory go-mutants was invoked in, so there
	// is nothing to run against when this fails.
	CodeWorkingDirectory Code = "GOM1003"
	// CodeConflictingFlags reports two flags that contradict each other, such
	// as `--json` with `--quiet`. It is separate from [CodeUsage] because
	// neither flag is wrong on its own: the remedy is to drop one, not to fix
	// a value.
	CodeConflictingFlags Code = "GOM1004"
	// CodeInvalidMutantPrefix reports a `--mutant` value that is not a mutant
	// id prefix at all — the wrong alphabet, or too short to mean anything.
	// A well-formed prefix that matches no mutant is not this: `--mutant` is a
	// filter, and an empty listing is an answer.
	CodeInvalidMutantPrefix Code = "GOM1005"
	// CodeUnimplementedOperators reports a selection that names only operators
	// this pre-release build cannot discover yet. It is a warning rather than
	// an error — the listing is genuinely empty — and it exists so that the
	// emptiness is never mistaken for a statement about the user's code.
	CodeUnimplementedOperators Code = "GOM1006"
	// CodeCatalogMismatch reports a catalogued mutant that discovery did not
	// report as a candidate. It is an internal invariant violation and always a
	// bug: the alternative is a listing whose coordinates point at nothing.
	CodeCatalogMismatch Code = "GOM1007"
	// CodeInertProfile reports a `--profile` that decided nothing because the
	// configuration file names operators, which take precedence over any tier.
	// It is a warning rather than an error — the listing is a real listing, of
	// the operators the file asked for — and it exists because the alternative
	// is a flag typed for this invocation losing to a file with no diagnostic,
	// which is the opposite of the precedence the help text promises.
	CodeInertProfile Code = "GOM1008"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM10xx block.
var codes = []Code{
	CodeUsage,
	CodeTestArgv,
	CodeWorkingDirectory,
	CodeConflictingFlags,
	CodeInvalidMutantPrefix,
	CodeUnimplementedOperators,
	CodeCatalogMismatch,
	CodeInertProfile,
}

// Codes returns every diagnostic code this package can report, in numeric
// order.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one command line failure carrying a stable [Code].
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without repeating the code.
	Message string
	// Hint is an optional second line naming the remedy. It is separate from
	// Message so that the first line of every error stays greppable.
	Hint string
	// Err is the underlying cause, or nil.
	Err error
}

// Error renders "GOM1001: <message>", with the cause appended when there is
// one. The hint is not part of it; see [RenderError].
func (e *Error) Error() string {
	msg := string(e.Code) + ": " + e.Message
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// usagef builds a usage error with the standard hint.
func usagef(format string, args ...any) *Error {
	return &Error{
		Code:    CodeUsage,
		Message: fmt.Sprintf(format, args...),
		Hint:    "run `go-mutants --help` to see the commands and flags",
	}
}

// An exitError carries an exit status that has already been decided, for the
// two conditions the error text alone cannot express: which signal ended the
// run, and therefore whether the answer is 130 or 143.
type exitError struct {
	code mutation.ExitCode
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// ExitCode maps an error from the command tree onto the documented exit status.
//
// The table is short by design. Everything that is not an interruption and not
// an opt-in policy failure is exit 2, usage mistakes included: a run that never
// started produced no measurement, and reporting "no policy failed" for it
// would be a lie a CI job would believe.
func ExitCode(err error) mutation.ExitCode {
	if err == nil {
		return mutation.ExitOK
	}
	var decided *exitError
	if errors.As(err, &decided) {
		return decided.code
	}
	return mutation.ExitInfrastructure
}

// RenderError writes err to w as one or more "error GOMxxxx: message" lines.
//
// Every package go-mutants funnels errors from already renders itself as
// "GOMxxxx: ...", so the code is lifted out of the text and re-emitted rather
// than prefixed onto it — otherwise every line would read "error GOM3002:
// GOM3002: ...". An error with no code at all is reported under [CodeUsage],
// which is what cobra and pflag produce and what they always mean.
//
// Two shapes are handled beyond the single line. A configuration file with
// several problems renders as one coded line per problem, and each is prefixed
// separately so that all of them stay greppable. A failed child process carries
// a tail of its own output, which is indented underneath rather than folded
// into the message, because it is the only part of an error that is not ours to
// word — that tail is deliberately left uncoded and indented, since it is the
// child's words and not a diagnostic of ours.
//
// A line inside a coded error that carries no code of its own inherits the code
// above it. Nothing go-mutants writes should produce one — a message is a
// single line, and internal/discover folds a multi-line loader blob before it
// gets here — but "every line is greppable" is the promise, and inheriting is
// the only repair that keeps a continuation attached to the error it belongs to
// instead of inventing a code for it. Blank lines are dropped rather than
// rendered as a code with nothing after it.
//
// The whole report is composed in memory and written once. A half-printed error
// is worse than an unprinted one, and there is nothing useful to do about a
// failure to write to standard error anyway, so the single write's result is
// deliberately dropped rather than checked and ignored five times over.
func RenderError(w io.Writer, err error) {
	if err == nil {
		return
	}
	var b strings.Builder
	text := strings.TrimRight(err.Error(), "\n")
	if _, _, coded := splitCode(firstLine(text)); coded {
		// current is the code the next uncoded line inherits. The branch is only
		// entered when the first line carries one, so the seed is never printed.
		current := string(CodeUsage)
		for _, raw := range strings.Split(text, "\n") {
			line := strings.TrimRight(raw, "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			code, rest, ok := splitCode(line)
			if !ok {
				code, rest = current, line
			}
			current = code
			fmt.Fprintf(&b, "error %s: %s\n", code, rest)
		}
	} else {
		// An error with no code at all is cobra's or pflag's, which is always one
		// line. It is still split, so that the one shape this function must never
		// produce — a line standing on its own with no "error " in front of it —
		// is unreachable rather than merely unlikely.
		for _, raw := range strings.Split(text, "\n") {
			fmt.Fprintf(&b, "error %s: %s\n", CodeUsage, strings.TrimRight(raw, "\r"))
		}
	}

	var cliErr *Error
	if errors.As(err, &cliErr) && cliErr.Hint != "" {
		b.WriteString("hint: " + cliErr.Hint + "\n")
	}
	if output := engine.OutputOf(err); output != "" {
		for _, line := range strings.Split(output, "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
	_, _ = io.WriteString(w, b.String())
}

// splitCode lifts a leading "GOM####: " off a line.
//
// It is a hand-rolled scan rather than a regular expression because it runs on
// the error path, where the fewer moving parts the better, and because the
// shape it accepts is the whole of the contract: three letters, four digits, a
// colon, a space. Anything else is left alone.
func splitCode(line string) (code, rest string, ok bool) {
	const width = len("GOM0000")
	if len(line) < width+2 || !strings.HasPrefix(line, "GOM") {
		return "", "", false
	}
	for i := 3; i < width; i++ {
		if line[i] < '0' || line[i] > '9' {
			return "", "", false
		}
	}
	if line[width] != ':' || line[width+1] != ' ' {
		return "", "", false
	}
	return line[:width], line[width+2:], true
}

// firstLine returns everything before the first newline.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
