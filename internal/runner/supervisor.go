// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

// TerminationGrace is how long a POSIX process group is given to shut down
// after SIGTERM before it is sent SIGKILL. Windows has no equivalent phase:
// TerminateJobObject is immediate by construction.
//
// Two seconds is enough for a Go test binary to run its deferred cleanup and
// flush, and short enough that a run of thousands of mutants does not spend a
// visible fraction of its wall clock waiting for processes that are never
// going to answer.
const TerminationGrace = 2 * time.Second

// IODrainGrace bounds how long [Run] waits for the output pipe to reach EOF
// after the child itself has exited. It is Cmd.WaitDelay, and it exists
// because a descendant that outlived its parent still holds the write end: a
// zero delay means "read until every writer closes", which for an orphaned
// daemon is never.
const IODrainGrace = 2 * time.Second

// supervision counters. They exist only so the tests can prove that the
// platform path they believe is taken really is taken — a Windows suite that
// silently fell back to killing one process would otherwise pass every
// behavioural assertion on a machine fast enough to hide it.
//
// They are observability, never input: nothing in this package reads them, so
// concurrent runs stay independent of each other.
var (
	supervisionAdopted    atomic.Int64
	supervisionTerminated atomic.Int64
)

// supervisor owns the process tree of exactly one child.
//
// The lifecycle is fixed and the order matters: newSupervisor is called before
// the child exists, so that a machine which cannot supervise is discovered
// before anything is running; configure prepares the command; adopt takes
// ownership of the started process and is the fail-closed gate; terminate ends
// the tree; release frees the handles.
type supervisor interface {
	// configure sets whatever the command needs in order to be supervisable,
	// before it starts. It cannot fail.
	configure(cmd *exec.Cmd)

	// adopt takes ownership of the freshly started process tree. Returning an
	// error means the tree is not owned, and [Run] then kills the child rather
	// than running it unsupervised.
	adopt(cmd *exec.Cmd) error

	// terminate ends every process in the tree. The exited channel is closed
	// once the child has been reaped, which lets a platform escalate from a
	// polite signal to a fatal one only when the polite one was ignored.
	//
	// grace is how long the tree is given to shut down of its own accord before
	// the fatal signal, and a grace of zero or less means none at all. The
	// caller chooses because the caller knows what the grace would buy: a
	// timed-out test binary's deferred cleanup and flushed output is the
	// evidence for why it timed out, and is worth two seconds; a tree over its
	// memory bound has already had its peak measured and is spending those two
	// seconds allocating. Windows has no polite phase to skip —
	// TerminateJobObject is immediate by construction — so the argument is
	// ignored there.
	//
	// It is best effort by contract: a process that is already gone, a handle
	// the kernel has invalidated, and a pid the caller no longer owns are all
	// ordinary outcomes, not failures to report.
	terminate(exited <-chan struct{}, grace time.Duration)

	// residentMemory reports how much memory the whole tree is holding right
	// now, in bytes, while it is still running. It is what [memoryWatchdog]
	// samples, and it belongs to the supervisor because the tree does: a
	// process group id on POSIX, a job handle on Windows, and nothing this
	// package could reconstruct from a pid alone.
	//
	// The false return means this platform has no live reading at all, not that
	// the tree is empty — an empty tree is zero and true. It is a statement
	// about the machine rather than about the moment, so a sampler that sees it
	// stops rather than retrying.
	residentMemory() (int64, bool)

	// peakMemory reports the highest the tree reached, in bytes. It is called
	// once, after the child has been reaped and before release, and it is given
	// the exit status because one platform keeps the answer there and the other
	// keeps it in the supervisor — a caller that had to know which would be a
	// caller with a platform branch in it.
	peakMemory(ps *os.ProcessState) (int64, bool)

	// release frees the supervisor's operating-system resources. It runs on
	// every path out of [Run], including the failure paths.
	release()
}
