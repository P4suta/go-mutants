// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package runner starts one child process, supervises its whole process tree,
// and returns what happened.
//
// It is the only place in go-mutants that creates processes, and it is
// deliberately small: no worker pool, no retry, no shell. A mutation run
// executes thousands of test binaries, and every one of them is a program
// written by somebody else that may hang, may fork, and may leave descendants
// behind. The single job of this package is that when go-mutants decides a
// child's time is up, nothing survives it.
//
// # No shell, ever
//
// [Spec.Argv] is an argument vector, not a command line. It is handed to
// os/exec unchanged and is never expanded, split, quoted, or interpreted by a
// shell. Test commands come from `.go-mutants.toml` and from `-- <test argv>`
// on the command line, both of which are captured verbatim, so a path with a
// space or a `$` in it is a path with a space or a `$` in it.
//
// # Killing a tree, not a process
//
// Killing only the process that was started is the bug this package exists to
// avoid: a `go test` binary that spawns a database, a helper server, or another
// `go` invocation leaves those children holding ports, file locks, and — the
// reason it bites here specifically — the pipe this package captures output
// through. The two platforms get there differently, and the difference is not
// cosmetic:
//
//   - Windows supervision is exact. A Job Object with
//     JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE is created before the child starts,
//     the child is assigned to it before it has run a single instruction, and
//     every process it later creates joins the job automatically.
//     TerminateJobObject ends the whole job at once, and closing the last
//     handle to the job kills whatever is still in it, so even a panicking
//     supervisor cannot leak the tree.
//   - POSIX supervision is best effort. The child gets its own process group
//     via SysProcAttr.Setpgid, and a kill is sent to the negated group id:
//     SIGTERM first, then SIGKILL after a grace period. A descendant that calls
//     setpgid or setsid for itself leaves that group and is no longer covered.
//     Nothing in the POSIX API can prevent that; the Job Object can.
//
// Fail-closed means fail-closed. If the job object cannot be created,
// configured, or assigned, [Run] kills the child it has already started and
// returns a GOM7201 error rather than running a test binary it cannot
// guarantee it can kill. An unsupervised child that hangs is a hung CI job.
//
// # Closing the assign-after-start window
//
// Windows offers no way, through os/exec, to create a process already inside a
// job, so a naive supervisor starts the child and then assigns it — and
// between those two calls a child that forks produces a grandchild outside the
// job, which then survives every kill.
//
// That window is not a formality. Measured here, on a machine running sixteen
// concurrent children — which is what a mutation run on eight cores looks like
// — the gap between CreateProcess and AssignProcessToJobObject ran from two to
// fifty milliseconds, with outliers past a hundred and eighty. Cmd.Start does
// real work after the process exists, and the Go scheduler is free to park the
// goroutine in the middle of it. A Go test binary boots and can spawn helpers
// well inside that gap. Left open, the window breaks this package's central
// promise silently, and does it most often on the loaded machine where the
// promise matters most.
//
// So the window is closed rather than tolerated. The child is created with
// CREATE_SUSPENDED, assigned to the job while not one of its instructions has
// run, and only then resumed. There is nothing left to race: a process that
// has never executed cannot have forked.
//
// Resuming costs one undocumented call, ntdll's NtResumeProcess, and that is a
// deliberate trade. The supported way to resume a process is to snapshot every
// thread on the machine, filter the one that belongs to this child, and resume
// it — a system-wide walk per mutant, thousands of times per run, for a
// process with a single thread. NtResumeProcess has been in ntdll since
// Windows NT and is what every process suspender on the platform uses. If it
// cannot be found or fails, [Run] kills the child and reports a GOM7201
// failure: a suspended child that is never resumed would hang a run rather
// than fail it, which is the one outcome worse than not running.
//
// POSIX needs none of this. Setpgid takes effect during fork, before the child
// runs at all, so the process group is established rather than assigned.
//
// Since Windows 8 nested jobs are supported, so a go-mutants run inside a CI
// agent that already put the shell in a job still gets its own.
//
// # What comes back
//
// [Result] separates three things that are easy to conflate:
//
//   - A child that ran and failed is not an error. [Result.Err] is reserved
//     for failures to start or supervise the process; a non-zero
//     [Result.ExitCode] is a fact about the test, and every caller of a
//     mutation runner needs to see it as data rather than as a failure.
//   - A child that was killed reports [Result.ExitCode] as
//     [ExitCodeUnavailable]. The status a terminated tree leaves behind
//     disagrees between platforms — a Windows job termination code against a
//     POSIX 128+SIGKILL — and none of it says anything the caller did not
//     already know from having asked for the kill.
//   - A timeout and a cancellation are distinguished by [Result.TimedOut] and
//     nothing else. On cancellation TimedOut is false, ExitCode is
//     [ExitCodeUnavailable], Err is nil, and the caller tells the two apart by
//     checking its own ctx.Err(). Keeping cancellation out of Err is what lets
//     the engine drain a Ctrl-C without every in-flight mutant turning into a
//     reported infrastructure error.
//
// Output is combined stdout and stderr in the order the child wrote it, both
// wired to a single pipe so the interleaving is the child's own. It is capped
// by keeping the *tail*: when a test explodes, the assertion failure is at the
// end and the megabytes of progress logs are at the front. len(Output) never
// exceeds the effective [Spec.OutputLimit], truncation marker included; see
// [OutputTruncatedPrefix].
//
// # Stragglers after a normal exit
//
// If the child exits but a descendant it left behind still holds the output
// pipe, reading to EOF would block forever. Cmd.WaitDelay bounds that wait, so
// a normal exit costs at most [IODrainGrace] extra before the pipes are forced
// closed and the captured output is returned as it stands.
//
// After such an exit this package does *not* sweep the process group on POSIX.
// It could: one kill(-pgid, SIGKILL) would clean up. But a mutation run churns
// through thousands of processes, the child has already been reaped by then, so
// its pid is free for the kernel to hand out again, and a recycled pid that
// happens to be a process group leader would make go-mutants kill something
// that was never part of this run. Losing a stray descendant is a leak; killing
// the user's shell is a catastrophe. Windows has no such trade-off, because the
// job object identifies the tree by ownership rather than by number, which is
// why closing the job handle sweeps there and nothing sweeps here.
//
// # Concurrency
//
// [Run] is safe to call from many goroutines at once and shares no mutable
// state between calls: every call gets its own supervisor, its own capture
// buffer, and its own timer. Two concurrent runs cannot observe or disturb each
// other, which is what makes an N-worker execution phase reproducible.
//
// # Platform support
//
// The supervisors cover windows and the `unix` build tag (linux, darwin, the
// BSDs, solaris, illumos). Anything else fails to compile on purpose: a
// platform without one of the two mechanisms would have to run unsupervised,
// and this package would rather not build than pretend.
package runner
