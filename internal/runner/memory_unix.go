// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix && !linux && !darwin

package runner

// memorySamplingSupported: the BSDs, solaris and illumos each expose a live
// resident size, and each does it differently — a kvm handle here, a
// /proc/<pid>/psinfo there. go-mutants is built and tested on linux, darwin and
// windows, and a sampler written for a platform nobody runs the suite on would
// be a bound whose correctness nothing checks. Measurement still works: ru_maxrss
// is POSIX.
const memorySamplingSupported = false

// kernelBoundsMemory: nothing here enforces a bound at all; see
// [memorySamplingSupported] and [exceededAtExit].
const kernelBoundsMemory = false

// maxRSSUnit converts ru_maxrss into bytes. The BSDs and the illumos family
// report kilobytes, as Linux does and unlike Darwin.
const maxRSSUnit = 1 << 10

// groupResidentMemory cannot answer here; see [memorySamplingSupported].
func groupResidentMemory(int) (int64, bool) { return 0, false }

// memorySamplingAvailable has nothing to probe; see [memorySamplingSupported].
func memorySamplingAvailable() bool { return false }
