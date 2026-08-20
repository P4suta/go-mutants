// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"context"
	"slices"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// linearThreshold is the subset size at or below which the search stops halving
// and scans instead.
//
// Halving pays for itself only while a split is cheaper than the builds it
// saves, and at four candidates it is not: the split costs a build to test each
// half, and the scan below spends one build per candidate and answers more —
// it copes with candidates that only fail together, which halving assumes away.
const linearThreshold = 4

// A verdict is what one build said: whether the snapshot compiled, and what the
// compiler printed while deciding.
type verdict struct {
	failed bool
	output string
}

// A probe rebuilds one file with a subset of its candidates and reports whether
// the snapshot still fails to compile.
//
// It is the only thing the search knows how to do, and deliberately the only
// thing: everything about restoring pristine bytes, writing guards, running a
// toolchain, and telling a compile error from a machine that cannot compile at
// all lives on the other side of it. That is what lets the algorithm below be
// tested against a table of which subsets "fail", with no snapshot and no
// toolchain in sight.
type probe func(ctx context.Context, subset []mutation.Mutant) (verdict, error)

// A condemned candidate is one the search rejected, together with the compiler
// output of the build that condemned it.
//
// The output is captured at the moment of rejection rather than re-derived
// later. By the time the phase finishes, the tree compiles and the message that
// explains this candidate no longer exists anywhere; a rejection without it
// would tell a user which mutant was dropped and never why.
type condemned struct {
	mutant mutation.Mutant
	output string
}

// isolate finds a subset of cands that compiles, and the candidates it had to
// drop to get there.
//
// The search assumes what delta debugging calls monotonicity: a subset that
// compiles stays compiling when candidates are removed from it, so failure is
// caused by the candidates in the subset rather than by their absence. Under
// that assumption a failing set can be split, each half asked separately, and
// the answers unioned — which finds every bad candidate in a file of n in
// O(bad · log n) builds instead of n.
//
// The assumption is not quite true, and this handles the case where it is not
// rather than trusting it. Two candidates can compile alone and fail together
// — a pair of guards in one expression, an alternative chain that only
// overflows a type in combination — and halving would then accept both halves
// and hand back a set that does not build. So every join is verified, and a
// join that fails falls back to [incremental], which accepts candidates one at
// a time and is immune to interaction at the cost of a build per candidate.
// The result is always a subset the probe has seen compile.
//
// The subset that comes back is a maximal set only in the monotone case; under
// interaction it is the greedy one, which is deterministic — the same inputs
// produce the same accepted set and the same rejections — and that is what the
// phase promises. The file is left holding whatever the last probe wrote, so
// the caller applies the accepted subset before moving on.
func isolate(ctx context.Context, cands []mutation.Mutant, p probe) ([]mutation.Mutant, []condemned, error) {
	if len(cands) == 0 {
		return nil, nil, nil
	}
	v, err := p(ctx, cands)
	if err != nil {
		return nil, nil, err
	}
	if !v.failed {
		return cands, nil, nil
	}
	if len(cands) <= linearThreshold {
		return incremental(ctx, cands, p)
	}

	mid := len(cands) / 2
	acceptedLeft, rejectedLeft, err := isolate(ctx, cands[:mid], p)
	if err != nil {
		return nil, nil, err
	}
	acceptedRight, rejectedRight, err := isolate(ctx, cands[mid:], p)
	if err != nil {
		return nil, nil, err
	}
	rejected := append(rejectedLeft, rejectedRight...)

	joined := append(slices.Clone(acceptedLeft), acceptedRight...)
	// Two shortcuts, and both are about not paying for a build whose answer is
	// already known. An empty join is the pristine file, which the phase proved
	// compiles before it started searching; a join equal to one half is a set
	// that half's own last probe already compiled.
	if len(joined) == 0 || len(acceptedLeft) == 0 || len(acceptedRight) == 0 {
		return joined, rejected, nil
	}
	v, err = p(ctx, joined)
	if err != nil {
		return nil, nil, err
	}
	if !v.failed {
		return joined, rejected, nil
	}

	accepted, interacting, err := incremental(ctx, joined, p)
	if err != nil {
		return nil, nil, err
	}
	return accepted, append(rejected, interacting...), nil
}

// incremental accepts candidates one at a time, keeping each one that still
// compiles beside the ones already accepted.
//
// It is the linear scan, and it is what the halving search degenerates to on
// purpose in two places: below [linearThreshold], where halving is not cheaper,
// and after a join that failed, where halving has just been proved wrong about
// this file. Because every step is a build of exactly the set being proposed,
// nothing here depends on candidates being independent — an interacting pair
// loses whichever of the two comes second, which is arbitrary but is the same
// arbitrary choice on every machine.
//
// The empty set is never probed. It is the pristine file, which the phase has
// already established compiles, and probing it would cost a build to learn
// something known.
func incremental(ctx context.Context, cands []mutation.Mutant, p probe) ([]mutation.Mutant, []condemned, error) {
	var accepted []mutation.Mutant
	var rejected []condemned
	for _, c := range cands {
		trial := append(slices.Clone(accepted), c)
		v, err := p(ctx, trial)
		if err != nil {
			return nil, nil, err
		}
		if v.failed {
			rejected = append(rejected, condemned{mutant: c, output: v.output})
			continue
		}
		accepted = trial
	}
	return accepted, rejected, nil
}
