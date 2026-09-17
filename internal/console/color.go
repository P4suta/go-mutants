// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"

	"github.com/P4suta/go-mutants/internal/mutation"
)

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

var (
	styleHeader  = lipgloss.NewStyle().Bold(true)
	stylePhase   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleDetail  = lipgloss.NewStyle().Faint(true)
	styleWarning = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleFailed  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleRule    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleScore   = lipgloss.NewStyle().Bold(true)
	styleRemoved = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleAdded   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

func outcomeStyle(o mutation.Outcome) lipgloss.Style {
	switch o {
	case mutation.OutcomeKilled, mutation.OutcomeTimedOut:
		return styleOK
	case mutation.OutcomeSurvived:
		return styleFailed
	case mutation.OutcomeErrored, mutation.OutcomeInconclusive, mutation.OutcomeNotRun:
		return styleWarning
	}
	return styleWarning
}
