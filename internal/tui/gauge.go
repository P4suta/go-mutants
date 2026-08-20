// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The gauge glyphs. ASCII, for the reason the package documentation gives: a
// ConHost that is not in UTF-8 mode renders a block character as mojibake, and
// a bar is the one element on screen that is read by its shape rather than by
// its text.
const (
	gaugeFull  = "#"
	gaugeEmpty = "-"
)

// gauge draws a bar of the given width, filled to a fraction of it.
//
// This is deliberately not bubbles/progress. That component's value is its
// spring animation, which pulls in github.com/charmbracelet/harmonica — a
// module this project does not depend on — to interpolate towards a target over
// several frames. The dashboard has nothing to interpolate: the score is a
// measured quantity that jumps when a mutant settles and is wrong at every
// intermediate value an animation would draw through. What is left of the
// component once the animation is removed is this function, and a static bar is
// also the one a resize can redraw without a frame of catching up.
//
// The fraction is clamped rather than trusted. It comes from
// [mutation.Score.Percent], which is in range by construction, and clamping is
// what keeps a future caller's rounding error from producing a bar wider than
// the frame.
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
	// A run with any score at all shows at least one cell, and a run that has
	// not reached the end shows at least one empty one. Rounding alone would
	// draw an empty bar for a score of two per cent and a full one for
	// ninety-nine, which are the two readings a user is most likely to act on.
	if filled == 0 && fraction > 0 {
		filled = 1
	}
	if filled == width && fraction < 1 {
		filled = width - 1
	}
	return on.Render(strings.Repeat(gaugeFull, filled)) +
		off.Render(strings.Repeat(gaugeEmpty, width-filled))
}
