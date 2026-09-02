// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probeable

import (
	"os"
	"testing"
	"time"
)

// TestWidth pins Width and nothing else, which is what makes it usable as the
// "test A" of a probe pass: it reaches exactly one probed site.
func TestWidth(t *testing.T) {
	if got := Width(); got != 3 {
		t.Errorf("Width() = %d, want 3", got)
	}
}

// TestLabel pins Label and nothing else, and is the "test B" every assertion
// about TestWidth is stated against.
func TestLabel(t *testing.T) {
	if got := Label(); got != "probe" {
		t.Errorf("Label() = %q, want %q", got, "probe")
	}
}

// TestReady pins the unprobed mutant's function, so that a run has a kill no
// probe pass can account for.
func TestReady(t *testing.T) {
	if !Ready() {
		t.Error("Ready() = false, want true")
	}
}

// TestFlagged fails when the environment says to, and passes otherwise.
//
// A probe pass has to classify a failing test as "no facts", and the failure
// has to be one the fixture can be asked for rather than one it always has:
// every session prepared over this module verifies it with `go test ./...`
// first, so a test that failed unconditionally would fail the preparation
// instead of the probe. It calls neither probed function, which is what makes
// the empty result it produces attributable to the failure rather than to
// having reached nothing.
func TestFlagged(t *testing.T) {
	if os.Getenv("PROBEABLE_FAIL") == "yes" {
		t.Fatal("PROBEABLE_FAIL asked this test to fail")
	}
}

// TestBlocks sleeps past any budget a probe pass gives it, when the
// environment says to.
//
// The environment gate is what keeps the fixture fast: the sleep would
// otherwise be paid by the verification run of every session prepared over this
// module, and by every whole-package target, for the sake of one test that asks
// for a timeout on purpose.
func TestBlocks(t *testing.T) {
	if os.Getenv("PROBEABLE_BLOCK") != "yes" {
		return
	}
	time.Sleep(30 * time.Second)
}
