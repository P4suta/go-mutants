// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// A memory bound is a budget, and every number in these tests is expressed as
// headroom above what this very test binary costs to start rather than as an
// absolute. A coverage-instrumented, race-detecting Go test binary on somebody
// else's machine is several times the size of the same binary here, and a
// literal "64 MiB" would be a bound the helper trips by merely booting on one
// machine and never reaches on another. [helperFootprint] measures it instead.
const (
	// hogStep is how much a helper adds to its resident set per step, and
	// hogStepDelay how long it waits between steps.
	//
	// The pause is the point. A sampler cannot see a peak that lived entirely
	// between two of its ticks, so the step delay is set well above
	// [runner.MemorySampleInterval]: a helper that grew as fast as the machine
	// allows would be testing the scheduler rather than the bound.
	hogStep      = 8 << 20
	hogStepDelay = 25 * time.Millisecond

	// hogTotal is how far a helper grows when nothing stops it. It is the
	// ceiling on what these tests can cost the machine, and it is deliberately
	// far below the headroom any of them allows, so that every one of them is
	// decided by a bound rather than by the helper running out of steps.
	hogTotal = 128 << 20

	// boundHeadroom is how much growth a bounded helper is allowed above the
	// footprint before the bound trips. It is a quarter of hogTotal, so a
	// helper that is not stopped is stopped by nothing rather than by luck.
	boundHeadroom = 32 << 20
)

// helperFootprint is the peak resident memory of a helper process that does
// nothing but exit.
//
// It is measured rather than assumed because it is the one number in this file
// that belongs to the machine: build tags, the race detector and coverage
// instrumentation each multiply it, and a bound written as an absolute would be
// a different test on every platform go-mutants supports.
func helperFootprint(t *testing.T) int64 {
	t.Helper()

	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "exit", "0"),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("measuring the helper's footprint: Err = %v, want nil", result.Err)
	}
	if result.PeakMemory <= 0 {
		t.Fatalf("PeakMemory = %d for a child that ran to completion, want the peak this platform measured",
			result.PeakMemory)
	}
	return result.PeakMemory
}

// hogArgs renders the three arguments every growing helper verb takes.
func hogArgs(total int) []string {
	return []string{strconv.Itoa(total), strconv.Itoa(hogStep), milliseconds(hogStepDelay)}
}

// requireEnforcement skips a test on a platform that can measure a peak but
// cannot watch one as it grows.
//
// It is a skip rather than a build tag so that the reason is stated once, in
// the run's own output, on the platform it applies to.
func requireEnforcement(t *testing.T) {
	t.Helper()

	if !runner.MemoryBoundSupported() {
		t.Skipf("this platform reports a peak but cannot sample a live tree, so Spec.MemoryLimit is not enforced here")
	}
}

// TestRunReportsThePeakResidentMemoryOfTheChild pins the measurement half of
// the memory contract: every child this package runs comes back with what it
// cost, whether or not anybody bounded it.
//
// The two assertions are a pair, and the upper one is not slack. A peak
// reported in the platform's own units rather than in bytes — kibibytes on
// Linux, which is the mistake this measurement invites — would come back a
// thousandfold too small or too large, and only one of the two bounds would
// catch each direction.
func TestRunReportsThePeakResidentMemoryOfTheChild(t *testing.T) {
	t.Parallel()

	const grown = 64 << 20
	footprint := helperFootprint(t)

	result := runner.Run(t.Context(), runner.Spec{
		Argv: append(helperCommand(t, "hog"), hogArgs(grown)...),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; output: %s", result.ExitCode, result.Output)
	}
	if result.PeakMemory < grown {
		t.Errorf("PeakMemory = %d, want at least the %d bytes the child made resident", result.PeakMemory, grown)
	}
	if ceiling := footprint + 16*int64(grown); result.PeakMemory > ceiling {
		t.Errorf("PeakMemory = %d, want below %d: a child that made %d bytes resident cannot cost that much",
			result.PeakMemory, ceiling, grown)
	}
	if result.MemoryExceeded {
		t.Error("MemoryExceeded = true for a run with no MemoryLimit")
	}
}

// TestRunWithoutAMemoryLimitNeverReportsExceeded pins that the bound is opt-in,
// the way the timeout is: a zero [runner.Spec.MemoryLimit] means unbounded and
// not "bounded at zero".
func TestRunWithoutAMemoryLimitNeverReportsExceeded(t *testing.T) {
	t.Parallel()

	result := runner.Run(t.Context(), runner.Spec{
		Argv: append(helperCommand(t, "hog"), hogArgs(hogTotal)...),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.MemoryExceeded {
		t.Errorf("MemoryExceeded = true with MemoryLimit unset; output: %s", result.Output)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0: nothing should have stopped it", result.ExitCode)
	}
	if result.PeakMemory <= 0 {
		t.Error("PeakMemory = 0: an unbounded run is still a measured one")
	}
}

// TestRunKillsATreeThatExceedsItsMemoryLimit is the enforcement half, and it is
// the test the whole feature exists for: a child that allocates without bound
// is stopped by the bound rather than by the deadline, and the tree goes with
// it.
//
// The timeout is present and generous on purpose. It is the control: if the
// bound did nothing, this run would still end — as a timeout, many seconds
// later — and the assertions below would say which of the two stopped it rather
// than hanging the suite.
func TestRunKillsATreeThatExceedsItsMemoryLimit(t *testing.T) {
	t.Parallel()
	requireEnforcement(t)

	const timeout = 30 * time.Second
	footprint := helperFootprint(t)
	sentinel := filepath.Join(t.TempDir(), "sentinel")

	start := time.Now()
	args := append([]string{sentinel, milliseconds(sentinelWriteMargin)}, hogArgs(hogTotal)...)
	result := runner.Run(t.Context(), runner.Spec{
		Argv:        append(helperCommand(t, "hogtree"), args...),
		Env:         helperEnviron(),
		Timeout:     timeout,
		MemoryLimit: footprint + boundHeadroom,
	})
	elapsed := time.Since(start)

	if result.Err != nil {
		t.Fatalf("Err = %v, want nil: a bound is a result, not a failure to run", result.Err)
	}
	if !result.MemoryExceeded {
		t.Fatalf("MemoryExceeded = false (exit %d, timed out %v) with a %d byte bound; output: %s",
			result.ExitCode, result.TimedOut, footprint+boundHeadroom, result.Output)
	}
	if result.TimedOut {
		t.Error("TimedOut = true: the bound must reach a runaway before the deadline does, which is the point of it")
	}
	if result.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d), as for any tree this package killed",
			result.ExitCode, runner.ExitCodeUnavailable)
	}
	if result.OK() {
		t.Error("OK() = true for a run the bound stopped")
	}
	if result.PeakMemory < footprint {
		t.Errorf("PeakMemory = %d, want at least the footprint %d: a killed process still reports what it reached",
			result.PeakMemory, footprint)
	}
	if elapsed >= timeout {
		t.Errorf("Run took %v, which is the whole %v timeout: the bound did not stop it early", elapsed, timeout)
	}

	// The grandchild is the proof the *tree* went. It writes only after the run
	// has certainly ended, so the file's absence cannot mean "we looked too
	// early", and the child announced it on the captured output, so it cannot
	// mean "it never started" either.
	if err := waitForFile(t, sentinel, sentinelWriteMargin+killSettle); err == nil {
		t.Error("the grandchild wrote its sentinel: the bound killed the process it measured and not the tree")
	}
}

// TestTheMemoryBoundSeesTheWholeTreeAndNotOnlyTheChild is the same claim from
// the other side, and it is the one an implementation that read a single
// process's resident size would pass everything else and fail here.
//
// The direct child does nothing but sleep, so its own footprint is nowhere near
// the bound; all the growth belongs to a grandchild. A bound that trips is a
// bound that summed the tree.
func TestTheMemoryBoundSeesTheWholeTreeAndNotOnlyTheChild(t *testing.T) {
	t.Parallel()
	requireEnforcement(t)

	const (
		timeout   = 30 * time.Second
		childIdle = 8 * time.Second
	)
	footprint := helperFootprint(t)

	// Two helper processes are alive at once, so the bound clears twice the
	// footprint before the headroom starts. That is also what makes the claim
	// exact: the sleeping child alone cannot reach this number.
	limit := 2*footprint + boundHeadroom

	start := time.Now()
	args := append(hogArgs(hogTotal), milliseconds(childIdle))
	result := runner.Run(t.Context(), runner.Spec{
		Argv:        append(helperCommand(t, "hogchild"), args...),
		Env:         helperEnviron(),
		Timeout:     timeout,
		MemoryLimit: limit,
	})
	elapsed := time.Since(start)

	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if !result.MemoryExceeded {
		t.Fatalf("MemoryExceeded = false with a %d byte bound and a grandchild growing past it: "+
			"the bound measured the child alone; output: %s", limit, result.Output)
	}
	if elapsed >= childIdle {
		t.Errorf("Run took %v; the child's own %v sleep ended it rather than the bound", elapsed, childIdle)
	}
}

// TestRunRecordsPeakMemoryInTheExecEvent pins the recording half. What a command
// cost is a fact about the execution, so it belongs on the `exec` record beside
// the duration and the exit code, and it is recorded for every command rather
// than only for the bounded ones.
func TestRunRecordsPeakMemoryInTheExecEvent(t *testing.T) {
	t.Parallel()

	recorder, sink := newRecording(t)
	const grown = 32 << 20
	result := runner.Run(t.Context(), runner.Spec{
		Argv:  append(helperCommand(t, "hog"), hogArgs(grown)...),
		Env:   helperEnviron(),
		Trace: recorder,
		Kind:  trace.ExecKindMutantRun,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}

	rec := onlyExec(t, sink).Exec
	if rec.PeakMemoryBytes != result.PeakMemory {
		t.Errorf("peak_memory_bytes = %d, want the result's PeakMemory %d", rec.PeakMemoryBytes, result.PeakMemory)
	}
	if rec.PeakMemoryBytes < grown {
		t.Errorf("peak_memory_bytes = %d, want at least the %d bytes the child made resident", rec.PeakMemoryBytes, grown)
	}
}

// TestAMemoryKillDoesNotWaitForATreeToShutDownPolitely is the difference
// between a bound and a bound that arrives too late.
//
// A timed-out tree is given [runner.TerminationGrace] after SIGTERM so a test
// binary can flush the output that is the evidence for *why* it timed out. A
// tree over its memory bound has no such evidence to flush — the peak was
// already measured, and the output is whatever the suite had printed — and it
// has something a hung one does not: an appetite. A process appending at the
// rate this helper does adds tens of megabytes per tick, so two seconds of
// politeness is hundreds of megabytes the bound was there to prevent, on a
// machine that is already at its limit. So a memory kill skips the grace.
//
// The helper is deaf to SIGTERM on purpose, which is what makes the claim
// checkable: on the polite path nothing short of SIGKILL stops it, and SIGKILL
// only arrives after the grace. The assertion is on what it reached rather than
// on the wall clock, because a peak is what the difference actually costs and a
// duration on a loaded machine is a second timing race.
func TestAMemoryKillDoesNotWaitForATreeToShutDownPolitely(t *testing.T) {
	t.Parallel()
	requireEnforcement(t)

	const timeout = 30 * time.Second
	footprint := helperFootprint(t)
	limit := footprint + boundHeadroom

	result := runner.Run(t.Context(), runner.Spec{
		Argv:        append(helperCommand(t, "deafhog"), hogArgs(hogTotal)...),
		Env:         helperEnviron(),
		Timeout:     timeout,
		MemoryLimit: limit,
	})

	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if !strings.Contains(string(result.Output), "deaf") {
		t.Fatalf("the helper never announced that it was ignoring SIGTERM, so this proves nothing; output: %s",
			result.Output)
	}
	if !result.MemoryExceeded {
		t.Fatalf("MemoryExceeded = false (exit %d, timed out %v) with a %d byte bound; output: %s",
			result.ExitCode, result.TimedOut, limit, result.Output)
	}
	// One tick of this helper's growth is [hogStep] every [hogStepDelay], which
	// is well under the headroom; the grace it must not have waited for is
	// twenty ticks of it. Twice the bound is the line between those two
	// answers, with room for the machine to be slow.
	if ceiling := 2 * limit; result.PeakMemory > ceiling {
		t.Errorf("PeakMemory = %d against a %d bound, over the %d ceiling: the kill waited out the termination grace "+
			"while the process kept allocating", result.PeakMemory, limit, ceiling)
	}
}
