// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package execute builds a snapshot's test binaries once and then schedules
// every mutant against them.
//
// It is the layer between instrumentation and the report. Instrumentation has
// already rewritten the snapshot so that one environment variable selects which
// mutant is live; this package compiles that tree into test binaries, runs them
// with that variable set, and turns exit statuses into [mutation.Outcome]
// values. It creates no processes of its own — internal/runner does that, and
// is the only place in go-mutants that does — and it makes no judgement about
// what a set of outcomes means: scoring, the expectations ledger, and the exit
// code belong to internal/mutation and the command line.
//
// # Build once, run thousands of times
//
// [BuildTestBinaries] enumerates the snapshot's packages with `go list`, skips
// the ones that have no test files at all, and compiles the rest with
// `go test -c` in parallel. Every mutant afterwards is measured by starting
// those same binaries directly rather than through `go test`, which matters for
// two reasons: it bypasses the go test result cache completely — the cache keys
// on inputs it cannot see the mutant in — and it removes a `go` invocation, a
// build graph load, and a package staleness check from the inner loop that runs
// once per mutant per package.
//
// Nothing here decides *which* binaries a mutant needs. Coverage-guided
// selection arrives in a later phase; until it does, every mutant is measured
// against every binary, which is slower and never wrong.
//
// # What an exit status means
//
// One attempt walks the binaries in order and stops at the first thing that
// settles the question:
//
//   - Exit 0 means this package's tests did not notice the mutant. The attempt
//     moves to the next binary, and a mutant that gets through all of them
//     survived.
//   - A non-zero exit is a killed mutant, and the binary that produced it is
//     recorded in [Attempt.KilledBy]. The remaining binaries are not run: they
//     cannot change the answer, and a mutation run's whole economy is in not
//     doing work whose result is already known.
//   - Exit [instrument.UnknownMutantExit] is not a test failure. It is the
//     generated runtime refusing an activation identity it has never heard of,
//     which means the catalogue and the instrumented tree disagree, and it is
//     reported as [CodeStaleCatalog] with the outcome errored. Treating it as a
//     kill would let a stale catalogue inflate a score.
//   - A timeout is not a result yet. See below.
//   - A process that could not be started or supervised is [CodeMutantStart]
//     and the outcome errored: go-mutants failed, and the run has learned
//     nothing about this mutant.
//
// # Timeouts are retried before they are believed
//
// A first timeout is not evidence. A loaded machine running N test binaries at
// once produces timeouts that say nothing about the mutant, and counting one as
// a detection would inflate the score exactly when the run is least able to
// notice. So [Schedule] holds every timed-out mutant back, and after the main
// queue has drained retries each one **serially** — one at a time, nothing else
// running, the same timeout as before:
//
//   - A second timeout is [mutation.OutcomeTimedOut], a confirmed detection.
//     A mutant that hangs an idle machine has changed behaviour the tests
//     noticed.
//   - A retry that finishes, whether it passes or fails, is
//     [mutation.OutcomeInconclusive]. Mixed evidence never counts as detection,
//     and it never counts as survival either.
//
// Both attempts are kept in [MutantResult.Attempts], because the report has to
// be able to show what actually happened rather than only the verdict.
//
// # Determinism
//
// [BuildTestBinaries] returns its binaries sorted by import path and [Schedule]
// returns its results in the order the mutants were given, whatever order the
// workers happened to finish in. Workers share no mutable state: each writes
// only its own slot in the result slice, and the retry pass runs after a full
// join, so nothing is written twice.
//
// # Isolation between workers
//
// Every child gets a composed environment rather than an inherited one. Every
// GO_MUTANTS_ variable is stripped before [instrument.ActiveEnv] is set, so a
// developer who exported one in their shell cannot silently activate a mutant;
// TMP, TEMP and TMPDIR are pointed at a per-worker directory, so two mutants
// running at once cannot meet in the temporary directory. What is left is
// inherited on purpose: GOFLAGS, GOPROXY, the module cache and the PATH that
// makes a project's tests work are part of what "the tests pass here" means.
//
// The snapshot itself is shared by every worker, which is what makes one build
// enough. A test that writes into its own package directory therefore writes
// into a tree every later mutant is measured against; detecting that is the
// snapshot re-digest gate's job, not this package's.
package execute
