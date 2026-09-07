// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/validate"
)

// The sentinels every refusal a caller can *act* on carries.
//
// They exist because the alternative was matching text. A consumer driving this
// API has to distinguish three things it does three different things about: its
// own suite failing (report it to the user), the repository moving underneath
// the engine (re-open the workspace), and go-mutants itself breaking (file a
// bug, with a code). None of those were separable without reading messages, and
// a message is the one part of an API nobody promises twice.
//
// Every message these appear in is unchanged: each is wrapped into the sentence
// the engine has always printed, so `errors.Is` is additive and no output moved.
var (
	// ErrWorkspaceClosed is returned by [Workspace.Exec] and [Workspace.Prepare]
	// after [Workspace.Close].
	ErrWorkspaceClosed = errors.New("workspace is closed")
	// ErrWorkspacePrepared is returned by a second [Workspace.Prepare]. A
	// workspace may be prepared exactly once, including when the preparation
	// failed after it began: the tree it froze is no longer the tree it froze.
	ErrWorkspacePrepared = errors.New("workspace has already been prepared")
	// ErrPrepareFailed is returned by [Workspace.Exec] after a
	// [Workspace.Prepare] that began and failed.
	//
	// It is a different condition from [ErrWorkspacePrepared] and has a
	// different answer. A *successful* preparation leaves the tree byte for byte
	// the snapshot [Open] froze — the instrumented sources live only in the
	// overlay the session owns — so commands go on running beside the session. A
	// failed one promises nothing about the tree: it may have stopped with
	// instrumented sources still in it, and a command compiled from those would
	// be measuring a program nobody wrote. The workspace is spent, and the
	// answer is to open another one.
	//
	// Every failed preparation carries it, including the ones that stopped
	// before anything was instrumented. Which failures left the tree alone is
	// not a question a caller could answer, and the engine does not answer it
	// either.
	ErrPrepareFailed = errors.New("workspace preparation failed; its tree may hold instrumented sources")
	// ErrSessionClosed is returned by [Session.Exec], [Session.Probe],
	// [Session.Control] and [Session.Changes] after [Session.Close] — or after
	// the parent workspace's, which closes the session too.
	ErrSessionClosed = errors.New("session is closed")
	// ErrInvalidMutantID reports an [ExecRequest.Mutant] that is not a mutant
	// identity at all: too short, too long, or not lowercase hexadecimal.
	ErrInvalidMutantID = errors.New("mutant id prefix is invalid")
	// ErrMutantNotFound reports a well-formed prefix that no catalogued mutant
	// carries, which is what a stale catalogue looks like from the outside.
	ErrMutantNotFound = errors.New("no mutant matches that id prefix")
	// ErrAmbiguousMutant reports a prefix carried by more than one mutant. The
	// engine refuses to guess; [MutantSelectionError.Matches] names them.
	ErrAmbiguousMutant = errors.New("mutant id prefix is ambiguous")
	// ErrMutantRejected reports a mutant validation proved does not compile.
	// It is data rather than a fault — the catalogue reports it as
	// [Mutant.Accepted] false and carries the compiler's own words in
	// [MutantSelectionError.Rejection] — and executing it is refused because
	// there is no binary it could be executed in.
	ErrMutantRejected = errors.New("mutant was rejected during validation")
	// ErrProbeInconsistent reports a probe pass whose log named a mutant this
	// session's catalogue cannot account for: an index out of order, an index
	// past the end of the catalogue, or one naming an *accepted* mutant
	// [Mutant.Probed] reports as unprobed.
	//
	// An index naming a mutant the mutant tree's validation *rejected* is not
	// one of them. The probe tree is instrumented from the whole catalogue, so
	// its log legitimately names a site whose mutation did not compile;
	// [Session.Probe] drops that index rather than refusing the pass, and
	// [Session.Probe]'s own documentation sets out the order the two checks and
	// that filter run in.
	//
	// It is the one sentinel here that is never a caller's doing. The indices
	// come from go-mutants' own probe runtime, written against the catalogue
	// go-mutants prepared, so this is the engine contradicting itself and the
	// answer is a bug report rather than a retry.
	//
	// [Session.Probe] returns it instead of dropping the offending index,
	// because [ProbeResult.Infected] is what licenses a consumer not to execute
	// a test: a set quietly repaired would be handed over as a measurement, and
	// an empty result would be read as a pass that infected nothing. Neither is
	// true, and both are silent.
	ErrProbeInconsistent = errors.New("the probe log names a mutant the catalogue cannot account for")
	// ErrInvalidSelection reports a [PrepareOptions.Selection] the engine will
	// not narrow a catalogue by: a path that is empty, absolute,
	// backslash-separated or escaping the module, or a [LineRange] that starts
	// below line 1 or ends before it starts. The sentence around it names the
	// entry that was wrong.
	//
	// It is a refusal rather than a selection of nothing on purpose, and it is
	// the judgement `--changed` already makes: a narrowing nothing can satisfy
	// selects no mutants, and a run that measured none and reported a perfect
	// score is the one failure a selection feature must not produce. So the
	// selection is refused before discovery starts rather than believed and
	// then found to match nothing.
	//
	// A path the engine *can* read and the module does not hold is not one of
	// them. Prepare has looked at no source when the options are resolved, a
	// selection is usually built from a diff — which names deleted files,
	// testdata and documents beside source — and a path naming no mutated file
	// simply selects nothing. Refusing it would make every consumer filter the
	// engine's own input on the engine's behalf.
	ErrInvalidSelection = errors.New("selection is invalid")
)

// A MutantSelectionError is [Session.Exec] refusing the mutant it was asked
// for, with the reason as a sentinel rather than as a sentence.
//
// The four reasons are four different mistakes: a malformed identity, a
// catalogue the caller has moved on from, a prefix too short to name one
// mutant, and a mutant that never compiled. A consumer resolving user input
// wants the first three to become advice and the fourth to become a report; the
// message alone made them one class.
type MutantSelectionError struct {
	// Prefix is what the request asked for, verbatim.
	Prefix string
	// Reason is one of [ErrInvalidMutantID], [ErrMutantNotFound],
	// [ErrAmbiguousMutant] and [ErrMutantRejected]. errors.Is matches it.
	Reason error
	// Matches are the [Mutant.DisplayID]s an ambiguous prefix names, sorted, so
	// that the list a caller prints does not depend on catalogue order. It is
	// empty for every other reason.
	Matches []string
	// Rejection is the catalogued rejection for [ErrMutantRejected], carrying
	// the compiler diagnostic that condemned the mutant, and nil otherwise.
	Rejection *Rejection

	// cause is the catalogue's own refusal, kept so that the message is the one
	// this API has always printed and so that errors.Is still reaches the
	// resolution's internal sentinel.
	cause error
}

// Error renders the two shapes the API has always printed: the catalogue's own
// refusal under the requested prefix, or the rejection under the mutant's
// display identity.
func (e *MutantSelectionError) Error() string {
	if e.Rejection != nil {
		return fmt.Sprintf("gomutants: session exec mutant %s was rejected during validation: %s",
			e.Rejection.DisplayID, e.Rejection.Diagnostic)
	}
	return fmt.Sprintf("gomutants: session exec mutant %q: %s", e.Prefix, errorText(e.cause))
}

// Is reports the sentinel this refusal carries.
func (e *MutantSelectionError) Is(target error) bool { return target != nil && target == e.Reason }

// Unwrap returns the catalogue's own refusal, or nil for a rejection, which no
// lower layer reported.
func (e *MutantSelectionError) Unwrap() error { return e.cause }

// A DriftError is preparation refusing a snapshot that stopped matching the
// manifest it was frozen from.
//
// It is the one preparation failure whose remedy belongs to the caller rather
// than to go-mutants: something wrote into the frozen tree — a command run
// before Prepare, a test that updates a golden file, a generator — and every
// number a run would then produce would be about a program nobody has.
//
// The changes are carried rather than only printed. A consumer that has to tell
// its user *which* file moved, or decide whether the write was its own, cannot
// get that out of a sentence without agreeing to parse one.
type DriftError struct {
	// Stage names the check that noticed: "commands" for the integrity gate at
	// the top of the instrumentation window, "discovery" for the comparison
	// between what discovery read and the frozen manifest, and "source
	// restoration", "verification", "probe instrumentation" or "probe source
	// restoration" for the checks around instrumentation.
	Stage string
	// Changes are the drifting paths in path order, with the digests on both
	// sides — [Change.BeforeSHA256] empty for a file that was added,
	// [Change.AfterSHA256] empty for one that was removed.
	Changes []Change
}

// Error renders the three sentences preparation prints for drift.
//
// The kind words are the snapshot layer's — added, removed, *changed* — and not
// [ChangeKind]'s, whose third spelling is "modified". The two vocabularies are
// deliberately different and this message is the older of them.
func (e *DriftError) Error() string {
	header := "gomutants: prepare " + e.Stage + " changed the snapshot outside instrumentation:"
	switch e.Stage {
	case driftStageCommands:
		header = "gomutants: prepare commands changed the frozen snapshot:"
	case driftStageDiscovery:
		header = "gomutants: prepare commands changed the snapshot during discovery:"
	}
	// The engine never builds one of these without a change in it — an empty
	// drift is not a failure — but a header followed by a blank line is what a
	// hand-built value would print, and a message ending in a newline is the
	// kind of thing that reaches a log with a hole in it.
	if len(e.Changes) == 0 {
		return header
	}
	lines := make([]string, 0, len(e.Changes))
	for _, change := range e.Changes {
		lines = append(lines, driftWord(change.Kind)+" "+change.Path)
	}
	return header + "\n" + strings.Join(lines, "\n")
}

// The two stages with sentences of their own: a command is not instrumentation,
// so "outside instrumentation" would name the wrong suspect for either of them.
//
// They are two rather than one because the writes they catch are different, and
// so is what a reader has to do about them. The gate finds a change that is
// still in the tree, whoever made it and whenever. Discovery's check finds a
// change that has already been undone — a command that rewrote a source file
// while the catalogue was being built and put it back before the gate ran — so
// the tree is pristine and the catalogue is not, and a reader looking for the
// file on disk would find nothing wrong with it.
const (
	driftStageCommands  = "commands"
	driftStageDiscovery = "discovery"
)

// driftWord is how the snapshot layer spells a change of this kind. It is
// derived from [snapshot.DriftKind] rather than written out, so the day that
// vocabulary changes is the day this message changes with it.
func driftWord(kind ChangeKind) string {
	switch kind {
	case ChangeAdded:
		return snapshot.DriftAdded.String()
	case ChangeRemoved:
		return snapshot.DriftRemoved.String()
	case ChangeModified:
		return snapshot.DriftChanged.String()
	default:
		return snapshot.DriftKind(0).String()
	}
}

// driftChanges turns the snapshot layer's drifts into the public change
// vocabulary, keeping order and both digests.
func driftChanges(drifts []snapshot.Drift) []Change {
	changes := make([]Change, 0, len(drifts))
	for _, drift := range drifts {
		changes = append(changes, Change{
			Kind:         changeKindOf(drift.Kind),
			Path:         drift.RelPath,
			BeforeSHA256: drift.WantSHA256,
			AfterSHA256:  drift.GotSHA256,
		})
	}
	return changes
}

// changeKindOf translates one drift kind. An unknown kind becomes the empty
// [ChangeKind] rather than a guess: a caller comparing against the three
// documented values then fails to match instead of matching the wrong one.
func changeKindOf(kind snapshot.DriftKind) ChangeKind {
	switch kind {
	case snapshot.DriftAdded:
		return ChangeAdded
	case snapshot.DriftRemoved:
		return ChangeRemoved
	case snapshot.DriftChanged:
		return ChangeModified
	default:
		return ""
	}
}

// A VerificationError is the caller's own test suite failing on the
// instrumented tree, during [PrepareOptions.Verify].
//
// This is the finding, not the fault. Instrumentation is behaviour-preserving,
// so a suite that fails here fails on the user's program: it is red, or it is
// flaky, or it depends on something the frozen snapshot does not carry. A
// consumer must report it as a finding about the repository rather than as a
// broken engine, and that decision is what this type exists to make possible
// without reading the message.
type VerificationError struct {
	// Command is the verification command as it was run, except that its Env is
	// always nil: [PrepareOptions.Verify]'s overlay was folded into the frozen
	// environment before the command started, and it is left out here on
	// purpose. An error is a value a consumer logs, and an environment is where
	// the tokens are.
	Command Command
	// ExitCode is the status the command exited with. It is zero for a
	// timeout, which TimedOut reports.
	ExitCode int
	// TimedOut reports a verification the supervisor had to kill at
	// [Command.Timeout].
	TimedOut bool
	// Duration is the wall-clock time the command took.
	Duration time.Duration
	// Output is the bounded combined output — what the user has to be shown,
	// because it is their failure.
	Output []byte
	// Truncated reports that Output lost bytes to [Command.OutputLimit], in
	// which case it begins with [OutputTruncatedPrefix]; TotalBytes is
	// everything the verification wrote, kept or not.
	//
	// They matter more here than anywhere else in this API. This output is the
	// evidence handed to a user whose suite is red, and a capture that quietly
	// lost the first megabyte — where a panic or a build failure would be —
	// reads exactly like a suite that failed for the reason shown at the end.
	Truncated  bool
	TotalBytes int64
}

// Error renders the two sentences preparation has always printed for a failed
// verification.
func (e *VerificationError) Error() string {
	if e.TimedOut {
		return fmt.Sprintf("gomutants: prepare instrumented verification timed out after %s", e.Command.Timeout)
	}
	return fmt.Sprintf("gomutants: prepare instrumented verification exited with status %d: %s",
		e.ExitCode, outputSummary(e.Output))
}

// A BuildError is a preparation phase that could not do its work: a package
// that would not load, a snapshot that would not compile, a test binary that
// would not build.
//
// It is the infrastructure half of the same line [VerificationError] draws.
// Every one of these carries a [BuildError.Code] the docs list and a bug report
// can quote, and the argv that produced it, because the audience for this
// failure is whoever has to reproduce it.
type BuildError struct {
	// Phase is the preparation phase that failed.
	Phase PreparePhase
	// Package is the import path the failing command was about, and empty where
	// the command was about the whole snapshot rather than one package.
	Package string
	// Argv is the failing command, or nil where no child process ran — a
	// package that would not load is decided in this process — and nil for the
	// two validation phases, whose failures come back through the seam the
	// bisection search is faked behind and name no command.
	Argv []string
	// ExitCode is the status the command exited with, and zero where the
	// failure retained none: a validation phase answers whether a subset
	// compiled, not with what status.
	ExitCode int
	// TimedOut reports a command the supervisor had to kill.
	TimedOut bool
	// Output is the tail of the command's output — the compiler's own words,
	// which are what makes the failure actionable.
	Output string
	// Code is the stable diagnostic code, for example "GOM7505".
	// [DiagnosticCode] returns the same value for any error carrying one.
	Code string

	// cause is the error the phase produced, kept so that this type's message
	// is exactly the one the API has always printed.
	cause error
}

// Error renders the cause, which is the sentence preparation has always
// printed: `gomutants: prepare test binaries: GOM7505: …`.
//
// A value built by hand has no cause and says so in a sentence rather than in
// the phase's wire spelling: an error type that prints an underscored token, or
// panics, is one nobody wants to meet in a log.
func (e *BuildError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	if e.Phase == "" {
		return "gomutants: preparation failed"
	}
	return "gomutants: prepare " + phaseWords(e.Phase) + " failed"
}

// phaseWords spells a phase for a sentence: "binary_build" is what the trace
// and the schema carry, and "binary build" is what a reader is owed.
func phaseWords(phase PreparePhase) string {
	return strings.ReplaceAll(string(phase), "_", " ")
}

// Unwrap returns the underlying failure, so errors.Is still reaches a
// cancellation and errors.As still reaches the internal error that carried the
// code.
func (e *BuildError) Unwrap() error { return e.cause }

// buildError types one preparation failure, whenever the phase reported one
// carrying a diagnostic code.
//
// The three shapes below are named because each answers more than the code: a
// package, an argv, an exit status, the compiler's output. Anything else that
// still carries a code — a toolchain probe or a supervised process failing
// straight through discovery, without one of those three wrapping it — becomes
// a [BuildError] too, because the code is the part a consumer reports and
// dropping it would leave a failure indistinguishable from a scratch directory
// that could not be made.
//
// An error carrying no code at all is returned unchanged. Wrapping it would
// announce a build that never happened, and a consumer reading Argv and Code
// off a value where both are empty learns less than it would from the message.
func buildError(phase PreparePhase, err error) error {
	typed := &BuildError{Phase: phase, cause: err}
	var executeErr *execute.Error
	var validateErr *validate.Error
	var discoverErr *discover.Error
	switch {
	case errors.As(err, &executeErr):
		typed.Code = executeErr.Code.String()
		typed.Package = executeErr.Package
		typed.Output = executeErr.Output
		typed.ExitCode = executeErr.ExitCode
		typed.TimedOut = executeErr.TimedOut
		typed.Argv = invocationArgv(executeErr.Invocation)
	case errors.As(err, &validateErr):
		typed.Code = validateErr.Code.String()
		typed.Output = validateErr.Output
		typed.TimedOut = validateErr.TimedOut
		typed.Argv = invocationArgv(validateErr.Invocation)
	case errors.As(err, &discoverErr):
		typed.Code = discoverErr.Code.String()
	default:
		code := DiagnosticCode(err)
		if code == "" {
			return err
		}
		typed.Code = code
		// Every go-mutants error answers these two, so the command and the
		// output are taken through the accessors rather than by naming the two
		// remaining packages and the next one after them.
		var described interface {
			Command() *runner.Invocation
			RetainedOutput() string
		}
		if errors.As(err, &described) {
			typed.Argv = invocationArgv(described.Command())
			typed.Output = described.RetainedOutput()
		}
	}
	return typed
}

// invocationArgv is the command an internal failure named, or nil when it named
// none.
func invocationArgv(invocation *runner.Invocation) []string {
	if invocation == nil {
		return nil
	}
	return slices.Clone(invocation.Argv)
}

// An ExecutionError is the measurement itself failing inside [Session.Exec],
// [Session.Probe] or [Session.Control]: a test binary that would not start or
// could not be supervised, a generated runtime that refused the activation it
// was handed, an infection log that is there and cannot be read.
//
// It is never a statement about the tests. A mutant that survived, was killed,
// or timed out is a result and comes back as one; this is the execution phase
// reporting that it has no result to give, which a consumer has to report as
// infrastructure rather than fold into a score.
//
// It covers what the execution phase reports and not everything the two calls
// can fail at. A session scratch directory that could not be created, an
// environment overlay or an instrumentation overlay that could not be composed,
// a fuzz workspace that could not be copied, and the artifacts captured after a
// fuzz target are plain errors today: they happen before or after the
// measurement, carry no diagnostic code, and a consumer reads their message.
type ExecutionError struct {
	// Call is "exec", "probe" or "control": which of the session's three runs
	// failed.
	Call string
	// Package is the import path of the test binary the failure was about,
	// whenever the failure named one — the binary that would not start, the one
	// whose runtime refused the activation, the one a cancellation cut off.
	//
	// It falls back to the request's own Package as it was given for the
	// failures that are about the pass rather than about one binary, and is
	// empty when neither says anything: the request's field is a *selector*,
	// which may be a module-relative directory and is empty for the ordinary
	// request that measures every prepared binary.
	Package string
	// Code is the stable diagnostic code, for example "GOM7513".
	Code string
	// Output is the tail of the failing command's output, when there was one.
	Output string

	// cause is the failure the execution layer reported, kept so that this
	// type's message is the one the API has always printed.
	cause error
}

// Error renders the cause, which is the sentence the API has always printed. A
// value built by hand has none and says so in a sentence of its own.
func (e *ExecutionError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	if e.Call == "" {
		return "gomutants: the session could not measure"
	}
	return "gomutants: session " + e.Call + " could not measure"
}

// Unwrap returns the underlying failure.
func (e *ExecutionError) Unwrap() error { return e.cause }

// executionError types one measurement failure. Like [buildError] it leaves an
// error no internal package produced exactly as it was.
//
// selector is the request's package, and it is the fallback rather than the
// answer: it selects which binaries to measure and says nothing about which one
// broke — it may be a module-relative directory, and it is empty for the
// ordinary request that measures all of them. The execution phase names the
// binary it could not start, so that import path wins whenever there is one.
func executionError(call, selector string, err error) error {
	code := execute.CodeOf(err)
	if code == "" {
		return err
	}
	pkg := selector
	var failure *execute.Error
	if errors.As(err, &failure) && failure.Package != "" {
		pkg = failure.Package
	}
	return &ExecutionError{
		Call:    call,
		Package: pkg,
		Code:    code.String(),
		Output:  execute.OutputOf(err),
		cause:   err,
	}
}

// A PackageNotPreparedError is a request naming a package this session has no
// test binary for.
//
// It is separated from every other refusal because it is the one a long-lived
// consumer meets in normal operation: a package list that has moved on since
// the session was prepared, or a typo in one. The remedy is to prepare again or
// to fix the name, and neither is "file a bug".
type PackageNotPreparedError struct {
	// Call is "exec", "probe" or "control".
	Call string
	// Package is the import path or module-relative directory that was asked
	// for, verbatim.
	Package string
}

// Error renders the sentence the API has always printed.
func (e *PackageNotPreparedError) Error() string {
	return fmt.Sprintf("gomutants: session %s package %q has no prepared test binary", e.Call, e.Package)
}

// A ReservedError is a request supplying something the engine owns: an
// environment variable it sets for itself, or a test flag one of its two
// timeout layers depends on.
//
// It names what was refused and who owns it, so that a consumer composing a
// request can say "drop this flag" rather than "the engine said no". Exactly
// one of Flag and Variable is set.
type ReservedError struct {
	// Call is "exec", "probe" or "control" for a flag, and empty for a
	// variable — the environment overlay is applied by workspace commands and
	// session targets alike, and the refusal is the same sentence for all of
	// them.
	Call string
	// Flag is the reserved test flag, with its leading dash.
	Flag string
	// Variable is the reserved environment variable's name.
	Variable string
	// Owner names what the reservation protects: "go-mutants", "the session",
	// "the Go fuzz coordinator", or "the session's process supervisor".
	Owner string
}

// Error renders the sentences the engine prints for a reserved variable and a
// reserved flag.
func (e *ReservedError) Error() string {
	if e.Variable != "" {
		return e.Variable + " is reserved by " + e.Owner
	}
	return "gomutants: session " + e.Call + ": " + e.Flag + " is reserved by " + e.Owner
}

// DiagnosticCode returns the stable GOM#### code carried anywhere in err's
// chain, or "" when it carries none.
//
// It exists because the codes are the one part of a failure that is promised to
// stay put, and they live in packages a consumer cannot import. The alternative
// — reading four characters out of a message — breaks the day the message is
// reworded, which is exactly the coupling this whole surface removes.
func DiagnosticCode(err error) string {
	if err == nil {
		return ""
	}
	if code := execute.CodeOf(err); code != "" {
		return code.String()
	}
	if code := validate.CodeOf(err); code != "" {
		return code.String()
	}
	if code := discover.CodeOf(err); code != "" {
		return code.String()
	}
	if code := runner.CodeOf(err); code != "" {
		return code
	}
	return gocmd.CodeOf(err)
}

// selectionError translates the catalogue's own resolution failure into the
// public refusal, keeping the internal error as the cause so that the message
// and the reachable sentinels are both unchanged.
func (s *Session) selectionError(prefix string, cause error) *MutantSelectionError {
	selection := &MutantSelectionError{Prefix: prefix, cause: cause}
	switch {
	case errors.Is(cause, mutation.ErrInvalidPrefix):
		selection.Reason = ErrInvalidMutantID
	case errors.Is(cause, mutation.ErrMutantNotFound):
		selection.Reason = ErrMutantNotFound
	case errors.Is(cause, mutation.ErrAmbiguousPrefix):
		selection.Reason = ErrAmbiguousMutant
		selection.Matches = s.matchingDisplayIDs(prefix)
	}
	return selection
}

// rejectionError is the refusal to execute a mutant validation condemned. The
// display identity is taken from the catalogue when the rejection index has
// none, so that the message names the mutant either way.
func rejectionError(prefix, displayID string, rejection Rejection) *MutantSelectionError {
	if rejection.DisplayID == "" {
		rejection.DisplayID = displayID
	}
	return &MutantSelectionError{Prefix: prefix, Reason: ErrMutantRejected, Rejection: &rejection}
}

// matchingDisplayIDs are the display identities of every catalogued mutant an
// ambiguous prefix names, sorted.
//
// The list is computed here rather than parsed out of the catalogue's own
// message, and sorted rather than left in catalogue order, because a caller
// prints it to somebody who is about to retype one of them.
func (s *Session) matchingDisplayIDs(prefix string) []string {
	matches := make([]string, 0, 2)
	for _, mutant := range s.publicCatalog.Mutants {
		if strings.HasPrefix(mutant.ID, prefix) {
			matches = append(matches, mutant.DisplayID)
		}
	}
	slices.Sort(matches)
	return matches
}

// errorText is err.Error() with nil rendered as the empty string, so that a
// message built around a cause cannot panic on one that was never set.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
