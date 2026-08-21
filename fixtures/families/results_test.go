// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import (
	"errors"
	"testing"
)

// TestCheck pins both sides of the boundary and the error identity.
//
// The 100 row is the only input at which `>` and `>=` disagree, and comparing
// with errors.Is rather than checking for non-nil is what kills the swallowed
// error: a test that only asked "did it fail" would still pass against a
// function that returned the wrong failure.
func TestCheck(t *testing.T) {
	if err := Check(101); !errors.Is(err, ErrTooHigh) {
		t.Errorf("Check(101) = %v, want %v", err, ErrTooHigh)
	}
	if err := Check(100); err != nil {
		t.Errorf("Check(100) = %v, want nil", err)
	}
	if err := Check(50); err != nil {
		t.Errorf("Check(50) = %v, want nil", err)
	}
}

// TestWrap pins each of the three ways through the function.
//
// The first row is what kills the comparison swap, the branch that stops
// firing, and the negated condition alike: all three send the first failure's
// caller to the second result. The second row is the only one that kills the
// swallowed `second`.
func TestWrap(t *testing.T) {
	if err := Wrap(ErrTooHigh, nil); !errors.Is(err, ErrTooHigh) {
		t.Errorf("Wrap(ErrTooHigh, nil) = %v, want %v", err, ErrTooHigh)
	}
	if err := Wrap(nil, ErrSecond); !errors.Is(err, ErrSecond) {
		t.Errorf("Wrap(nil, ErrSecond) = %v, want %v", err, ErrSecond)
	}
	if err := Wrap(nil, nil); err != nil {
		t.Errorf("Wrap(nil, nil) = %v, want nil", err)
	}
}

func TestLabel(t *testing.T) {
	if got := Label(0); got != "ok" {
		t.Errorf("Label(0) = %q, want %q", got, "ok")
	}
	if got := Label(1); got != "error" {
		t.Errorf("Label(1) = %q, want %q", got, "error")
	}
}
