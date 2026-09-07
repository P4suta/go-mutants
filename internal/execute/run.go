// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testflag"
	"github.com/P4suta/go-mutants/trace"
)

// InProcessTimeoutFactor multiplies a mutant's timeout to get the
// `-test.timeout` the binary is given for itself.
//
// The supervisor is the authority: it kills the whole process tree, and it is
// the only mechanism that can end a mutant that hangs outside the testing
// framework's reach. The in-process deadline is insurance underneath it, and it
// is set deliberately *later* so that the two never race — a test binary that
// panicked itself on its own deadline would produce a non-zero exit, and a
// non-zero exit is a kill. Doubling makes the supervisor always first, while
// still bounding a child the supervisor somehow lost.
const InProcessTimeoutFactor = 2

// A MutantRun is one mutant to execute.
type MutantRun struct {
	// ID is the full activation identity, as [instrument.ActiveEnv] takes it.
	// The display prefix is not enough: the generated runtime matches on the
	// whole identity and exits [instrument.UnknownMutantExit] for anything else.
	ID string
	// DisplayID is the short form of the identity, as a console prints it. It
	// is carried for the recording alone — nothing here matches on it — so that
	// an account of a run reads in the same shortened identities the report and
	// the console use. It may be empty, and a caller that has none says so by
	// leaving it so rather than by shortening the identity here: how much of an
	// id is shown is the catalogue's decision and not this package's.
	DisplayID string
	// Package is the import path of the package the mutated source belongs to,
	// carried for the recording alone, as DisplayID is.
	//
	// It is the *mutant's* package and never the test scope the binaries were
	// built from. A consumer reads it as an import path and joins on it — a
	// recording of a mutation run beside one of the test run that measured it
	// — so a `./internal/...` there would be a pattern wearing the shape of a
	// package. The caller is the one that knows it: this package is handed
	// identities and binaries, and the catalogue is where a mutant's package
	// is written down. Empty is what a caller with no package for a mutant
	// says, and it is omitted from the event rather than guessed at.
	Package string
	// Timeout bounds one attempt at one test binary. It is required: a mutant
	// with no budget is refused rather than run unbounded, because a run that
	// never ends is worse than a mutant reported wrongly.
	Timeout time.Duration

	// MemoryLimit bounds the resident memory of each test binary's whole
	// process tree, in bytes, as [runner.Spec.MemoryLimit] takes it. Zero means
	// no bound.
	//
	// It is Timeout's twin and it is optional where Timeout is required, which
	// is the one place the pair is not symmetric. A mutant with no deadline is
	// refused because a run that never ends is worse than a mutant reported
	// wrongly; a mutant with no memory bound is accepted because there are
	// platforms where no bound can be enforced at all — see
	// [runner.MemoryBoundSupported] — and refusing every mutant on those would
	// be refusing to run rather than running with one fewer guarantee.
	//
	// A tree that passes it is killed and reported as [mutation.OutcomeKilled],
	// with [Attempt.MemoryExceeded] saying what did the killing. That is not a
	// new verdict: the original program was measured under the budget the bound
	// was derived from, so a tree that needs several times what the whole suite
	// needed has been changed observably, which is what a kill means.
	MemoryLimit int64

	// Binaries narrows the measurement to a subset of the test binaries, as
	// indices into the `bins` slice given to [RunOne] or [Schedule]. It is how
	// coverage-guided selection reaches this package: internal/coverage decides
	// which test packages reach a mutant's lines, and only those binaries are
	// started for it.
	//
	// Nil — the zero value — means every binary, which is what a run with no
	// coverage information does and what every caller before coverage-guided
	// selection existed was doing implicitly.
	//
	// A non-nil but *empty* subset is refused with [CodeMutantInvalid] rather
	// than obeyed. Walking zero binaries would report the mutant as survived
	// having started nothing, which is the same flattering green
	// [CodeNoTestBinaries] refuses for a whole run: a mutant no binary covers is
	// not executed at all and is recorded by the engine, not handed here with an
	// empty list. An index outside the slice is refused for the same reason —
	// it can only mean the caller's binaries and this one's have drifted apart.
	//
	// The order the indices are given in is the order the binaries are tried,
	// and duplicates are not removed: this package runs what it is told to run,
	// and a caller that wants each binary once passes each index once.
	Binaries []int

	// Tests narrows what each selected binary runs to the named top-level
	// tests, keyed by the binary's import path. It is how test-level
	// narrowing reaches this package: internal/coverage decides which tests
	// of a binary reach a mutant's lines, and the binary is started with only
	// those selected — `-test.run` anchored to an alternation of their escaped
	// names, placed with the harness-owned flags ahead of Args.
	//
	// A binary with no entry runs whole, which is what every caller before
	// test-level narrowing was asking for. An entry for a binary the run does
	// not start — one not in Binaries — is refused with [CodeMutantInvalid],
	// because it would describe a measurement never made; so is an empty
	// list, for the reason an empty Binaries is: a binary told to run no
	// tests passes having run nothing. While any entry is present Args may not
	// supply `-test.run`, since the binary's flag package would let the last
	// one win and the measurement would not be the one the selection
	// describes.
	//
	// Names are matched whole. A subtest is selected through its parent, and
	// a name is the parent's: `TestX/case_3` is not a name this accepts.
	Tests map[string][]string

	// Args are passed verbatim to each selected Go test binary after the
	// harness-owned timeout flag. They make one prepared binary reusable for a
	// top-level test, a fuzz target, or an ordinary whole-package run without
	// invoking a shell or rebuilding it. A caller may not supply -test.timeout:
	// the outer process-tree supervisor and the later in-process deadline are a
	// paired safety boundary and cannot be overridden per target.
	Args []string

	// OutputLimit caps the combined output kept from each binary this run
	// starts, as [runner.Spec.OutputLimit] takes it: zero or negative selects
	// internal/runner's own default, and a positive value below its floor is
	// raised to it.
	//
	// It is per run rather than per [Options] because the two callers of one
	// prepared session want different numbers out of the same binaries — a
	// console wants a screenful, a consumer archiving the evidence of a kill
	// wants all of it — and the run is the smallest thing that knows which.
	OutputLimit int

	// RecordTestLog asks each binary this run starts to write down what it
	// consulted — see [TestLog] and [Attempt.TestLogs].
	//
	// It is off by default, and it is a request rather than a promise: a fuzz
	// target and a run with no scratch directory record nothing and say why.
	RecordTestLog bool
}

// An Attempt is one pass over the test binaries with one mutant active.
//
// It is deliberately not a verdict. A single timeout is an Attempt with
// [mutation.OutcomeTimedOut] and is *not* a confirmed detection; only
// [Schedule] — which can retry it serially — decides what a mutant's outcome
// finally is.
type Attempt struct {
	// Worker is the scheduler slot that made this pass, counting from zero.
	//
	// It is stamped by [Schedule] rather than by [RunOne], which does not know
	// and must not care: a single pass is the same measurement whoever makes
	// it. What it is for is the record — a report and a recording both say
	// which worker ran a mutant, and a run whose mutants slowed each other down
	// is a run where that is the first thing to look at — and the serial
	// timeout retry, which is worker 0 and has the machine to itself.
	Worker int
	// Outcome is what this pass observed. It is one of killed, survived,
	// timed out, errored, or not run — never inconclusive, which is a verdict
	// about two attempts rather than an observation of one.
	Outcome mutation.Outcome
	// KilledBy is the import path of the test binary that detected the mutant —
	// the one whose tests failed, or the one it hung — and is empty for every
	// other outcome. The import path rather than the file name: it is what a
	// report renders and what stays meaningful between runs.
	KilledBy string
	// Duration is the wall-clock time the child processes took, summed over the
	// binaries this attempt actually ran. A survivor's number therefore covers
	// every binary; a kill's covers only those up to and including the one that
	// failed.
	Duration time.Duration
	// Binaries are the test binaries this attempt started, in launch order, by
	// import path. It stops where the attempt stopped: a mutant killed by the
	// second of three binaries was measured against two, and naming all three
	// would describe a measurement that was never made.
	//
	// The import path rather than the file that was executed, for the reason
	// [KilledBy] uses it: the file is named after a digest, in a directory the
	// run deletes, and the import path is what a report renders and what stays
	// meaningful between runs.
	Binaries []string
	// Tests are the tests each of those binaries was narrowed to, keyed by
	// import path, exactly as [MutantRun.Tests] named them — for the binaries
	// this attempt started and had a selection for. It is nil when nothing was
	// narrowed, and a binary that ran whole has no entry: an attempt reports
	// what it did, and a selection for a binary it never reached is not
	// something it did.
	Tests map[string][]string
	// ExecSeqs are the `exec` events those starts were recorded at, in the same
	// order. They are how an attempt is joined to the commands underneath it,
	// and through them to the output the recording preserved.
	//
	// An untraced run records nothing and is handed a zero for every start,
	// which is not a sequence anything can be found at, so nothing is listed:
	// an empty list means there is no recording, never that an attempt started
	// nothing.
	ExecSeqs []int64
	// TestLogs are what each binary this attempt started recorded about the
	// environment variables and files it consulted, in the same order as
	// Binaries and one per element of it.
	//
	// It is nil unless [MutantRun.RecordTestLog] asked for it, which is the
	// difference between "nothing was recorded" and "nothing was touched": an
	// empty slice beside a run that recorded would be the second sentence, and
	// it is the one a consumer acts on.
	TestLogs []TestLog
	// OutputTail is the last [OutputTailLines] lines the deciding binary
	// printed: the failing one for a kill, the timed-out one for a timeout, the
	// failing command for an error. It is empty for a survivor, whose output is
	// thousands of lines of nothing having gone wrong.
	OutputTail string
	// Output is the whole of what [MutantRun.OutputLimit] kept of that same
	// binary's combined output — OutputTail is a summary of exactly these bytes
	// — and it is empty wherever OutputTail is, for the same reason. A
	// survivor's output multiplied by every mutant in a run is the memory the
	// cap exists to bound, and an attempt nobody finished decided nothing.
	Output []byte
	// OutputBytes is everything that binary wrote, kept or not, and Truncated
	// reports that the cap dropped some of it — in which case Output begins with
	// [runner.OutputTruncatedPrefix]. They are [runner.Result]'s own fields
	// carried up unchanged, and they are zero and false wherever Output is
	// empty.
	OutputBytes int64
	Truncated   bool
	// PeakMemory is the highest memory any binary of this attempt was
	// observed to hold, in bytes, and MemoryExceeded reports that one of them
	// passed [MutantRun.MemoryLimit] and had its tree killed for it.
	//
	// PeakMemory is the maximum over the binaries the attempt started rather than
	// the deciding binary's alone — unlike Output, which is one binary's — and
	// that is what the number is for: an attempt's cost is the worst moment it
	// put the machine through, and a caller comparing it against a budget is
	// asking about that moment. It is zero on a platform that could not measure.
	//
	// MemoryExceeded never accompanies [mutation.OutcomeTimedOut]: the two are
	// different kills, and internal/runner reports exactly one of them.
	PeakMemory     int64
	MemoryExceeded bool
	// Err carries a [Code] from this package, with the underlying cause
	// reachable through it. It is set whenever Outcome is
	// [mutation.OutcomeErrored], and on exactly one other outcome: a not-run
	// attempt whose child a cancellation killed, where it is [CodeInterrupted]
	// and names the binary that was cut off.
	//
	// An error here is therefore not by itself a statement that anything went
	// wrong, and nothing may classify on its presence: Outcome is what
	// distinguishes an infrastructure failure from a run somebody stopped. It
	// is set on that path because "which binary was still running when Ctrl-C
	// arrived" is the first thing a reader of an interrupted run asks, and an
	// outcome alone cannot say it.
	Err error
}

// RunOne executes one mutant against the test binaries, in order, and stops at
// the first binary that settles the question.
//
// Stopping early is not an optimisation detail, it is the shape of the
// measurement: once one package's tests have failed, the mutant is killed, and
// running the remaining binaries could not change that while costing the run
// the very time mutation testing is short of.
//
// Which binaries "the test binaries" means is [MutantRun.Binaries]: every one
// of them by default, and the coverage-selected subset when the caller narrowed
// it. Narrowing changes the cost of a run and not its meaning — a binary that
// never reaches the mutant's lines can only report that it survived.
//
// The environment is composed rather than inherited — see [mutantEnv] — and the
// working directory is each binary's own package directory, because a Go test
// resolves testdata relative to where it runs. That working directory is inside
// the snapshot, which is why [Options.ScratchDir] is resolved against the
// go-mutants process's own directory before it is created and handed over, and
// why one that cannot be resolved is refused rather than passed along.
//
// RunOne is safe for concurrent use as long as each caller passes a distinct
// [Options.ScratchDir]; [Schedule] gives every worker its own.
func RunOne(ctx context.Context, opts Options, m MutantRun, bins []TestBinary) Attempt {
	switch {
	case strings.TrimSpace(m.ID) == "":
		return errored(&Error{Code: CodeMutantInvalid, Message: "the mutant has no activation identity"})
	case m.Timeout <= 0:
		return errored(&Error{
			Code:    CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) + " has no timeout, and go-mutants does not run a test binary unbounded",
		})
	case len(bins) == 0:
		return errored(&Error{
			Code: CodeNoTestBinaries,
			Message: "the mutant " + display(m.ID) +
				" has no test binaries to be measured against; reporting it as survived would be a green produced by running nothing",
		})
	}
	if err := validateArgs(m); err != nil {
		return errored(err)
	}

	selected, err := selectBinaries(m, bins)
	if err != nil {
		return errored(err)
	}
	if err = validateTestSelection(m.Tests, selected, func(importPath, why string) error {
		return &Error{
			Code:    CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) + " names tests of " + importPath + " " + why,
		}
	}); err != nil {
		return errored(err)
	}

	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return errored(err)
	}

	env := mutantEnvFrom(opts.Env, m.ID, scratch)
	logs := planTestLog(m.RecordTestLog, scratch, m.Args)

	attempt := Attempt{Outcome: mutation.OutcomeSurvived}
	for i, bin := range selected {
		// Asked before each binary rather than only after one answers, so a
		// cancelled run stops instead of starting the rest of the queue just to
		// have internal/runner refuse each one in turn.
		if ctx.Err() != nil {
			attempt.Outcome = mutation.OutcomeNotRun
			return attempt
		}

		logPath := logs.path(i)
		tests := m.Tests[bin.ImportPath]
		spec, result := startTarget(ctx, opts, trace.ExecKindMutantRun, m.ID, bin, env,
			m.Timeout, m.MemoryLimit, m.Args, tests, logPath, m.OutputLimit)
		attempt.Duration += result.Duration
		attempt.PeakMemory = max(attempt.PeakMemory, result.PeakMemory)
		attempt.Binaries = append(attempt.Binaries, bin.ImportPath)
		if len(tests) > 0 {
			if attempt.Tests == nil {
				attempt.Tests = make(map[string][]string)
			}
			attempt.Tests[bin.ImportPath] = slices.Clone(tests)
		}
		if result.TraceSeq != 0 {
			attempt.ExecSeqs = append(attempt.ExecSeqs, result.TraceSeq)
		}
		// Read as soon as the binary is gone and before anything is decided
		// about it, so that a killed or failing target still reports what it had
		// managed to write.
		var record TestLog
		if logs.record {
			record = logs.read(bin, logPath, result)
			attempt.TestLogs = append(attempt.TestLogs, record)
		}

		// The order of these cases is the contract, and the third is the one
		// that is easy to get wrong. internal/runner reports no exit status only
		// for a tree it killed itself, so — the timeout having already been
		// ruled out above — [runner.ExitCodeUnavailable] means the child was
		// cancelled. It is deliberately matched before the "non-zero is a kill"
		// branch, because -1 is very much non-zero: reading it as a kill would
		// turn every mutant in flight at Ctrl-C into a detection.
		switch {
		case result.Err != nil:
			attempt.Outcome = mutation.OutcomeErrored
			attempt.keep(result)
			attempt.Err = &Error{
				Code:    CodeMutantStart,
				Message: "the test binary for " + bin.ImportPath + " could not be run",
				Output:  attempt.OutputTail,
				Err:     result.Err,
				// The binary is one go-mutants compiled itself, in a snapshot that
				// is deleted when the run ends, so the import path alone leaves
				// nothing to reproduce. The runner's own error already named the
				// command; this reuses it rather than describing it twice.
				Invocation: runner.CommandOf(spec, result),
				// And the import path beside it, because that is the part that
				// outlives the snapshot: a caller reports, groups and looks up a
				// package, and it is *this* binary's rather than whatever the
				// caller selected — a request may name a directory, or nothing at
				// all and mean every prepared binary.
				Package: bin.ImportPath,
			}
			return attempt

		case result.TimedOut:
			// Not a verdict. Schedule retries this serially before anybody is
			// allowed to call it a detection.
			attempt.Outcome = mutation.OutcomeTimedOut
			attempt.KilledBy = bin.ImportPath
			attempt.keep(result)
			return attempt

		case result.MemoryExceeded:
			// A verdict, and — unlike the timeout above — one that is settled
			// here. A timeout is retried serially because the machine may simply
			// have been busy; a memory bound is not a measurement of how loaded
			// the machine is. It is a multiple of what the unmutated tests were
			// measured to need, and a tree that reached it did so by allocating,
			// which running it again on an idle machine would only do a second
			// time.
			//
			// It sits ahead of the unavailable-status branch below because a
			// killed tree carries no status: read there, this mutant would be
			// reported as interrupted, which is the "not run" that quietly
			// removes it from the score.
			attempt.Outcome = mutation.OutcomeKilled
			attempt.KilledBy = bin.ImportPath
			attempt.MemoryExceeded = true
			attempt.keep(result)
			return attempt

		case result.ExitCode == runner.ExitCodeUnavailable:
			// Not run, and named. The outcome is what the score is computed
			// from and it says the mutant was never measured; the error beside
			// it says which binary was in flight when the signal arrived, which
			// is the first question anybody asks of an interrupted run and the
			// one thing the outcome cannot answer. See [Attempt.Err].
			attempt.Outcome = mutation.OutcomeNotRun
			attempt.Err = &Error{
				Code:    CodeInterrupted,
				Message: "the test binary for " + bin.ImportPath + " was interrupted",
				// What the suite had printed by the time the supervisor killed
				// it, which is how far the run had got. It is kept here rather
				// than in [Attempt.OutputTail] deliberately: that field is the
				// *deciding* binary's output and reaches the report as this
				// mutant's evidence, and an attempt nobody finished decided
				// nothing. On the error it travels with the command it belongs
				// to and is printed under it.
				Output:     tail(result.Output),
				Err:        context.Cause(ctx),
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			}
			return attempt

		case result.ExitCode == instrument.UnknownMutantExit:
			// The generated runtime refusing an identity it has never heard of.
			// Never a kill: the catalogue and the instrumented tree have drifted
			// apart, and a score built on that would be a fiction.
			attempt.Outcome = mutation.OutcomeErrored
			attempt.keep(result)
			attempt.Err = &Error{
				Code: CodeStaleCatalog,
				Message: "the generated runtime in " + bin.ImportPath + " does not know the mutant " +
					display(m.ID) + "; the catalogue and the instrumented snapshot disagree",
				Output: attempt.OutputTail,
				// Nothing failed down in the runner — the child ran and refused —
				// so there is no inner error carrying the command, and this names
				// the binary and the directory it ran in.
				//
				// Not the activation: [instrument.ActiveEnv] is in the child's
				// environment, which an [runner.Invocation] deliberately does not
				// carry and the renderer therefore never prints. The `command:`
				// line under this failure is the binary run *unactivated*, which
				// is the honest thing to hand somebody — it is a real command
				// they can paste — and reproducing the mutant itself is what
				// `explain` is for.
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			}
			return attempt

		case testLogUnsupported(logPath, result, record):
			// A binary that refused the flag exited 2 having run no test, and
			// exit 2 is non-zero — so this case sits ahead of the kill below
			// deliberately. A missing feature is not a detection, and reporting
			// one would be a score built on binaries that never started a test.
			attempt.Outcome = mutation.OutcomeErrored
			attempt.keep(result)
			attempt.Err = testLogUnsupportedError("mutant run", bin, spec, result)
			return attempt

		case result.ExitCode != 0:
			attempt.Outcome = mutation.OutcomeKilled
			attempt.KilledBy = bin.ImportPath
			attempt.keep(result)
			return attempt
		}
	}
	return attempt
}

// keep records the deciding binary's capture on the attempt: the bytes the
// budget kept, the two facts about what was dropped, and the tail a console
// prints.
//
// The four are set together in one place so that they cannot drift apart. The
// tail is a summary of exactly the bytes in Output, and a later branch that set
// one without the other would produce an attempt whose summary described output
// it does not carry.
//
// The clone is not a formality: [runner.Result.Output] is the buffer the
// capture built, and an attempt is a value that outlives the run it came from.
func (a *Attempt) keep(result runner.Result) {
	a.Output = slices.Clone(result.Output)
	a.OutputBytes = result.OutputBytes
	a.Truncated = result.Truncated
	a.OutputTail = tail(result.Output)
}

// startTarget starts one prepared test binary and waits for it.
//
// This is the whole of what [RunOne] and [RunProbe] do to a child process, and
// it is one function precisely because the two must agree about it. A probe
// pass is only evidence about a mutant run if the same tests ran the same way:
// same working directory — a Go test resolves testdata relative to where it
// runs — same paired timeouts, and the same arguments in the same order. What
// the two trees disagree about is the *tree* and the environment composed for
// it, both of which arrive here already decided.
//
// It returns the spec alongside the result because a failure has to be able to
// name what was started, and this is the only place that knows: the argument
// vector is composed here and nowhere else. Rebuilding it at the call site to
// put it into an error would be a second copy of this function's rules, which
// is precisely the drift the one function exists to prevent.
//
// The label is the caller's, and it is the one thing about a start that this
// function must not decide. A probe process and a mutant's are the same binary
// started the same way — that is the point of them sharing this function — so
// in a recording the two passes are told apart by their kind and by nothing
// else, and a default here would be the one place a pass could be recorded as
// the other.
//
// The deadline is rendered as a duration string rather than a number of
// seconds, because `-test.timeout` takes Go's own duration syntax and a
// sub-second budget written as a number would truncate to `0`.
//
// testLogPath is where this binary writes what it consulted, and is empty for a
// call that records nothing. It goes ahead of the caller's arguments, which is
// where cmd/go puts its own: the standard flag package keeps the last value it
// sees, so a flag placed after a target's arguments would be the one the engine
// silently overrode rather than the one it supplied.
func startTarget(
	ctx context.Context,
	opts Options,
	kind string,
	subject string,
	bin TestBinary,
	env []string,
	timeout time.Duration,
	memoryLimit int64,
	args []string,
	tests []string,
	testLogPath string,
	outputLimit int,
) (runner.Spec, runner.Result) {
	argv := make([]string, 0, len(args)+4)
	argv = append(argv, bin.BinPath, "-test.timeout="+(InProcessTimeoutFactor*timeout).String())
	if testLogPath != "" {
		argv = append(argv, testLogFlagName+"="+testLogPath)
	}
	if len(tests) > 0 {
		argv = append(argv, testRunSelector(tests))
	}
	argv = append(argv, args...)
	spec := runner.Spec{
		Argv:        argv,
		Dir:         bin.Dir,
		Env:         env,
		Timeout:     timeout,
		MemoryLimit: memoryLimit,
		OutputLimit: outputLimit,
		Trace:       opts.Trace,
		Kind:        kind,
		Subject:     subject,
	}
	return spec, opts.runProcess(ctx, spec)
}

// overridesTimeout reports whether a target's arguments try to set the flag the
// process supervisor owns. Both spellings accepted by the standard flag package
// are matched, including the separated value form; allowing either would let a
// target turn off the in-process half of the timeout guarantee while the public
// API still claimed the supplied budget.
//
// The rule is one function and the sentences are three, because the three
// passes — a mutant run, a probe pass and a control run — refuse the same flag
// and have to say so about three different things. A second copy of the *rule*
// is what would go wrong: a spelling added to one and not the others would
// leave one of the three able to disarm its own supervisor.
func overridesTimeout(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.timeout")
	})
}

// validateArgs protects the two flags RunOne owns: the timeout always, and the
// test log while the run asked for one.
func validateArgs(m MutantRun) error {
	switch {
	case overridesTimeout(m.Args):
		return &Error{
			Code: CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) +
				" target overrides -test.timeout, which is reserved by the process supervisor",
		}
	case len(m.Tests) > 0 && suppliesTestRun(m.Args):
		return &Error{
			Code: CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) +
				" target supplies -test.run, which this run reserved by narrowing the measurement to named tests",
		}
	case m.RecordTestLog && suppliesTestLog(m.Args):
		return &Error{
			Code: CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) + " target supplies " + testLogFlagName +
				", which this run reserved by asking for the test log to be recorded",
		}
	}
	return nil
}

// selectSubset resolves a subset of binary indices against the binaries a call
// was given, and leaves both refusals to the caller.
//
// The resolution is one function and the sentences are three, for the reason
// [overridesTimeout] is one: a mutant run, a probe pass and a control run all
// narrow the same slice by the same rule, while what an empty or out-of-range
// subset *means* is different for each — a mutant no binary covers, a pass that
// would license skipping every execution, a control that would report the
// original program passing having started nothing. Each keeps its own code and
// its own sentence; none keeps its own copy of the loop.
//
// The nil case returns the slice itself rather than a copy: the caller owns it,
// nothing here writes to it, and copying every binary list once per mutant would
// be a per-mutant allocation bought with nothing.
func selectSubset(
	subset []int, bins []TestBinary, refuseEmpty func() error, refuseIndex func(index int) error,
) ([]TestBinary, error) {
	if subset == nil {
		return bins, nil
	}
	if len(subset) == 0 {
		return nil, refuseEmpty()
	}
	selected := make([]TestBinary, 0, len(subset))
	for _, index := range subset {
		if index < 0 || index >= len(bins) {
			return nil, refuseIndex(index)
		}
		selected = append(selected, bins[index])
	}
	return selected, nil
}

// selectBinaries resolves [MutantRun.Binaries] against the binaries this run
// was given.
func selectBinaries(m MutantRun, bins []TestBinary) ([]TestBinary, error) {
	return selectSubset(m.Binaries, bins,
		func() error {
			return &Error{
				Code: CodeMutantInvalid,
				Message: "the mutant " + display(m.ID) +
					" was given an empty set of test binaries to be measured against; a mutant no binary covers is not executed at all, and running none of them would report it as survived having started nothing",
			}
		},
		func(index int) error {
			return &Error{
				Code: CodeMutantInvalid,
				Message: "the mutant " + display(m.ID) + " names test binary " + strconv.Itoa(index) +
					" of " + strconv.Itoa(len(bins)) + "; the caller's binaries and this run's have drifted apart",
			}
		})
}

// workerScratch resolves a worker's temporary directory, makes sure it exists,
// and reports the empty parent as "leave the inherited one alone".
//
// The resolution is not a formality, and it is why an unresolvable directory is
// refused rather than passed along. The path this returns is handed to a child
// as TMP, TEMP and TMPDIR, and that child runs in a package directory *inside
// the snapshot* — so a relative path would send a test's temporary files into
// the tree every later mutant is measured against, which is exactly the drift
// the scratch directory exists to prevent. It is resolved against the go-mutants
// process's working directory, which is where the creation below would have put
// it in any case.
func workerScratch(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil || abs == "" {
		return "", &Error{
			Code: CodeScratchDir,
			Message: "the worker's temporary directory " + strconv.Quote(dir) +
				" could not be resolved against the working directory",
			Err: err,
		}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", &Error{
			Code:    CodeScratchDir,
			Message: "the worker's temporary directory " + strconv.Quote(abs) + " could not be created",
			Err:     err,
		}
	}
	return abs, nil
}

// errored builds the attempt that reports a failure of go-mutants itself,
// before any child process was started.
func errored(err error) Attempt {
	return Attempt{Outcome: mutation.OutcomeErrored, Err: err}
}

// display shortens an activation identity for a message. Reports carry the
// full identity; a one-line diagnostic carries as much of it as the console
// shows.
func display(id string) string {
	const shown = 20
	if len(id) <= shown {
		return id
	}
	return id[:shown]
}
