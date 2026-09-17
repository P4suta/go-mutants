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

const livenessHelperEnv = "PROCESS_LIVENESS_HELPER"

func TestProcessLivenessHelper(t *testing.T) {
	if !testkit.HelperEnabled(livenessHelperEnv) {
		return
	}
	time.Sleep(60 * time.Second)
}

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
