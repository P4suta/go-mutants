// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"io"
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"

	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/engine"
)

// A terminalProbe answers what a writer is attached to.
//
// It exists so that the selection rules can be tested. Every other input to the
// decision — the flags, the environment — a test can set; whether standard
// output is a terminal is the one it cannot, and a rule that could only be
// exercised on the branch where the answer is "no" would be a rule with no
// tests at all.
type terminalProbe func(io.Writer) (isTerminal bool, profile colorprofile.Profile)

// detectTerminal is the real probe.
//
// Both questions are answered by the libraries that will have to live with the
// answer: charmbracelet/x/term, which is what bubbletea itself uses to decide
// whether it can take the terminal, and charmbracelet/colorprofile, which is
// what tells ANSI from a dumb terminal from a pipe. Neither is reimplemented
// here. Probing for escape-sequence support by hand is how a tool ends up
// writing cursor movement into a log file, and the one thing worse than a
// dashboard nobody can see is a dashboard nobody can see written into a CI
// artefact.
func detectTerminal(w io.Writer) (bool, colorprofile.Profile) {
	f, ok := w.(*os.File)
	if !ok {
		return false, colorprofile.NoTTY
	}
	return term.IsTerminal(f.Fd()), colorprofile.Detect(f, os.Environ())
}

// wantsDashboard reports whether the live dashboard should be used for a run
// whose renderer writes to w.
//
// Every condition is a reason the dashboard would be the wrong answer, and each
// is checked separately so that the reason survives in the code rather than
// being collapsed into one boolean:
//
//   - --no-tui is the escape hatch, and beats every heuristic, as a flag the
//     user typed for this invocation should.
//   - --json hands standard output to the report document. A dashboard would
//     draw over the thing the user asked for.
//   - --quiet asks for less output, not for a different kind of it.
//   - --no-color and NO_COLOR ask for output that is text and nothing else. A
//     dashboard is cursor movement before it is colour, and its survivor diff
//     is red and green before it is anything: what is left of it in monochrome
//     is worse than the plain lines, which were designed to be read that way.
//   - CI is set, which means the output is a log file that happens to have a
//     terminal on the other end of it often enough to matter. Plain lines are
//     what a log wants, and they are what the plain renderer is for.
//   - The destination is not a terminal, or is one that cannot do better than
//     ASCII — a dumb terminal, a pipe, an editor's output pane.
//
// The environment is read here rather than passed in because internal/console
// reads it the same way for the same decision; see [console.ColorEnabled].
func wantsDashboard(w io.Writer, o *runOptions, probe terminalProbe) bool {
	if o.noTUI || o.json || o.quiet || o.noColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CI") != "" {
		return false
	}
	isTerminal, profile := probe(w)
	if !isTerminal {
		return false
	}
	return profile > colorprofile.Ascii
}

// dashboardInput is what the dashboard should read keys from, or nil.
//
// Only a terminal is handed over. bubbletea puts what it is given into raw
// mode and reads it for the whole run, which is right for a keyboard and wrong
// for everything else: a redirected standard input is somebody's data, and a
// dashboard that consumed it — or that failed trying — would break a run that
// was otherwise fine. Nil disables input entirely, and Ctrl-C then arrives the
// way it does in plain mode, as a signal, which [watchSignals] already handles
// identically.
//
// This is why the dashboard is gated on standard *output* rather than on both
// streams: `go-mutants run < /dev/null` on a terminal still gets its dashboard,
// and gives up only the key that stops the run early.
func dashboardInput(r io.Reader) io.Reader {
	f, ok := r.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return nil
	}
	return f
}

// replayFinal prints the events the dashboard kept, through the plain renderer.
//
// This is what puts the closing summary in the scrollback of a run that watched
// a dashboard instead of reading lines. It runs after the alternate screen has
// been restored — [tui.Renderer.Run] does not return until bubbletea has given
// the terminal back — so the block lands on the primary screen where it can be
// scrolled to, selected, and pasted into a bug report.
//
// It is the plain renderer's own rendering, fed a stream of the events it would
// have rendered anyway, rather than a summary this package formats: the block a
// user sees after a dashboard run is byte for byte the block a plain run
// prints, and it stays that way without anybody remembering to keep two
// formatters in step.
//
// The warnings are replayed with it, ahead of the summary. In a plain run they
// were printed as they happened; in a dashboard run the alternate screen took
// them with it when it closed, and a warning nobody can read is a warning that
// was never published. Their text is nowhere else — the summary states how many
// there were, not what they said.
func replayFinal(w io.Writer, version string, color bool, events []engine.Event) error {
	if len(events) == 0 {
		return nil
	}
	stream := make(chan engine.Event, len(events))
	for _, e := range events {
		stream <- e
	}
	close(stream)
	// Never quiet: a dashboard is not something --quiet can be combined with
	// (see [wantsDashboard]), so there is no run that reaches this with
	// anything to suppress.
	return console.NewPlain(w, version, color, false).Run(context.Background(), stream)
}
