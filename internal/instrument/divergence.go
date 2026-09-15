// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"io"
	"strconv"
	"strings"
)

// censusFormat opens the header line of every loop census and names the format
// itself, for [infectionFormat]'s reason: the file outlives the process that
// wrote it and is read by a different program than the one that generated the
// runtime.
const censusFormat = "gomutants-loop-census-v1"

// limitsFormat is the same for the table a run hands back to its mutants.
const limitsFormat = "gomutants-loop-limits-v1"

// censusHeader renders the line a counting runtime writes before its first
// count: the format and how many loop sites the tree it was generated for
// holds.
//
// The generator and the reader both go through this function, which is the only
// reason they cannot drift.
func censusHeader(loops int) string {
	return censusFormat + " " + strconv.Itoa(loops)
}

// limitsHeader is the same for a limit table.
func limitsHeader(loops int) string {
	return limitsFormat + " " + strconv.Itoa(loops)
}

// ReadLoopCensus returns, per loop site, the largest iteration count the census
// records for it, and zero for a site the census never names.
//
// The census is what an instrumented tree's runtime appended to while the test
// command ran with nothing activated: one line per site per time that site's
// running maximum rose, several processes appending to one file, so the header
// may appear more than once and every occurrence has to be this tree's.
//
// The reader is fail-closed for [ReadInfectionLog]'s reason turned around. A
// census is what every ceiling is derived from, so a file that has been
// truncated, mixed with another tree's, or written by a runtime built from a
// different set of loops must yield nothing at all rather than the part of
// itself that still parses: the part that still parses is a set of ceilings
// that are too low for the loops it forgot, and a ceiling that is too low is a
// mutant reported as diverged that terminates perfectly well. The caller has one
// safe reading of an error — "this run took no census, so every ceiling is the
// floor" — and no safe reading of a partial one.
func ReadLoopCensus(r io.Reader, loops int) ([]uint64, error) {
	if loops < 0 {
		return nil, &Error{
			Code: CodeLoopCensus,
			Message: "a loop census cannot be read against a tree of " + strconv.Itoa(loops) +
				" loops: a tree holds none or some, never fewer than none",
		}
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, &Error{Code: CodeLoopCensus, Message: "cannot read the loop census", Err: err}
	}
	if len(data) == 0 {
		return nil, &Error{
			Code: CodeLoopCensus,
			Message: "the loop census is empty, so the process that was to write it did not get " +
				"as far as its own header",
		}
	}
	if data[len(data)-1] != '\n' {
		return nil, &Error{
			Code:    CodeLoopCensus,
			Message: "the loop census ends mid-line, so the process writing it died before it finished",
		}
	}

	header := censusHeader(loops)
	out := make([]uint64, loops)
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\n")
		if line == header {
			continue
		}
		site, count, err := readCensusLine(line, loops)
		if err != nil {
			return nil, err
		}
		out[site] = max(out[site], count)
	}
	return out, nil
}

// readCensusLine reads one "<site> <count>" line of a census.
func readCensusLine(line string, loops int) (site int, count uint64, err error) {
	refuse := func(why string) error {
		return &Error{
			Code:    CodeLoopCensus,
			Message: "the loop census holds " + strconv.Quote(line) + ", which " + why,
		}
	}
	left, right, ok := strings.Cut(line, " ")
	if !ok {
		return 0, 0, refuse("is neither this tree's header nor a site and a count")
	}
	site, err = strconv.Atoi(left)
	if err != nil || site < 0 || site >= loops {
		return 0, 0, refuse("names no loop site of this tree")
	}
	count, err = strconv.ParseUint(right, 10, 64)
	if err != nil {
		return 0, 0, refuse("does not carry a count")
	}
	return site, count, nil
}

// WriteLoopLimits renders the table a run hands its mutant processes: one
// ceiling per loop site, in site order.
//
// It is written by the engine and read by the generated runtime, so the format
// is stated once, here, and both ends go through this file.
func WriteLoopLimits(w io.Writer, limits []uint64) error {
	var b strings.Builder
	b.WriteString(limitsHeader(len(limits)))
	b.WriteByte('\n')
	for site, limit := range limits {
		b.WriteString(strconv.Itoa(site))
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(limit, 10))
		b.WriteByte('\n')
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return &Error{Code: CodeLoopCensus, Message: "cannot write the loop limit table", Err: err}
	}
	return nil
}
