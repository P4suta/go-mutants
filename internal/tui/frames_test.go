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

var dashboardFrames = []struct {
	name   string
	width  int
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
			fmt.Fprintf(&rendered, "|%s\n", strings.TrimRight(line, " "))
		}
		rendered.WriteString("\n")
	}
	testkit.Golden(t, "frames.golden.txt", []byte(rendered.String()))
}
