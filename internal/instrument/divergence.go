// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
)

func LoopFileSuffix(modulePath string) string {
	sum := sha256.Sum256([]byte(modulePath))
	return "." + hex.EncodeToString(sum[:4])
}

const censusFormat = "gomutants-loop-census-v1"

const limitsFormat = "gomutants-loop-limits-v1"

func censusHeader(loops int) string {
	return censusFormat + " " + strconv.Itoa(loops)
}

func limitsHeader(loops int) string {
	return limitsFormat + " " + strconv.Itoa(loops)
}

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
