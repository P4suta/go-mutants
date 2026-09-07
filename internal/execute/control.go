// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// A ControlRun is one run of the *original program* through the prepared test
// binaries.
//
// It is [MutantRun] with the activation identity taken out and nothing put in
// its place, and that is the whole of it. The mutant tree's binaries are the
// user's program plus a switch — the generated runtime takes every original
// branch when [instrument.ActiveEnv] is unset — so the same binaries, started
// the same way with nothing activated, *are* the program the user wrote. A
// caller that needed a control beside a mutant run had to freeze a second copy
// of the module and build a second set of binaries to get one; this is that
// answer out of the binaries it already has.
type ControlRun struct {
	// Timeout bounds one attempt at one test binary, exactly as
	// [MutantRun.Timeout] does, and is required for the same reason: a control
	// that never ends is worse than one reported wrongly.
	Timeout time.Duration

	// MemoryLimit bounds the resident memory of each test binary's whole
	// process tree, exactly as [MutantRun.MemoryLimit] does and with the same
	// meaning for zero. A control is what a mutant's execution is compared
	// against, so it is measured under the mutant's budget: a control given
	// more of the machine than the execution beside it is a control of a
	// different program.
	MemoryLimit int64

	// Binaries narrows the run to a subset of the test binaries, as indices
	// into the `bins` slice, exactly as [MutantRun.Binaries] does. Nil means
	// every binary, in the order they were given.
	//
	// A non-nil but empty subset is refused rather than obeyed, and here the
	// refusal is about meaning rather than cost: a control that started nothing
	// would come back as exit 0, which is the same answer as "the original
	// program passes these tests" — and that answer is exactly what licenses a
	// consumer to read a mutant's failing suite as the mutant's doing.
	Binaries []int

	// Args are passed verbatim to each selected test binary after the
	// harness-owned timeout flag, as [MutantRun.Args] are, and `-test.timeout`
	// is reserved for the same reason. They are meant to be the *same*
	// arguments the mutant run was given: a control of a different target is a
	// control of nothing.
	Args []string

	// OutputLimit caps the combined output kept from each binary the control
	// starts, exactly as [MutantRun.OutputLimit] does and with the same
	// defaults.
	OutputLimit int

	// RecordTestLog asks each binary the control starts to write down what it
	// consulted, exactly as [MutantRun.RecordTestLog] does — and a caller
	// comparing a mutant run against its control asks both or neither, because
	// the inputs the original program read are what say whether the two are
	// still about the same thing.
	RecordTestLog bool
}

// A ControlAttempt is one pass over the test binaries with nothing activated.
//
// It carries no outcome vocabulary, deliberately. A control is not a mutant and
// has nothing to survive or be killed by: what it reports is what the original
// program did — a status, or a timeout — and turning that into a verdict is the
// caller's business.
type ControlAttempt struct {
	// Package is the import path of the test binary that decided the run: the
	// one whose tests failed, or the one that hung. It is empty when every
	// binary passed, which is the same rule [Attempt.KilledBy] follows and for
	// the same reason — a name here is a name the caller can report, and one
	// invented for a run nothing decided would be a fact about nothing.
	Package string

	// ExitCode is the status of the binary this run stopped at — the deciding
	// binary's; when nothing decided, the last binary that ran — and it is
	// [runner.ExitCodeUnavailable] for a tree the supervisor killed.
	//
	// The timeout case is the one worth stating. internal/runner reports no
	// exit status at all for a tree it killed and says so with that value
	// rather than by inventing one, and it is carried up unchanged — exactly as
	// the public CommandResult carries it for a workspace command with the same
	// field set. A zero here would be a status the child never returned, and it
	// would read as *green* to a caller that forgot to look at TimedOut.
	ExitCode int
	// TimedOut reports a binary the supervisor had to kill at [ControlRun.Timeout].
	TimedOut bool

	// Duration is the wall-clock time the child processes took, summed over
	// every binary this run started — which is not the binary Output describes.
	// It is zero when Err is set, along with everything else the run did not
	// establish.
	Duration time.Duration

	// Output is what [ControlRun.OutputLimit] kept of one binary's combined
	// output: the deciding binary's; when nothing decided, the last binary that
	// ran. OutputBytes is everything *that* binary wrote whether kept or not —
	// it is one binary's total and never the run's, which is why Duration sums
	// over the binaries started and this does not — and Truncated reports that
	// the cap dropped some of it, in which case Output begins with
	// [runner.OutputTruncatedPrefix]. A consumer that wants one package's output
	// asks for that package.
	//
	// Unlike [Attempt], a control keeps the capture even when everything
	// passed, and the asymmetry is deliberate. A survivor's output is thousands
	// of lines of nothing having gone wrong multiplied by every mutant in a
	// run, which is the memory [Attempt] exists to bound; a control is one run
	// per mutant run at most, and its output is the very thing a consumer diffs
	// a mutant's failure against.
	Output      []byte
	OutputBytes int64
	Truncated   bool

	// PeakMemory is the highest memory any binary of this run was
	// observed to hold, in bytes, and MemoryExceeded reports that one of them
	// passed [ControlRun.MemoryLimit] and had its tree killed for it. They are
	// [Attempt.PeakMemory] and [Attempt.MemoryExceeded] exactly, and MemoryExceeded
	// stands beside TimedOut rather than inside it for the reason
	// [runner.Result] keeps them apart: they are different kills, and a consumer
	// that conflated them would report the user's program as slow when it is
	// large.
	PeakMemory     int64
	MemoryExceeded bool

	// Binaries are the test binaries this run started, in launch order, by
	// import path, and ExecSeqs the `exec` events they were recorded at. They
	// are [Attempt.Binaries] and [Attempt.ExecSeqs] exactly and mean the same
	// thing: what was run, and where the account of it is. The list stops where
	// the run stopped.
	Binaries []string
	ExecSeqs []int64

	// TestLogs are what each binary this run started recorded about what it
	// consulted, one per element of Binaries and in the same order. It is
	// [Attempt.TestLogs] exactly, and nil unless [ControlRun.RecordTestLog]
	// asked for it.
	TestLogs []TestLog

	// Err is set when the control could not be made at all, and always carries
	// a [Code] from this package. It is never set alongside facts: a run that
	// failed reports no status, because a status it did not observe is exactly
	// what a consumer would act on.
	Err error
}

// RunControl runs the original program through the prepared test binaries and
// reports what it did.
//
// It is [RunOne]'s sibling and shares its process core — see [startTarget] —
// because a control is only a control if it is the same program started the
// same way: the same executable, the same working directory, the same paired
// timeouts and the same arguments in the same order. What differs is one entry
// in the environment, and that is the point of the whole function.
//
// The run stops at the first binary that does not exit zero, and here that is
// not an optimisation but the answer: a control run asks whether the original
// program passes these tests, and the first binary that says no has answered
// it. A control that fails is a fact about the repository — the suite is red,
// or flaky, or depends on something the frozen snapshot does not carry — and
// running the rest could only say so again.
//
// A cancelled context is an error rather than a result. internal/runner reports
// no exit status for a child it killed, and reading that as an ordinary
// non-zero status would report the original program as failing whenever
// somebody stopped the run — the worst of the wrong answers, because a consumer
// would then attribute nothing to any mutant.
//
// RunControl is safe for concurrent use as long as each caller passes a
// distinct [Options.ScratchDir], as [RunOne] is.
func RunControl(ctx context.Context, opts Options, c ControlRun, bins []TestBinary) ControlAttempt {
	switch {
	case c.Timeout <= 0:
		return controlErrored(&Error{
			Code:    CodeControlInvalid,
			Message: "the control run has no timeout, and go-mutants does not run a test binary unbounded",
		})
	case len(bins) == 0:
		return controlErrored(&Error{
			Code: CodeControlInvalid,
			Message: "the control run has no test binaries to run the original program in; reporting it " +
				"as having passed while having started nothing would license blaming every mutant for a suite that never ran",
		})
	}
	if err := validateControlArgs(c); err != nil {
		return controlErrored(err)
	}
	selected, err := selectControlBinaries(c, bins)
	if err != nil {
		return controlErrored(err)
	}
	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return controlErrored(err)
	}

	env := controlEnvFrom(opts.Env, scratch)
	logs := planTestLog(c.RecordTestLog, scratch, c.Args)
	attempt := ControlAttempt{}
	// The capture of whichever binary the run ends at, kept by reference until
	// then. Cloning every binary's would copy up to a whole output budget per
	// package to keep one, and every copy but the last would be discarded.
	var last runner.Result
	for i, bin := range selected {
		// Asked before each binary, as [RunOne] and [RunProbe] ask, so a
		// cancelled run stops rather than starting the rest of the queue to
		// have each one refused. Nothing is running at this point — whatever
		// came before has been reaped — so the failure names no command.
		if ctx.Err() != nil {
			return attempt.failed(controlInterrupted(ctx, "", nil))
		}

		logPath := logs.path(i)
		spec, result := startTarget(ctx, opts, trace.ExecKindControlRun, bin.ImportPath,
			bin, env, c.Timeout, c.MemoryLimit, c.Args, logPath, c.OutputLimit)
		last = result
		attempt.Duration += result.Duration
		attempt.PeakMemory = max(attempt.PeakMemory, result.PeakMemory)
		// Carried up as internal/runner reported it, [runner.ExitCodeUnavailable]
		// included: see [ControlAttempt.ExitCode]. A failure drops it again,
		// because a run that could not be made observed no status at all.
		attempt.ExitCode = result.ExitCode
		attempt.Binaries = append(attempt.Binaries, bin.ImportPath)
		if result.TraceSeq != 0 {
			attempt.ExecSeqs = append(attempt.ExecSeqs, result.TraceSeq)
		}
		// Read as soon as the binary is gone, as [RunOne] reads it, so that a
		// control that then failed still says what its binaries touched.
		var record TestLog
		if logs.record {
			record = logs.read(bin, logPath, result)
			attempt.TestLogs = append(attempt.TestLogs, record)
		}

		// The order of these cases is [RunOne]'s, and the third is the one that
		// is easy to get wrong: internal/runner reports no exit status only for
		// a tree it killed itself, so — the timeout having been ruled out
		// already — [runner.ExitCodeUnavailable] means the child was cancelled.
		//
		// [instrument.UnknownMutantExit] is deliberately *not* among them.
		// Nothing is activated here, so the generated runtime has no identity
		// to refuse and never produces it; a control that exited with that
		// status did so because the user's own test did, and calling it a stale
		// catalogue would blame go-mutants for the repository's exit code.
		switch {
		case result.Err != nil:
			return attempt.failed(&Error{
				Code:    CodeControlStart,
				Message: "the control run's test binary for " + bin.ImportPath + " could not be run",
				Output:  tail(result.Output),
				Err:     result.Err,
				// The same reasoning as [CodeMutantStart]'s: the binary is one
				// go-mutants compiled itself, in a snapshot that is deleted when
				// the run ends, so the import path alone leaves nothing to
				// reproduce — and the import path beside it is the half of the
				// diagnosis that outlives the tree.
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			})

		case result.TimedOut:
			// The answer, not a step towards one. Unlike a mutant's timeout
			// there is nothing to retry serially: a control that hangs is the
			// user's program hanging, and reporting it twice would cost the
			// budget twice to say the same thing.
			attempt.TimedOut = true
			attempt.Package = bin.ImportPath
			attempt.keep(result)
			return attempt

		case result.MemoryExceeded:
			// The answer too, and the same shape as the timeout above: the
			// original program needed more of the machine than the budget the
			// mutants are measured under, which a consumer reads as "the bound
			// is wrong" rather than as anything about a mutant. Ahead of the
			// unavailable-status branch, because a killed tree carries no
			// status.
			attempt.MemoryExceeded = true
			attempt.Package = bin.ImportPath
			attempt.keep(result)
			return attempt

		case result.ExitCode == runner.ExitCodeUnavailable:
			// This one *was* running when the signal arrived, so it is named.
			return attempt.failed(controlInterrupted(ctx, bin.ImportPath, runner.CommandOf(spec, result)))

		case testLogUnsupported(logPath, result, record):
			// Ahead of the non-zero branch below, for the reason [RunOne] puts
			// it ahead of the kill: a control that reported exit 2 here would
			// say the original program is red, and a consumer reads that as
			// licence to attribute nothing to any mutant.
			return attempt.failed(testLogUnsupportedError("control run", bin, spec, result))

		case result.ExitCode != 0:
			attempt.Package = bin.ImportPath
			attempt.keep(result)
			return attempt
		}
	}
	attempt.keep(last)
	return attempt
}

// keep records one binary's capture on the attempt: the bytes the budget kept,
// and the two facts about what was dropped.
//
// It is called once, at whichever return the run reached, rather than on every
// binary. [runner.Result.Output] is the buffer the capture built and an attempt
// is a value that outlives the run it came from, so exactly one copy has to be
// made — and making one per binary would copy up to a whole output budget per
// package to keep the last of them.
func (a *ControlAttempt) keep(result runner.Result) {
	a.Output = slices.Clone(result.Output)
	a.OutputBytes = result.OutputBytes
	a.Truncated = result.Truncated
}

// ControlRecord is one control run as the recording holds it.
//
// It is a note rather than a payload of its own because the trace contract's
// event `type` enum is closed and holds none for a control: the per-binary
// `exec` events of kind [trace.ExecKindControlRun] are the account of what ran,
// and this is the one line that summarises the call so that the result has an
// event to name. See docs/trace-v1.md.
//
// The detail is free text for a reader and never a field to branch on — the
// facts a consumer acts on are in the `exec` events and in the returned
// [ControlAttempt] — so it is written as a sentence: what the control came to,
// where, which binaries it ran, and which executions those were.
func ControlRecord(c ControlRun, attempt ControlAttempt) trace.NoteRecord {
	var detail strings.Builder
	switch {
	case attempt.Err != nil:
		detail.WriteString(attempt.Err.Error())
	case attempt.TimedOut:
		detail.WriteString("timed out after " + c.Timeout.String())
	default:
		detail.WriteString("exit " + strconv.Itoa(attempt.ExitCode))
	}
	if attempt.Package != "" {
		detail.WriteString(" in " + attempt.Package)
	}
	if len(attempt.Binaries) > 0 {
		detail.WriteString("; binaries: " + strings.Join(attempt.Binaries, ", "))
	}
	if len(attempt.ExecSeqs) > 0 {
		seqs := make([]string, len(attempt.ExecSeqs))
		for i, seq := range attempt.ExecSeqs {
			seqs[i] = strconv.FormatInt(seq, 10)
		}
		detail.WriteString("; exec: " + strings.Join(seqs, ", "))
	}
	return trace.NoteRecord{
		Kind: trace.NoteControl,
		// The code of the failure when there was one, so a note about a control
		// that could not be made is searchable by the same identifier the error
		// a caller received carries. It is empty for every control that ran.
		Code:   CodeOf(attempt.Err).String(),
		Detail: detail.String(),
	}
}

// validateControlArgs protects the two flags the control run owns, exactly as
// [validateArgs] protects [RunOne]'s and through the same two rules.
func validateControlArgs(c ControlRun) error {
	switch {
	case overridesTimeout(c.Args):
		return &Error{
			Code:    CodeControlInvalid,
			Message: "the control target overrides -test.timeout, which is reserved by the process supervisor",
		}
	case c.RecordTestLog && suppliesTestLog(c.Args):
		return &Error{
			Code: CodeControlInvalid,
			Message: "the control target supplies " + testLogFlagName +
				", which this run reserved by asking for the test log to be recorded",
		}
	}
	return nil
}

// selectControlBinaries resolves [ControlRun.Binaries] against the binaries
// this run was given, as [selectBinaries] does for a mutant.
func selectControlBinaries(c ControlRun, bins []TestBinary) ([]TestBinary, error) {
	return selectSubset(c.Binaries, bins,
		func() error {
			return &Error{
				Code: CodeControlInvalid,
				Message: "the control run was given an empty set of test binaries; a control that started " +
					"none of them would report the original program as passing having run nothing",
			}
		},
		func(index int) error {
			return &Error{
				Code: CodeControlInvalid,
				Message: "the control run names test binary " + strconv.Itoa(index) + " of " +
					strconv.Itoa(len(bins)) + "; the caller's binaries and this run's have drifted apart",
			}
		})
}

// controlInterrupted builds the failure of a control a cancelled context ended,
// exactly as [probeInterrupted] does for a pass: the cause stays reachable, and
// pkg and command are the binary that was cut off — empty and nil when the run
// stopped between binaries with nothing running, because naming the command it
// was *about* to start would send a reader looking for a process that never
// existed.
func controlInterrupted(ctx context.Context, pkg string, command *runner.Invocation) error {
	return &Error{
		Code:       CodeInterrupted,
		Message:    "the control run was interrupted",
		Err:        context.Cause(ctx),
		Invocation: command,
		Package:    pkg,
	}
}

// controlErrored builds the attempt that reports a control which observed
// nothing at all, before any binary was started.
func controlErrored(err error) ControlAttempt {
	return ControlAttempt{Err: err}
}

// failed is [controlErrored] for a control that had already started something:
// it reports no status, and it keeps the account of what it ran.
//
// The two halves are deliberately different, as [ProbeAttempt.failed]'s are.
// The status and the capture go, because a control that could not be completed
// observed nothing and a partial answer is exactly what a wrong one looks like;
// the binaries, their executions and their test logs stay, because "which
// binaries had already run, and what did they touch" is the first question a
// failed control raises and the one thing nothing downstream could
// reconstruct.
func (a ControlAttempt) failed(err error) ControlAttempt {
	return ControlAttempt{Binaries: a.Binaries, ExecSeqs: a.ExecSeqs, TestLogs: a.TestLogs, Err: err}
}
