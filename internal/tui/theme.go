// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type theme struct {
	profile termenv.Profile

	text     lipgloss.Style
	header   lipgloss.Style
	phase    lipgloss.Style
	detail   lipgloss.Style
	label    lipgloss.Style
	ok       lipgloss.Style
	failed   lipgloss.Style
	warning  lipgloss.Style
	rule     lipgloss.Style
	score    lipgloss.Style
	removed  lipgloss.Style
	added    lipgloss.Style
	idle     lipgloss.Style
	gaugeOn  lipgloss.Style
	gaugeOff lipgloss.Style
}

func newTheme(out io.Writer) theme {
	r := lipgloss.NewRenderer(out)
	return themeFrom(r)
}

func asciiTheme() theme {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	return themeFrom(r)
}

func themeFrom(r *lipgloss.Renderer) theme {
	style := func() lipgloss.Style { return r.NewStyle() }
	colour := func(c string) lipgloss.Style { return r.NewStyle().Foreground(lipgloss.Color(c)) }
	return theme{
		profile:  r.ColorProfile(),
		text:     style(),
		header:   style().Bold(true),
		phase:    colour(ansiCyan),
		detail:   style().Faint(true),
		label:    style().Faint(true),
		ok:       colour(ansiGreen),
		failed:   colour(ansiRed),
		warning:  colour(ansiYellow),
		rule:     colour(ansiCyan),
		score:    style().Bold(true),
		removed:  colour(ansiRed),
		added:    colour(ansiGreen),
		idle:     style().Faint(true),
		gaugeOn:  colour(ansiGreen),
		gaugeOff: style().Faint(true),
	}
}

func (t theme) outcome(o mutation.Outcome) lipgloss.Style {
	switch o {
	case mutation.OutcomeKilled, mutation.OutcomeTimedOut:
		return t.ok
	case mutation.OutcomeSurvived:
		return t.failed
	case mutation.OutcomeErrored, mutation.OutcomeInconclusive, mutation.OutcomeNotRun:
		return t.warning
	}
	return t.warning
}

const (
	ansiRed    = "1"
	ansiGreen  = "2"
	ansiYellow = "3"
	ansiCyan   = "6"
)
