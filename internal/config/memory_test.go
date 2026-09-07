// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import "testing"

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
