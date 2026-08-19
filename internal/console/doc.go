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
// # Determinism
//
// With colour off, the output is exactly the bytes below — no escape
// sequences, no cursor movement, no width-dependent padding, one line per
// event:
//
//	go-mutants 0.1.0-dev (run 20260819T101112Z-a1b2)
//	phase discover: locating the Go toolchain and copying the workspace
//	phase baseline: building the snapshot, then 3 timed runs of go test ./...
//	baseline run 1/3: 152ms
//	baseline run 2/3: 149ms
//	baseline run 3/3: 210ms
//	baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)
//	warning GOM0001: mutation phases not yet implemented — run ends after baseline (pre-release)
//	run ok: baseline only: 7 files snapshotted, workspace digest 1a2b3c4d5e6f7a8b
//
// The run id and the durations vary between runs, and nothing else does. That
// is what makes the format safe to grep in CI and safe to assert on in tests.
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
