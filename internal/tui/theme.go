// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// A theme is every styling decision the dashboard makes, bound to one output.
//
// It exists so that nothing in this package reaches for lipgloss's global
// default renderer. That renderer detects its colour profile from os.Stdout the
// first time it is used, which makes the appearance of a frame depend on
// process-wide state a test cannot set without affecting every other test in
// the binary. A theme is passed in, so a test asks for [asciiTheme] and gets
// deterministic bytes.
type theme struct {
	// profile is what the destination can display. It is kept beside the
	// renderer because bubbles' progress bar takes a termenv profile of its
	// own and would otherwise detect a second, possibly different, one.
	profile termenv.Profile

	// The palette. The colours are the eight ANSI names rather than 256-colour
	// or true-colour values, and they are deliberately the same assignment
	// internal/console makes — green for a detection, red for a survivor,
	// yellow for the outcomes that mean "could not say", cyan for a rule — so
	// that the dashboard and the summary printed underneath it do not disagree
	// about what red means. The one thing a tool cannot know is what colour the
	// terminal behind it is, so nothing here assumes a dark background.
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

// newTheme builds the theme for an output, detecting what it can display.
func newTheme(out io.Writer) theme {
	r := lipgloss.NewRenderer(out)
	return themeFrom(r)
}

// asciiTheme is the theme most tests use: a renderer that writes nowhere,
// pinned to the ASCII profile so that no escape sequence at all reaches the
// assertion.
//
// The ASCII profile drops every style, text decoration included: lipgloss
// renders through termenv, and a termenv style on the Ascii profile returns its
// argument untouched. What a test on this theme asserts is therefore the layout
// alone — where the words are, and how wide the lines are in characters.
//
// That is deliberately not the whole story, because it is not the code path
// production takes. Every style becomes several bytes of escape sequence on a
// real terminal, and arithmetic or slicing that is correct on plain text can be
// wrong on styled text in ways no assertion here would see. The frame is
// therefore also drawn on a theme pinned to a colour profile, where what is
// asserted is that the escape sequences come out whole; see
// TestTheStyledFrameFitsTheTerminalAndClosesEveryStyle.
func asciiTheme() theme {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	return themeFrom(r)
}

// themeFrom builds the palette on a renderer.
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

// outcome is how a settled mutant's label is coloured. It is internal/console's
// rule, restated because the style values there are unexported: a kill and a
// confirmed timeout are detections and are green, a survivor is the finding the
// run exists to produce and is red, and the two that mean "the run could not
// say" are yellow rather than red, because colouring them like a verdict would
// make an inconclusive result read as a failure of the tests.
func (t theme) outcome(o mutation.Outcome) lipgloss.Style {
	switch o {
	case mutation.OutcomeKilled, mutation.OutcomeTimedOut:
		return t.ok
	case mutation.OutcomeSurvived:
		return t.failed
	default:
		return t.warning
	}
}

// The eight-colour palette, by ANSI number.
const (
	ansiRed    = "1"
	ansiGreen  = "2"
	ansiYellow = "3"
	ansiCyan   = "6"
)
