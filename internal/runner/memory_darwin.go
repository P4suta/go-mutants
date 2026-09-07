// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

// memorySamplingSupported: macOS can say what a process cost once it is gone
// and not what it is costing now, so a bound is measured here and not enforced.
//
// The live reading exists — proc_pidinfo(PROC_PIDTASKINFO) and mach's task_info
// both return a resident size — and both are C entry points in libproc and libSystem
// with no wrapper in golang.org/x/sys. The kinfo_proc that sysctl does return
// carries a `struct vmspace` whose fields Darwin leaves as padding, which x/sys
// spells honestly as Dummy, Dummy2, Dummy3 and Dummy4. Reaching the real numbers
// therefore means taking a cgo dependency, and go-mutants is a tool people install
// with `go install` on machines that may have no C toolchain at all: paying for
// that to bound a budget would be the wrong trade, and paying for it silently
// would be worse.
//
// So the engine asks [MemoryBoundSupported] before it derives a bound, and says
// once, in a warning, that a runaway mutant on this platform is stopped by the
// timeout alone.
const memorySamplingSupported = false

// maxRSSUnit converts ru_maxrss into bytes. Darwin reports it in bytes, which
// is the one place it disagrees with Linux and the BSDs it descends from.
const maxRSSUnit = 1

// groupResidentMemory cannot answer here; see [memorySamplingSupported].
func groupResidentMemory(int) (int64, bool) { return 0, false }

// memorySamplingAvailable has nothing to probe: the mechanism is absent at
// compile time, so there is no runtime question to ask.
func memorySamplingAvailable() bool { return false }
