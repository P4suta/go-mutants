// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// ColorEnabled decides whether output to w may carry ANSI styling.
//
// Four conditions, in the order a user would expect them to win:
//
//   - noColor, which is `--no-color`, is absolute. A flag the user typed for
//     this invocation beats every heuristic.
//   - NO_COLOR set to anything non-empty turns colour off, per the informal
//     standard at no-color.org.
//   - CI set to anything non-empty turns colour off. A CI log is read as a
//     file far more often than it is read in a terminal, and most runners set
//     CI while also handing the process a pipe — but not all of them do, and
//     the ones that allocate a pty would otherwise fill their logs with escape
//     sequences.
//   - Otherwise, colour is on only if w is a terminal.
//
// A writer that is not an *os.File — a bytes.Buffer in a test, a log sink —
// never gets colour, which is what lets the golden tests assert bytes without
// setting environment variables.
func ColorEnabled(w io.Writer, noColor bool) bool {
	if noColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("CI") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// The styles. Colours are the eight ANSI names rather than 256-colour or true
// colour values, so that the output is legible on a light background, on a dark
// one, and in whatever palette the user has configured — the one thing a tool
// cannot know is what colour the terminal behind it is.
var (
	styleHeader  = lipgloss.NewStyle().Bold(true)
	stylePhase   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleDetail  = lipgloss.NewStyle().Faint(true)
	styleWarning = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleFailed  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)
