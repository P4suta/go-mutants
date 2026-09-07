// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"strings"
	"testing"
)

// TestParseSizeReadsBinaryUnitsAndRefusesTheDecimalOnes is the whole of the
// size vocabulary in one table.
//
// The refusals matter more than the acceptances. `GB` is not read as `GiB`
// because the two differ by 7%, and a bound that is quietly 7% tighter than it
// reads is a bound that kills a legitimate suite for reasons nobody can see; it
// is also not read as a *decimal* gigabyte, because a person writing a memory
// budget in a Go project means the number their machine's task manager shows
// them. Refusing it and naming the accepted spellings is the only answer that
// cannot be wrong in silence.
func TestParseSizeReadsBinaryUnitsAndRefusesTheDecimalOnes(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		text string
		want int64
	}{
		{"1", 1},
		{"1024", 1024},
		{"512B", 512},
		{"64KiB", 64 << 10},
		{"2MiB", 2 << 20},
		{"2GiB", 2 << 30},
		{"1.5GiB", 3 << 29},
		{"1TiB", 1 << 40},
		{" 2 GiB ", 2 << 30},
		{"2gib", 2 << 30},
	} {
		got, err := parseSize(c.text)
		if err != nil {
			t.Errorf("parseSize(%q) = unexpected error %v", c.text, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.text, got, c.want)
		}
	}

	for _, bad := range []string{"", "   ", "2GB", "2kB", "2MB", "big", "NaN", "Inf", "1e400", "2GiBs"} {
		if got, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) = %d, want a refusal", bad, got)
		}
	}

	// A negative size is representable, so it is *read* here and refused by the
	// validator — which is the same division `test.timeout` makes, and the
	// reason a bad value in a file is reported at the line it was written on
	// rather than as a nameless conversion failure.
	for _, negative := range []string{"-1", "-2GiB"} {
		size, err := parseSize(negative)
		if err != nil {
			t.Errorf("parseSize(%q) = %v, want it read and left to the validator", negative, err)
			continue
		}
		if size >= 0 {
			t.Errorf("parseSize(%q) = %d, want a negative", negative, size)
		}
		if err := (Overlay{Memory: Explicit(size)}).Validate(); err == nil {
			t.Errorf("a memory bound of %q was accepted by the validator", negative)
		}
	}
}

// A number that parses, is finite, and is still bigger than an int64 is its own
// refusal, separate from every other one above: `1e400` is caught by
// strconv.ParseFloat, and `1e19` is not. The guard between them is written as a
// division so the comparison itself cannot overflow, and it is the branch that
// stands between a configured bound and math.MinInt64 — a negative bound every
// process is over, which would kill every mutant a run started.
func TestParseSizeRefusesASizeLargerThanAnyMachineHas(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"1e19",
		// The scale is applied to the comparison as well, so a number small
		// enough on its own is still refused once its unit is read.
		"1e19KiB",
		"10000000000TiB",
		// float64(math.MaxInt64) rounds *up* to 2^63, which is the one value
		// that would pass a `>` test and then convert to math.MinInt64.
		"9223372036854775808",
	} {
		got, err := parseSize(text)
		if err == nil {
			t.Errorf("parseSize(%q) = %d, want a refusal", text, got)
			continue
		}
		if !strings.Contains(err.Error(), "larger than any machine has") {
			t.Errorf("parseSize(%q) = %v, want the size to be refused for being too large", text, err)
		}
	}
}

// formatSize is the other half of the vocabulary, and it has one promise:
// what it prints, parseSize reads back. That promise is what lets a bound
// quoted in a diagnostic be pasted into the configuration that produced it, and
// it is the only reason this renders `1KiB` rather than `1024B`.
//
// The table is the unit boundaries and nothing else, because the boundaries are
// where every mistake this function can make lives: one byte either side of
// 1 KiB, the exact power where a unit gives way to the next, and the size past
// the last unit the list holds.
func TestFormatSizePrintsWhatParseSizeReadsBack(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		size int64
		want string
	}{
		{0, "0B"},
		{1, "1B"},
		// Bytes hold right up to the boundary, and the boundary itself is the
		// first size that is not bytes.
		{1023, "1023B"},
		{1 << 10, "1KiB"},
		{1536, "1.5KiB"},
		{(1 << 20) - 1, "1023.9990234375KiB"},
		// Each exact power is the smallest size its unit names, which is the
		// off-by-one the loop's break condition decides.
		{1 << 20, "1MiB"},
		{1 << 30, "1GiB"},
		{1 << 40, "1TiB"},
		{3 << 39, "1.5TiB"},
		// TiB is the last suffix, so a larger size keeps counting in it rather
		// than losing its unit.
		{1 << 50, "1024TiB"},
	} {
		got := formatSize(c.size)
		if got != c.want {
			t.Errorf("formatSize(%d) = %q, want %q", c.size, got, c.want)
			continue
		}
		back, err := parseSize(got)
		if err != nil {
			t.Errorf("parseSize(formatSize(%d)) = %v, and a bound this tool prints has to be one it reads",
				c.size, err)
			continue
		}
		if back != c.size {
			t.Errorf("parseSize(formatSize(%d)) = %d, want the size it started as", c.size, back)
		}
	}
}

// The one place formatSize is read by a user is the refusal of a bound that
// leaves no room, and a refusal that quotes the value back has to quote it in
// the spelling the file accepts. Nothing else in this package asserts that
// sentence, so an empty rendering — or one in the wrong unit — would be a
// diagnostic nobody could act on and a test nobody would see fail.
func TestANonPositiveMemoryBoundIsQuotedBackInItsOwnUnits(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ text, quoted string }{
		{"-1", "-1B"},
		// A negative is below the byte boundary, so it is quoted in bytes
		// however it was written. That is not a rounding of the truth: the
		// unit loop divides, and dividing a negative would name a unit the
		// value does not have.
		{"-2GiB", "-2147483648B"},
		{"0", "0B"},
	} {
		size, err := parseSize(c.text)
		if err != nil {
			t.Fatalf("parseSize(%q) = %v", c.text, err)
		}
		validateErr := (Overlay{Memory: Explicit(size)}).Validate()
		if validateErr == nil {
			t.Errorf("Validate accepted a memory bound of %q", c.text)
			continue
		}
		got := only(t, validateErr)
		if got.Code != CodeNonPositiveMemory {
			t.Errorf("code = %s, want %s", got.Code, CodeNonPositiveMemory)
		}
		want := "a memory bound of " + c.quoted + " leaves no room for a test binary: " +
			"omit the key to derive max(1GiB, largest baseline peak × 4)"
		if got.Message != want {
			t.Errorf("message = %q, want %q", got.Message, want)
		}
	}
}

// TestParseMemoryIsTheFlagsDoorIntoTheSameRule pins that `--memory` is judged by
// the validator the file is judged by, and reports the same two codes.
func TestParseMemoryIsTheFlagsDoorIntoTheSameRule(t *testing.T) {
	t.Parallel()

	if got, err := ParseMemory("2GiB"); err != nil || got != 2<<30 {
		t.Errorf("ParseMemory(2GiB) = %v, %v", got, err)
	}
	if _, err := ParseMemory("plenty"); err == nil {
		t.Error("ParseMemory accepted a non-size")
	} else if got := only(t, err); got.Code != CodeInvalidSize {
		t.Errorf("code = %s, want %s", got.Code, CodeInvalidSize)
	} else if got.Key != "--memory" {
		t.Errorf("key = %s, want the flag", got.Key)
	}
	if _, err := ParseMemory("0"); err == nil {
		t.Error("ParseMemory accepted zero")
	} else if got := only(t, err); got.Code != CodeNonPositiveMemory {
		t.Errorf("code = %s, want %s", got.Code, CodeNonPositiveMemory)
	}
}

// TestMemoryIsUnsetAtZeroLikeTheTimeout pins the one thing a new setting in
// `[test]` most easily gets wrong.
//
// Zero means "derive it" for the timeout and for this, so the round trip from a
// resolved [Config] back to an [Overlay] has to leave it unset — otherwise
// [Defaults] would not validate against its own validator, and every run would
// refuse to start.
func TestMemoryIsUnsetAtZeroLikeTheTimeout(t *testing.T) {
	t.Parallel()

	resolved := Defaults()
	if resolved.Test.Memory != 0 {
		t.Errorf("Defaults().Test.Memory = %d, want 0: the bound is derived unless a project says otherwise",
			resolved.Test.Memory)
	}
	if o := resolved.overlay(); o.Memory.IsSet() {
		t.Error("a resolved configuration with no memory bound round-trips as one that set it to zero")
	}
	if err := resolved.Validate(); err != nil {
		t.Errorf("Defaults().Validate() = %v, want nil", err)
	}
}
