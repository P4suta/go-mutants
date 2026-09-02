// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
)

const (
	// logDigest stands in for a catalogue digest. Nothing in the reader hashes
	// anything, so a readable string says more here than a real digest would:
	// the assertions are about the header matching, not about what it names.
	logDigest = "cafefeed"
	// logHeader is the header a runtime generated for that catalogue and four
	// mutants writes once per process.
	logHeader = "gomutants-infection-v1 " + logDigest + " 4"
)

// TestReadInfectionLogAcceptsRepeatedIdenticalHeaders is the one shape of the
// format that looks malformed and is not.
//
// A probe pass runs one target's test binaries against a single log, and every
// process writes its own header before its first index, because a process that
// buffered a header until it had something to say would have nothing at all to
// say if it died. So a repeat is ordinary — as long as it is a repeat: a header
// naming a different catalogue in the same file means two runs' facts were
// mixed, and no part of that file can be attributed to either.
func TestReadInfectionLogAcceptsRepeatedIdenticalHeaders(t *testing.T) {
	t.Parallel()

	log := strings.Join([]string{logHeader, "2", logHeader, "0", logHeader}, "\n") + "\n"
	got, err := instrument.ReadInfectionLog(strings.NewReader(log), logDigest, 4)
	if err != nil {
		t.Fatalf("ReadInfectionLog: %v", err)
	}
	if want := []uint32{0, 2}; !slices.Equal(got, want) {
		t.Errorf("ReadInfectionLog = %v, want %v", got, want)
	}
}

// TestReadInfectionLogSortsAndDeduplicates states what the reader's answer is,
// as opposed to what the file happens to hold.
//
// The file is an append log written by several processes in whatever order they
// reached their sites, and one mutant's site can be reported by more than one of
// them. The caller is asking a set question — which mutants could this target
// have observed — so the answer is a set, in one order, and a header-only log is
// the empty answer rather than an error: a process that ran and infected nothing
// proved exactly that.
func TestReadInfectionLogSortsAndDeduplicates(t *testing.T) {
	t.Parallel()

	log := strings.Join([]string{logHeader, "3", "0", "3", "1", "0"}, "\n") + "\n"
	got, err := instrument.ReadInfectionLog(strings.NewReader(log), logDigest, 4)
	if err != nil {
		t.Fatalf("ReadInfectionLog: %v", err)
	}
	if want := []uint32{0, 1, 3}; !slices.Equal(got, want) {
		t.Errorf("ReadInfectionLog = %v, want %v", got, want)
	}

	empty, err := instrument.ReadInfectionLog(strings.NewReader(logHeader+"\n"), logDigest, 4)
	if err != nil {
		t.Fatalf("ReadInfectionLog over a header-only log: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("a log holding only its header read as %v, want no indices", empty)
	}
}

// TestReadInfectionLogRejectsAnythingItCannotReadWhole is the fail-closed half,
// and the reason it is a table rather than a handful of tests.
//
// An infection fact licenses not running a test. Every row below is a file that
// could be read as "fewer mutants were infected than really were", either by
// dropping a line or by attributing one to the wrong catalogue, and each of them
// has to come back as an error carrying no indices at all — never as the part of
// the file that still parses, which is precisely the part that would look like
// a smaller answer.
func TestReadInfectionLogRejectsAnythingItCannotReadWhole(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		log  string
	}{{
		// A probe process that died before writing its header proved nothing at
		// all, and an empty file is what that looks like.
		name: "empty input",
		log:  "",
	}, {
		name: "no header",
		log:  "0\n1\n",
	}, {
		name: "a header naming another catalogue",
		log:  "gomutants-infection-v1 deadbeef 4\n0\n",
	}, {
		name: "a header naming another catalogue size",
		log:  "gomutants-infection-v1 " + logDigest + " 5\n0\n",
	}, {
		name: "a header from an unknown version of the format",
		log:  "gomutants-infection-v2 " + logDigest + " 4\n0\n",
	}, {
		// Two runs appended to one file. Neither half can be told from the
		// other afterwards, so neither is reported.
		name: "a second header that differs from the first",
		log:  logHeader + "\n0\ngomutants-infection-v1 deadbeef 4\n1\n",
	}, {
		name: "an index that is not a number",
		log:  logHeader + "\n0\nsurvived\n",
	}, {
		name: "a negative index",
		log:  logHeader + "\n-1\n",
	}, {
		name: "an index past the end of the catalogue",
		log:  logHeader + "\n4\n",
	}, {
		// One past the largest uint32, which is the index type the runtime
		// writes and the guards spell.
		name: "an index past the width of the index type",
		log:  logHeader + "\n4294967296\n",
	}, {
		// The process died mid-write. Whatever that line was going to say, the
		// file no longer says it, and a reader that took the prefix would be
		// inventing an index.
		name: "a truncated last line",
		log:  logHeader + "\n0\n1",
	}, {
		name: "a header that was never finished",
		log:  strings.TrimSuffix(logHeader, " 4"),
	}, {
		name: "a blank line",
		log:  logHeader + "\n\n0\n",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, err := instrument.ReadInfectionLog(strings.NewReader(c.log), logDigest, 4)
			if err == nil {
				t.Fatalf("ReadInfectionLog accepted %q and returned %v", c.log, got)
			}
			if len(got) != 0 {
				t.Errorf("ReadInfectionLog refused %q and returned %v anyway", c.log, got)
			}
			if code := instrument.CodeOf(err); code != instrument.CodeInfectionLog {
				t.Errorf("ReadInfectionLog(%q) reported code %q, want %q", c.log, code, instrument.CodeInfectionLog)
			}
		})
	}
}
