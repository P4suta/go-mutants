// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"context"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/P4suta/go-mutants/internal/engine"
)

// A Renderer draws an engine event stream as a live dashboard.
//
// It satisfies the same interface internal/console's plain renderer does, and
// obeys the same contract: [Renderer.Run] consumes events until the channel
// closes, whatever happens to the terminal in the meantime, because the
// engine's sends block and a consumer that stopped reading would deadlock the
// shutdown it was reacting to.
//
// The zero value is not usable. Use [New], which is also where the cancel
// function is supplied: the dashboard has a key that stops the run, and the
// [console.Renderer] interface has no place to put one. Extending the
// constructor rather than the interface is deliberate — the plain renderer has
// no key to bind and should not have to accept a function it would ignore.
type Renderer struct {
	// out is where the alternate screen is drawn. See the package
	// documentation for why it is standard output.
	out io.Writer
	// in is where keystrokes come from. A nil reader disables input entirely,
	// which is what a test uses and what a run with a redirected standard input
	// gets: Ctrl-C then arrives as a signal instead, and internal/cli's
	// handler cancels the run exactly as this renderer's key binding would
	// have.
	in io.Reader
	// version is the go-mutants version in the header.
	version string
	// cancel cancels the run's context.
	cancel func()

	// programOptions are extra bubbletea options. They exist for tests, which
	// run the program headless; production never sets them.
	programOptions []tea.ProgramOption

	// mu guards final, which the forwarding goroutine writes and the caller
	// reads after Run has returned.
	mu sync.Mutex
	// final is the tail of the stream, kept for internal/cli to print once the
	// alternate screen is gone; see [Renderer.Final].
	final []engine.Event
}

// New returns a renderer that draws on out and reads keys from in.
//
// cancel is what Ctrl-C calls. It may be nil, in which case Ctrl-C stops
// meaning anything and only the second press — which quits — has an effect;
// that is a programming error rather than a configuration, and it is tolerated
// rather than panicked over because a dashboard is not worth crashing a run for.
func New(out io.Writer, in io.Reader, version string, cancel func()) *Renderer {
	return &Renderer{out: out, in: in, version: version, cancel: cancel}
}

// Run draws events until the channel is closed.
//
// ctx is accepted for the [console.Renderer] contract and deliberately does not
// abort the loop, for the reason the plain renderer documents: a cancelled run
// still has to finish arriving. The dashboard has a second reason — the whole
// point of its Ctrl-C is that cancelling the context is not the end of the run
// but the start of its shutdown, and a renderer that closed on cancellation
// would tear the screen down before the report existed.
//
// The forwarding goroutine outlives the program on purpose. When the user quits
// early with a second Ctrl-C, bubbletea returns while the engine is still
// unwinding; [tea.Program.Send] is a no-op once the program has stopped, so the
// loop keeps draining at no cost and the engine finishes cleanly.
func (r *Renderer) Run(ctx context.Context, events <-chan engine.Event) error {
	_ = ctx

	options := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithOutput(r.out),
		tea.WithInput(r.in),
		// Signals are internal/cli's, not bubbletea's. Left to itself
		// bubbletea turns SIGINT into an immediate exit, which would tear the
		// screen down while the engine was still writing the partial report,
		// and would do it on a path — an external `kill -INT`, a SIGTERM —
		// where the plain renderer keeps rendering to the end. One handler, one
		// meaning: see internal/cli's watchSignals.
		tea.WithoutSignalHandler(),
	}
	options = append(options, r.programOptions...)

	program := tea.NewProgram(newModel(modelOptions(r)), options...)

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range events {
			r.keep(event)
			program.Send(eventMsg{event: event})
		}
		program.Send(streamClosedMsg{})
	}()

	_, err := program.Run()
	// Joined before returning, always: the contract this renderer signed says
	// the stream has been consumed to the end by the time Run returns, and
	// internal/cli relies on it to know the engine has finished with the
	// terminal.
	<-drained

	if err != nil {
		return &Error{Code: CodeProgram, Message: "the live dashboard stopped before the run did", Err: err}
	}
	return nil
}

// modelOptions builds the model options from the renderer's configuration.
func modelOptions(r *Renderer) options {
	return options{
		version: r.version,
		cancel:  r.cancel,
		theme:   newTheme(r.out),
	}
}

// keep records the events that have to survive the alternate screen.
//
// Everything the dashboard drew is erased when the screen is restored, so the
// two kinds of thing a user still needs afterwards are kept and handed back:
// the warnings, whose text is nowhere else, and the closing block, which is the
// answer the run exists to produce. See [Renderer.Final].
func (r *Renderer) keep(event engine.Event) {
	switch event.(type) {
	case engine.Warning, engine.ReportPublished, engine.RunCompleted:
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.final = append(r.final, event)
}

// Final returns the events internal/cli should print once the dashboard has
// exited, in the order they arrived.
//
// It is the warnings, the report's paths, and the closing summary — replayed
// through the plain renderer rather than reformatted here, so that the block a
// user reads after a dashboard run is byte for byte the block they would have
// read from a plain one. A second implementation of that block is exactly the
// thing this project keeps refusing to write.
//
// It is safe to call once [Renderer.Run] has returned.
func (r *Renderer) Final() []engine.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]engine.Event, len(r.final))
	copy(out, r.final)
	return out
}
