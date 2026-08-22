// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package tui renders an engine event stream as a live dashboard.
//
// It is the second of the two implementations of [console.Renderer]; the other
// is the plain line renderer in internal/console. Both consume the same
// `chan engine.Event` and neither of them computes a number the engine did not
// publish — see internal/console's package documentation for why that rule
// exists. The dashboard adds one thing on top of the stream: the passage of
// time, which it gets from a ticker rather than from an event, because "this
// worker has been on the same mutant for nine seconds" is a fact no event can
// carry.
//
// # The frame
//
// A run in flight looks like this, at 80 columns:
//
//	go-mutants 0.1.0-dev  run 20260819T101112Z-a1b2            elapsed 00:42
//	phase mutate: discovering candidates, validating them, then executing...
//	baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)
//	validated 47 mutants, 2 rejections
//	coverage: 3 test binaries, 46 of 47 mutants covered, 1 uncovered
//	score 72.34% [##################----------] 12/47  eta 00:38
//	killed 8  survived 3  timeout 1  inconcl 0  errored 0  not-run 0
//
//	workers
//	  0  1a2b3c4d  internal/engine/engine.go:412       lt-to-le    2.1s
//	  1  9f8e7d6c  internal/report/report.go:88        neq-to-eq   404ms
//	  2  idle
//
//	survivors 3
//	SURVIVED   9f8e7d6c  internal/report/untested.go:9:12  neq-to-eq
//	    - !=
//	    + ==
//
//	ctrl+c: stop, publish partial report
//
// The coverage line appears only on a run that did a coverage pass; a run with
// coverage off publishes a warning saying why instead, and the line is simply
// absent. Every other line is drawn from the moment there is anything to put in
// it.
//
// The outcome labels, the eight-character id, the coordinates, the rule, and
// the two-line diff are internal/console's format, produced by its own exported
// helpers, so that a survivor reads identically whichever renderer showed it —
// and so that a user who read one and pastes from the other is pasting the same
// bytes. The label is [console.ResultLabel] and not [console.OutcomeLabel] for
// that reason: a survivor no test binary reaches reads "SURVIVED (uncovered)"
// in the feed and in the summary replayed underneath the dashboard alike, which
// is the difference between a test to sharpen and a test to write.
//
// # Why the alternate screen is on standard output
//
// The dashboard draws where the plain renderer would have written, which is
// standard output, and it is chosen only when standard output is a terminal
// that no document is being written to: `--json` hands standard output to the
// report and takes the dashboard away with it (see internal/cli). Two
// consequences make this safe rather than merely conventional. The alternate
// screen restores the primary screen on exit, so nothing the dashboard drew is
// left in the scrollback to interleave with what is printed afterwards; and
// what is printed afterwards — the closing summary — goes to the same stream a
// plain run would have used, so `go-mutants run | tee` and `go-mutants run`
// disagree about the dashboard and agree about the summary.
//
// Drawing on standard error instead would invert both properties: a run whose
// output was redirected to a file would still paint a dashboard, and the
// summary would arrive on a different stream from the one it was asked for.
//
// # Ctrl-C
//
// The dashboard never quits on its own before the run has finished. Ctrl-C is a
// [tea.KeyMsg] — the terminal is in raw mode, so it is a keystroke and not a
// signal — and the first one cancels the run's context and nothing else. The
// engine then does what it does for a signal in plain mode: it unwinds, marks
// everything it never reached as not-run, publishes the partial report, sends
// [engine.RunCompleted], and closes the stream. Only then does the model quit,
// which is what guarantees that the alternate screen is torn down after the
// report exists and not before.
//
// A second Ctrl-C is the escape hatch: it quits the dashboard immediately. The
// run itself is already cancelled and keeps unwinding — the renderer goes on
// draining the stream to the end, because the engine's sends block and
// abandoning them would deadlock the cleanup that removes the snapshot. What
// the second press buys is the terminal back; what it costs is watching the
// last few seconds of the run happen without a picture of it.
//
// The operating system's own escalation is untouched and remains the answer for
// a process that really is stuck.
//
// A dashboard with no keyboard — standard input redirected, so there is no
// terminal to put in raw mode — keeps the first meaning and loses the second.
// Ctrl-C is then a signal rather than a keystroke, internal/cli's handler
// cancels the run, and everything downstream of the cancellation is identical;
// what is gone is the second press, which was never anything but a way to stop
// watching. See [Renderer.Run] for why the input is disabled rather than read.
//
// # Windows
//
// Both Windows Terminal and ConHost work: bubbletea enables
// ENABLE_VIRTUAL_TERMINAL_PROCESSING on the output handle and
// ENABLE_VIRTUAL_TERMINAL_INPUT on the input handle when it takes the terminal,
// and restores both afterwards.
//
// One caveat, found by reading bubbletea's tty_windows.go: it does not touch
// the console output code page. A ConHost still on a legacy OEM code page — the
// default for a console started by something other than Windows Terminal —
// therefore renders multi-byte UTF-8 as mojibake. Every glyph this package
// draws is ASCII for that reason, the progress gauge included, so the dashboard
// is legible on a console that is not in UTF-8 mode. Colour is unaffected: VT
// sequences are ASCII.
package tui
