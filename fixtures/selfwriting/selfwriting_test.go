// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package selfwriting

import (
	"os"
	"testing"
)

// WitnessFile is what [TestTrimAndWriteAWitness] leaves in the directory it ran
// in.
//
// It is named rather than spelled inline because it is the string the drift
// gate reports and therefore the string the tests that drive this fixture look
// for. `go test` runs a test binary with its own package's source directory as
// the working directory, so the relative name lands beside this file — in the
// snapshot when go-mutants is measuring, and in `fixtures/selfwriting/` when
// somebody runs the suite here by hand. The second is why `git status
// --porcelain --ignored -- fixtures` is a CI gate.
const WitnessFile = "witness.txt"

// TestTrimAndWriteAWitness passes, and writes a file into its own package
// directory on the way.
//
// Both halves are load bearing. A failing test would stop a run at the baseline
// with a baseline error, which is a different refusal proven by a different
// fixture; the drift gate can only be reached by a suite the run is willing to
// believe.
func TestTrimAndWriteAWitness(t *testing.T) {
	if got := Trim(-1); got != 0 {
		t.Errorf("Trim(-1) = %d, want 0", got)
	}
	if got := Trim(7); got != 7 {
		t.Errorf("Trim(7) = %d, want 7", got)
	}
	if err := os.WriteFile(WitnessFile, []byte("the suite was here\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", WitnessFile, err)
	}
}
