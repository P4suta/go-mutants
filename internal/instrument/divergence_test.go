// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
)

func censusOf(loops int, lines ...string) string {
	return "gomutants-loop-census-v1 " + itoa(loops) + "\n" + strings.Join(lines, "")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestReadLoopCensusTakesTheLargestCountEachSiteReached(t *testing.T) {
	t.Parallel()

	census := censusOf(3, "0 4\n", "1 9\n", "0 41\n") +
		censusOf(3, "1 2\n", "0 7\n")
	got, err := instrument.ReadLoopCensus(strings.NewReader(census), 3)
	if err != nil {
		t.Fatalf("ReadLoopCensus: %v", err)
	}
	want := []uint64{41, 9, 0}
	if len(got) != len(want) {
		t.Fatalf("ReadLoopCensus returned %d ceilings, want %d", len(got), len(want))
	}
	for site, count := range want {
		if got[site] != count {
			t.Errorf("site %d reached %d, want %d (counts %v)", site, got[site], count, got)
		}
	}
}

func TestReadLoopCensusRejectsAnythingItCannotReadWhole(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		census string
		loops  int
	}{
		{name: "an empty file", census: "", loops: 2},
		{
			name:   "a last line the writer never finished",
			census: censusOf(2, "0 4\n", "1 9"),
			loops:  2,
		},
		{
			name:   "a header from a tree with a different number of loops",
			census: censusOf(3, "0 4\n"),
			loops:  2,
		},
		{
			name:   "a line that is neither a header nor a count",
			census: censusOf(2, "0 4\n", "something else\n"),
			loops:  2,
		},
		{
			name:   "a site this tree does not hold",
			census: censusOf(2, "2 4\n"),
			loops:  2,
		},
		{
			name:   "a count that is not a number",
			census: censusOf(2, "0 many\n"),
			loops:  2,
		},
		{
			name:   "a negative site",
			census: censusOf(2, "-1 4\n"),
			loops:  2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := instrument.ReadLoopCensus(strings.NewReader(test.census), test.loops)
			if err == nil {
				t.Fatalf("ReadLoopCensus accepted %q and answered %v", test.census, got)
			}
			if code := instrument.CodeOf(err); code != instrument.CodeLoopCensus {
				t.Errorf("CodeOf(%v) = %q, want %q", err, code, instrument.CodeLoopCensus)
			}
			if got != nil {
				t.Errorf("a refused census yielded %v, want nothing at all", got)
			}
		})
	}

	if _, err := instrument.ReadLoopCensus(strings.NewReader(""), -1); err == nil {
		t.Error("ReadLoopCensus accepted a tree of fewer than no loops")
	}
}

func TestALimitTableIsWrittenTheWayTheRuntimeReadsIt(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	limits := []uint64{1 << 20, 4096, 0}
	if err := instrument.WriteLoopLimits(&out, limits); err != nil {
		t.Fatalf("WriteLoopLimits: %v", err)
	}
	text := out.String()
	if !strings.HasPrefix(text, "gomutants-loop-limits-v1 3\n") {
		t.Errorf("the table does not open with this tree's header:\n%s", text)
	}
	for site, limit := range limits {
		want := itoa(site) + " " + itoa(int(limit)) + "\n"
		if !strings.Contains(text, want) {
			t.Errorf("the table does not hold %q:\n%s", want, text)
		}
	}
	if got, want := strings.Count(text, "\n"), len(limits)+1; got != want {
		t.Errorf("the table holds %d lines, want %d: a header and one ceiling per site", got, want)
	}
}
