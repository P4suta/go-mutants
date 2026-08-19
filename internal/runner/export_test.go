// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import "os/exec"

// This file is compiled only under `go test`. It exposes the internals the
// supervision tests need without widening the package's real API: which
// mechanism a platform uses and how often it was exercised are facts the tests
// must be able to assert, but nothing in go-mutants should be able to branch
// on them.

// SupervisorKind is the mechanism this build supervises process trees with.
const SupervisorKind = supervisorKind

// AdoptedCount is how many process trees this process has taken ownership of
// since it started. Tests assert deltas around a call, never absolutes: the
// counter is global and every other test in the binary moves it.
func AdoptedCount() int64 { return supervisionAdopted.Load() }

// TerminatedCount is how many process trees this process has killed.
func TerminatedCount() int64 { return supervisionTerminated.Load() }

// StartSuspendedForTest runs the supervisor's real configure/Start/adopt
// sequence, calling between after the child has been started but before it has
// been adopted, and then waits for the child.
//
// It exists so a test can look inside the moment [Run] closes: on Windows the
// child must not have executed a single instruction while between runs, which
// is the whole claim CREATE_SUSPENDED makes and the one thing no observation
// of a finished Run can confirm.
func StartSuspendedForTest(cmd *exec.Cmd, between func()) error {
	sup, err := newSupervisor()
	if err != nil {
		return err
	}
	defer sup.release()

	sup.configure(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	between()
	if err := sup.adopt(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return cmd.Wait()
}

// EffectiveOutputLimit exposes the output-limit defaulting rule.
func EffectiveOutputLimit(limit int) int { return effectiveOutputLimit(limit) }

// TruncationNotice exposes the notice a capped capture is prefixed with.
func TruncationNotice(total int64) string { return truncationNotice(total) }
