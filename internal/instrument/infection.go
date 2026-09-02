// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"io"
	"slices"
	"strconv"
	"strings"
)

// infectionFormat opens the header line of every infection log and names the
// format itself.
//
// It carries a version because the log outlives the process that wrote it and
// is read by a different program than the one that generated the runtime: a
// reader that met a format it did not know and guessed would be attributing
// lines it might be misreading to mutants it might not have.
const infectionFormat = "gomutants-infection-v1"

// infectionHeader renders the line a probe runtime writes before its first
// index: the format, the catalogue the indices are dense in, and how many
// indices that catalogue can hold.
//
// The generator and the reader both go through this function, which is the only
// reason they cannot drift: a header written one way and matched another would
// turn every log into "this target proved nothing", silently and everywhere.
func infectionHeader(digest string, n int) string {
	return infectionFormat + " " + digest + " " + strconv.Itoa(n)
}

// ReadInfectionLog returns the distinct mutant indices an infection log
// records, sorted ascending.
//
// The log is what a probe tree's generated runtime appended to while the tests
// ran; digest and n are the catalogue's [mutation.Catalog.Digest] and the length
// of the runtime's array, which is the catalogue's size or one when it is empty.
// Several processes append to one log, so the header may appear more than once
// — a process buffering it until it had an index to write would have nothing at
// all to say if it died — and every occurrence has to be the one this
// catalogue's runtime writes.
//
// The reader is fail-closed, and that is the whole design rather than a
// defensive habit. An infection fact is a licence not to execute a test, so a
// log that has been truncated, mixed with another run's, or written by a runtime
// built from a different catalogue must yield nothing at all rather than the
// part of itself that still parses — the part that still parses is exactly what
// a smaller, wrong answer looks like. The caller has one safe reading of an
// error, which is "this target yields no infection facts", and no safe reading
// of a partial one.
func ReadInfectionLog(r io.Reader, digest string, n int) ([]uint32, error) {
	if n <= 0 {
		return nil, &Error{
			Code: CodeInfectionLog,
			Message: "an infection log cannot be read against a catalogue of " + strconv.Itoa(n) +
				" mutants: a probe runtime sizes its array to at least one",
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
	// A line the writer never terminated is a process that died mid-write.
	// Taking the prefix would be inventing the rest of an index.
	if data[len(data)-1] != '\n' {
		return nil, &Error{
			Code: CodeInfectionLog,
			Message: "the infection log ends mid-line, so the process that wrote it " +
				"died before its last write completed",
		}
	}

	header := infectionHeader(digest, n)
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
		if index >= uint64(n) {
			return nil, &Error{
				Code: CodeInfectionLog,
				Message: "the infection log names mutant index " + strconv.FormatUint(index, 10) +
					", which is outside the " + strconv.Itoa(n) + " the catalogue holds",
			}
		}
		seen[uint32(index)] = true
	}

	// A set, in one order: the file is an append log written by however many
	// processes reached however many sites, and the question it answers — which
	// mutants could this target have observed — has no order of its own.
	out := make([]uint32, 0, len(seen))
	for index := range seen {
		out = append(out, index)
	}
	slices.Sort(out)
	return out, nil
}
