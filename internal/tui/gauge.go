// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	gaugeFull  = "#"
	gaugeEmpty = "-"
)

func gauge(fraction float64, width int, on, off lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	if math.IsNaN(fraction) || fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(math.Round(fraction * float64(width)))
	if filled == 0 && fraction > 0 {
		filled = 1
	}
	if filled == width && fraction < 1 {
		filled = width - 1
	}
	return on.Render(strings.Repeat(gaugeFull, filled)) +
		off.Render(strings.Repeat(gaugeEmpty, width-filled))
}
