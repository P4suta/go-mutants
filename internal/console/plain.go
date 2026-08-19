// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/P4suta/go-mutants/internal/engine"
)

// A Renderer turns one engine event stream into output.
//
// The contract is the mirror image of the engine's: Run must read events until
// the channel closes, because the engine's sends block, and it must not return
// early on error without draining. Both implementations satisfy it the same
// way, and [PlainRenderer.Run] is the reference.
type Renderer interface {
	// Run consumes events until the channel is closed and returns the first
	// output error, or nil. It is called once per run, from its own goroutine,
	// started before the engine is.
	Run(ctx context.Context, events <-chan engine.Event) error
}

// A PlainRenderer writes one line per event.
//
// The zero value is not usable: Out must be set. Use [NewPlain], which also
// wraps the writer so that a run does not pay for a syscall per line.
type PlainRenderer struct {
	// Out is where lines go. It is written to from Run's goroutine only.
	Out io.Writer
	// Version is the go-mutants version printed in the header. It is passed in
	// rather than imported so that internal/cli remains the one place the
	// version string exists.
	Version string
	// Color enables ANSI styling. Decide it with [ColorEnabled].
	Color bool
	// Quiet suppresses the header, the phase banners, and the per-run baseline
	// progress. What survives is what a user who asked for quiet still needs:
	// the baseline summary, every warning, and the closing line. Errors are not
	// this type's to print and are unaffected.
	Quiet bool
}

// NewPlain returns a renderer writing buffered lines to out.
func NewPlain(out io.Writer, version string, color, quiet bool) *PlainRenderer {
	return &PlainRenderer{Out: out, Version: version, Color: color, Quiet: quiet}
}

// Run renders events until the channel closes.
//
// ctx is accepted for the [Renderer] contract and deliberately does not abort
// the loop. A cancelled run still has to finish arriving: the engine unwinds,
// cleans up, publishes its warnings and its [engine.RunCompleted], and closes.
// A renderer that stopped reading at the cancellation would deadlock the very
// shutdown it was reacting to, and would drop the last thing the user needs to
// see.
//
// A write failure stops the rendering but not the draining: the loop keeps
// consuming so the engine can finish, and the first error is returned at the
// end.
//
// Every line is flushed as it is written. The buffer is there so that a styled
// line reaches the terminal as one syscall rather than as four fragments, not
// to batch the run's output: a "phase discover" banner exists to say what is
// happening while a large workspace is copied, and a banner that arrives at the
// same moment as the closing line is a banner that was never worth printing.
func (r *PlainRenderer) Run(ctx context.Context, events <-chan engine.Event) error {
	_ = ctx
	w := bufio.NewWriter(r.Out)
	var failure error
	for event := range events {
		if failure != nil {
			continue
		}
		line, ok := r.line(event)
		if !ok {
			continue
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			failure = err
			continue
		}
		if err := w.Flush(); err != nil {
			failure = err
		}
	}
	return failure
}

// line renders one event, and reports whether anything should be printed at
// all. Every event this package knows is handled explicitly; an event added to
// the sealed interface later shows up here as the default case and prints
// nothing rather than a Go struct dump.
func (r *PlainRenderer) line(event engine.Event) (string, bool) {
	switch e := event.(type) {
	case engine.RunPlanned:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleHeader, fmt.Sprintf("go-mutants %s (run %s)", r.Version, e.RunID)), true

	case engine.PhaseChanged:
		if r.Quiet {
			return "", false
		}
		return r.paint(stylePhase, "phase "+e.Phase.String()+":") + " " + e.Detail, true

	case engine.BaselineProgress:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleDetail, fmt.Sprintf("baseline run %d/%d: %s",
			e.Run, e.Of, FormatDuration(e.Duration))), true

	case engine.BaselineCompleted:
		return r.paint(styleOK, "baseline ok:") + fmt.Sprintf(" avg %s, slowest %s, timeout %s (%s)",
			FormatDuration(e.Average), FormatDuration(e.Slowest),
			FormatDuration(e.Timeout), e.TimeoutSource), true

	case engine.Warning:
		return r.paint(styleWarning, "warning "+e.Code+":") + " " + e.Message, true

	case engine.ReportPublished:
		return "report " + e.Format + ": " + e.Path, true

	case engine.RunCompleted:
		style := styleOK
		if e.Status != engine.StatusOK {
			style = styleFailed
		}
		line := r.paint(style, "run "+e.Status.String()+":")
		if e.Summary == "" {
			return line, true
		}
		return line + " " + e.Summary, true

	default:
		return "", false
	}
}

// paint applies a style, or does not.
//
// The guard is at the string level rather than inside a configured lipgloss
// renderer on purpose: with colour off, no styling code runs at all, so the
// output cannot depend on what lipgloss decided about the terminal it thinks it
// is attached to.
func (r *PlainRenderer) paint(style lipgloss.Style, s string) string {
	if !r.Color {
		return s
	}
	return style.Render(s)
}

// FormatDuration renders a duration the way every go-mutants line does:
// rounded to the millisecond.
//
// Rounding is not cosmetic. An unrounded Go duration prints all the precision
// the clock had — "151.9873ms" — which is noise in a line a human reads and, in
// a log two runs are diffed from, noise that hides the change that matters. A
// millisecond is finer than any measurement here is meaningful to: the runner's
// own supervision overhead is larger than that.
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Millisecond).String()
}
