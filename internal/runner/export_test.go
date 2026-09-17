// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import "os/exec"

const SupervisorKind = supervisorKind

func AdoptedCount() int64 { return supervisionAdopted.Load() }

func TerminatedCount() int64 { return supervisionTerminated.Load() }

func StartSuspendedForTest(cmd *exec.Cmd, between func()) error {
	sup, err := newSupervisor(0)
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

func EffectiveOutputLimit(limit int) int { return effectiveOutputLimit(limit) }

func TruncationNotice(total int64) string { return truncationNotice(total) }

func KernelBoundsMemory() bool { return kernelBoundsMemory }

func AccountedPeakBelongsToTheChild() bool { return accountedPeakBelongsToTheChild }
