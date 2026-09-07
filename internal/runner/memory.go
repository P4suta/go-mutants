// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"sync/atomic"
	"time"
)

// MemorySampleInterval is how often a bounded run looks at what its process
// tree is using.
//
// A bound enforced by sampling is only ever as tight as its interval: a tree is
// killed at the bound plus about one tick of its own growth, not at the bound.
// A hundred milliseconds is the number that keeps that overshoot small for the
// process this exists to stop. Measured against this package's own hog helper —
// which adds 8 MiB every 25 ms, so about 32 MiB per tick — a bound in the
// hundreds of megabytes is reached at 1.07 to 1.23 times its value. A runaway
// mutant grows faster than that and overshoots further, which is the honest
// statement: the sampler bounds the damage, it does not bound it exactly.
//
// The cost is one `/proc` directory read plus one small file read per process
// on the machine, ten times a second per *bounded* mutant, or one job query on
// Windows. An unbounded run starts no sampler and pays none of it.
//
// It is deliberately not derived from the limit. A sampler whose interval
// depended on the budget would be a second policy nobody asked for, and the
// only thing it could buy is precision about a number that is itself four times
// a measurement.
const MemorySampleInterval = 100 * time.Millisecond

// MemoryBoundSupported reports whether this platform can enforce
// [Spec.MemoryLimit].
//
// Measurement and enforcement are separate capabilities and only one of them is
// universal. [Result.PeakMemory] comes from what the operating system reports about
// a process that has already exited, which every supported platform can do;
// enforcing a bound needs the tree's resident size *while it runs*, which Linux
// answers from /proc and Windows from the job object, and which macOS exposes
// only through libproc — a cgo dependency this repository does not have and will
// not take for a budget.
//
// It is a probe rather than a build tag on the one platform where the tag is
// not the whole answer: Linux enforces a bound by reading /proc, and a
// container or a hardened kernel can leave that unreadable. Assuming the tag
// there would be worse than failing: nothing breaks — the sampler reads no
// number and stops — but the run would have derived a bound, put it in the
// report, and enforced it nowhere, which is exactly the silent hole this whole
// feature exists to close. The answer is asked for once and cached; it cannot
// change under a running process.
//
// A caller that derives a bound is expected to ask, and to say so once rather
// than to hand out a limit that would be quietly ignored. See the engine's
// memory derivation.
func MemoryBoundSupported() bool { return memorySamplingSupported && memorySamplingAvailable() }

// exceededAtExit reports whether the bound is what ended a child that this
// package did not kill.
//
// It is the half of the bound the sampler cannot see, and it exists for one
// platform. On Windows the kernel carries a line of its own a quarter above the
// sampler's (see the job object's JOB_OBJECT_LIMIT_JOB_MEMORY), so a tree that
// grows fast enough crosses both between two samples, has its next commit
// refused, and is killed by the Go runtime with "out of memory" and a non-zero
// status. Nothing sampled it, and without this the run would report an ordinary
// failing test — a mutant `killed` for a reason nobody can find, or a control
// that makes the user's own program look broken.
//
// Three conditions, and each of them is there to keep this from becoming a
// false kill:
//
//   - The platform has a kernel line at all. On POSIX nothing but the sampler
//     can end a tree for its memory, so a peak above the bound there means the
//     tree went over it and *finished anyway* — a fact, and not a cause. Saying
//     otherwise would report every suite that spikes between two ticks as
//     killed by the bound, which on a loaded machine is most of them.
//   - This package did not kill the tree itself. A timeout and a cancellation
//     already have their verdict, and [Result.TimedOut] and
//     [Result.MemoryExceeded] are never both set.
//   - The child exited non-zero. A tree the kernel refused an allocation to does
//     not finish; one that exited cleanly was not stopped by anything, whatever
//     its peak reached.
//
// A caller therefore gets [Result.MemoryExceeded] *and* the child's own exit
// code, and both mean what they say: the tree was over the bound, and this is
// the status the runtime died with when its next allocation was refused.
func exceededAtExit(kernelBounded bool, limit, peak int64, exitCode int, killed bool) bool {
	return kernelBounded && !killed && limit > 0 && peak > limit && exitCode != 0
}

// memoryWatchdog samples a running tree's resident memory, remembers the
// highest it saw, and reports the moment the tree passes its limit.
//
// It exists because rlimits cannot do this job. RLIMIT_AS bounds address space,
// and the Go runtime reserves hundreds of gigabytes of it on a 64-bit machine
// before allocating anything, so any RLIMIT_AS small enough to be a budget kills
// every Go binary at start-up. RLIMIT_DATA is Linux-only and covers the brk
// segment rather than the mappings a Go heap actually lives in. And neither of
// them reaches the child's own children, which is precisely where a `go test`
// binary keeps the memory this package is responsible for.
type memoryWatchdog struct {
	// peak is the highest sample taken, in bytes. It is read after the tree has
	// been reaped, from the goroutine that started the sampler, so it is atomic
	// rather than guarded: there is exactly one writer and one reader and no
	// invariant spanning them.
	peak atomic.Int64

	// exceeded is closed the first and only time a sample passes the limit.
	// Closing rather than sending is what lets [Run]'s select read it beside
	// the timeout and the cancellation, all three of which mean the same thing
	// to the supervisor.
	exceeded chan struct{}

	// done stops the sampler, and stopped is closed once it has gone. Waiting
	// for the second is what makes [memoryWatchdog.stop] a happens-before edge
	// for the peak.
	done    chan struct{}
	stopped chan struct{}
}

// memoryWarmUp is the sampling schedule between the first sample and the
// steady interval: the offsets, from the moment the child is adopted, of the
// next few.
//
// It exists because most children are short. A mutant that fails its first
// assertion is gone in ten milliseconds, and a sampler that first looked a
// hundred milliseconds in would report nothing for the majority of what a run
// executes — which on Linux, where the sample is the only measurement there is,
// would be no peak at all. So the very first sample is taken on the caller's
// goroutine before [watchMemory] returns, and these follow it closely. Early
// samples are small numbers about a process that is still becoming one, and a
// small true number is worth more than a missing one; the bound is checked
// against them too, since a tree that has already passed its limit at 10 ms
// has certainly passed it.
var memoryWarmUp = []time.Duration{10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond}

// watchMemory takes one sample of sup's tree at once, then keeps sampling it:
// on the [memoryWarmUp] schedule, then every [MemorySampleInterval]. A limit of
// zero means the sampler only measures: it remembers the highest sample and
// trips on nothing.
func watchMemory(sup supervisor, limit int64) *memoryWatchdog {
	w := &memoryWatchdog{
		exceeded: make(chan struct{}),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	if !w.take(sup, limit, false) {
		close(w.stopped)
		return w
	}
	go w.sample(sup, limit)
	return w
}

// take is one sample; it reports whether the sampler should go on. A platform
// that cannot answer ends it (see [memoryWatchdog.sample]), and so does the
// sample that passes the limit. A thorough sample may scan the whole process
// table and is what the steady ticks take; the quick ones are for a child's
// first milliseconds.
func (w *memoryWatchdog) take(sup supervisor, limit int64, thorough bool) bool {
	used, ok := sup.usedMemory(thorough)
	if !ok {
		return false
	}
	w.record(used)
	if limit > 0 && used > limit {
		close(w.exceeded)
		return false
	}
	return true
}

// sample is the watchdog's loop.
//
// A platform that cannot answer ends the loop rather than spinning on it. That
// is fail-open, and it is the right way round here for the same reason a
// missing measurement leaves the bound unset: a budget that cannot be measured
// must not become a kill, because the mutant it would kill is indistinguishable
// from the suite that is merely large. Windows keeps a second line of defence
// in any case — the job object carries the limit itself, and the kernel enforces
// it whether or not anybody is looking.
func (w *memoryWatchdog) sample(sup supervisor, limit int64) {
	defer close(w.stopped)

	started := time.Now()
	for _, at := range memoryWarmUp {
		select {
		case <-w.done:
			return
		case <-time.After(time.Until(started.Add(at))):
		}
		if !w.take(sup, limit, false) {
			return
		}
	}

	ticker := time.NewTicker(MemorySampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
		}
		if !w.take(sup, limit, true) {
			return
		}
	}
}

// record keeps the highest sample seen. A sample that is not a new high is
// dropped without a write, which is what keeps a run that never grows from
// paying for the counter ten times a second.
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

// stop ends the sampler and waits for it, so that the peak it observed is
// established before the caller reads it. A nil watchdog is an unbounded run,
// which started no sampler and has nothing to wait for.
func (w *memoryWatchdog) stop() {
	if w == nil {
		return
	}
	close(w.done)
	<-w.stopped
}

// observedPeak is the highest sample the watchdog took, and is zero for a run
// that was never bounded or never grew enough to be sampled.
func (w *memoryWatchdog) observedPeak() int64 {
	if w == nil {
		return 0
	}
	return w.peak.Load()
}

// exceededC is the channel a bound trips on, and is nil — which blocks forever
// in a select, exactly as a nil timer channel does — when there is no bound.
func (w *memoryWatchdog) exceededC() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.exceeded
}
