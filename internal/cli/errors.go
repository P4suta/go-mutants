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
//
// It also owns three blocks that belong to one command each — GOM80xx to
// `doctor`, GOM81xx to `init`, and GOM82xx to `report list|latest|clean` —
// rather than more GOM10xx numbers. Those commands do not measure anything, so
// none of their failures is a mistake in an invocation; a user reading one
// should see at a glance that it is about the environment, the configuration
// file, or the run history, and that the remedy is in that one place.
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
	// CodeMutantUnresolved reports a `run --mutant` prefix that is well formed
	// and did not select exactly one mutant: nothing matched, or several did.
	// It is separate from [CodeInvalidMutantPrefix], which is a prefix that
	// could never match anything, because the remedies are different — one is
	// fixed by retyping the value and the other by looking at what it matched —
	// and separate from `list`'s reading of the same flag, where a prefix
	// matching several mutants lists all of them and is not an error at all.
	CodeMutantUnresolved Code = "GOM1009"
	// CodeUnreadableReport reports a file named on the command line that could
	// not be read at all: a path that does not exist, a directory, a file
	// without permission. It is a usage code rather than an infrastructure one
	// because the mistake is in the command line and the remedy is to name a
	// different path.
	CodeUnreadableReport Code = "GOM1010"
	// CodeInvalidReportDocument reports a file that was read and is not a run
	// report this build can use: not JSON, the wrong document type, a schema
	// version from another release, or a document the published schema refuses.
	// The failure it carries keeps its own code — this package does not re-code
	// what internal/report and internal/schemas decided — and this one names the
	// file, which is what `report merge` over several of them needs.
	CodeInvalidReportDocument Code = "GOM1011"
	// CodeGitHubSummary reports that the GitHub Actions step summary could not
	// be appended to. It is printed and never returned: the summary and the
	// survivor annotations are a convenience on top of a run that has already
	// executed its mutants, filed its report, and printed its closing block, and
	// letting a failure to decorate a job page turn a failed score gate's exit 1
	// into an exit 2 would tell a CI job that the tool broke when the truth is
	// that the tests missed something. See internal/cli's emitGitHub.
	CodeGitHubSummary Code = "GOM1012"
)

// The `doctor` codes, which are the GOM80xx block. There is one, and that is
// the design: every individual check reports itself as a row of the table with
// its own words, so the command's only failure is the fact that a row said
// fail.
const (
	// CodeEnvironmentUnusable reports that at least one `doctor` check failed.
	// The table has already named which and why, so this carries the count and
	// nothing else — and it is what makes the command exit 2, which is the
	// answer a CI job branches on.
	CodeEnvironmentUnusable Code = "GOM8001"
)

// The `init` codes, which are the GOM81xx block.
const (
	// CodeConfigurationExists reports a `.go-mutants.toml` that is already
	// there. `init` never overwrites and has no --force: a configuration file is
	// hand-edited, it is the only record of decisions nobody wrote down twice,
	// and a flag that replaces it wholesale is a flag somebody will type by
	// accident. Deleting the file first is the deliberate act that flag would
	// have pretended to be.
	CodeConfigurationExists Code = "GOM8101"
	// CodeConfigurationUnreadable reports a `.go-mutants.toml` that `init
	// --check` could not read: a directory of that name, a permission failure.
	// A file that is not there is not this — it is a check that failed, since
	// `init` would have created it.
	CodeConfigurationUnreadable Code = "GOM8102"
	// CodeConfigurationNotWritten reports a configuration file that could not be
	// created: a directory that is not writable, a disk that is full, a name
	// taken by something that is not a file.
	CodeConfigurationNotWritten Code = "GOM8103"
	// CodeConfigurationStale reports `init --check` finding a file that is not
	// what this build of `init` would write. It is the one failure in this
	// package that exits 1 rather than 2, because it is an opt-in gate a CI job
	// asked for rather than anything being wrong with the machine; see
	// [initLong].
	CodeConfigurationStale Code = "GOM8104"
)

// The run-history codes, which are the GOM82xx block: what `report list`,
// `report latest` and `report clean` refuse. The store's own failures — a
// directory that cannot be walked, a marker naming somebody else, a file that
// will not delete — keep internal/report's codes, because this package does not
// re-code what the store decided.
const (
	// CodeNotAModuleRoot reports a history command run somewhere that is not a
	// module root. A run is filed under the module it measured, so without a
	// go.mod there is nothing to say which history is being asked about — and
	// for `report clean`, which deletes, guessing would be the worst possible
	// answer.
	CodeNotAModuleRoot Code = "GOM8201"
	// CodeNoStoredRun reports `report latest` finding no run for this module.
	// An empty *listing* is an answer and exits 0; a `latest` with nothing to
	// print is not, because the command's whole output is one document and
	// there is none.
	CodeNoStoredRun Code = "GOM8202"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside one of the four
// blocks this package owns: GOM10xx for the command line itself, GOM80xx for
// `doctor`, GOM81xx for `init`, and GOM82xx for the run-history commands.
var codes = []Code{
	CodeUsage,
	CodeTestArgv,
	CodeWorkingDirectory,
	CodeConflictingFlags,
	CodeInvalidMutantPrefix,
	CodeUnimplementedOperators,
	CodeCatalogMismatch,
	CodeInertProfile,
	CodeMutantUnresolved,
	CodeUnreadableReport,
	CodeInvalidReportDocument,
	CodeGitHubSummary,
	CodeEnvironmentUnusable,
	CodeConfigurationExists,
	CodeConfigurationUnreadable,
	CodeConfigurationNotWritten,
	CodeConfigurationStale,
	CodeNotAModuleRoot,
	CodeNoStoredRun,
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
// conditions the error text alone cannot express: which signal ended the run,
// and therefore whether the answer is 130 or 143, and a policy gate that failed
// on a run which did everything right.
type exitError struct {
	code mutation.ExitCode
	err  error
	// silent suppresses the "error GOM....:" line [RenderError] would otherwise
	// write. It is for the one failure the user has already been told about in
	// full: a policy gate reports itself in the run's closing summary, naming
	// the survivors and the score, and repeating a shortened version of that on
	// standard error would both duplicate it and dress a correct measurement up
	// as something having gone wrong.
	silent bool
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
// One error renders as nothing at all: a failed policy gate, which has already
// reported itself in the run's closing summary. See [exitError].
//
// The whole report is composed in memory and written once. A half-printed error
// is worse than an unprinted one, and there is nothing useful to do about a
// failure to write to standard error anyway, so the single write's result is
// deliberately dropped rather than checked and ignored five times over.
func RenderError(w io.Writer, err error) {
	if err == nil {
		return
	}
	var decided *exitError
	if errors.As(err, &decided) && decided.silent {
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
