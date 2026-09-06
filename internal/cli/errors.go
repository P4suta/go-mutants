// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
)

// A Code is a stable, user-facing diagnostic code. This package owns the
// GOM10xx block: mistakes in how go-mutants was invoked, as opposed to mistakes
// in what it was asked to do.
//
// It also owns four blocks that belong to one command each — GOM80xx to
// `doctor`, GOM81xx to `init`, GOM82xx to `report list|latest|clean`, and
// GOM83xx to the `trace` commands — rather than more GOM10xx numbers. Those
// commands do not measure anything, so none of their failures is a mistake in
// an invocation; a user reading one should see at a glance that it is about the
// environment, the configuration file, the run history, or a recording, and that
// the remedy is in that one place.
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
	// CodeTraceUnavailable reports a run that asked for a recording and could
	// not have the directory it named: one inside the workspace and outside
	// `report.directory`, where the stream would be read as part of the tree and
	// reported as drift, or one that could not be created at all. It is printed
	// as a warning and never returned. The run goes on recording into memory,
	// which is what every untraced run does, and a diagnostic that could stop
	// the run it is a diagnostic of would invert the point of having one.
	CodeTraceUnavailable Code = "GOM1013"
	// CodeDiagnosticsUnavailable reports a diagnostics bundle a failed run could
	// not write: a directory that would land where the snapshot reads, or one
	// that could not be created or filled at all. It is printed as a warning and
	// never returned, and it never changes the exit status.
	//
	// That is the same judgement [CodeTraceUnavailable] carries and it matters
	// more here, because this one arrives on a run that has already failed. A
	// bundle that could turn a failed baseline's exit 2 into a different exit 2
	// for a different reason would tell a CI job "the tool broke" where the
	// truth is "your tests did not pass" — and a diagnostic that can change what
	// a run reports inverts the point of having one.
	CodeDiagnosticsUnavailable Code = "GOM1014"
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

// The trace codes, which are the GOM83xx block: what `trace list|summary|diff
// |validate|clean` refuse. The reader's own failures — a stream outside the
// published contract, a line the schema rejects — keep their codes, because
// this package does not re-code what the trace package and internal/schemas
// decided.
const (
	// CodeNoTraceRecorded reports a recording that is not there: a workspace
	// that has never traced a run, or a run id nothing was filed under. An
	// empty *listing* is not this and exits 0; a `summary` or a `diff` with
	// nothing to read is, because those commands' whole output is a recording
	// and there is none.
	CodeNoTraceRecorded Code = "GOM8301"
	// CodeUnreadableTrace reports a file named on the command line that is not
	// a recording this build can read: a path that does not exist, a directory
	// with no stream in it, a stream whose events are outside the contract.
	CodeUnreadableTrace Code = "GOM8302"
	// CodeTraceNotRemoved reports a recording `trace clean` could not delete.
	// Deleting is the whole of what that command does, so one that could not
	// must not exit 0.
	CodeTraceNotRemoved Code = "GOM8303"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside one of the five
// blocks this package owns: GOM10xx for the command line itself, GOM80xx for
// `doctor`, GOM81xx for `init`, GOM82xx for the run-history commands, and
// GOM83xx for the `trace` commands.
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
	CodeTraceUnavailable,
	CodeDiagnosticsUnavailable,
	CodeEnvironmentUnusable,
	CodeConfigurationExists,
	CodeConfigurationUnreadable,
	CodeConfigurationNotWritten,
	CodeConfigurationStale,
	CodeNotAModuleRoot,
	CodeNoStoredRun,
	CodeNoTraceRecorded,
	CodeUnreadableTrace,
	CodeTraceNotRemoved,
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
// Under the message and the hint, and above that tail, comes what the failure
// was about: the argument vector as `command:` and the working directory as
// `dir:`, both indented and both uncoded for the same reason the tail is. They
// are asked for through [commandOf] and [outputOf] rather than from one
// package, because five packages produce errors that carry them and a renderer
// that knew only internal/engine's used to drop a compiler's whole diagnostics
// on the floor — a GOM7505 arrived here as one line saying a test binary would
// not build, with the reason discarded. What is printed is what the error
// carries: an error that named no command prints neither line, and one whose
// command had no working directory of its own prints only the first.
//
// A line inside a coded error that carries no code of its own inherits the code
// above it. Nothing go-mutants writes should produce one — a message is a
// single line, and internal/discover folds a multi-line loader blob before it
// gets here — but "every line is greppable" is the promise, and inheriting is
// the only repair that keeps a continuation attached to the error it belongs to
// instead of inventing a code for it. Blank lines are dropped rather than
// rendered as a code with nothing after it.
//
// Underneath all of it, a failed run that left a diagnostics bundle names it as
// `diagnostics: <dir>`. It is last because it is where to go next rather than
// part of what went wrong, and it is printed here rather than by `run` because
// `run` returns its failure and this function renders it afterwards — a line
// printed by the command would have arrived above the error it belongs to. See
// [diagnosticsError].
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
	// The two lines stand or fall together. An invocation with no argument
	// vector is a spec the runner refused for having none, and a lone `dir:`
	// under it would say a command ran somewhere without saying what it was.
	if command := commandOf(err); command != nil && len(command.Argv) > 0 {
		b.WriteString("    command: " + renderArgv(command.Argv) + "\n")
		if command.Dir != "" {
			b.WriteString("    dir: " + command.Dir + "\n")
		}
	}
	if output := outputOf(err); output != "" {
		for _, line := range strings.Split(output, "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
	// Last, under everything the failure had to say, because it is where to go
	// next rather than part of what went wrong. It is uncoded and unindented for
	// the same reason the `trace:` line the renderer prints is: it is a path to
	// be selected and pasted, and `grep '^diagnostics: '` is how a CI step picks
	// the directory up to attach it.
	if directory := diagnosticsOf(err); directory != "" {
		b.WriteString("diagnostics: " + directory + "\n")
	}
	_, _ = io.WriteString(w, b.String())
}

// A diagnosticsError is a run failure with the bundle that was written for it.
//
// It exists so that the line naming the bundle is printed *under* the failure
// it explains, by the one function that prints failures. `run` could have
// printed the path itself, but it returns the error and internal/cli renders it
// afterwards, so the line would have arrived above the error it belongs to —
// and a reader would meet a path before the problem it is a path to.
//
// It wraps rather than replaces, so [ExitCode], [errors.As] and every other
// question anybody asks of the failure reach the failure. A bundle never
// changes what a run reports.
type diagnosticsError struct {
	err error
	// directory is where the bundle went.
	directory string
}

func (e *diagnosticsError) Error() string { return e.err.Error() }
func (e *diagnosticsError) Unwrap() error { return e.err }

// DiagnosticsDirectory names the bundle written for this failure, which is what
// [RenderError] prints under it.
func (e *diagnosticsError) DiagnosticsDirectory() string { return e.directory }

// A diagnosticsCarrier is an error that had a diagnostics bundle written for
// it. It is an interface for the reason [outputCarrier] is one: the renderer
// asks a question rather than knowing a type.
type diagnosticsCarrier interface{ DiagnosticsDirectory() string }

// diagnosticsOf returns the bundle written for the outermost error in err's
// tree that has one, or "" when none does. The walk is [outputOf]'s, for the
// same reasons.
func diagnosticsOf(err error) string {
	var found string
	walkCauses(err, func(e error) bool {
		carrier, ok := e.(diagnosticsCarrier)
		if !ok {
			return false
		}
		found = carrier.DiagnosticsDirectory()
		return found != ""
	})
	return found
}

// renderWarning writes one "warning GOMxxxx: message" line, and the hint under
// it, for the conditions this package has to report without failing the run.
//
// It is deliberately not [RenderError]. Nothing has failed: a run whose trace
// directory was refused has measured everything it was asked to measure and is
// about to exit on its own verdict, and an "error GOM1013" line would tell a CI
// log parser that something went wrong with the run when what went wrong was
// the diagnostic. The shape is the plain renderer's own, so that a warning this
// package prints and a warning the engine published read as one kind of thing.
//
// The whole line is composed and written once, and the write's result is
// dropped, for the reasons [RenderError] gives about both.
func renderWarning(w io.Writer, err error) {
	var coded *Error
	if !errors.As(err, &coded) {
		return
	}
	// coded.Error() already carries "GOMxxxx: " and the cause behind the
	// message, which is the whole of what a reader needs and exactly what the
	// error renders elsewhere.
	line := "warning " + coded.Error() + "\n"
	if coded.Hint != "" {
		line += "hint: " + coded.Hint + "\n"
	}
	_, _ = io.WriteString(w, line)
}

// An outputCarrier is an error that kept what the failing command printed.
//
// It is an interface rather than a type assertion per package because five
// packages produce one — internal/engine, internal/execute, internal/validate,
// internal/gocmd and internal/runner — and the renderer that prints them all is
// the worst possible place to have to remember a sixth. Asking through one
// method is also what lets each package keep its own field and its own
// trimming rule.
type outputCarrier interface{ RetainedOutput() string }

// A commandCarrier is an error that knows which command it was about.
type commandCarrier interface{ Command() *runner.Invocation }

// outputOf returns the retained output of the outermost error in err's tree
// that kept any, or "" when none did.
//
// Outermost wins, and that is a decision rather than an accident of the walk.
// The outer error is the one that decided what a terminal should see:
// internal/engine, internal/execute and internal/validate trim a fifty-line
// tail for a console, while internal/runner retains up to a megabyte for a
// report and a recording. Preferring the inner capture would bury the failure
// in exactly the scrollback the tail was trimmed out of, and printing both
// would print the same bytes twice at two different lengths.
//
// The walk continues *past* a carrier with nothing to say rather than stopping
// at it, which is the other half of the rule. A start failure arrives as an
// [engine.Error] wrapping a [runner.Error] with no tail of its own — there was
// no child to produce one — while the runner's error knows what it tried to
// run, so a walk that stopped at the first error merely capable of answering
// would print nothing for it.
func outputOf(err error) string {
	var found string
	walkCauses(err, func(e error) bool {
		carrier, ok := e.(outputCarrier)
		if !ok {
			return false
		}
		found = carrier.RetainedOutput()
		return found != ""
	})
	return found
}

// commandOf returns the command the outermost error in err's tree that names
// one was about, or nil when none does. The precedence and the walking-past are
// [outputOf]'s, for the same reasons.
func commandOf(err error) *runner.Invocation {
	var found *runner.Invocation
	walkCauses(err, func(e error) bool {
		carrier, ok := e.(commandCarrier)
		if !ok {
			return false
		}
		found = carrier.Command()
		return found != nil
	})
	return found
}

// walkCauses visits err and everything it wraps, outermost first and in branch
// order, until visit answers true. It reports whether anything did.
//
// It is a hand-rolled traversal rather than repeated errors.As calls because of
// the branches. An error joined out of several — internal/engine joins a run's
// failure with whatever cleaning up the scratch directory said, and so does the
// public session — unwraps to a *slice*, and following a single cause at a time
// reaches the join, finds no cause under it, and stops with the evidence one
// branch away. errors.As would find a carrier inside such a tree, but only the
// first one: it cannot be asked for "the next one that actually has an answer",
// which is the question both callers here are really asking.
func walkCauses(err error, visit func(error) bool) bool {
	for err != nil {
		if visit(err) {
			return true
		}
		switch cause := err.(type) {
		case interface{ Unwrap() error }:
			err = cause.Unwrap()
		case interface{ Unwrap() []error }:
			for _, branch := range cause.Unwrap() {
				if walkCauses(branch, visit) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// renderArgv lays an argument vector out as one line somebody can read, and in
// the ordinary case paste.
//
// An element is printed verbatim unless it is empty or contains whitespace or a
// double quote. Quoting everything would escape the absolute paths that make up
// most of a go-mutants command line into something nobody can read; quoting
// nothing would silently turn `-run` `Test A` into three shell words, so the
// command somebody was handed to reproduce the failure would not be the command
// that failed. An empty element is quoted because it would otherwise vanish
// altogether, taking the argument count with it.
//
// The quoting is written out here rather than handed to strconv.Quote, and the
// difference is the whole point of the line. strconv.Quote produces a Go string
// literal, so it escapes backslashes — and the one platform whose ordinary
// paths contain a space is the one whose separator is a backslash, which turns
// the command a Windows user most needs to paste into `"C:\\Program
// Files\\Go\\bin\\go.exe"`: correct as source, wrong in every shell there is.
// Only the quote that would end the quoting is escaped; everything else,
// backslashes included, goes through as it was.
func renderArgv(argv []string) string {
	rendered := make([]string, len(argv))
	for i, arg := range argv {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\v\f\r\"") {
			rendered[i] = arg
			continue
		}
		rendered[i] = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
	}
	return strings.Join(rendered, " ")
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
