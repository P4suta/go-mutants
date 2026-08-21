// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/cache"
)

// TestCodesAreCompleteUniqueAndInThisPackagesBlock is the guard against a code
// that two conditions share or that belongs to somebody else. The numbers are
// part of the command line contract — CI configurations and bug reports quote
// them — so they are checked rather than trusted.
func TestCodesAreCompleteUniqueAndInThisPackagesBlock(t *testing.T) {
	t.Parallel()

	codes := cache.Codes()
	if len(codes) == 0 {
		t.Fatal("this package reports no codes at all")
	}
	seen := make(map[cache.Code]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			t.Errorf("the code %s is listed twice", code)
		}
		seen[code] = true
		if !strings.HasPrefix(string(code), "GOM79") || len(code) != len("GOM7900") {
			t.Errorf("%s is outside the GOM79xx block this package owns", code)
		}
	}
	if !slices.IsSortedFunc(codes, func(x, y cache.Code) int { return strings.Compare(string(x), string(y)) }) {
		t.Errorf("the codes are not in numeric order: %v", codes)
	}
	codes[0] = "GOM7999"
	if slices.Contains(cache.Codes(), cache.Code("GOM7999")) {
		t.Error("Codes handed out its own slice")
	}
}

// TestAnErrorRendersItsCodeAndKeepsItsCause: one greppable line, and the cause
// still reachable through errors.Is.
func TestAnErrorRendersItsCodeAndKeepsItsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("access is denied")
	err := &cache.Error{Code: cache.CodeEntryNotWritten, Message: "an outcome could not be stored", Err: cause}

	want := "GOM7905: an outcome could not be stored: access is denied"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if got := cache.CodeOf(fmt.Errorf("wrapped: %w", err)); got != cache.CodeEntryNotWritten {
		t.Errorf("CodeOf through a wrap = %q, want %q", got, cache.CodeEntryNotWritten)
	}
	if got := cache.CodeOf(cause); got != "" {
		t.Errorf("CodeOf of a foreign error = %q, want the empty code", got)
	}
}
