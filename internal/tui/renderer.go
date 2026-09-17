// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"context"
	"io"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"

	"github.com/P4suta/go-mutants/internal/engine"
)

type Renderer struct {
	out     io.Writer
	in      io.Reader
	version string
	cancel  func()

	programOptions []tea.ProgramOption

	mu    sync.Mutex
	final []engine.Event
}

func New(out io.Writer, in io.Reader, version string, cancel func()) *Renderer {
	return &Renderer{out: out, in: in, version: version, cancel: cancel}
}

func (r *Renderer) Run(ctx context.Context, events <-chan engine.Event) error {
	_ = ctx

	options := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithOutput(r.out),
		tea.WithInput(keyboard(r.in)),
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
	<-drained

	if err != nil {
		return &Error{Code: CodeProgram, Message: "the live dashboard stopped before the run did", Err: err}
	}
	return nil
}

func keyboard(in io.Reader) io.Reader {
	f, ok := in.(*os.File)
	if !ok || term.IsTerminal(f.Fd()) {
		return in
	}
	return nil
}

func modelOptions(r *Renderer) options {
	return options{
		version: r.version,
		cancel:  r.cancel,
		theme:   newTheme(r.out),
	}
}

func (r *Renderer) keep(event engine.Event) {
	switch event.(type) {
	case engine.Warning, engine.ReportPublished, engine.DirectoryKept, engine.RunCompleted:
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.final = append(r.final, event)
}

func (r *Renderer) Final() []engine.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]engine.Event, len(r.final))
	copy(out, r.final)
	return out
}
