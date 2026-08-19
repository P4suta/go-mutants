// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package engine orchestrates one mutation run and reports what it is doing
// through a single stream of events.
//
// # What the engine is responsible for
//
// [Run] owns the sequence: locate the Go toolchain, copy the workspace into a
// disposable snapshot, prove that the snapshot builds, measure the unmutated
// tests, and derive the per-mutant timeout from those measurements. The later
// phases — discovery, instrumentation, validation, execution, reporting —
// attach to the same sequence as they land. Nothing in this package writes to
// the user's tree, and nothing in it draws.
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
// debris in the user's.
//
// # Pre-release scope
//
// The mutation phases are not implemented yet. A run therefore ends after the
// baseline, emits the [CodeMutationPhasesPending] warning, and completes with
// [StatusOK]. That is deliberate: the phase exists to prove the execution layer
// end to end before any code is rewritten, and a run that quietly reported a
// mutation score of 100% would be the worst possible way to ship it.
package engine
