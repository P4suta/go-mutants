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
	// The pace is the point, and it is set against two lines rather than one.
	// A sampler cannot see a peak that lived entirely between two of its ticks,
	// so a helper that grew as fast as the machine allows would be testing the
	// scheduler; and on Windows the kernel's own line sits a quarter above the
	// sampler's, so a helper that grew by more than that quarter per tick would
	// cross both between two samples and be killed by the kernel before
	// anything read a number above the bound. Four mebibytes every twenty
	// milliseconds is twenty per tick, which is inside the quarter of every
	// bound these tests set.
	//
	// That the arithmetic is close is why [runner.Result.MemoryExceeded] is also
	// set from the final peak: a helper the sampler misses is still a helper
	// that went over, and the tests below hold whichever path reports it.
	hogStep      = 4 << 20
	hogStepDelay = 20 * time.Millisecond

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

	// A child that lives long enough to be sampled more than once: on Linux
	// the sampler is the only measurement that is the child's own (see
	// [runner.Result.PeakMemory]), and a helper that exits at once would be
	// measured at whatever its first milliseconds happened to hold.
	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "sleep", milliseconds(3*runner.MemorySampleInterval)),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("measuring the helper's footprint: Err = %v, want nil", result.Err)
	}
	if result.PeakMemory <= 0 {
		t.Fatalf("PeakMemory = %d for a measured child that lived %s, want the peak this platform observed",
			result.PeakMemory, 3*runner.MemorySampleInterval)
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
// not "bounded at zero" — and that a run is measured whether or not it is
// bounded.
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
		t.Error("PeakMemory = 0: an unbounded run is still a sampled one")
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
	// Two things can enforce this bound and they end the tree differently, so
	// the assertion is on the contract rather than on whichever one won today.
	//
	// The sampler reads a number above the limit and kills the tree, and a tree
	// this package killed reports [runner.ExitCodeUnavailable] like any other.
	// Where the platform carries a kernel line of its own — Windows'
	// JOB_OBJECT_LIMIT_JOB_MEMORY, a quarter above the sampler's — a tree that
	// crosses both between two samples has its next commit refused instead: the
	// Go runtime dies of it with a status of its own, nothing killed anything,
	// and [runner.Result.MemoryExceeded] is set from the final peak by
	// exceededAtExit, which keeps the child's exit code on purpose. That code
	// was 2 on the windows-latest run that found this assertion.
	//
	// Both are the bound doing its job, and what they have in common is the
	// pair above — MemoryExceeded set and TimedOut clear — plus a status that
	// is not success.
	killedByUs := result.ExitCode == runner.ExitCodeUnavailable
	endedByTheKernelsLine := runner.KernelBoundsMemory() && result.ExitCode != 0
	if !killedByUs && !endedByTheKernelsLine {
		t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d) for a tree this package killed, or a "+
			"non-zero status for one the kernel's own line ended (this platform carries such a line: %v)",
			result.ExitCode, runner.ExitCodeUnavailable, runner.KernelBoundsMemory())
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
//
// The same two enforcement paths as
// [TestRunKillsATreeThatExceedsItsMemoryLimit] end this one differently, and
// here the difference is not only the exit code. The sampler kills the whole
// tree and the run ends early with MemoryExceeded. The kernel's own line, where
// there is one, refuses the *grandchild's* next commit — and this test's direct
// child only sleeps and then exits 0, so `exceededAtExit`, which needs a
// non-zero status to attribute anything to the bound, cannot report it. The
// kill is real and invisible from here.
//
// So the claim this test is named for is asserted from the peak, which holds on
// both paths: the number came from the tree rather than from the sleeping
// child. The early ending is asserted only on the path where it means the bound
// stopped something, and the other path is held to the one thing left that
// tells enforcement from nothing at all — the grandchild did not get its whole
// budget.
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
	// The claim itself, and it does not depend on which path enforced the
	// bound: a peak above two footprints is a peak that summed a tree, since
	// neither the sleeping child nor a just-started grandchild costs more than
	// one.
	if treeOnly := 2 * footprint; result.PeakMemory <= treeOnly {
		t.Errorf("PeakMemory = %d, no more than the two footprints (%d) the child and its grandchild "+
			"cost before either grew: the bound measured a process rather than the tree",
			result.PeakMemory, treeOnly)
	}
	switch {
	case result.MemoryExceeded:
		if elapsed >= childIdle {
			t.Errorf("Run took %v; the child's own %v sleep ended it rather than the bound", elapsed, childIdle)
		}
	case runner.KernelBoundsMemory():
		// The kernel refused the grandchild's commit and the sleeping child
		// carried none of that in its status. What separates that from a bound
		// nothing enforced is how far the tree got: a grandchild nothing
		// stopped grows to the helper's whole budget.
		if unbounded := footprint + hogTotal; result.PeakMemory >= unbounded {
			t.Errorf("PeakMemory = %d, which is a footprint plus the helper's whole %d budget: the "+
				"grandchild ran out of steps rather than out of budget, so nothing bounded the tree",
				result.PeakMemory, hogTotal)
		}
	default:
		t.Fatalf("MemoryExceeded = false with a %d byte bound and a grandchild growing past it, on a "+
			"platform whose only enforcement is the sampler: the bound measured the child alone; "+
			"output: %s", limit, result.Output)
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

// TestABurstThatOutrunsTheSamplerIsStillMeasured pins what a bound does about
// the tree it cannot sample.
//
// This helper allocates its whole budget in one burst and exits, which takes
// well under a single [runner.MemorySampleInterval]; the sampler's first tick
// is one interval in, so in the ordinary case it takes no sample at all. What
// must be true either way is that the *peak* comes back — a run that measured
// nothing about a child that went over its budget could not tell anybody it
// had.
//
// Whether that is also reported as a memory *kill* is the platform's answer,
// and the two are deliberately different questions. On Windows the kernel
// carries a line of its own above the sampler's, so a burst this fast has its
// commit refused and dies of the bound; elsewhere nothing but the sampler can
// end a tree for its memory, so a burst that finished finished — the peak is a
// fact about the run and not the cause of its ending, and calling it a kill
// would report every suite that spikes between two ticks as killed by the
// bound.
func TestABurstThatOutrunsTheSamplerIsStillMeasured(t *testing.T) {
	t.Parallel()
	requireEnforcement(t)

	footprint := helperFootprint(t)
	limit := footprint + boundHeadroom

	result := runner.Run(t.Context(), runner.Spec{
		Argv:        append(helperCommand(t, "burst"), strconv.Itoa(hogTotal)),
		Env:         helperEnviron(),
		Timeout:     30 * time.Second,
		MemoryLimit: limit,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.TimedOut {
		t.Fatal("TimedOut = true for a helper that allocates and exits")
	}
	// Only a platform whose accounting is the child's own can measure a burst
	// the sampler missed; on Linux the sampler is the only witness, so a burst
	// that ended between two ticks is honestly reported at whatever it saw.
	if runner.AccountedPeakBelongsToTheChild() && result.PeakMemory <= limit {
		t.Errorf("PeakMemory = %d, want above the %d bound: the helper did not grow far enough to prove anything",
			result.PeakMemory, limit)
	}
	// Where nothing but the sampler can end a tree, a burst it missed is a run
	// that completed. Reporting it as a kill is the false kill this rule was
	// narrowed to avoid.
	if !runner.KernelBoundsMemory() && result.MemoryExceeded && result.ExitCode == 0 {
		t.Error("a helper that allocated and exited cleanly was reported as killed by its bound")
	}
}

// TestPeakMemoryBelongsToTheChildAndNotTheParent pins the one property a
// measurement has to have before a bound can be derived from it: it is about
// the child.
//
// On Linux the kernel's ru_maxrss for a child started with clone(CLONE_VM|
// CLONE_VFORK) — which is how os/exec starts every process — begins at the
// parent's own high-water mark, so a `/bin/true` started by a process that once
// held a gibibyte reports a gibibyte. Measured on this repository's own machine
// before this test existed: 2 MiB before the parent touched memory, 1 GiB after.
// A baseline peak taken that way is the go-mutants process's size, not the
// suite's, and four times it is a bound that stops nothing.
//
// So the test process makes itself large first, then measures a child that
// does nothing but wait long enough to be sampled, and requires the child's
// peak to be a fraction of what the parent holds.
func TestPeakMemoryBelongsToTheChildAndNotTheParent(t *testing.T) {
	t.Parallel()

	const parentGrowth = 256 << 20
	ballast := make([]byte, parentGrowth)
	for i := 0; i < len(ballast); i += 4096 {
		ballast[i] = 1
	}
	defer func() { _ = ballast[0] }()

	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "sleep", milliseconds(3*runner.MemorySampleInterval)),
		Env:  helperEnviron(),
	})
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("Err = %v, ExitCode = %d, want a clean exit; output: %s", result.Err, result.ExitCode, result.Output)
	}
	if result.PeakMemory <= 0 {
		t.Fatalf("PeakMemory = %d for a measured child that lived %s, want the peak this platform observed",
			result.PeakMemory, 3*runner.MemorySampleInterval)
	}
	if result.PeakMemory >= parentGrowth/2 {
		t.Errorf("PeakMemory = %d for a child that only slept, while its parent holds %d: the measurement "+
			"is the parent's high-water mark, not the child's", result.PeakMemory, parentGrowth)
	}
}

// TestAnUnboundedRunIsStillSampled pins that measuring and bounding are
// separate things: a run that names no limit is never killed and never reports
// MemoryExceeded, and still comes back with a peak, because every run is
// sampled and only a bounded one can trip.
func TestAnUnboundedRunIsStillSampled(t *testing.T) {
	t.Parallel()

	const grown = 64 << 20
	result := runner.Run(t.Context(), runner.Spec{
		Argv: append(helperCommand(t, "hog"), hogArgs(grown)...),
		Env:  helperEnviron(),
	})
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("Err = %v, ExitCode = %d, want a clean exit; output: %s", result.Err, result.ExitCode, result.Output)
	}
	if result.MemoryExceeded {
		t.Error("MemoryExceeded = true for a run that named no limit")
	}
	if result.PeakMemory < grown {
		t.Errorf("PeakMemory = %d, want at least the %d bytes the child made resident", result.PeakMemory, grown)
	}
}
