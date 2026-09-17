// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestAnErrorRendersItsCodeItsMessageAndItsCause pins the one line a user reads
// when git will not answer.
//
// The three parts are deliberately separate values and the rendering is
// deliberately two of them: the code and the message are this package's words
// and go into the sentence, and Output is git's own and does not — internal/cli
// lays that out underneath, so a message that inlined it would print it twice.
// The cause is appended when there is one and the separator is not optional
// punctuation: without it the two sentences run together into a line that reads
// as one claim.
func TestAnErrorRendersItsCodeItsMessageAndItsCause(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  *Error
		want string
	}{{
		name: "a failure with no cause",
		err:  &Error{Code: CodeNotARepository, Message: "there is no working tree here"},
		want: "GOM7711: there is no working tree here",
	}, {
		name: "a failure with a cause",
		err: &Error{
			Code:    CodeGitUnavailable,
			Message: "`git rev-parse` could not be run",
			Err:     errors.New("exec: \"git\": executable file not found in $PATH"),
		},
		want: "GOM7710: `git rev-parse` could not be run: " +
			"exec: \"git\": executable file not found in $PATH",
	}, {
		// Output is git's words, and this is where they are *not*. A renderer
		// that appended them here would put them in the message and again
		// underneath it.
		name: "a failure carrying git's own output",
		err: &Error{
			Code:    CodeDiffFailed,
			Message: "`git diff` failed",
			Output:  "fatal: bad revision 'nope'",
		},
		want: "GOM7714: `git diff` failed",
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.err.Error(); got != test.want {
				t.Errorf("Error() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestAnErrorIsFoundThroughAWrapping is the property every caller in this
// package relies on: prefix, resolveRef, mergeBase and diff all ask what code
// an error carries after somebody else has wrapped it.
func TestAnErrorIsFoundThroughAWrapping(t *testing.T) {
	t.Parallel()

	cause := errors.New("no such ref")
	inner := &Error{Code: CodeUnknownRef, Message: "git cannot find it", Output: "fatal: bad revision", Err: cause}
	wrapped := fmt.Errorf("resolving the ref: %w", inner)

	if got := CodeOf(wrapped); got != CodeUnknownRef {
		t.Errorf("CodeOf(wrapped) = %q, want %q", got, CodeUnknownRef)
	}
	if got := OutputOf(wrapped); got != "fatal: bad revision" {
		t.Errorf("OutputOf(wrapped) = %q, want git's own words", got)
	}
	if !errors.Is(wrapped, cause) {
		t.Error("Unwrap does not reach the cause")
	}

	// And an error from somewhere else carries neither, rather than the zero
	// value of something this package might have said.
	foreign := errors.New("a plain error")
	if got := CodeOf(foreign); got != "" {
		t.Errorf("CodeOf(a foreign error) = %q, want the empty code", got)
	}
	if got := OutputOf(foreign); got != "" {
		t.Errorf("OutputOf(a foreign error) = %q, want nothing", got)
	}
	if got := CodeOf(nil); got != "" {
		t.Errorf("CodeOf(nil) = %q, want the empty code", got)
	}
	if got := OutputOf(nil); got != "" {
		t.Errorf("OutputOf(nil) = %q, want nothing", got)
	}
}

// TestEveryCodeIsSpelledTheWayItIsPrinted writes the seven strings out.
//
// They are what a user reads in a failure and what docs/errors.md lists, so
// they are asserted as literals rather than derived from the constants: a test
// that compared `string(c)` with `c.String()` would pass however the block was
// renumbered.
func TestEveryCodeIsSpelledTheWayItIsPrinted(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{CodeGitUnavailable.String(), "GOM7710"},
		{CodeNotARepository.String(), "GOM7711"},
		{CodeNoUpstream.String(), "GOM7712"},
		{CodeUnknownRef.String(), "GOM7713"},
		{CodeDiffFailed.String(), "GOM7714"},
		{CodeMalformedDiff.String(), "GOM7715"},
		{CodeUntrackedUnreadable.String(), "GOM7716"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("a code renders as %q, want %q", pair[0], pair[1])
		}
	}

	// And the list a doctor prints is the same seven, in the same order, as a
	// copy rather than as the package's own slice.
	got := Codes()
	if len(got) != len(codes) {
		t.Fatalf("Codes() has %d entries, want %d", len(got), len(codes))
	}
	got[0] = "GOM0000"
	if codes[0] == "GOM0000" {
		t.Error("Codes() handed out the package's own slice")
	}
	for _, code := range codes {
		if !strings.HasPrefix(string(code), "GOM771") {
			t.Errorf("%s is outside the block this package owns", code)
		}
	}
}
