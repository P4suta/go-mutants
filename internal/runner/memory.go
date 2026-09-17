// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"sync/atomic"
	"time"
)

type MemoryEnforcement string

const (
	MemoryEnforcedByKernel  MemoryEnforcement = "kernel"
	MemoryEnforcedBySampler MemoryEnforcement = "sampler"
	MemoryUnenforced        MemoryEnforcement = "none"
)

func MemoryBound() MemoryEnforcement {
	switch {
	case kernelBoundsMemory:
		return MemoryEnforcedByKernel
	case memorySamplingSupported:
		return MemoryEnforcedBySampler
	default:
		return MemoryUnenforced
	}
}

const MemorySampleInterval = 100 * time.Millisecond

func MemoryBoundSupported() bool { return memorySamplingSupported && memorySamplingAvailable() }

func exceededAtExit(kernelBounded bool, limit, peak int64, exitCode int, killed bool) bool {
	return kernelBounded && !killed && limit > 0 && peak > limit && exitCode != 0
}

type memoryWatchdog struct {
	peak atomic.Int64

	exceeded chan struct{}

	done    chan struct{}
	stopped chan struct{}
}

var memoryWarmUp = []time.Duration{
	1 * time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 5 * time.Millisecond, 7 * time.Millisecond,
	10 * time.Millisecond, 15 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond,
}

type sampleOutcome int

const (
	sampleTaken sampleOutcome = iota
	sampleUnanswered
	sampleExceeded
)

func watchMemory(sup supervisor, limit int64) *memoryWatchdog {
	w := &memoryWatchdog{
		exceeded: make(chan struct{}),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	if w.take(sup, limit, false) == sampleExceeded {
		close(w.stopped)
		return w
	}
	go w.sample(sup, limit)
	return w
}

func (w *memoryWatchdog) take(sup supervisor, limit int64, thorough bool) sampleOutcome {
	used, ok := sup.usedMemory(thorough)
	if !ok {
		return sampleUnanswered
	}
	w.record(used)
	if limit > 0 && used > limit {
		close(w.exceeded)
		return sampleExceeded
	}
	return sampleTaken
}

func (w *memoryWatchdog) sample(sup supervisor, limit int64) {
	defer close(w.stopped)

	answered := false
	started := time.Now()
	for _, at := range memoryWarmUp {
		select {
		case <-w.done:
			return
		case <-time.After(time.Until(started.Add(at))):
		}
		switch w.take(sup, limit, false) {
		case sampleExceeded:
			return
		case sampleTaken:
			answered = true
		case sampleUnanswered:
		}
	}
	if !answered {
		return
	}

	ticker := time.NewTicker(MemorySampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
		}
		if w.take(sup, limit, true) != sampleTaken {
			return
		}
	}
}

func (w *memoryWatchdog) record(used int64) {
	for {
		seen := w.peak.Load()
		if used <= seen {
			return
		}
		if w.peak.CompareAndSwap(seen, used) {
			return
		}
	}
}

func (w *memoryWatchdog) stop() {
	if w == nil {
		return
	}
	close(w.done)
	<-w.stopped
}

func (w *memoryWatchdog) observedPeak() int64 {
	if w == nil {
		return 0
	}
	return w.peak.Load()
}

func (w *memoryWatchdog) exceededC() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.exceeded
}
