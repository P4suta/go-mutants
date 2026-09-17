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

// TestTheDirectoryHoldsNoOtherMutantsWitness is the isolation proof, and it is
// the first test in this file because it has to run before the one that writes.
//
// Under `--isolate` every worker has its own copy of the instrumented tree and
// that copy is put back between mutants, so each mutant finds the directory as
// the run left it. Drop the restore and this fails for every mutant after the
// first — turning [Nudge]'s survivors into kills, which is a signal no count of
// drifted files can give.
//
// It is conditional on a mutant being active, and that is not a convenience.
// The baseline runs this suite several times in one tree on purpose; an
// unconditional check would fail the second of those and stop the run at the
// baseline gate, which is a different refusal proven by a different fixture.
func TestTheDirectoryHoldsNoOtherMutantsWitness(t *testing.T) {
	if os.Getenv("GO_MUTANTS_ACTIVE") == "" {
		t.Skip("no mutant is active, so this is a baseline run and the tree may carry the previous one's witness")
	}
	if _, err := os.Stat(WitnessFile); err == nil {
		t.Fatalf("%s is already here, so this mutant is being measured in a directory an earlier one wrote into",
			WitnessFile)
	}
}

// TestNudge calls the fixture's survivor and asserts nothing about it, which is
// what makes every mutant of [Nudge] live in a passing binary.
func TestNudge(t *testing.T) {
	_ = Nudge(1)
}

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
