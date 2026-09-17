// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package interval composes overlapping byte spans into a forest of nested sites.
package interval

import (
	"fmt"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type Relation int

const (
	Disjoint Relation = iota
	Identical
	Contains
	ContainedBy
	PartialOverlap
)

func (r Relation) String() string {
	switch r {
	case Disjoint:
		return "disjoint"
	case Identical:
		return "identical"
	case Contains:
		return "contains"
	case ContainedBy:
		return "contained-by"
	case PartialOverlap:
		return "partial-overlap"
	default:
		return fmt.Sprintf("Relation(%d)", int(r))
	}
}

func Relate(a, b mutation.Span) Relation {
	switch {
	case a == b:
		return Identical
	case a.StrictlyContains(b):
		return Contains
	case b.StrictlyContains(a):
		return ContainedBy
	case !a.Overlaps(b):
		return Disjoint
	default:
		return PartialOverlap
	}
}
