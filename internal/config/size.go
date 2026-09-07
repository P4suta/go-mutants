// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// sizeUnits are the suffixes a byte size may carry, longest first so that the
// match cannot mistake the `B` at the end of `GB` for a unit of its own.
var sizeUnits = []struct {
	suffix string
	scale  float64
}{
	{"KIB", 1 << 10},
	{"MIB", 1 << 20},
	{"GIB", 1 << 30},
	{"TIB", 1 << 40},
	{"B", 1},
}

// sizeSpellings is the list every refusal names, so that a user told "no" is
// told what "yes" looks like in the same sentence.
const sizeSpellings = `B, KiB, MiB, GiB, TiB`

// parseSize reads a byte size such as "2GiB".
//
// The units are binary and only binary, and the decimal spellings are refused
// rather than reinterpreted. A developer who writes `4GB` for a memory budget
// means four gibibytes about as often as they mean four gigabytes, and the two
// differ by 7%: read as decimal, a `4GB` bound is 300 MiB tighter than its
// author intended, and the only symptom is a mutant killed for memory on a
// machine where the same suite passes. Naming the accepted spellings costs one
// error message; guessing costs a wrong verdict nobody can see.
//
// A bare number is bytes, and a fractional value is accepted because `1.5GiB`
// is a bound a person types.
//
// It is deliberately a second implementation of the rule internal/devtools'
// cache budget uses rather than a shared one: that tool is a development
// utility this module's production code does not import, and a package that
// exists to define this repository's configuration vocabulary should not have
// its vocabulary defined somewhere else.
func parseSize(text string) (int64, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(text))
	if trimmed == "" {
		return 0, fmt.Errorf("a size may not be empty: write a number of bytes, or a number with a unit (%s)",
			sizeSpellings)
	}

	number, scale := trimmed, float64(1)
	for _, unit := range sizeUnits {
		if rest, found := strings.CutSuffix(trimmed, unit.suffix); found {
			number, scale = strings.TrimSpace(rest), unit.scale
			break
		}
	}

	// Every rejection below is a rejection rather than a clamp, and the last
	// three are the reason this has a test of its own. Converting a float64
	// that does not fit into an int64 is undefined in Go and produces
	// math.MinInt64 on every machine this runs on — a *negative* bound, which
	// every process is over, so a run would kill every mutant it started. NaN
	// is the same failure in different clothes: every comparison against it is
	// false, so nothing would ever be over it and the bound would silently do
	// nothing at all.
	value, err := strconv.ParseFloat(number, 64)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%q is not a size: write a number of bytes, or a number with a unit (%s); "+
			"the decimal spellings (kB, MB, GB) are refused so that a bound is never quietly smaller than it reads",
			text, sizeSpellings)
	case math.IsNaN(value):
		return 0, fmt.Errorf("%q is not a number at all: a size is a number of bytes, optionally with a unit (%s)",
			text, sizeSpellings)
	case math.IsInf(value, 0):
		return 0, fmt.Errorf("%q has no size: a size is a number of bytes, optionally with a unit (%s)",
			text, sizeSpellings)
	// Divided rather than multiplied, so the comparison itself cannot overflow,
	// and `>=` rather than `>`, because float64(math.MaxInt64) rounds *up* to
	// 2^63 — the one value that passes a `>` test and then converts to
	// math.MinInt64.
	case value >= float64(math.MaxInt64)/scale:
		return 0, fmt.Errorf("%q is larger than any machine has (the limit is just under %d bytes, and the units are %s)",
			text, int64(math.MaxInt64), sizeSpellings)
	}
	// A negative value is not refused here. It parses, it is representable, and
	// it is exactly the mistake the validator's own message is written for: one
	// rule, one sentence, one place — see [validateOverlay].
	return int64(value * scale), nil
}

// formatSize renders a byte count the way a person reads one, in the units
// [parseSize] accepts, so that a bound quoted back in a diagnostic can be
// pasted into the configuration that produced it.
func formatSize(n int64) string {
	if n < 1<<10 {
		return strconv.FormatInt(n, 10) + "B"
	}
	value, unit := float64(n), ""
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= 1 << 10
		unit = suffix
		if value < 1<<10 {
			break
		}
	}
	return strconv.FormatFloat(value, 'f', -1, 64) + unit
}
