// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"errors"
	"testing"
)

// TestCodeStringIsTheCodeItself pins Code.String: it renders the code verbatim,
// not the empty string, so a diagnostic printed through it names its GOM code.
func TestCodeStringIsTheCodeItself(t *testing.T) {
	t.Parallel()

	if got := CodeFileUnreadable.String(); got != "GOM4140" {
		t.Errorf("Code.String() = %q, want %q", got, "GOM4140")
	}
}

// TestErrorRendersCodeMessageAndCause pins Error.Error: it prints the code and
// the message always, and appends the cause exactly when there is one. The
// no-cause case pins the `e.Err != nil` guard against dropping the cause, and
// the cause case pins it against appending a colon to nothing.
func TestErrorRendersCodeMessageAndCause(t *testing.T) {
	t.Parallel()

	withoutCause := (&Error{Code: CodeLoadFailed, Message: "loading failed"}).Error()
	if withoutCause != "GOM4110: loading failed" {
		t.Errorf("without a cause = %q, want %q", withoutCause, "GOM4110: loading failed")
	}

	withCause := (&Error{Code: CodeLoadFailed, Message: "loading failed", Err: errors.New("boom")}).Error()
	if withCause != "GOM4110: loading failed: boom" {
		t.Errorf("with a cause = %q, want the cause appended", withCause)
	}
}
