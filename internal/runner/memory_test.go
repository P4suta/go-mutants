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

const (
	hogStep      = 4 << 20
	hogStepDelay = 20 * time.Millisecond

	hogTotal = 128 << 20

	boundHeadroom = 32 << 20
)

func helperFootprint(t *testing.T) int64 {
	t.Helper()

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

func hogArgs(total int) []string {
	return []string{strconv.Itoa(total), strconv.Itoa(hogStep), milliseconds(hogStepDelay)}
}

func requireEnforcement(t *testing.T) {
	t.Helper()

	if !runner.MemoryBoundSupported() {
		t.Skipf("this platform reports a peak but cannot sample a live tree, so Spec.MemoryLimit is not enforced here")
	}
}

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

	if err := waitForFile(t, sentinel, sentinelWriteMargin+killSettle); err == nil {
		t.Error("the grandchild wrote its sentinel: the bound killed the process it measured and not the tree")
	}
}

func TestTheMemoryBoundSeesTheWholeTreeAndNotOnlyTheChild(t *testing.T) {
	t.Parallel()
	requireEnforcement(t)

	const (
		timeout   = 30 * time.Second
		childIdle = 8 * time.Second
	)
	footprint := helperFootprint(t)

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
	if ceiling := 2 * limit; result.PeakMemory > ceiling {
		t.Errorf("PeakMemory = %d against a %d bound, over the %d ceiling: the kill waited out the termination grace "+
			"while the process kept allocating", result.PeakMemory, limit, ceiling)
	}
}

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
	if runner.AccountedPeakBelongsToTheChild() && result.PeakMemory <= limit {
		t.Errorf("PeakMemory = %d, want above the %d bound: the helper did not grow far enough to prove anything",
			result.PeakMemory, limit)
	}
	if !runner.KernelBoundsMemory() && result.MemoryExceeded && result.ExitCode == 0 {
		t.Error("a helper that allocated and exited cleanly was reported as killed by its bound")
	}
}

func TestPeakMemoryBelongsToTheChildAndNotTheParent(t *testing.T) {
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
