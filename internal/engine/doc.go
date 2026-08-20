// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package engine orchestrates one mutation run and reports what it is doing
// through a single stream of events.
//
// # What the engine is responsible for
//
// [Run] owns the sequence, and every step of it is a gate on the next:
//
//  1. Locate the Go toolchain and copy the workspace into a disposable
//     snapshot. Nothing in this package writes to the user's tree.
//  2. Build the snapshot, then run the unmutated tests as many times as
//     `test.baseline_runs` asks, and derive the per-mutant timeout from the
//     slowest of them. A red suite stops the run: every mutant would be
//     reported as killed by tests that were already failing.
//  3. Discover the candidates the selection asks for, and catalogue them. The
//     include and exclude patterns apply here and never to the snapshot walk —
//     a file that is not copied is not built, and the workspace digest must not
//     move when a pure selection setting changes.
//  4. Instrument the snapshot with the whole catalogue and compile it,
//     bisecting to isolate any candidate that does not build. Those become the
//     report's `rejected[]` rather than a failure.
//  5. Run the whole test command once against the instrumented tree with
//     nothing activated. This is the semantic preservation gate: with
//     [instrument.ActiveEnv] unset every guard takes the branch holding the
//     user's own bytes, so a red suite here means go-mutants changed the
//     program and [CodeInstrumentedBaselineFailed] stops the run.
//  6. Re-digest the snapshot. The instrumented files and the generated runtime
//     package are the only things allowed to have moved; anything else is a
//     test writing into the tree every later mutant is measured against, and
//     [CodeWorkspaceDrift] names the files.
//  7. Build each test package's binary once, profile them if coverage-guided
//     selection is on, then execute every accepted mutant against the binaries
//     it needs, activating one per process.
//  8. Build the run report from the catalogue and the outcomes, file it in the
//     history store, and decide the exit status from the document that was
//     published rather than from a second tally beside it.
//
// # Coverage-guided selection
//
// Most of a mutation run's wall-clock time is spent proving that mutants no
// test reaches survive. Step 7 therefore runs each test binary once with
// coverage collection on, maps the lines each one reached onto the catalogue
// with internal/coverage, and then measures every mutant only against the
// binaries that reach it. A mutant no binary reaches is not executed at all: it
// is reported as a survivor with `uncovered` set, which is both true — nothing
// runs the line, so nothing could have caught the edit — and more actionable
// than the bare "survived" a full run would have spent a process to reach.
//
// Two rules decide whether this happens, and both are documented where the code
// that applies them lives:
//
//   - **Auto-on, and only for the built-in test command.** Coverage narrowing is
//     on whenever the effective `test.command` is `go test ./...`, and off with
//     a [coverage.CodeCustomTestCommand] warning for anything else. There is no
//     setting, because there is nothing to choose between: the mapping is from a
//     *test binary* to the lines it reached, and it is sound only because
//     go-mutants compiled those binaries itself and knows what each one is. A
//     custom command is an opaque program whose coverage cannot be attributed to
//     them, and a wrong attribution does not cost time — it skips a mutant a
//     test does cover, which is a kill lost and a score inflated.
//   - **Fail open, always.** Every way the pass can fail — the
//     coverage-instrumented build not compiling, a profiling run failing,
//     `go tool covdata` missing or erroring, a profile that will not parse, a
//     profile set with no blocks in it, a module path the profiles do not line
//     up with — publishes a [coverage.CodeUnavailable] warning and continues
//     with every mutant measured against every binary. None of them can fail a
//     run. Coverage-guided selection is an optimisation: without it the run does
//     strictly more work and reaches exactly the same verdicts, and trading a
//     correct slow answer for no answer would be the wrong way round.
//
// The drift gate needs no coverage entry in its allowlist, and that is a
// property of where the data is written rather than an omission. The
// GOCOVERDIR-equivalent directories and the rendered textfmt profiles live
// under the run's scratch directory, which os.MkdirTemp creates *beside* the
// snapshot; [snapshot.Snapshot.Redigest] walks the snapshot manifest, so
// nothing outside the snapshot root can appear as drift. The gate also runs
// before the coverage pass does, so it could not have seen it either way. What
// is worth stating plainly is the other half: the profiling pass runs the test
// binaries with their working directory inside the snapshot, *after* the gate,
// so a test that writes into its own package directory during that pass is not
// detected — exactly as it is not during mutant execution, which is the hazard
// `--isolate` is reserved for.
//
// # The event contract
//
// The engine never renders. It publishes to the caller-provided
// [Options.Events] channel, and a renderer — internal/console today, the
// bubbletea dashboard later — turns those events into output. The contract is
// deliberately narrow so the two cannot drift:
//
//   - The engine sends every event on [Options.Events] and closes the channel
//     exactly once, on return, whether the run succeeded or failed.
//   - [RunCompleted] is terminal: it is the last event before the close on
//     every path, including the failure and interruption paths. A renderer can
//     therefore treat "no RunCompleted" as a bug rather than as a state to
//     handle.
//   - Sends block. The caller must drain the channel until it closes, from a
//     goroutine started before [Run] is called, or the run deadlocks. A
//     buffered channel is recommended but is not a substitute for draining.
//   - A nil [Options.Events] is allowed and means "publish nothing". Nothing is
//     closed in that case either, since closing a nil channel panics.
//   - [MutantStarted] and [MutantFinished] are published from the execution
//     workers, so their relative order is whatever order the machine settled
//     the mutants in. Everything a renderer has to be able to reproduce byte
//     for byte is in [RunCompleted.Run], which is composed on the run's own
//     goroutine after every worker has joined.
//   - The two are *not* paired one to one. A mutant coverage excluded from
//     execution publishes [MutantFinished] with no preceding [MutantStarted],
//     because nothing started: there is no worker and no attempt, and inventing
//     a start would be inventing a process. A renderer that tracks worker slots
//     has to allow for it; see [MutantFinished] for the full contract.
//
// # Interruption
//
// A cancelled context stops the run at the first place that notices, and what
// happens next depends on how far it got. Once a catalogue exists there is
// something true to say — which mutants there were, which were measured, and
// which the signal cut short — so an interrupted report is built with every
// unmeasured mutant recorded as not-run, published, and announced through
// [ReportPublished] exactly as a completed one is. Before that point there is
// nothing to publish, and inventing an empty report would file a document
// claiming the workspace holds no mutants. The error returned always has
// context.Canceled in its chain, which is what the command line maps to exit
// 130 or 143.
//
// # What the engine promises about the machine
//
// The snapshot is always cleaned up, on every return path, and a cleanup that
// fails is reported as a [Warning] rather than as a run failure: a directory
// left in the temporary area is untidy, not wrong, and burying the real error
// underneath it would be. Child processes never inherit a GO_MUTANTS_ variable
// from the parent — activation is the engine's to set, not the user's — and
// they run with TMP, TEMP, and TMPDIR pointed at a scratch directory beside the
// snapshot, so that a test which writes to the temporary directory cannot leave
// debris in the user's. The compiled test binaries live in that scratch
// directory too, which is what keeps them out of the drift gate's view.
package engine
