// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"errors"
	"fmt"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

func mutationSpan(start, end uint32) mutation.Span {
	return mutation.Span{StartByte: start, EndByte: end}
}

// TestCodesAreWellFormed keeps the diagnostic codes usable as the stable
// handles they are advertised to be: unique, sorted, and inside the block this
// package owns. A duplicated code makes two different failures
// indistinguishable to anyone searching for one.
func TestCodesAreWellFormed(t *testing.T) {
	t.Parallel()

	codes := instrument.Codes()
	if len(codes) == 0 {
		t.Fatal("Codes() is empty")
	}
	if !slices.IsSorted(codes) {
		t.Errorf("Codes() = %v, want them in numeric order", codes)
	}
	seen := map[instrument.Code]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Errorf("code %s appears twice", c)
		}
		seen[c] = true

		rest, ok := strings.CutPrefix(string(c), "GOM")
		if !ok || len(rest) != 4 {
			t.Errorf("code %q is not of the form GOM####", c)
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			t.Errorf("code %q does not end in a number", c)
			continue
		}
		if n < 7300 || n > 7399 {
			t.Errorf("code %q is outside the GOM73xx block this package owns", c)
		}
	}
}

// TestCodesAreReachable asserts the list is complete: every code the package
// documents can actually be produced. A code nobody can trigger is dead
// documentation, and one that is triggered but unlisted would be missing from
// `doctor`'s table.
func TestCodesAreReachable(t *testing.T) {
	t.Parallel()

	produced := map[instrument.Code]bool{}
	record := func(err error) {
		if code := instrument.CodeOf(err); code != "" {
			produced[code] = true
		}
	}

	_, err := instrument.Flatten([]byte("a $ b"))
	record(err)
	_, err = instrument.Flatten([]byte("`unterminated"))
	record(err)

	const src = "0123456789"
	_, _, err = instrument.Apply([]byte(src), []instrument.Splice{{
		Span: mutationSpan(2, 12), Original: []byte("23"),
	}})
	record(err)
	_, _, err = instrument.Apply([]byte(src), []instrument.Splice{{
		Span: mutationSpan(2, 4), Original: []byte("XX"),
	}})
	record(err)
	_, _, err = instrument.Apply([]byte(src), []instrument.Splice{
		spliceAt(src, 1, 5, "a"), spliceAt(src, 3, 7, "b"),
	})
	record(err)
	_, m, err := instrument.Apply([]byte(src), []instrument.Splice{spliceAt(src, 2, 6, "x")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	_, err = m.MapSpan(mutationSpan(3, 8))
	record(err)

	// The remaining three are the flattener's own postconditions, which no
	// input is meant to reach, and they are produced here through the test-only
	// hooks in export_test.go. Asserting instead that they are unreachable
	// would be asserting something this test cannot check: "no input reaches
	// them" is a claim about every possible input, and the only way to be wrong
	// about it quietly is to write it down and stop looking. Running them
	// proves the one thing that matters about a postcondition — that it fires.
	record(instrument.CheckFlat([]byte("a\nb")))
	record(instrument.VerifyTokensAgainst([]byte("ab"), []byte("a b")))
	_, err = instrument.FlattenLiteral(token.STRING, "`abc\ndef")
	record(err)

	// The instrumenter's own refusals, produced by the same list the refusal
	// test checks the codes of, so that a new code cannot be added to one place
	// and forgotten in the other.
	for _, f := range instrumentationFailures(t) {
		record(f.err)
	}

	for _, c := range instrument.Codes() {
		if !produced[c] {
			t.Errorf("no test produces code %s", c)
		}
	}
}

func TestErrorFormatting(t *testing.T) {
	t.Parallel()

	cause := errors.New("underlying")
	err := &instrument.Error{Code: instrument.CodeSpliceMismatch, Message: "spliced the wrong bytes", Err: cause}

	const want = "GOM7311: spliced the wrong bytes: underlying"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if got := instrument.CodeOf(fmt.Errorf("wrapped: %w", err)); got != instrument.CodeSpliceMismatch {
		t.Errorf("CodeOf through a wrapper = %q, want %q", got, instrument.CodeSpliceMismatch)
	}
	if got := instrument.CodeOf(cause); got != "" {
		t.Errorf("CodeOf(foreign error) = %q, want empty", got)
	}
	if got := instrument.CodeOf(nil); got != "" {
		t.Errorf("CodeOf(nil) = %q, want empty", got)
	}

	bare := &instrument.Error{Code: instrument.CodeNotFlat, Message: "no cause"}
	if got, want := bare.Error(), "GOM7304: no cause"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
