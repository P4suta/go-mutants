// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package interval_test

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// span is shorthand for the span literals these tables are made of. The forest
// speaks the catalogue's span type, so the tests do too; the span type's own
// behaviour (Len, IsEmpty, String, the reversed case) is pinned in
// internal/mutation, and what this package depends on — that a span covering no
// bytes never becomes a site — is pinned through Build in forest_test.go.
func span(start, end uint32) mutation.Span {
	return mutation.Span{StartByte: start, EndByte: end}
}

// TestRelate covers all four relations two non-empty spans can stand in, from
// both sides: Relate must classify the mirrored pair as the mirrored relation.
// Note that interval.Contains is strict, where mutation.Span.Contains — the
// predicate Relate is built on — is reflexive: equal spans are Identical here.
func TestRelate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		a, b     mutation.Span
		want     interval.Relation
		mirrored interval.Relation
	}{
		{
			name: "identical spans",
			a:    span(4, 9), b: span(4, 9),
			want: interval.Identical, mirrored: interval.Identical,
		},
		{
			name: "strictly nested",
			a:    span(0, 20), b: span(5, 9),
			want: interval.Contains, mirrored: interval.ContainedBy,
		},
		{
			name: "nested sharing the start",
			a:    span(0, 20), b: span(0, 9),
			want: interval.Contains, mirrored: interval.ContainedBy,
		},
		{
			name: "nested sharing the end",
			a:    span(0, 20), b: span(11, 20),
			want: interval.Contains, mirrored: interval.ContainedBy,
		},
		{
			name: "disjoint with a gap",
			a:    span(0, 4), b: span(9, 12),
			want: interval.Disjoint, mirrored: interval.Disjoint,
		},
		{
			name: "disjoint but adjacent",
			a:    span(0, 4), b: span(4, 12),
			want: interval.Disjoint, mirrored: interval.Disjoint,
		},
		{
			name: "straddling the end",
			a:    span(0, 10), b: span(5, 15),
			want: interval.PartialOverlap, mirrored: interval.PartialOverlap,
		},
		{
			name: "straddling by one byte",
			a:    span(0, 10), b: span(9, 11),
			want: interval.PartialOverlap, mirrored: interval.PartialOverlap,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := interval.Relate(tc.a, tc.b); got != tc.want {
				t.Errorf("Relate(%s, %s) = %s, want %s", tc.a, tc.b, got, tc.want)
			}
			if got := interval.Relate(tc.b, tc.a); got != tc.mirrored {
				t.Errorf("Relate(%s, %s) = %s, want %s", tc.b, tc.a, got, tc.mirrored)
			}
		})
	}
}

func TestRelationString(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		relation interval.Relation
		want     string
	}{
		{relation: interval.Disjoint, want: "disjoint"},
		{relation: interval.Identical, want: "identical"},
		{relation: interval.Contains, want: "contains"},
		{relation: interval.ContainedBy, want: "contained-by"},
		{relation: interval.PartialOverlap, want: "partial-overlap"},
		{relation: interval.Relation(42), want: "Relation(42)"},
	} {
		if got := tc.relation.String(); got != tc.want {
			t.Errorf("Relation(%d).String() = %q, want %q", int(tc.relation), got, tc.want)
		}
	}
}
