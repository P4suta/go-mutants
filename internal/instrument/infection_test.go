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
	// logHeader is the header a runtime generated for that digest and a
	// four-mutant catalogue writes once per process.
	logHeader = "gomutants-infection-v1 " + logDigest + " 4"
	// emptyHeader is the header the runtime of an *empty* catalogue writes. The
	// four is not a typo turned into a one: the array is never zero-length, so
	// its width is one where the catalogue's size is nought, and the two numbers
	// stop agreeing exactly here. The reader is told the catalogue's size and
	// derives this for itself.
	emptyHeader = "gomutants-infection-v1 " + logDigest + " 1"
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

// TestReadInfectionLogBoundsIndicesByTheCatalogueSize pins the one place where
// the header's number and the reader's bound are deliberately not the same
// number.
//
// The runtime's array is never zero-length — a length of one keeps the generated
// source one shape — so the header of an empty catalogue's runtime says one
// where the catalogue holds nothing. The caller is handed the catalogue's own
// size and the reader derives the header's width from it through the rule the
// generators use, so the caller never has to know that rule. Bounding indices by
// the size rather than by the width is what stops an empty catalogue's log
// admitting a mutant 0 that does not exist, and it is the only reading under
// which the reader's answer is always a set of real mutants.
func TestReadInfectionLogBoundsIndicesByTheCatalogueSize(t *testing.T) {
	t.Parallel()

	// An empty catalogue's probe still ran, still wrote its header, and still
	// proved something: that nothing was infected, because nothing could be.
	got, err := instrument.ReadInfectionLog(strings.NewReader(emptyHeader+"\n"), logDigest, 0)
	if err != nil {
		t.Fatalf("ReadInfectionLog over an empty catalogue's log: %v", err)
	}
	if got == nil {
		t.Error("an empty catalogue's log read as a nil slice, want an empty one: it is an answer, not an absent one")
	}
	if len(got) != 0 {
		t.Errorf("an empty catalogue's log read as %v, want no indices", got)
	}

	// The last index a four-mutant catalogue has. Its neighbour, 4, is in the
	// rejection table: off by one in either direction is the mistake worth
	// pinning from both sides.
	last, err := instrument.ReadInfectionLog(strings.NewReader(logHeader+"\n3\n"), logDigest, 4)
	if err != nil {
		t.Fatalf("ReadInfectionLog over the catalogue's last index: %v", err)
	}
	if want := []uint32{3}; !slices.Equal(last, want) {
		t.Errorf("ReadInfectionLog = %v, want %v", last, want)
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
		// mutants is the catalogue size the log is read against, spelled out on
		// every row because it is the argument half of these cases are about.
		mutants int
	}{{
		// A probe process that died before writing its header proved nothing at
		// all, and an empty file is what that looks like.
		name:    "empty input",
		log:     "",
		mutants: 4,
	}, {
		name:    "no header",
		log:     "0\n1\n",
		mutants: 4,
	}, {
		name:    "a header naming another catalogue",
		log:     "gomutants-infection-v1 deadbeef 4\n0\n",
		mutants: 4,
	}, {
		name:    "a header naming another catalogue size",
		log:     "gomutants-infection-v1 " + logDigest + " 5\n0\n",
		mutants: 4,
	}, {
		// The array width rather than the catalogue size: a log written by an
		// empty catalogue's runtime is not a log of this one, and reading it as
		// though it were would attribute nothing to four mutants that exist.
		name:    "a header whose width belongs to another catalogue",
		log:     emptyHeader + "\n",
		mutants: 4,
	}, {
		name:    "a header from an unknown version of the format",
		log:     "gomutants-infection-v2 " + logDigest + " 4\n0\n",
		mutants: 4,
	}, {
		// Two runs appended to one file. Neither half can be told from the
		// other afterwards, so neither is reported.
		name:    "a second header that differs from the first",
		log:     logHeader + "\n0\ngomutants-infection-v1 deadbeef 4\n1\n",
		mutants: 4,
	}, {
		name:    "an index that is not a number",
		log:     logHeader + "\n0\nsurvived\n",
		mutants: 4,
	}, {
		name:    "a negative index",
		log:     logHeader + "\n-1\n",
		mutants: 4,
	}, {
		name:    "an index equal to the catalogue size",
		log:     logHeader + "\n4\n",
		mutants: 4,
	}, {
		name:    "an index past the catalogue size",
		log:     logHeader + "\n9\n",
		mutants: 4,
	}, {
		// The array is one wide and the catalogue is empty, so the one index the
		// array could hold names no mutant. This is the case the reader would
		// get wrong if it bounded indices by the header's number.
		name:    "an empty catalogue's log naming index 0",
		log:     emptyHeader + "\n0\n",
		mutants: 0,
	}, {
		// A catalogue cannot hold fewer than no mutants, so this is a caller
		// bug; refusing it is also what keeps the index bound meaningful.
		name:    "a negative catalogue size",
		log:     logHeader + "\n0\n",
		mutants: -1,
	}, {
		// One past the largest uint32, which is the index type the runtime
		// writes and the guards spell.
		name:    "an index past the width of the index type",
		log:     logHeader + "\n4294967296\n",
		mutants: 4,
	}, {
		// The process died mid-write. Whatever that line was going to say, the
		// file no longer says it, and a reader that took the prefix would be
		// inventing an index.
		name:    "a truncated last line",
		log:     logHeader + "\n0\n1",
		mutants: 4,
	}, {
		name:    "a header that was never finished",
		log:     strings.TrimSuffix(logHeader, " 4"),
		mutants: 4,
	}, {
		name:    "a blank line",
		log:     logHeader + "\n\n0\n",
		mutants: 4,
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, err := instrument.ReadInfectionLog(strings.NewReader(c.log), logDigest, c.mutants)
			if err == nil {
				t.Fatalf("ReadInfectionLog accepted %q against %d mutants and returned %v", c.log, c.mutants, got)
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
