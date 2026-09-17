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

type terminalProbe func(io.Writer) (isTerminal bool, profile colorprofile.Profile)

func detectTerminal(w io.Writer) (bool, colorprofile.Profile) {
	f, ok := w.(*os.File)
	if !ok {
		return false, colorprofile.NoTTY
	}
	return term.IsTerminal(f.Fd()), colorprofile.Detect(f, os.Environ())
}

func wantsDashboard(w io.Writer, o *runOptions, probe terminalProbe) bool {
	if o.noTUI || o.json || o.quiet || o.noColor || o.verbose > 0 {
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

func dashboardInput(r io.Reader) io.Reader {
	f, ok := r.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return nil
	}
	return f
}

func replayFinal(w io.Writer, version string, color bool, events []engine.Event) error {
	if len(events) == 0 {
		return nil
	}
	stream := make(chan engine.Event, len(events))
	for _, e := range events {
		stream <- e
	}
	close(stream)
	return console.NewPlain(w, version, color, false).Run(context.Background(), stream)
}
