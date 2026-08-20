// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
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
	// Timeout bounds one attempt at one test binary. It is required: a mutant
	// with no budget is refused rather than run unbounded, because a run that
	// never ends is worse than a mutant reported wrongly.
	Timeout time.Duration

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
}

// An Attempt is one pass over the test binaries with one mutant active.
//
// It is deliberately not a verdict. A single timeout is an Attempt with
// [mutation.OutcomeTimedOut] and is *not* a confirmed detection; only
// [Schedule] — which can retry it serially — decides what a mutant's outcome
// finally is.
type Attempt struct {
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
	// OutputTail is the last [OutputTailLines] lines the deciding binary
	// printed: the failing one for a kill, the timed-out one for a timeout, the
	// failing command for an error. It is empty for a survivor, whose output is
	// thousands of lines of nothing having gone wrong.
	OutputTail string
	// Err is set only when Outcome is [mutation.OutcomeErrored], and always
	// carries a [Code] from this package with the underlying cause reachable
	// through it.
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

	selected, err := selectBinaries(m, bins)
	if err != nil {
		return errored(err)
	}

	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return errored(err)
	}

	env := mutantEnv(m.ID, scratch)
	// A duration string rather than a number of seconds: `-test.timeout` takes
	// Go's own duration syntax, and rendering it that way keeps sub-second
	// budgets from truncating to `0`.
	deadline := "-test.timeout=" + (InProcessTimeoutFactor * m.Timeout).String()

	attempt := Attempt{Outcome: mutation.OutcomeSurvived}
	for _, bin := range selected {
		// Asked before each binary rather than only after one answers, so a
		// cancelled run stops instead of starting the rest of the queue just to
		// have internal/runner refuse each one in turn.
		if ctx.Err() != nil {
			attempt.Outcome = mutation.OutcomeNotRun
			return attempt
		}

		result := opts.runProcess(ctx, runner.Spec{
			Argv:    []string{bin.BinPath, deadline},
			Dir:     bin.Dir,
			Env:     env,
			Timeout: m.Timeout,
		})
		attempt.Duration += result.Duration

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
			attempt.OutputTail = tail(result.Output)
			attempt.Err = &Error{
				Code:    CodeMutantStart,
				Message: "the test binary for " + bin.ImportPath + " could not be run",
				Output:  attempt.OutputTail,
				Err:     result.Err,
			}
			return attempt

		case result.TimedOut:
			// Not a verdict. Schedule retries this serially before anybody is
			// allowed to call it a detection.
			attempt.Outcome = mutation.OutcomeTimedOut
			attempt.KilledBy = bin.ImportPath
			attempt.OutputTail = tail(result.Output)
			return attempt

		case result.ExitCode == runner.ExitCodeUnavailable:
			attempt.Outcome = mutation.OutcomeNotRun
			return attempt

		case result.ExitCode == instrument.UnknownMutantExit:
			// The generated runtime refusing an identity it has never heard of.
			// Never a kill: the catalogue and the instrumented tree have drifted
			// apart, and a score built on that would be a fiction.
			attempt.Outcome = mutation.OutcomeErrored
			attempt.OutputTail = tail(result.Output)
			attempt.Err = &Error{
				Code: CodeStaleCatalog,
				Message: "the generated runtime in " + bin.ImportPath + " does not know the mutant " +
					display(m.ID) + "; the catalogue and the instrumented snapshot disagree",
				Output: attempt.OutputTail,
			}
			return attempt

		case result.ExitCode != 0:
			attempt.Outcome = mutation.OutcomeKilled
			attempt.KilledBy = bin.ImportPath
			attempt.OutputTail = tail(result.Output)
			return attempt
		}
	}
	return attempt
}

// selectBinaries resolves [MutantRun.Binaries] against the binaries this run
// was given.
//
// The nil case returns the slice itself rather than a copy: the caller owns it,
// nothing here writes to it, and copying every binary list once per mutant would
// be a per-mutant allocation bought with nothing.
func selectBinaries(m MutantRun, bins []TestBinary) ([]TestBinary, error) {
	if m.Binaries == nil {
		return bins, nil
	}
	if len(m.Binaries) == 0 {
		return nil, &Error{
			Code: CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) +
				" was given an empty set of test binaries to be measured against; a mutant no binary covers is not executed at all, and running none of them would report it as survived having started nothing",
		}
	}
	selected := make([]TestBinary, 0, len(m.Binaries))
	for _, index := range m.Binaries {
		if index < 0 || index >= len(bins) {
			return nil, &Error{
				Code: CodeMutantInvalid,
				Message: "the mutant " + display(m.ID) + " names test binary " + strconv.Itoa(index) +
					" of " + strconv.Itoa(len(bins)) + "; the caller's binaries and this run's have drifted apart",
			}
		}
		selected = append(selected, bins[index])
	}
	return selected, nil
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
