// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"strings"
	"testing"
)

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

func TestParseSizeRefusesASizeLargerThanAnyMachineHas(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"1e19",
		"1e19KiB",
		"10000000000TiB",
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

func TestFormatSizePrintsWhatParseSizeReadsBack(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		size int64
		want string
	}{
		{0, "0B"},
		{1, "1B"},
		{1023, "1023B"},
		{1 << 10, "1KiB"},
		{1536, "1.5KiB"},
		{(1 << 20) - 1, "1023.9990234375KiB"},
		{1 << 20, "1MiB"},
		{1 << 30, "1GiB"},
		{1 << 40, "1TiB"},
		{3 << 39, "1.5TiB"},
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

func TestANonPositiveMemoryBoundIsQuotedBackInItsOwnUnits(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ text, quoted string }{
		{"-1", "-1B"},
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
