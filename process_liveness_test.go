// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// livenessHelperEnv switches the sleeping subprocess below on. Its presence,
// not its value, is what [testkit.HelperEnabled] reads.
const livenessHelperEnv = "PROCESS_LIVENESS_HELPER"

// TestProcessLivenessHelper is not a test. It is a process that stays alive
// until something kills it, which is the only thing a liveness probe can be
// checked against.
func TestProcessLivenessHelper(t *testing.T) {
	if !testkit.HelperEnabled(livenessHelperEnv) {
		return
	}
	time.Sleep(60 * time.Second)
}

// TestTheLivenessProbeTellsRunningFromGone keeps [processIsGone] honest.
//
// It is the probe that carries the cleanup proof in this package: every
// assertion that a killed target really died is one call to it, and a probe that
// answered "gone" unconditionally would turn all of them green for ever without
// a word. That is the same defect the proof was written to replace — an
// assertion that cannot fail — so the probe gets an assertion of its own, and it
// has to be wrong in both directions to pass.
//
// A subprocess rather than a synthesized pid, because both implementations are
// about what the operating system says: signal 0 against a live process group on
// POSIX, an opened handle whose exit code is STILL_ACTIVE on Windows. A pid
// picked out of the air would exercise neither.
func TestTheLivenessProbeTellsRunningFromGone(t *testing.T) {
	t.Parallel()

	argv := testkit.HelperArgv("TestProcessLivenessHelper")
	sleeper := exec.Command(argv[0], argv[1:]...)
	sleeper.Env = append(os.Environ(), livenessHelperEnv+"=1")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("starting the sleeping helper: %v", err)
	}
	pid := sleeper.Process.Pid
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})

	gone, err := processIsGone(pid)
	if err != nil {
		t.Fatalf("probing the running helper (pid %d): %v", pid, err)
	}
	if gone {
		t.Fatalf("processIsGone said pid %d had ended while it was still running, "+
			"so every cleanup proof in this package is vacuous", pid)
	}

	if err := sleeper.Process.Kill(); err != nil {
		t.Fatalf("killing the helper (pid %d): %v", pid, err)
	}
	// Waited for before probing, because a reaped process is what the cleanup
	// proofs ask about: internal/runner has always waited for its child by the
	// time Exec returns, and an unreaped one is a zombie that POSIX still
	// reports as alive.
	_ = sleeper.Wait()

	started := time.Now()
	for {
		gone, err := processIsGone(pid)
		if err != nil {
			t.Fatalf("probing the killed helper (pid %d): %v", pid, err)
		}
		if gone {
			return
		}
		if elapsed := time.Since(started); elapsed > targetDeathBound {
			t.Fatalf("processIsGone still said pid %d was running %s after it was killed and reaped",
				pid, elapsed)
		}
		time.Sleep(targetDeathPoll)
	}
}
