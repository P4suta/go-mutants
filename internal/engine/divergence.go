// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/P4suta/go-mutants/internal/instrument"
)

const (
	DivergenceFactor = 1024
	DivergenceFloor  = 1 << 20
)

const (
	loopCensusName = "loop-census"
	loopLimitsName = "loop-limits"
)

func loopCeilings(observed []uint64) []uint64 {
	out := make([]uint64, len(observed))
	for i, count := range observed {
		out[i] = loopCeiling(count)
	}
	return out
}

func loopCeiling(observed uint64) uint64 {
	if observed > math.MaxUint64/DivergenceFactor {
		return math.MaxUint64
	}
	return max(DivergenceFloor, observed*DivergenceFactor)
}

func divergenceCensus(scratch string) string { return filepath.Join(scratch, loopCensusName) }

func (s *session) deriveLoopLimits(scratch string, runtimes []instrument.Result) string {
	base := filepath.Join(scratch, loopLimitsName)
	wrote := false
	for _, tree := range runtimes {
		if tree.Loops == 0 {
			continue
		}
		suffix := instrument.LoopFileSuffix(tree.ModulePath)
		observed, err := readLoopCensus(divergenceCensus(scratch)+suffix, tree.Loops)
		if err != nil {
			s.warn(CodeLoopCensusUnusable, "the loop census of "+tree.ModulePath+
				" could not be read, so its mutants are bounded in time alone: "+err.Error())
			return ""
		}
		if err := writeLoopLimits(base+suffix, loopCeilings(observed)); err != nil {
			s.warn(CodeLoopCensusUnusable, "the loop ceilings of "+tree.ModulePath+
				" could not be written, so its mutants are bounded in time alone: "+err.Error())
			return ""
		}
		wrote = true
	}
	if !wrote {
		return ""
	}
	return base
}

func readLoopCensus(path string, loops int) ([]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return instrument.ReadLoopCensus(file, loops)
}

func writeLoopLimits(path string, limits []uint64) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := instrument.WriteLoopLimits(file, limits); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func divergenceDetail(loops int) string {
	return countNoun(loops, "loop") + " at " + strconv.Itoa(DivergenceFactor) +
		"x what the baseline counted, floor " + strconv.Itoa(DivergenceFloor)
}
