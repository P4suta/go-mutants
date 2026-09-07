// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"path/filepath"
	"testing"
)

// TestAKernelWithoutProcfsCannotEnforceABound is the fail-closed claim, and it
// is a claim about the *report* rather than about the bound.
//
// Nothing goes wrong if /proc is unreadable: the sampler reads no number, stops
// on the first tick, and the run goes on with its mutants bounded in time
// alone. That is the right behaviour and it is also invisible — a container
// with a hidden procfs would report itself as a platform that enforces bounds,
// derive one from the baseline, put it in the report, and enforce nothing. So
// the capability is probed rather than assumed from the build tag, and a
// machine that cannot answer says so where every other unenforceable platform
// says it.
func TestAKernelWithoutProcfsCannotEnforceABound(t *testing.T) {
	// Not parallel: it moves a package-level seam.
	original := procRoot
	t.Cleanup(func() {
		procRoot = original
		resetProcfsProbe()
	})

	procRoot = filepath.Join(t.TempDir(), "no-such-proc")
	resetProcfsProbe()
	if MemoryBoundSupported() {
		t.Error("MemoryBoundSupported() = true with no procfs to read")
	}
	if _, ok := groupResidentMemory(1); ok {
		t.Error("groupResidentMemory answered from a procfs that is not there")
	}

	procRoot = original
	resetProcfsProbe()
	if !MemoryBoundSupported() {
		t.Error("MemoryBoundSupported() = false on a Linux machine with a readable /proc")
	}
}

// TestTheProcfsProbeIsMadeOnce pins that the answer is cached: it is asked
// before every bounded run, and a run of thousands of mutants must not pay a
// file read per mutant for a question whose answer cannot change.
func TestTheProcfsProbeIsMadeOnce(t *testing.T) {
	original := procRoot
	t.Cleanup(func() {
		procRoot = original
		resetProcfsProbe()
	})

	resetProcfsProbe()
	if !MemoryBoundSupported() {
		t.Fatal("MemoryBoundSupported() = false on a Linux machine with a readable /proc")
	}
	// The seam moves and the answer does not, because nothing reads procfs a
	// second time.
	procRoot = filepath.Join(t.TempDir(), "no-such-proc")
	if !MemoryBoundSupported() {
		t.Error("the probe was made again after it had already answered")
	}
}
