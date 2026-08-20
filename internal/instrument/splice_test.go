// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// spliceAt builds a splice that replaces src[start:end], taking Original from
// the source so that the tests exercise the success path rather than the
// mismatch check.
func spliceAt(src string, start, end uint32, replacement string) instrument.Splice {
	return instrument.Splice{
		Span:        mutation.Span{StartByte: start, EndByte: end},
		Original:    []byte(src[start:end]),
		Replacement: []byte(replacement),
	}
}

func TestApply(t *testing.T) {
	t.Parallel()

	const src = "0123456789"

	cases := []struct {
		name    string
		src     string
		splices []instrument.Splice
		want    string
	}{
		{
			name: "no splices copies the source",
			src:  src,
			want: src,
		},
		{
			name:    "replace at the start",
			src:     src,
			splices: []instrument.Splice{spliceAt(src, 0, 1, "X")},
			want:    "X123456789",
		},
		{
			name:    "replace at the end",
			src:     src,
			splices: []instrument.Splice{spliceAt(src, 9, 10, "X")},
			want:    "012345678X",
		},
		{
			name:    "replace grows the source",
			src:     src,
			splices: []instrument.Splice{spliceAt(src, 3, 5, "abc")},
			want:    "012abc56789",
		},
		{
			name:    "replace shrinks the source",
			src:     src,
			splices: []instrument.Splice{spliceAt(src, 3, 7, "x")},
			want:    "012x789",
		},
		{
			name:    "empty span inserts",
			src:     src,
			splices: []instrument.Splice{spliceAt(src, 5, 5, "Y")},
			want:    "01234Y56789",
		},
		{
			name:    "empty replacement deletes",
			src:     src,
			splices: []instrument.Splice{spliceAt(src, 2, 4, "")},
			want:    "01456789",
		},
		{
			name:    "replace everything",
			src:     src,
			splices: []instrument.Splice{spliceAt(src, 0, 10, "X")},
			want:    "X",
		},
		{
			name: "several splices",
			src:  src,
			splices: []instrument.Splice{
				spliceAt(src, 0, 2, "aa"),
				spliceAt(src, 4, 5, "LONGER"),
				spliceAt(src, 8, 10, ""),
			},
			want: "aa23LONGER567",
		},
		{
			name: "splices given out of order",
			src:  src,
			splices: []instrument.Splice{
				spliceAt(src, 8, 10, ""),
				spliceAt(src, 0, 2, "aa"),
				spliceAt(src, 4, 5, "LONGER"),
			},
			want: "aa23LONGER567",
		},
		{
			name: "touching splices",
			src:  src,
			splices: []instrument.Splice{
				spliceAt(src, 2, 4, "AB"),
				spliceAt(src, 4, 6, "CD"),
			},
			want: "01ABCD6789",
		},
		{
			name: "insertion touching a replacement",
			src:  src,
			splices: []instrument.Splice{
				spliceAt(src, 3, 3, "<"),
				spliceAt(src, 3, 5, "!"),
			},
			want: "012<!56789",
		},
		{
			name: "empty source",
			src:  "",
			want: "",
		},
		{
			name:    "insertion into an empty source",
			src:     "",
			splices: []instrument.Splice{spliceAt("", 0, 0, "X")},
			want:    "X",
		},
		{
			name:    "multi-line replacement",
			src:     "a\nb\nc",
			splices: []instrument.Splice{spliceAt("a\nb\nc", 2, 3, "x\ny")},
			want:    "a\nx\ny\nc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, m, err := instrument.Apply([]byte(tc.src), tc.splices)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if string(out) != tc.want {
				t.Errorf("Apply\n got %q\nwant %q", out, tc.want)
			}
			if m.SrcLen() != uint32(len(tc.src)) || m.OutLen() != uint32(len(out)) {
				t.Errorf("map reports %d -> %d bytes, want %d -> %d",
					m.SrcLen(), m.OutLen(), len(tc.src), len(out))
			}
			if m.Splices() != len(tc.splices) {
				t.Errorf("map records %d splices, want %d", m.Splices(), len(tc.splices))
			}
			checkOffsetMap(t, []byte(tc.src), out, tc.splices, m)
		})
	}
}

// TestApplyDoesNotAliasTheSource guards a subtle hazard: a caller that keeps
// the pristine bytes to compare against would otherwise be comparing against
// bytes the splicer had already edited.
func TestApplyDoesNotAliasTheSource(t *testing.T) {
	t.Parallel()

	src := []byte("0123456789")
	out, _, err := instrument.Apply(src, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out[0] = 'X'
	if src[0] != '0' {
		t.Fatalf("Apply returned bytes aliasing the source: src is now %q", src)
	}
}

func TestApplyErrors(t *testing.T) {
	t.Parallel()

	const src = "0123456789"

	cases := []struct {
		name    string
		splices []instrument.Splice
		want    instrument.Code
	}{
		{
			name: "original bytes do not match",
			splices: []instrument.Splice{{
				Span:        mutation.Span{StartByte: 2, EndByte: 4},
				Original:    []byte("XX"),
				Replacement: []byte("ab"),
			}},
			want: instrument.CodeSpliceMismatch,
		},
		{
			name: "original of the wrong length",
			splices: []instrument.Splice{{
				Span:        mutation.Span{StartByte: 2, EndByte: 4},
				Original:    []byte("234"),
				Replacement: []byte("ab"),
			}},
			want: instrument.CodeSpliceMismatch,
		},
		{
			name: "stale original on an empty span",
			splices: []instrument.Splice{{
				Span:        mutation.Span{StartByte: 2, EndByte: 2},
				Original:    []byte("2"),
				Replacement: []byte("ab"),
			}},
			want: instrument.CodeSpliceMismatch,
		},
		{
			name: "span past the end of the source",
			splices: []instrument.Splice{{
				Span:     mutation.Span{StartByte: 8, EndByte: 12},
				Original: []byte("89"),
			}},
			want: instrument.CodeSpliceSpan,
		},
		{
			name: "reversed span",
			splices: []instrument.Splice{{
				Span:     mutation.Span{StartByte: 5, EndByte: 3},
				Original: nil,
			}},
			want: instrument.CodeSpliceSpan,
		},
		{
			name: "overlapping splices",
			splices: []instrument.Splice{
				spliceAt(src, 2, 6, "a"),
				spliceAt(src, 4, 8, "b"),
			},
			want: instrument.CodeSpliceOverlap,
		},
		{
			name: "identical spans",
			splices: []instrument.Splice{
				spliceAt(src, 2, 6, "a"),
				spliceAt(src, 2, 6, "b"),
			},
			want: instrument.CodeSpliceOverlap,
		},
		{
			name: "identical insertion points",
			splices: []instrument.Splice{
				spliceAt(src, 3, 3, "a"),
				spliceAt(src, 3, 3, "b"),
			},
			want: instrument.CodeSpliceOverlap,
		},
		{
			name: "nested spans",
			splices: []instrument.Splice{
				spliceAt(src, 1, 9, "outer"),
				spliceAt(src, 3, 5, "inner"),
			},
			want: instrument.CodeSpliceOverlap,
		},
		{
			name: "insertion inside a replaced span",
			splices: []instrument.Splice{
				spliceAt(src, 1, 9, "outer"),
				spliceAt(src, 5, 5, "!"),
			},
			want: instrument.CodeSpliceOverlap,
		},
		{
			name: "enclosing span overlaps a later one",
			splices: []instrument.Splice{
				spliceAt(src, 0, 1, "a"),
				spliceAt(src, 1, 9, "wide"),
				spliceAt(src, 8, 9, "b"),
			},
			want: instrument.CodeSpliceOverlap,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, m, err := instrument.Apply([]byte(src), tc.splices)
			if err == nil {
				t.Fatalf("Apply = %q, want error %s", out, tc.want)
			}
			if code := instrument.CodeOf(err); code != tc.want {
				t.Fatalf("Apply = error %v with code %q, want %q", err, code, tc.want)
			}
			if out != nil {
				t.Errorf("Apply returned %q alongside its error, want nil", out)
			}
			if m.SrcLen() != 0 || m.OutLen() != 0 || m.Splices() != 0 {
				t.Errorf("Apply returned a populated map alongside its error")
			}
		})
	}
}

// TestApplyIsOrderIndependent pins the determinism the catalogue depends on:
// the same set of splices produces the same bytes however discovery happened
// to order them.
func TestApplyIsOrderIndependent(t *testing.T) {
	t.Parallel()

	const src = "the quick brown fox jumps"
	splices := []instrument.Splice{
		spliceAt(src, 0, 3, "THE"),
		spliceAt(src, 10, 15, ""),
		spliceAt(src, 16, 19, "cat"),
		spliceAt(src, 25, 25, "!"),
	}

	want, _, err := instrument.Apply([]byte(src), splices)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for i := range len(splices) {
		rotated := slices.Concat(splices[i:], splices[:i])
		got, _, err := instrument.Apply([]byte(src), rotated)
		if err != nil {
			t.Fatalf("Apply with rotation %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("rotation %d produced %q, want %q", i, got, want)
		}
	}
}

// A failReporter is the part of *testing.T that the shared assertions below use.
// *rapid.T offers the same two methods without being a *testing.T, so stating
// the requirement as an interface lets the table tests and the property test
// share one set of assertions instead of two drifting copies.
type failReporter interface {
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// A region is one splice's footprint in both coordinate systems, worked out
// from the splices alone. Recomputing it here rather than asking the map is
// what makes the assertions below a check of the map instead of a restatement
// of it.
type region struct {
	origStart, origEnd uint32
	outStart, outEnd   uint32
}

// sortedSplices returns the splices in the order Apply must use, so that the
// tests can talk about "the first splice" without depending on how the caller
// listed them.
func sortedSplices(splices []instrument.Splice) []instrument.Splice {
	sorted := slices.Clone(splices)
	slices.SortFunc(sorted, func(a, b instrument.Splice) int { return a.Span.Compare(b.Span) })
	return sorted
}

func regionsOf(splices []instrument.Splice) []region {
	sorted := sortedSplices(splices)

	regions := make([]region, 0, len(sorted))
	delta := int64(0)
	for _, s := range sorted {
		outStart := uint32(int64(s.Span.StartByte) + delta)
		regions = append(regions, region{
			origStart: s.Span.StartByte,
			origEnd:   s.Span.EndByte,
			outStart:  outStart,
			outEnd:    outStart + uint32(len(s.Replacement)),
		})
		delta += int64(len(s.Replacement)) - int64(s.Span.Len())
	}
	return regions
}

// checkOffsetMap asserts every guarantee the map makes, for every offset in
// both coordinate systems.
func checkOffsetMap(t failReporter, src, out []byte, splices []instrument.Splice, m instrument.OffsetMap) {
	regions := regionsOf(splices)

	// Covered offsets are the ones a splice wrote over or wrote in; their
	// bytes are not shared between the two buffers, so the byte-identity
	// property says nothing about them. Interior offsets are the strictly
	// inner ones, where the map promises no exact answer.
	covered := func(off uint32, start, end func(region) uint32) bool {
		for _, r := range regions {
			if start(r) <= off && off < end(r) {
				return true
			}
		}
		return false
	}
	interior := func(off uint32, start, end func(region) uint32) bool {
		for _, r := range regions {
			if start(r) < off && off < end(r) {
				return true
			}
		}
		return false
	}
	origStart := func(r region) uint32 { return r.origStart }
	origEnd := func(r region) uint32 { return r.origEnd }
	outStart := func(r region) uint32 { return r.outStart }
	outEnd := func(r region) uint32 { return r.outEnd }

	prev := uint32(0)
	for off := uint32(0); off <= uint32(len(src)); off++ {
		got, exact := m.ToOutput(off)
		if want := !interior(off, origStart, origEnd); exact != want {
			t.Errorf("ToOutput(%d) exact = %v, want %v", off, exact, want)
		}
		if got < prev {
			t.Errorf("ToOutput is not monotonic: ToOutput(%d) = %d after %d", off, got, prev)
		}
		prev = got
		if got > uint32(len(out)) {
			t.Fatalf("ToOutput(%d) = %d, past the %d-byte output", off, got, len(out))
		}
		if exact && off < uint32(len(src)) && !covered(off, origStart, origEnd) && out[got] != src[off] {
			t.Errorf("ToOutput(%d) = %d, which holds %q, want %q",
				off, got, out[got], src[off])
		}
		// An offset no splice covered addresses a byte that survived into the
		// output, so the round trip returns exactly it. Covered offsets
		// addressed bytes that are gone, and there the two directions each
		// answer with the nearest surviving anchor instead of inverting one
		// another — see ToOriginal's documentation.
		if !covered(off, origStart, origEnd) {
			if back, ok := m.ToOriginal(got); !ok || back != off {
				t.Errorf("ToOriginal(ToOutput(%d)) = %d, %v; want %d, true", off, back, ok, off)
			}
		}
	}

	prev = 0
	for off := uint32(0); off <= uint32(len(out)); off++ {
		got, exact := m.ToOriginal(off)
		if want := !interior(off, outStart, outEnd); exact != want {
			t.Errorf("ToOriginal(%d) exact = %v, want %v", off, exact, want)
		}
		if got < prev {
			t.Errorf("ToOriginal is not monotonic: ToOriginal(%d) = %d after %d", off, got, prev)
		}
		prev = got
		if got > uint32(len(src)) {
			t.Fatalf("ToOriginal(%d) = %d, past the %d-byte source", off, got, len(src))
		}
		if exact && off < uint32(len(out)) && got < uint32(len(src)) &&
			!covered(off, outStart, outEnd) && out[off] != src[got] {
			t.Errorf("ToOriginal(%d) = %d, which holds %q, want %q", off, got, src[got], out[off])
		}
		// The mirror of the round trip above: an output offset outside every
		// replacement addresses a byte that came from the source unchanged.
		if !covered(off, outStart, outEnd) {
			if there, ok := m.ToOutput(got); !ok || there != off {
				t.Errorf("ToOutput(ToOriginal(%d)) = %d, %v; want %d, true", off, there, ok, off)
			}
		}
	}
}

func TestOffsetMapOutOfRange(t *testing.T) {
	t.Parallel()

	src := "0123456789"
	out, m, err := instrument.Apply([]byte(src), []instrument.Splice{spliceAt(src, 4, 6, "X")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got, exact := m.ToOutput(uint32(len(src)) + 1); exact {
		t.Errorf("ToOutput past the source reported exact (%d)", got)
	}
	if got, exact := m.ToOriginal(uint32(len(out)) + 1); exact {
		t.Errorf("ToOriginal past the output reported exact (%d)", got)
	}
	if got, exact := m.ToOutput(uint32(len(src))); !exact || got != uint32(len(out)) {
		t.Errorf("ToOutput(len) = %d, %v; want %d, true", got, exact, len(out))
	}
}

func TestOffsetMapMapSpan(t *testing.T) {
	t.Parallel()

	// "abcdefghij" with [4,6) replaced by a longer run, so that a span
	// enclosing the splice has to grow by the difference.
	const src = "abcdefghij"
	_, m, err := instrument.Apply([]byte(src), []instrument.Splice{spliceAt(src, 4, 6, "XYZ!")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	cases := []struct {
		name     string
		in       mutation.Span
		want     mutation.Span
		wantCode instrument.Code
	}{
		{
			name: "before the splice",
			in:   mutation.Span{StartByte: 0, EndByte: 3},
			want: mutation.Span{StartByte: 0, EndByte: 3},
		},
		{
			name: "enclosing the splice grows by the difference",
			in:   mutation.Span{StartByte: 2, EndByte: 8},
			want: mutation.Span{StartByte: 2, EndByte: 10},
		},
		{
			name: "exactly the splice",
			in:   mutation.Span{StartByte: 4, EndByte: 6},
			want: mutation.Span{StartByte: 4, EndByte: 8},
		},
		{
			name: "after the splice shifts",
			in:   mutation.Span{StartByte: 7, EndByte: 9},
			want: mutation.Span{StartByte: 9, EndByte: 11},
		},
		{
			name:     "starting inside the splice",
			in:       mutation.Span{StartByte: 5, EndByte: 8},
			wantCode: instrument.CodeSpanStraddles,
		},
		{
			name:     "ending inside the splice",
			in:       mutation.Span{StartByte: 1, EndByte: 5},
			wantCode: instrument.CodeSpanStraddles,
		},
		{
			name:     "past the end of the source",
			in:       mutation.Span{StartByte: 8, EndByte: 20},
			wantCode: instrument.CodeSpliceSpan,
		},
		{
			name:     "reversed",
			in:       mutation.Span{StartByte: 8, EndByte: 2},
			wantCode: instrument.CodeSpliceSpan,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := m.MapSpan(tc.in)
			if tc.wantCode != "" {
				if err == nil {
					t.Fatalf("MapSpan(%s) = %s, want error %s", tc.in, got, tc.wantCode)
				}
				if code := instrument.CodeOf(err); code != tc.wantCode {
					t.Fatalf("MapSpan(%s) = error %v with code %q, want %q", tc.in, err, code, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("MapSpan(%s): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("MapSpan(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestMapSpanWithInsertionOnABoundary pins the rule MapSpan documents for the
// one ambiguous case: inserted text sits at an offset rather than over a range,
// so a span ending exactly there has to either take it or leave it. The rule is
// that an insertion attaches to the text before it, which makes it part of the
// span that ends at its offset and not part of the one that starts there.
func TestMapSpanWithInsertionOnABoundary(t *testing.T) {
	t.Parallel()

	const src = "abcdefghij"
	out, m, err := instrument.Apply([]byte(src), []instrument.Splice{spliceAt(src, 4, 4, "<")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if string(out) != "abcd<efghij" {
		t.Fatalf("Apply = %q, want %q", out, "abcd<efghij")
	}

	cases := []struct {
		name string
		in   mutation.Span
		want string // the text the mapped span covers in the output
	}{
		{name: "span ending at the insertion takes it", in: mutation.Span{StartByte: 0, EndByte: 4}, want: "abcd<"},
		{name: "span starting at the insertion leaves it", in: mutation.Span{StartByte: 4, EndByte: 8}, want: "efgh"},
		{name: "span enclosing the insertion keeps it", in: mutation.Span{StartByte: 2, EndByte: 6}, want: "cd<ef"},
		{name: "empty span at the insertion", in: mutation.Span{StartByte: 4, EndByte: 4}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapped, err := m.MapSpan(tc.in)
			if err != nil {
				t.Fatalf("MapSpan(%s): %v", tc.in, err)
			}
			got, err := mapped.Slice(out)
			if err != nil {
				t.Fatalf("Slice: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("MapSpan(%s) = %s, covering %q, want %q", tc.in, mapped, got, tc.want)
			}
		})
	}
}

// TestMapSpanTracksNestedRewrites is the case the interval forest depends on:
// an enclosing site is spliced after the sites nested inside it have already
// moved, and its span must still cover the same text.
func TestMapSpanTracksNestedRewrites(t *testing.T) {
	t.Parallel()

	const src = "if a && b {\n\tf(x)\n}"
	site := mutation.Span{StartByte: 0, EndByte: uint32(len(src))}
	inner := spliceAt(src, 5, 7, "||") // the condition's connective

	out, m, err := instrument.Apply([]byte(src), []instrument.Splice{inner})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	moved, err := m.MapSpan(site)
	if err != nil {
		t.Fatalf("MapSpan: %v", err)
	}
	got, err := moved.Slice(out)
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if want := "if a || b {\n\tf(x)\n}"; string(got) != want {
		t.Errorf("the enclosing site now covers %q, want %q", got, want)
	}
}

func TestCountLines(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 0},
		{"a\n", 1},
		{"a\nb", 1},
		{"a\r\nb\r\n", 2},
		{"\n\n\n", 3},
		{"a\rb", 0}, // a lone carriage return is not a line break
	}
	for _, tc := range cases {
		if got := instrument.CountLines([]byte(tc.in)); got != tc.want {
			t.Errorf("CountLines(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLinePreserving(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		splices []instrument.Splice
		want    bool
	}{
		{
			name: "no splices",
			want: true,
		},
		{
			name:    "single-line replacement of single-line original",
			splices: []instrument.Splice{{Original: []byte("a + b"), Replacement: []byte("a - b")}},
			want:    true,
		},
		{
			name: "guard around a multi-line statement keeps its line breaks",
			// The Form S shape: the mutated copy is folded onto one line and
			// the original is reproduced verbatim in the else branch, so the
			// replacement contains exactly as many line breaks as it replaced.
			splices: []instrument.Splice{{
				Original:    []byte("f(\n\ta,\n)"),
				Replacement: []byte("if __gm.M[7] { g(a,) } else { f(\n\ta,\n) }"),
			}},
			want: true,
		},
		{
			name: "replacement adding a line",
			splices: []instrument.Splice{{
				Original:    []byte("x = 1"),
				Replacement: []byte("x = 1\ny = 2"),
			}},
			want: false,
		},
		{
			name: "replacement dropping a line",
			splices: []instrument.Splice{{
				Original:    []byte("f(\n\ta,\n)"),
				Replacement: []byte("f(a,)"),
			}},
			want: false,
		},
		{
			name: "one bad splice among good ones",
			splices: []instrument.Splice{
				{Original: []byte("a"), Replacement: []byte("b")},
				{Original: []byte("c"), Replacement: []byte("d\ne")},
				{Original: []byte("f"), Replacement: []byte("g")},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := instrument.LinePreserving(tc.splices); got != tc.want {
				t.Errorf("LinePreserving = %v, want %v", got, tc.want)
			}
		})
	}
}

// lineOf returns the 1-based line number of a byte offset.
func lineOf(b []byte, off uint32) int {
	return 1 + instrument.CountLines(b[:off])
}

// TestLinePreservingMeansLinesAreActuallyPreserved connects the predicate to
// the property it stands for. LinePreserving is an assertion callers make
// about splices they are about to apply, so its meaning has to be checked
// against the bytes rather than assumed from its definition.
func TestLinePreservingMeansLinesAreActuallyPreserved(t *testing.T) {
	t.Parallel()

	const src = "package p\n\nfunc f(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn f(\n\t\ta,\n\t\tb,\n\t)\n}\n"

	// Two guards on the same file: one around a single-line statement, one
	// around a statement spanning three lines whose original is reproduced
	// verbatim inside the guard.
	inner := uint32(strings.Index(src, "return a"))
	outerStart := uint32(strings.Index(src, "return f("))
	outerEnd := outerStart + uint32(len("return f(\n\t\ta,\n\t\tb,\n\t)"))

	splices := []instrument.Splice{
		spliceAt(src, inner, inner+uint32(len("return a")),
			"if __gm.M[0] { return b } else { return a }"),
		spliceAt(src, outerStart, outerEnd,
			"if __gm.M[1] { return f(b,a,) } else { return f(\n\t\ta,\n\t\tb,\n\t) }"),
	}
	if !instrument.LinePreserving(splices) {
		t.Fatalf("LinePreserving = false for splices that keep every line break")
	}

	out, m, err := instrument.Apply([]byte(src), splices)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := instrument.CountLines(out), instrument.CountLines([]byte(src)); got != want {
		t.Errorf("spliced file has %d lines, want %d", got, want)
	}
	for off := uint32(0); off <= uint32(len(src)); off++ {
		mapped, exact := m.ToOutput(off)
		if !exact {
			continue
		}
		if got, want := lineOf(out, mapped), lineOf([]byte(src), off); got != want {
			t.Fatalf("byte %d moved from line %d to line %d", off, want, got)
		}
	}

	// And the negative: a splice that folds a multi-line statement onto one
	// line is rejected by the predicate, and does move later lines.
	folding := []instrument.Splice{spliceAt(src, outerStart, outerEnd, "return f(b,a,)")}
	if instrument.LinePreserving(folding) {
		t.Fatalf("LinePreserving = true for a splice that removes three line breaks")
	}
	folded, fm, err := instrument.Apply([]byte(src), folding)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	last := uint32(len(src)) - 1
	mapped, exact := fm.ToOutput(last)
	if !exact {
		t.Fatalf("the last byte should map exactly")
	}
	if lineOf(folded, mapped) == lineOf([]byte(src), last) {
		t.Fatalf("a splice removing three line breaks left the final line number unchanged")
	}
}

// --- property test ---

// spliceSetGen draws a source and a set of splices that Apply must accept:
// ascending, non-overlapping, and never two insertions at one offset.
func spliceSetGen() *rapid.Generator[struct {
	Src     string
	Splices []instrument.Splice
}] {
	type spliceSet = struct {
		Src     string
		Splices []instrument.Splice
	}
	return rapid.Custom(func(t *rapid.T) spliceSet {
		src := rapid.StringOfN(rapid.SampledFrom([]rune("ab\n\t{}")), 0, 40, -1).Draw(t, "src")

		var splices []instrument.Splice
		cursor := uint32(0)
		prevEmpty := false
		first := true
		for range rapid.IntRange(0, 5).Draw(t, "count") {
			gap := rapid.Uint32Range(0, 4).Draw(t, "gap")
			length := rapid.Uint32Range(0, 4).Draw(t, "length")
			// Two insertions at the same offset have no defined order, and
			// Apply rejects them; the generator stays on the success path.
			if gap == 0 && length == 0 && (prevEmpty || first) && !first {
				gap = 1
			}
			start := cursor + gap
			end := start + length
			if uint64(end) > uint64(len(src)) {
				break
			}
			splices = append(splices, instrument.Splice{
				Span:        mutation.Span{StartByte: start, EndByte: end},
				Original:    []byte(src[start:end]),
				Replacement: []byte(rapid.SampledFrom([]string{"", "x", "yy", "a\nb", "\n", "long replacement"}).Draw(t, "replacement")),
			})
			cursor, prevEmpty, first = end, length == 0, false
		}
		return spliceSet{Src: src, Splices: splices}
	})
}

// TestApplyInvariants states what Apply guarantees for any legal set of
// splices, rather than re-deriving the output the way Apply does: the length
// arithmetic, every replacement present where the map says it is, every
// untouched byte reachable through the map, and order independence.
func TestApplyInvariants(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		set := spliceSetGen().Draw(rt, "set")
		src, splices := []byte(set.Src), set.Splices

		out, m, err := instrument.Apply(src, splices)
		if err != nil {
			rt.Fatalf("Apply(%q, %v) = error %v", src, splices, err)
		}

		wantLen := int64(len(src))
		for _, s := range splices {
			wantLen += int64(len(s.Replacement)) - int64(s.Span.Len())
		}
		if int64(len(out)) != wantLen {
			rt.Fatalf("output is %d bytes, want %d", len(out), wantLen)
		}

		// Every replacement sits where the independently computed regions say
		// it does, and the map agrees about the offsets around it. A splice's
		// own start offset maps to the start of its replacement, except for an
		// insertion, where it maps past the inserted text: an empty span has
		// nothing of its own to point at, and the offset belongs to the byte
		// that was there before.
		for i, r := range regionsOf(splices) {
			replacement := sortedSplices(splices)[i].Replacement
			if !bytes.HasPrefix(out[r.outStart:], replacement) {
				rt.Fatalf("replacement %q is not at output offset %d of %q", replacement, r.outStart, out)
			}
			at, exact := m.ToOutput(r.origStart)
			if !exact {
				rt.Fatalf("a splice's own start offset %d did not map exactly", r.origStart)
			}
			want := r.outStart
			if r.origStart == r.origEnd {
				want = r.outEnd
			}
			if at != want {
				rt.Fatalf("ToOutput(%d) = %d, want %d", r.origStart, at, want)
			}
		}

		checkOffsetMap(rt, src, out, splices, m)

		if len(splices) > 1 {
			rotated := slices.Concat(splices[1:], splices[:1])
			again, _, err := instrument.Apply(src, rotated)
			if err != nil {
				rt.Fatalf("Apply with rotated splices: %v", err)
			}
			if !bytes.Equal(out, again) {
				rt.Fatalf("Apply depends on splice order: %q vs %q", out, again)
			}
		}

		if instrument.LinePreserving(splices) {
			for off := uint32(0); off <= uint32(len(src)); off++ {
				mapped, exact := m.ToOutput(off)
				if !exact {
					continue
				}
				if got, want := lineOf(out, mapped), lineOf(src, off); got != want {
					rt.Fatalf("byte %d moved from line %d to line %d under line-preserving splices",
						off, want, got)
				}
			}
		}
	})
}
