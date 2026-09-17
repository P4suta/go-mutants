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
	logDigest   = "cafefeed"
	logHeader   = "gomutants-infection-v1 " + logDigest + " 4"
	emptyHeader = "gomutants-infection-v1 " + logDigest + " 1"
)

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

func TestReadInfectionLogBoundsIndicesByTheCatalogueSize(t *testing.T) {
	t.Parallel()

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

	last, err := instrument.ReadInfectionLog(strings.NewReader(logHeader+"\n3\n"), logDigest, 4)
	if err != nil {
		t.Fatalf("ReadInfectionLog over the catalogue's last index: %v", err)
	}
	if want := []uint32{3}; !slices.Equal(last, want) {
		t.Errorf("ReadInfectionLog = %v, want %v", last, want)
	}
}

func TestReadInfectionLogRejectsAnythingItCannotReadWhole(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		log     string
		mutants int
	}{{
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
		name:    "a header whose width belongs to another catalogue",
		log:     emptyHeader + "\n",
		mutants: 4,
	}, {
		name:    "a header from an unknown version of the format",
		log:     "gomutants-infection-v2 " + logDigest + " 4\n0\n",
		mutants: 4,
	}, {
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
		name:    "an empty catalogue's log naming index 0",
		log:     emptyHeader + "\n0\n",
		mutants: 0,
	}, {
		name:    "a negative catalogue size",
		log:     logHeader + "\n0\n",
		mutants: -1,
	}, {
		name:    "an index past the width of the index type",
		log:     logHeader + "\n4294967296\n",
		mutants: 4,
	}, {
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
