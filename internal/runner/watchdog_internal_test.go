// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// scriptedSupervisor answers the watchdog with a list of samples instead of a
// process.
//
// The sampler's threshold is the one thing about the bound no behavioural test
// can pin exactly: a real process grows in chunks the sampler sees at whatever
// moment its ticker fires, so "it tripped somewhere around the limit" is all a
// hog helper can prove. Scripting the samples turns the question into
// arithmetic — which sample trips it — and arithmetic is the part that has to
// be the same on every platform, because it is what the kernel's own line on
// Windows has to sit *above*.
type scriptedSupervisor struct {
	mu      sync.Mutex
	samples []int64
	taken   int
	// unmeasurable makes the platform unable to answer at all, which is not the
	// same as an empty tree and has to end the sampler rather than stall it.
	unmeasurable bool
}

func (s *scriptedSupervisor) configure(*exec.Cmd)                      {}
func (s *scriptedSupervisor) adopt(*exec.Cmd) error                    { return nil }
func (s *scriptedSupervisor) terminate(<-chan struct{}, time.Duration) {}
func (s *scriptedSupervisor) release()                                 {}

func (s *scriptedSupervisor) peakMemory(*os.ProcessState) (int64, bool) { return 0, false }

// usedMemory hands back the next scripted sample, and repeats the last one
// once the script runs out so a watchdog that was expected not to trip has
// something to keep reading.
func (s *scriptedSupervisor) usedMemory() (int64, bool) {
	if s.unmeasurable {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.samples) == 0 {
		return 0, true
	}
	i := min(s.taken, len(s.samples)-1)
	s.taken++
	return s.samples[i], true
}

// takenCount is how many samples the watchdog has read.
func (s *scriptedSupervisor) takenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taken
}

// TestTheSamplerTripsOnTheFirstSampleAboveTheRequestedLimit pins the threshold
// the whole bound is expressed in, on every platform.
//
// It matters twice. It is the number a user writes in `test.memory` and expects
// to mean what it says; and it is the number Windows' own kernel limit has to
// sit strictly *above*, because a kernel limit set to the same value caps the
// job's accounting at it — so the sampler would read the limit, never read more
// than the limit, and never trip, while the child died of a failed allocation
// with no explanation anybody could render.
func TestTheSamplerTripsOnTheFirstSampleAboveTheRequestedLimit(t *testing.T) {
	t.Parallel()

	const limit = 1 << 20

	for _, c := range []struct {
		name    string
		samples []int64
		want    bool
	}{
		{"below the limit", []int64{limit / 2, limit - 1}, false},
		{"exactly at the limit", []int64{limit - 1, limit}, false},
		{"one byte over", []int64{limit - 1, limit + 1}, true},
		{"far over", []int64{limit / 2, 4 * limit}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			sup := &scriptedSupervisor{samples: c.samples}
			w := watchMemory(sup, limit)
			t.Cleanup(w.stop)

			select {
			case <-w.exceededC():
				if !c.want {
					t.Errorf("the bound tripped on samples %v, none of which is above %d", c.samples, limit)
				}
				return
			case <-time.After(20 * MemorySampleInterval):
			}
			if c.want {
				t.Errorf("the bound did not trip within %d samples of %v against a limit of %d",
					sup.takenCount(), c.samples, limit)
			}
		})
	}
}

// TestTheSamplerRemembersTheHighestSampleItSaw pins the other half of what the
// watchdog is for: a run that is bounded is also a run that is measured, and
// the number reported is the worst moment rather than the last one.
func TestTheSamplerRemembersTheHighestSampleItSaw(t *testing.T) {
	t.Parallel()

	const limit = 1 << 30
	sup := &scriptedSupervisor{samples: []int64{10, 4096, 512, 1024}}
	w := watchMemory(sup, limit)

	// Long enough for the whole script and then some; nothing here trips, so
	// the sampler runs until it is stopped.
	deadline := time.Now().Add(20 * MemorySampleInterval)
	for time.Now().Before(deadline) && sup.takenCount() < len(sup.samples) {
		time.Sleep(MemorySampleInterval / 4)
	}
	w.stop()

	if got := w.observedPeak(); got != 4096 {
		t.Errorf("observedPeak() = %d, want the highest sample 4096", got)
	}
}

// TestASamplerOnAPlatformThatCannotMeasureStopsRatherThanSpins pins the
// fail-open half. A budget nothing can measure must not become a kill, and a
// sampler that kept asking would be a goroutine per mutant asking a question
// with no answer ten times a second for the life of the run.
func TestASamplerOnAPlatformThatCannotMeasureStopsRatherThanSpins(t *testing.T) {
	t.Parallel()

	sup := &scriptedSupervisor{unmeasurable: true}
	w := watchMemory(sup, 1)
	t.Cleanup(w.stop)

	select {
	case <-w.exceededC():
		t.Fatal("the bound tripped on a platform that reported no measurement at all")
	case <-time.After(5 * MemorySampleInterval):
	}
	if got := w.observedPeak(); got != 0 {
		t.Errorf("observedPeak() = %d, want 0 where nothing could be measured", got)
	}
}

// TestOnlyAKernelLineCanEndATreeTheSamplerDidNotSee pins the retroactive half
// of the bound, and — more importantly — the three things that keep it from
// becoming a false kill.
//
// A sampler ten times a second cannot catch a tree that crosses its line and
// dies inside one tick, and on Windows that is not a corner case: the kernel
// limit sits a quarter above the sampler's, so a child growing fast enough
// crosses both between two samples, has its next commit refused, and the Go
// runtime kills it with "out of memory" and a non-zero status. The sampler saw
// nothing, and reporting an ordinary failing test there means a mutant `killed`
// for a reason nobody can find and a control that makes the user's program look
// broken.
//
// The three refusals matter as much as the acceptance. Measured on a loaded
// machine, ordinary `-cover` test binaries of a three-function fixture peaked at
// 607 MiB against a 256 MiB bound and finished perfectly well — so "the peak was
// over the line" is a fact about a run and not a cause of its ending. A rule
// that read it as a cause reported every one of those as killed by the bound,
// which is exactly the false kill this feature exists to avoid.
func TestOnlyAKernelLineCanEndATreeTheSamplerDidNotSee(t *testing.T) {
	t.Parallel()

	const limit = 1 << 20
	for _, c := range []struct {
		name     string
		kernel   bool
		limit    int64
		peak     int64
		exitCode int
		killed   bool
		want     bool
	}{
		{"a kernel-bounded child that died over its line", true, limit, limit + 1, 2, false, true},
		{"the same child on a platform with no kernel line", false, limit, limit + 1, 2, false, false},
		{"a child that finished cleanly over the line", true, limit, limit * 4, 0, false, false},
		{"a child that failed a test under the line", true, limit, limit / 2, 1, false, false},
		{"a child exactly at the line", true, limit, limit, 2, false, false},
		{"a child whose peak nobody could measure", true, limit, 0, 2, false, false},
		{"an unbounded child that used a lot", true, 0, 1 << 40, 2, false, false},
		{"a tree this package killed", true, limit, limit * 4, ExitCodeUnavailable, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := exceededAtExit(c.kernel, c.limit, c.peak, c.exitCode, c.killed)
			if got != c.want {
				t.Errorf("exceededAtExit(%t, %d, %d, %d, %t) = %t, want %t",
					c.kernel, c.limit, c.peak, c.exitCode, c.killed, got, c.want)
			}
		})
	}
}

// TestTheFirstSampleIsTakenBeforeWatchMemoryReturns pins why a child that
// lives ten milliseconds still reports a peak: the sampler's first look is
// taken on the caller's goroutine, before the child has had a chance to
// finish, and only the later ones wait.
func TestTheFirstSampleIsTakenBeforeWatchMemoryReturns(t *testing.T) {
	t.Parallel()

	sup := &scriptedSupervisor{samples: []int64{7 << 20}}
	w := watchMemory(sup, 0)
	if got := w.observedPeak(); got != 7<<20 {
		t.Errorf("observedPeak right after watchMemory = %d, want the first sample %d, taken synchronously",
			got, 7<<20)
	}
	w.stop()
}
