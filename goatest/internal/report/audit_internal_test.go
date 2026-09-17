// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"strings"
	"testing"
)

const hexDigitRuns = 4

func TestAValidDigestIsSixtyFourLowercaseHexDigits(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("0123456789abcdef", hexDigitRuns)
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "every digit and letter it admits", input: full, want: true},
		{name: "the lowest digest", input: strings.Repeat("0", len(full)), want: true},
		{name: "the highest digest", input: strings.Repeat("f", len(full)), want: true},
		{name: "nothing at all"},
		{name: "one character short", input: full[:len(full)-1]},
		{name: "one character long", input: full + "0"},
		{name: "the same digits in capitals", input: strings.ToUpper(full)},
		{name: "a letter past f", input: full[:len(full)-1] + "g"},
		{name: "the character below zero", input: full[:len(full)-1] + "/"},
		{name: "the character above nine", input: full[:len(full)-1] + ":"},
		{name: "the character below a", input: full[:len(full)-1] + "`"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := validSHA256(test.input); got != test.want {
				t.Fatalf("validSHA256(%q) = %t, want %t", test.input, got, test.want)
			}
		})
	}
}

func TestEveryRunKindThisRunnerWritesIsOneItReadsBack(t *testing.T) {
	t.Parallel()
	for _, kind := range []RunKind{RunFull, RunChangeset, RunPackage, RunReplay, RunOperation} {
		if !knownRunKind(kind) {
			t.Errorf("knownRunKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []RunKind{"", "future", "FULL", "package "} {
		if knownRunKind(kind) {
			t.Errorf("knownRunKind(%q) = true, want false", kind)
		}
	}
}

func TestEveryVerdictThisRunnerWritesIsOneItReadsBack(t *testing.T) {
	t.Parallel()
	for _, verdict := range []Verdict{
		"", VerdictAssured, VerdictChangeAssured, VerdictScopeAssured, VerdictDefect,
		VerdictInsufficient, VerdictError, VerdictReproduced, VerdictResolved, VerdictCompleted,
	} {
		if !knownVerdict(verdict) {
			t.Errorf("knownVerdict(%q) = false, want true", verdict)
		}
	}
	for _, verdict := range []Verdict{"future", "assured", "ASSURED "} {
		if knownVerdict(verdict) {
			t.Errorf("knownVerdict(%q) = true, want false", verdict)
		}
	}
}

func TestACountAddsUpOrSaysWhichEquationItBroke(t *testing.T) {
	t.Parallel()
	balanced := CountAccounting{Discovered: 10, Selected: 7, Executed: 5, Skipped: 2, Excluded: 3}
	for _, test := range []struct {
		name  string
		count CountAccounting
		want  string
	}{
		{name: "a count that adds up", count: balanced},
		{name: "a count of nothing at all"},
		{
			name:  "a negative discovered",
			count: CountAccounting{Discovered: -1}, want: "negative count",
		},
		{
			name:  "a negative selected",
			count: CountAccounting{Selected: -1}, want: "negative count",
		},
		{
			name:  "a negative executed",
			count: CountAccounting{Executed: -1}, want: "negative count",
		},
		{
			name:  "a negative skipped",
			count: CountAccounting{Skipped: -1}, want: "negative count",
		},
		{
			name:  "a negative excluded",
			count: CountAccounting{Excluded: -1}, want: "negative count",
		},
		{
			name:  "a discovery that is not selection and exclusion",
			count: CountAccounting{Discovered: 11, Selected: 7, Executed: 5, Skipped: 2, Excluded: 3},
			want:  "discovered=11 selected=7 excluded=3",
		},
		{
			name:  "a selection that is not execution and skipping",
			count: CountAccounting{Discovered: 10, Selected: 7, Executed: 4, Skipped: 2, Excluded: 3},
			want:  "selected=7 executed=4 skipped=2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCount("targets", test.count)
			switch {
			case test.want == "" && err != nil:
				t.Fatalf("validateCount(%+v) = %v, want no complaint", test.count, err)
			case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
				t.Fatalf("validateCount(%+v) = %v, want it to say %q", test.count, err, test.want)
			}
		})
	}
}
