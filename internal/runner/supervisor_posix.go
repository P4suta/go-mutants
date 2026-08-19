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

// supervisorKind names the mechanism this platform supervises with.
const supervisorKind = "process-group"

// groupSupervisor owns a POSIX process group holding one child process tree.
//
// Unlike a Windows job object, a process group is a number rather than an
// ownership relation: a descendant that calls setpgid or setsid leaves it and
// stops being covered. That is a real hole and there is no POSIX way to close
// it, which is why the package documentation calls this supervision best
// effort while calling the Windows path exact. In practice a `go test` binary
// and the helpers it spawns stay in the group.
type groupSupervisor struct {
	// pgid is the group id, which equals the child's pid because Setpgid with
	// no Pgid makes the child the leader of a new group.
	pgid int
}

// newSupervisor cannot fail here: the group is created by the kernel as part
// of starting the child, so there is nothing to allocate up front.
func newSupervisor() (supervisor, error) { return &groupSupervisor{}, nil }

// configure asks the kernel to put the child in a new process group of its
// own. Any descendant it later creates inherits that group, which is what
// makes a single kill reach the tree.
func (s *groupSupervisor) configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// adopt records the group id. There is nothing to fail: if Setpgid had not
// taken effect the child would not have started at all.
func (s *groupSupervisor) adopt(cmd *exec.Cmd) error {
	s.pgid = cmd.Process.Pid
	supervisionAdopted.Add(1)
	return nil
}

// terminate signals the whole group, politely first.
//
// SIGTERM gives a test binary the chance to run its deferred cleanup and flush
// its output, which is worth having because that output is the evidence for
// why the mutant timed out. SIGKILL follows after [TerminationGrace] if the
// group is still there — a hung test is exactly the case that ignores SIGTERM,
// so the escalation is not optional.
//
// The kill goes to -pgid while the child is still un-reaped, so the pid the
// group is named after cannot yet have been recycled by the kernel. That
// ordering is the reason this package never sweeps the group after a normal
// exit; see the package documentation.
func (s *groupSupervisor) terminate(exited <-chan struct{}) {
	supervisionTerminated.Add(1)
	if s.pgid <= 0 {
		return
	}
	_ = syscall.Kill(-s.pgid, syscall.SIGTERM)

	timer := time.NewTimer(TerminationGrace)
	defer timer.Stop()
	select {
	case <-exited:
		return
	case <-timer.C:
	}
	_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
}

// release has nothing to free: a process group is a number, not a handle.
func (s *groupSupervisor) release() {}

// exitCodeOf reads the child's status, mapping a signal death to the shell's
// 128+N convention. ProcessState.ExitCode reports -1 for a signalled process,
// which would be indistinguishable from "no status at all"; 137 for a SIGKILL
// is both distinguishable and what every other tool on the machine prints.
func exitCodeOf(ps *os.ProcessState) int {
	if ps == nil {
		return ExitCodeUnavailable
	}
	if status, ok := ps.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return ps.ExitCode()
}
