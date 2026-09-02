// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testflag"
)

// A ProbeRun is one test target to run against the probe tree.
//
// It is [MutantRun] with the activation identity taken out and the log put in,
// which is the whole difference between the two passes: a probe tree activates
// nothing and records everything it would have changed.
type ProbeRun struct {
	// Timeout bounds one attempt at one test binary, exactly as
	// [MutantRun.Timeout] does, and is required for the same reason: a pass
	// that never ends is worse than a mutant measured wrongly.
	Timeout time.Duration

	// Binaries narrows the pass to a subset of the test binaries, as indices
	// into the `bins` slice, exactly as [MutantRun.Binaries] does. Nil means
	// every binary.
	//
	// A non-nil but empty subset is refused rather than obeyed, and here the
	// refusal is a soundness rule rather than a policy: a pass that started
	// nothing would come back with the empty set of infected mutants, which is
	// the same string of bytes as "every probed mutant is untouched by this
	// test" and licenses skipping every execution of it.
	Binaries []int

	// Args are passed verbatim to each selected test binary after the
	// harness-owned timeout flag, as [MutantRun.Args] are, and `-test.timeout`
	// is reserved for the same reason.
	Args []string

	// LogPath is the file the probe runtime appends its infection log to, as
	// [instrument.ProbeEnv] names it. It is required: a pass with nowhere to
	// record would exit zero having written nothing, which reads exactly like a
	// pass that recorded nothing.
	//
	// Every binary of one pass is given the *same* path. Several processes
	// appending to one log is what the format is built for, and it is what
	// makes the answer a statement about the target rather than about one of
	// the binaries that ran it. The caller owns the file: it must be private to
	// this pass, because a log two passes appended to cannot be told apart.
	LogPath string

	// Digest and Mutants are the catalogue the indices are dense in — its
	// [mutation.Catalog.Digest] and [mutation.Catalog.Len] — and are what
	// [instrument.ReadInfectionLog] checks the log's header against. They are
	// the catalogue's own, not the runtime's array width; the reader derives
	// the width from the size through the rule the generators use.
	Digest  string
	Mutants int
}

// A ProbeOutcome is how one pass over the probe tree ended.
//
// Exactly one of the four is a measurement. That asymmetry is the design: an
// infection fact is a licence not to execute a test, so anything the pass
// cannot vouch for has to be reported as "no facts" and never as "nothing was
// infected", which is the same answer spelled in a way a caller would act on.
type ProbeOutcome string

// The probe outcomes.
const (
	// ProbeMeasured is a pass whose every binary exited zero and whose log
	// could be read. It is the only outcome carrying [ProbeAttempt.Infected].
	ProbeMeasured ProbeOutcome = "measured"
	// ProbeTestFailed is a pass in which a test binary exited non-zero. The
	// probe tree is semantics-preserving, so a red suite there is a flaky test
	// or a bug in go-mutants — and in either case the run cannot be trusted to
	// have reached the sites it would have reached.
	ProbeTestFailed ProbeOutcome = "test-failed"
	// ProbeTimedOut is a pass the supervisor had to kill. The sites it had not
	// reached yet are indistinguishable from the ones it would never reach.
	ProbeTimedOut ProbeOutcome = "timed-out"
	// ProbeUnavailable is a pass whose runtime exited
	// [instrument.ProbeUnavailableExit]: it could not open or write the log it
	// was told to. The process refuses to run rather than run silently, because
	// silence is the one lie a probe must never tell.
	ProbeUnavailable ProbeOutcome = "unavailable"
)

// A ProbeAttempt is one pass over the test binaries of the probe tree.
type ProbeAttempt struct {
	// Outcome is how the pass ended. It is meaningful only when Err is nil.
	Outcome ProbeOutcome
	// Infected are the catalogue indices whose site produced a value the
	// mutant would not have, sorted ascending and distinct.
	//
	// It is non-nil exactly when Outcome is [ProbeMeasured], the empty set
	// included — a target that ran and infected nothing is a fact, and the one
	// a caller acts on most. Every other outcome, and every error, carries nil,
	// so a caller that forgets to look at the outcome ranges over nothing
	// rather than over a set that means something else.
	Infected []uint32
	// ExitCode is the status of the binary that decided the pass: the failing
	// one, or the last one when every binary passed.
	ExitCode int
	// Duration is the wall-clock time the child processes took, summed over the
	// binaries this pass actually ran.
	Duration time.Duration
	// OutputTail is the last [OutputTailLines] lines the deciding binary
	// printed. It is empty for a measured pass, whose output is a passing
	// suite.
	OutputTail string
	// Err is set when the pass could not be made at all, and always carries a
	// [Code] from this package. It is never set alongside facts.
	Err error
}

// RunProbe runs one test target against the probe tree and reports which
// catalogued mutants that target could have observed.
//
// It is [RunOne]'s sibling and shares its process core — see [startTarget] —
// because the evidence is only evidence if the same tests ran the same way.
// What differs is everything above that: no mutant is activated, the
// environment names a log instead of an identity, and the binaries are the
// probe tree's rather than the mutant tree's.
//
// The pass stops at the first binary that does not exit zero, and that is a
// soundness rule rather than a saving. The indices the remaining binaries would
// append cannot be combined with a pass that already failed: the result would
// be a subset of the truth wearing the shape of the whole of it, and a subset
// here is a test skipped that should have run. For the same reason the log is
// read only after every selected binary has passed.
//
// A missing log after a clean exit is the empty set rather than a failure. The
// generated runtime writes its header in `init`, before any test code runs, so
// a binary that produced no file is a binary that never linked a probe — and
// one that never linked a probe ran no probed site. An *existing* log that
// cannot be read is [CodeProbeLog]: it is a measurement nobody can interpret,
// and the part of it that still parses is exactly what a smaller, wrong answer
// looks like.
//
// RunProbe is safe for concurrent use as long as each caller passes a distinct
// [Options.ScratchDir] and a distinct [ProbeRun.LogPath]. Two passes appending
// to one log would each read the other's indices as their own.
func RunProbe(ctx context.Context, opts Options, p ProbeRun, bins []TestBinary) ProbeAttempt {
	switch {
	case p.Timeout <= 0:
		return probeErrored(&Error{
			Code:    CodeProbeInvalid,
			Message: "the probe pass has no timeout, and go-mutants does not run a test binary unbounded",
		})
	case strings.TrimSpace(p.LogPath) == "":
		return probeErrored(&Error{
			Code: CodeProbeInvalid,
			Message: "the probe pass has no infection log to record into; a pass that recorded nowhere would " +
				"exit having written nothing, which reads exactly like a pass that saw nothing infected",
		})
	case len(bins) == 0:
		return probeErrored(&Error{
			Code: CodeProbeInvalid,
			Message: "the probe pass has no test binaries to measure; reporting no infected mutants having " +
				"started nothing would license skipping every execution of this target",
		})
	}
	if err := validateProbeArgs(p); err != nil {
		return probeErrored(err)
	}
	selected, err := selectProbeBinaries(p, bins)
	if err != nil {
		return probeErrored(err)
	}
	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return probeErrored(err)
	}

	env := probeEnvFrom(opts.Env, scratch, p.LogPath)
	attempt := ProbeAttempt{Outcome: ProbeMeasured}
	for _, bin := range selected {
		// Asked before each binary, as [RunOne] asks, so a cancelled run stops
		// rather than starting the rest of the queue to have each refused.
		if ctx.Err() != nil {
			return probeErrored(probeInterrupted(ctx))
		}

		result := startTarget(ctx, opts, bin, env, p.Timeout, p.Args)
		attempt.Duration += result.Duration
		attempt.ExitCode = result.ExitCode

		// The order of these cases is [RunOne]'s, and the third is the one that
		// is easy to get wrong: internal/runner reports no exit status only for
		// a tree it killed itself, so — the timeout having been ruled out
		// already — [runner.ExitCodeUnavailable] means the child was cancelled.
		// Reading it as an ordinary non-zero status would turn every probe in
		// flight at Ctrl-C into a "test-failed", which is harmless, and reading
		// it as a pass would turn it into a licence.
		switch {
		case result.Err != nil:
			return probeErrored(&Error{
				Code:    CodeProbeStart,
				Message: "the probe tree's test binary for " + bin.ImportPath + " could not be run",
				Output:  tail(result.Output),
				Err:     result.Err,
			})

		case result.TimedOut:
			attempt.Outcome = ProbeTimedOut
			attempt.OutputTail = tail(result.Output)
			return attempt

		case result.ExitCode == runner.ExitCodeUnavailable:
			return probeErrored(probeInterrupted(ctx))

		case result.ExitCode == instrument.ProbeUnavailableExit:
			// The generated runtime refusing to run because it cannot record.
			// Never a failed suite: the tests were not the thing that went
			// wrong, and the difference is what tells a broken machine from a
			// broken test.
			attempt.Outcome = ProbeUnavailable
			attempt.OutputTail = tail(result.Output)
			return attempt

		case result.ExitCode != 0:
			attempt.Outcome = ProbeTestFailed
			attempt.OutputTail = tail(result.Output)
			return attempt
		}
	}

	infected, err := readInfection(p)
	if err != nil {
		return probeErrored(err)
	}
	attempt.Infected = infected
	return attempt
}

// readInfection reads back the log every binary of the pass appended to.
//
// The missing file is the whole subtlety and is argued at [RunProbe]. Note that
// it is answered with an *allocated* empty slice rather than nil: this
// package's contract is that nil means "no facts", and "no site was infected"
// is a fact.
func readInfection(p ProbeRun) ([]uint32, error) {
	file, err := os.Open(p.LogPath)
	if errors.Is(err, fs.ErrNotExist) {
		return []uint32{}, nil
	}
	if err != nil {
		return nil, &Error{
			Code: CodeProbeLog,
			Message: "the infection log " + strconv.Quote(p.LogPath) +
				" is there and could not be opened, so what the pass recorded cannot be read",
			Err: err,
		}
	}
	defer func() { _ = file.Close() }()

	infected, err := instrument.ReadInfectionLog(file, p.Digest, p.Mutants)
	if err != nil {
		return nil, &Error{
			Code: CodeProbeLog,
			Message: "the infection log " + strconv.Quote(p.LogPath) +
				" cannot be read against the catalogue it was written for",
			Err: err,
		}
	}
	if infected == nil {
		infected = []uint32{}
	}
	return infected, nil
}

// validateProbeArgs protects the timeout owned by the pass, exactly as
// [validateArgs] protects [RunOne]'s and for the same reason: the outer
// process-tree supervisor and the in-process deadline are a paired boundary,
// and a target that turned half of it off would leave a probe process able to
// outlive the budget the caller was promised.
func validateProbeArgs(p ProbeRun) error {
	for _, arg := range p.Args {
		if testflag.Match(arg, "test.timeout") {
			return &Error{
				Code:    CodeProbeInvalid,
				Message: "the probe target overrides -test.timeout, which is reserved by the process supervisor",
			}
		}
	}
	return nil
}

// selectProbeBinaries resolves [ProbeRun.Binaries] against the binaries this
// pass was given, as [selectBinaries] does for a mutant.
//
// The nil case returns the slice itself rather than a copy, for the reason
// given there: the caller owns it and nothing here writes to it.
func selectProbeBinaries(p ProbeRun, bins []TestBinary) ([]TestBinary, error) {
	if p.Binaries == nil {
		return bins, nil
	}
	if len(p.Binaries) == 0 {
		return nil, &Error{
			Code: CodeProbeInvalid,
			Message: "the probe pass was given an empty set of test binaries; a pass that started none of " +
				"them would report no infected mutants having measured nothing",
		}
	}
	selected := make([]TestBinary, 0, len(p.Binaries))
	for _, index := range p.Binaries {
		if index < 0 || index >= len(bins) {
			return nil, &Error{
				Code: CodeProbeInvalid,
				Message: "the probe pass names test binary " + strconv.Itoa(index) + " of " +
					strconv.Itoa(len(bins)) + "; the caller's binaries and this pass's have drifted apart",
			}
		}
		selected = append(selected, bins[index])
	}
	return selected, nil
}

// probeInterrupted builds the failure of a pass a cancelled context ended. The
// cause stays reachable, which is how a caller tells a Ctrl-C from a machine
// that broke.
func probeInterrupted(ctx context.Context) error {
	return &Error{
		Code:    CodeInterrupted,
		Message: "the probe pass was interrupted",
		Err:     context.Cause(ctx),
	}
}

// probeErrored builds the attempt that reports a pass which yielded no facts at
// all. Infected stays nil, which is the whole contract.
func probeErrored(err error) ProbeAttempt {
	return ProbeAttempt{Err: err}
}
