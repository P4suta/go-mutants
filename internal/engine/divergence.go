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

// The two numbers that turn a census into ceilings.
//
// DivergenceFactor is how many times the original program's own work a mutant
// may do before the run stops calling it slow and starts calling it a mutant
// that does not return. DivergenceFloor is what applies where the original did
// almost nothing, or was never in that loop at all: a loop the census never saw
// has no measurement to scale, and a loop it saw go round twice would otherwise
// be held to a ceiling a legitimate edit could pass.
//
// Both are generous, and being generous is nearly free — which is the whole
// argument for counting rather than waiting. The cost of a margin against a
// stopwatch is seconds per mutant; the cost of a margin against a counter is
// the loop's own work multiplied by the factor, which for the loops that
// actually spin is a few milliseconds. So the margin here is a thousand times
// what a timeout could afford and still settles a thousand times sooner. See
// [ADR 0013].
//
// [ADR 0013]: https://github.com/P4suta/go-mutants/blob/main/docs/adr/0013-a-mutant-that-does-not-return-is-decided-by-work.md
const (
	DivergenceFactor = 1024
	DivergenceFloor  = 1 << 20
)

// loopCensusName and loopLimitsName are the two files a run writes beside its
// scratch, before the per-module suffix the generated runtimes add.
const (
	loopCensusName = "loop-census"
	loopLimitsName = "loop-limits"
)

// loopCeilings turns one tree's census into the table its mutants are held to.
//
// A count is scaled and floored, and a count large enough that scaling it would
// overflow is given no ceiling at all: a loop the original took four thousand
// million million times round is not one this mechanism has anything to say
// about, and saying it in wrapped arithmetic would say the opposite.
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

// divergenceCensus is the path the instrumented baseline is told to record
// into, and the path every ceiling this run enforces is derived from.
func divergenceCensus(scratch string) string { return filepath.Join(scratch, loopCensusName) }

// deriveLoopLimits reads what the instrumented baseline counted and writes the
// table the mutant runs are held to, returning the path they are given.
//
// One census and one table per module, because one generated runtime per module
// numbers its own loops: the environment names a path and each runtime adds
// [instrument.LoopFileSuffix] to it. A run over a single module has one of each
// and the suffix decides nothing.
//
// It fails open, and the direction is the argument. Every ceiling comes from
// this file, so a census that cannot be read whole would yield ceilings that
// are too low for the loops it forgot — and a ceiling that is too low reports a
// mutant that terminates as one that does not. The empty path the caller gets
// back means "no ceilings", which is the shape every run had before it could
// count: bounded in time alone, and said once rather than silently.
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

// readLoopCensus opens one module's census and reads it against that module's
// own number of loops.
func readLoopCensus(path string, loops int) ([]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// Nothing is written to it, so a close that fails has nothing to report
	// that the read has not already answered.
	defer func() { _ = file.Close() }()
	return instrument.ReadLoopCensus(file, loops)
}

// writeLoopLimits writes one module's ceilings where its runtime will look for
// them.
func writeLoopLimits(path string, limits []uint64) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := instrument.WriteLoopLimits(file, limits); err != nil {
		// The write failed, so the close cannot add anything a caller would
		// act on: this table is already not one a mutant may be held to.
		_ = file.Close()
		return err
	}
	return file.Close()
}

// divergenceDetail renders what a run derived, for the recording and for -v.
func divergenceDetail(loops int) string {
	return countNoun(loops, "loop") + " at " + strconv.Itoa(DivergenceFactor) +
		"x what the baseline counted, floor " + strconv.Itoa(DivergenceFloor)
}
