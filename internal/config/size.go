// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

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

const sizeSpellings = `B, KiB, MiB, GiB, TiB`

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
	case value >= float64(math.MaxInt64)/scale:
		return 0, fmt.Errorf("%q is larger than any machine has (the limit is just under %d bytes, and the units are %s)",
			text, int64(math.MaxInt64), sizeSpellings)
	}
	return int64(value * scale), nil
}

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
