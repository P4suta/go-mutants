// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package runner

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

const supervisorKind = "process-group"

type groupSupervisor struct {
	pgid int
}

func newSupervisor(int64) (supervisor, error) { return &groupSupervisor{}, nil }

func (s *groupSupervisor) configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func (s *groupSupervisor) adopt(cmd *exec.Cmd) error {
	s.pgid = cmd.Process.Pid
	supervisionAdopted.Add(1)
	return nil
}

func (s *groupSupervisor) terminate(exited <-chan struct{}, grace time.Duration) {
	supervisionTerminated.Add(1)
	if s.pgid <= 0 {
		return
	}
	if grace > 0 {
		_ = syscall.Kill(-s.pgid, syscall.SIGTERM)

		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-exited:
			return
		case <-timer.C:
		}
	}
	_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
}

func (s *groupSupervisor) usedMemory(thorough bool) (int64, bool) {
	if thorough {
		return treeAndGroupResidentMemory(s.pgid)
	}
	return treeResidentMemory(s.pgid)
}

func (s *groupSupervisor) peakMemory(ps *os.ProcessState) (int64, bool) {
	return peakRSSOf(ps)
}

func (s *groupSupervisor) release() {}

func exitCodeOf(ps *os.ProcessState) int {
	if ps == nil {
		return ExitCodeUnavailable
	}
	if status, ok := ps.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return ps.ExitCode()
}
