// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// dashboardFrames are the states of a run worth being able to look at.
//
// Every other test in this package asserts a substring or a shape of a frame --
// that the numbers are there, that nothing is wider than the terminal -- which
// is the right way to state a rule and the wrong way to notice a rendering. A
// row that stopped being drawn, a column that lost its alignment, a label that
// changed: each passes every substring assertion in this package and is the
// first thing a user would see.
//
// These are goldens of the **stripped** frame on the ASCII theme, which is the
// distinction view_test.go's own doc comment draws and this file keeps. A
// byte-exact golden of a *styled* frame would be a test of lipgloss's escape
// sequences, regenerated on every release of it; the stripped ASCII frame is
// this package's layout and nothing else.
var dashboardFrames = []struct {
	name  string
	width int
	// height is the terminal's, and is part of the state rather than a detail:
	// what gives way when there is not enough of it is a decision this package
	// makes.
	height int
	build  func(*testing.T) *harness
}{
	{
		name: "before-anything-happens", width: 100, height: 24,
		build: func(t *testing.T) *harness { return newThemedHarness(t, asciiTheme()) },
	},
	{
		name: "baseline", width: 100, height: 24,
		build: func(t *testing.T) *harness {
			h := newThemedHarness(t, asciiTheme())
			h.events(t, planned(3)[:5]...)
			return h
		},
	},
	{
		name: "in-flight", width: 100, height: 24,
		build: runInFlight,
	},
	{
		name: "with-coverage", width: 100, height: 24,
		build: func(t *testing.T) *harness { return coverageRunInFlight(t, asciiTheme()) },
	},
	{
		name: "narrow", width: 50, height: 20,
		build: runInFlight,
	},
	{
		name: "wide-and-short", width: 200, height: 12,
		build: runInFlight,
	},
	{
		name: "after-one-interrupt", width: 100, height: 24,
		build: func(t *testing.T) *harness {
			h := runInFlight(t)
			h.send(t, tea.KeyMsg{Type: tea.KeyCtrlC})
			return h
		},
	},
	{
		name: "completed", width: 100, height: 24,
		build: func(t *testing.T) *harness {
			h := runInFlight(t)
			h.clock.Advance(11 * time.Second)
			h.events(t, engine.RunCompleted{
				Status: engine.StatusOK,
				Run: &engine.RunSummary{
					RunID:  "20260819T101112Z-a1b2",
					Counts: engine.Counts{Total: 47, Killed: 40, Survived: 5, TimedOut: 1, Errored: 1},
					Score:  mutation.Score{Detected: 41, Denominator: 47},
				},
			})
			return h
		},
	},
}

// TestTheDashboardLooksLikeThis records what a user sees.
func TestTheDashboardLooksLikeThis(t *testing.T) {
	t.Parallel()

	var rendered strings.Builder
	for _, state := range dashboardFrames {
		h := state.build(t)
		lines := frame(t, h, state.width, state.height)
		if len(lines) != state.height {
			t.Errorf("%s is %d lines tall, want %d", state.name, len(lines), state.height)
		}
		fmt.Fprintf(&rendered, "== %s (%dx%d)\n", state.name, state.width, state.height)
		for _, line := range lines {
			// Trailing spaces are how a frame pads to its width, and a golden
			// full of them is one no editor leaves alone. The width is checked
			// by the tests that check widths.
			fmt.Fprintf(&rendered, "|%s\n", strings.TrimRight(line, " "))
		}
		rendered.WriteString("\n")
	}
	testkit.Golden(t, "frames.golden.txt", []byte(rendered.String()))
}
