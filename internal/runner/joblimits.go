// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

const (
	jobLimitKillOnClose uint32 = 0x00002000
	jobLimitJobMemory   uint32 = 0x00000200
)

const kernelMemoryHeadroomDivisor = 4

type jobLimits struct {
	Flags     uint32
	JobMemory uintptr
}

func jobLimitsFor(memoryLimit int64) jobLimits {
	limits := jobLimits{Flags: jobLimitKillOnClose}
	if value, set := kernelJobMemoryLimit(memoryLimit); set {
		limits.Flags |= jobLimitJobMemory
		limits.JobMemory = value
	}
	return limits
}

func kernelJobMemoryLimit(limit int64) (uintptr, bool) {
	const ceiling = uint64(^uintptr(0))
	if limit <= 0 || uint64(limit) >= ceiling {
		return 0, false
	}
	headroom := uint64(limit) + max(uint64(limit)/kernelMemoryHeadroomDivisor, 1)
	if headroom > ceiling || headroom < uint64(limit) {
		return uintptr(ceiling), true
	}
	return uintptr(headroom), true
}
