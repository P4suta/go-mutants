// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"context"
	"slices"

	"github.com/P4suta/go-mutants/internal/mutation"
)

const linearThreshold = 4

type verdict struct {
	failed  bool
	output  string
	execSeq int64
	blamed  []string
}

type probe func(ctx context.Context, subset []mutation.Mutant) (verdict, error)

type condemned struct {
	mutant mutation.Mutant
	output string
}

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
