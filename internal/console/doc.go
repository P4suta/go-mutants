// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package console renders an engine event stream as plain lines.
//
// It is one of the two implementations of [Renderer]; the other is the
// bubbletea dashboard in internal/tui. Both consume the same
// `chan engine.Event`, which is what keeps them honest: the engine composes
// every human-readable detail, and a renderer decides only where a string goes
// and what colour it is. A renderer that computed a number for itself would be
// a second place for the derivation rule to live, and the two would disagree
// the first time one of them changed.
//
// # The shape of a run
//
// With colour off, the output is exactly these bytes — no escape sequences, no
// cursor movement, no width-dependent padding:
//
//	go-mutants 0.1.0-dev (run 20260819T101112Z-a1b2)
//	phase discover: locating the Go toolchain and copying the workspace
//	phase baseline: building the snapshot, then 3 timed runs of go test ./...
//	baseline run 1/3: 152ms
//	baseline run 2/3: 149ms
//	baseline run 3/3: 210ms
//	baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)
//	phase mutate: discovering candidates, validating them, then executing the mutants
//	discovered 4 candidates, 12 skips
//	validated 4 mutants, 0 rejections
//	baseline run 1/1: 168ms
//	KILLED     1a2b3c4d  clamp.go:12:9  lt-to-le  < -> <=  (181ms)
//	SURVIVED   9f8e7d6c  untested.go:9:12  neq-to-eq  != -> ==  (176ms)
//	    - !=
//	    + ==
//	phase report: writing the run report
//	report run: /home/u/.cache/go-mutants/workspaces/1a2b/runs/20260819T101112Z-a1b2.json
//	report latest: /home/u/.cache/go-mutants/workspaces/1a2b/latest.json
//	SURVIVED   9f8e7d6c  untested.go:9:12  neq-to-eq  != -> ==  (176ms)
//	    - !=
//	    + ==
//	mutants 4  killed 3  survived 1  timeout 0  inconclusive 0  errored 0  not-run 0  rejected 0
//	score 75.00%
//	skip const-decl 4
//	run 20260819T101112Z-a1b2  exit 0
//
// The run id, the durations, and the report paths vary between runs, and
// nothing else does. That is what makes the format safe to grep in CI and safe
// to assert on in tests.
//
// # Why the survivors are printed twice
//
// The live line says what just happened, in the order the machine settled the
// mutants; the closing block says what the run found, worst first and in a
// fixed order. The second is the one a person scrolls to and a script tails,
// and it is the only part of the output that is byte-deterministic for a given
// set of outcomes: [engine.MutantFinished] arrives from whichever worker
// finished first, while [engine.RunCompleted] is composed after every worker
// has joined. Under `--quiet` the live lines are dropped and the block is not,
// so nothing actionable depends on which of the two a reader saw.
//
// # The result line
//
// A result line is the outcome in a fixed nine-column field, the first eight
// hex characters of the mutant's display id, its coordinates, the rule that
// proposed the edit, the edit itself, and how long it took. A survivor carries
// the edit again underneath as a two-line diff, because a survivor is the one
// outcome a reader has to act on and `- <original>` / `+ <replacement>` is what
// they will paste into a test. Outcomes with no line of their own — a mutant
// the run never reached — are counted in the closing block instead; see
// [OutcomeLabel].
//
// # Colour
//
// Colour is opt-in and triple-gated: it is used only when the destination is a
// terminal, NO_COLOR is unset, CI is unset, and `--no-color` was not given. See
// [ColorEnabled]. When it is off, no styling function is called at all, rather
// than being called and asked to produce nothing — a library that decides for
// itself whether a colour profile is available is exactly the kind of thing
// that turns a byte-exact golden test into a flake.
//
// # Streams
//
// Everything a renderer writes goes to one writer, in the order the events
// arrived, because the ordering between two streams is not something a user or
// a CI log can rely on. Errors are not rendered here at all: an error ends the
// run, and internal/cli prints it to standard error after the stream has
// closed.
package console
