// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"io"
	"slices"
	"strconv"
	"strings"
)

const infectionFormat = "gomutants-infection-v1"

func infectionHeader(digest string, n int) string {
	return infectionFormat + " " + digest + " " + strconv.Itoa(n)
}

func ReadInfectionLog(r io.Reader, digest string, mutants int) ([]uint32, error) {
	if mutants < 0 {
		return nil, &Error{
			Code: CodeInfectionLog,
			Message: "an infection log cannot be read against a catalogue of " + strconv.Itoa(mutants) +
				" mutants: a catalogue holds none or some, never fewer than none",
		}
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, &Error{Code: CodeInfectionLog, Message: "cannot read the infection log", Err: err}
	}
	if len(data) == 0 {
		return nil, &Error{
			Code: CodeInfectionLog,
			Message: "the infection log is empty, so the process that was to write it " +
				"did not get as far as its own header",
		}
	}
	if data[len(data)-1] != '\n' {
		return nil, &Error{
			Code: CodeInfectionLog,
			Message: "the infection log ends mid-line, so the process that wrote it " +
				"died before its last write completed",
		}
	}

	header := infectionHeader(digest, arraySize(mutants))
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if lines[0] != header {
		return nil, &Error{
			Code: CodeInfectionLog,
			Message: "the infection log opens with " + strconv.Quote(lines[0]) +
				" rather than " + strconv.Quote(header),
		}
	}

	seen := make(map[uint32]bool, len(lines)-1)
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, infectionFormat) {
			if line != header {
				return nil, &Error{
					Code: CodeInfectionLog,
					Message: "the infection log holds a second header " + strconv.Quote(line) +
						" beside " + strconv.Quote(header) +
						", so two runs appended to one file and neither one's indices can be told from the other's",
				}
			}
			continue
		}
		index, err := strconv.ParseUint(line, 10, 32)
		if err != nil {
			return nil, &Error{
				Code:    CodeInfectionLog,
				Message: "the infection log holds " + strconv.Quote(line) + " where a mutant index belongs",
			}
		}
		if index >= uint64(mutants) {
			return nil, &Error{
				Code: CodeInfectionLog,
				Message: "the infection log names mutant index " + strconv.FormatUint(index, 10) +
					", which is outside the " + strconv.Itoa(mutants) + " the catalogue holds",
			}
		}
		seen[uint32(index)] = true
	}

	out := make([]uint32, 0, len(seen))
	for index := range seen {
		out = append(out, index)
	}
	slices.Sort(out)
	return out, nil
}
