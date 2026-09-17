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

type scriptedSupervisor struct {
	mu                sync.Mutex
	samples           []int64
	taken             int
	unmeasurable      bool
	unanswerableFirst int
	accounted         int64
}

func (s *scriptedSupervisor) configure(*exec.Cmd)                      {}
func (s *scriptedSupervisor) adopt(*exec.Cmd) error                    { return nil }
func (s *scriptedSupervisor) terminate(<-chan struct{}, time.Duration) {}
func (s *scriptedSupervisor) release()                                 {}

func (s *scriptedSupervisor) peakMemory(*os.ProcessState) (int64, bool) {
	return s.accounted, s.accounted > 0
}

func (s *scriptedSupervisor) usedMemory(bool) (int64, bool) {
	if s.unmeasurable {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unanswerableFirst > 0 {
		s.unanswerableFirst--
		s.taken++
		return 0, false
	}
	if len(s.samples) == 0 {
		return 0, true
	}
	i := min(s.taken, len(s.samples)-1)
	s.taken++
	return s.samples[i], true
}

func (s *scriptedSupervisor) takenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taken
}

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

func TestTheSamplerRemembersTheHighestSampleItSaw(t *testing.T) {
	t.Parallel()

	const limit = 1 << 30
	sup := &scriptedSupervisor{samples: []int64{10, 4096, 512, 1024}}
	w := watchMemory(sup, limit)

	deadline := time.Now().Add(20 * MemorySampleInterval)
	for time.Now().Before(deadline) && sup.takenCount() < len(sup.samples) {
		time.Sleep(MemorySampleInterval / 4)
	}
	w.stop()

	if got := w.observedPeak(); got != 4096 {
		t.Errorf("observedPeak() = %d, want the highest sample 4096", got)
	}
}

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

func TestTheAccountedPeakIsConsultedOnlyWhereItBelongsToTheChild(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name      string
		sampled   int64
		accounted int64
	}{
		{"accounting above the samples", 10 << 20, 300 << 20},
		{"accounting below the samples", 50 << 20, 10 << 20},
		{"no accounting at all", 10 << 20, 0},
		{"nothing at all", 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sup := &scriptedSupervisor{accounted: c.accounted}
			w := &memoryWatchdog{}
			w.record(c.sampled)
			want := c.sampled
			if accountedPeakBelongsToTheChild {
				want = max(c.sampled, c.accounted)
			}
			if got := peakOf(sup, nil, w); got != want {
				t.Errorf("peakOf(sampled %d, accounted %d) = %d, want %d", c.sampled, c.accounted, got, want)
			}
		})
	}
}

func TestAChildNotVisibleYetDoesNotEndTheSamplerForever(t *testing.T) {
	t.Parallel()

	sup := &scriptedSupervisor{unanswerableFirst: 1, samples: []int64{9 << 20}}
	w := watchMemory(sup, 0)
	t.Cleanup(w.stop)

	deadline := time.After(5 * MemorySampleInterval)
	for w.observedPeak() == 0 {
		select {
		case <-deadline:
			t.Fatalf("observedPeak() = 0 after %d samples; a child that was not in /proc for the"+
				" opening look was treated as a platform that can never answer", sup.takenCount())
		case <-time.After(time.Millisecond):
		}
	}
	if got := w.observedPeak(); got != 9<<20 {
		t.Errorf("observedPeak() = %d, want the first sample the platform could answer %d", got, 9<<20)
	}
}
