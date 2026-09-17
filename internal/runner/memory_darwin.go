// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

const memorySamplingSupported = false

const kernelBoundsMemory = false

const accountedPeakBelongsToTheChild = true

const maxRSSUnit = 1

func treeResidentMemory(int) (int64, bool) { return 0, false }

func treeAndGroupResidentMemory(int) (int64, bool) { return 0, false }

func memorySamplingAvailable() bool { return false }
