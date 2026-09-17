// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

const TerminationGrace = 2 * time.Second

const IODrainGrace = 2 * time.Second

var (
	supervisionAdopted    atomic.Int64
	supervisionTerminated atomic.Int64
)

type supervisor interface {
	configure(cmd *exec.Cmd)

	adopt(cmd *exec.Cmd) error

	terminate(exited <-chan struct{}, grace time.Duration)

	usedMemory(thorough bool) (int64, bool)

	peakMemory(ps *os.ProcessState) (int64, bool)

	release()
}
