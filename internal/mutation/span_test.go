// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"testing"
)

func TestNewSpanValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		start   uint32
		end     uint32
		wantErr error
	}{
		{name: "ordinary range", start: 10, end: 20},
		{name: "empty range is an insertion point", start: 7, end: 7},
		{name: "zero span", start: 0, end: 0},
		{name: "reversed", start: 20, end: 10, wantErr: ErrSpanReversed},
		{name: "reversed by one", start: 1, end: 0, wantErr: ErrSpanReversed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			span, err := NewSpan(tc.start, tc.end)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("NewSpan(%d, %d) error = %v, want %v", tc.start, tc.end, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if span.StartByte != tc.start || span.EndByte != tc.end {
				t.Fatalf("NewSpan(%d, %d) = %v", tc.start, tc.end, span)
			}
		})
	}
}

func TestSpanGeometry(t *testing.T) {
	t.Parallel()

	outer := Span{StartByte: 10, EndByte: 20}

	tests := []struct {
		name             string
		other            Span
		contains         bool
		strictlyContains bool
		overlaps         bool
	}{
		{
			name: "identical", other: Span{StartByte: 10, EndByte: 20},
			contains: true, strictlyContains: false, overlaps: true,
		},
		{
			name: "strictly inside", other: Span{StartByte: 12, EndByte: 18},
			contains: true, strictlyContains: true, overlaps: true,
		},
		{
			name: "flush left", other: Span{StartByte: 10, EndByte: 15},
			contains: true, strictlyContains: true, overlaps: true,
		},
		{
			name: "empty at the left boundary", other: Span{StartByte: 10, EndByte: 10},
			contains: true, strictlyContains: true, overlaps: false,
		},
		{
			name: "empty at the right boundary", other: Span{StartByte: 20, EndByte: 20},
			contains: true, strictlyContains: true, overlaps: false,
		},
		{
			name: "abutting on the right", other: Span{StartByte: 20, EndByte: 30},
			contains: false, strictlyContains: false, overlaps: false,
		},
		{
			name: "straddling the right edge", other: Span{StartByte: 15, EndByte: 25},
			contains: false, strictlyContains: false, overlaps: true,
		},
		{
			name: "entirely before", other: Span{StartByte: 0, EndByte: 5},
			contains: false, strictlyContains: false, overlaps: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := outer.Contains(tc.other); got != tc.contains {
				t.Errorf("%v.Contains(%v) = %v, want %v", outer, tc.other, got, tc.contains)
			}
			if got := outer.StrictlyContains(tc.other); got != tc.strictlyContains {
				t.Errorf("%v.StrictlyContains(%v) = %v, want %v", outer, tc.other, got, tc.strictlyContains)
			}
			if got := outer.Overlaps(tc.other); got != tc.overlaps {
				t.Errorf("%v.Overlaps(%v) = %v, want %v", outer, tc.other, got, tc.overlaps)
			}
			if got := tc.other.Overlaps(outer); got != tc.overlaps {
				t.Errorf("Overlaps is not symmetric for %v and %v", outer, tc.other)
			}
		})
	}
}

func TestSpanCompareIsATotalOrder(t *testing.T) {
	t.Parallel()

	ordered := []Span{
		{StartByte: 0, EndByte: 0},
		{StartByte: 0, EndByte: 4},
		{StartByte: 1, EndByte: 2},
		{StartByte: 1, EndByte: 9},
		{StartByte: 5, EndByte: 5},
	}
	for i, a := range ordered {
		for j, b := range ordered {
			got := a.Compare(b)
			switch {
			case i < j && got >= 0:
				t.Errorf("%v.Compare(%v) = %d, want negative", a, b, got)
			case i == j && got != 0:
				t.Errorf("%v.Compare(%v) = %d, want 0", a, b, got)
			case i > j && got <= 0:
				t.Errorf("%v.Compare(%v) = %d, want positive", a, b, got)
			}
		}
	}
}

func TestSpanLenAndEmpty(t *testing.T) {
	t.Parallel()

	if got := (Span{StartByte: 4, EndByte: 9}).Len(); got != 5 {
		t.Errorf("Len() = %d, want 5", got)
	}
	if !(Span{StartByte: 4, EndByte: 4}).IsEmpty() {
		t.Error("an equal-bounds span should be empty")
	}
	// A reversed span is invalid; Len must not underflow into four billion.
	if got := (Span{StartByte: 9, EndByte: 4}).Len(); got != 0 {
		t.Errorf("reversed Len() = %d, want 0", got)
	}
}

func TestSpanSlice(t *testing.T) {
	t.Parallel()

	src := []byte("return a == b")

	got, err := (Span{StartByte: 9, EndByte: 11}).Slice(src)
	if err != nil {
		t.Fatalf("Slice() error = %v", err)
	}
	if string(got) != "==" {
		t.Errorf("Slice() = %q, want %q", got, "==")
	}

	if _, err := (Span{StartByte: 9, EndByte: 99}).Slice(src); !errors.Is(err, ErrSpanOutOfRange) {
		t.Errorf("out-of-range Slice() error = %v, want ErrSpanOutOfRange", err)
	}
	if _, err := (Span{StartByte: 9, EndByte: 2}).Slice(src); !errors.Is(err, ErrSpanReversed) {
		t.Errorf("reversed Slice() error = %v, want ErrSpanReversed", err)
	}
}

func TestSpanString(t *testing.T) {
	t.Parallel()

	if got := (Span{StartByte: 12, EndByte: 20}).String(); got != "[12,20)" {
		t.Errorf("String() = %q, want %q", got, "[12,20)")
	}
}
