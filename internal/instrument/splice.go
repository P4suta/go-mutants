// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// A Splice replaces one byte range of a source file.
//
// Original is not redundant with Span. It is the caller's statement of what it
// believes those bytes are, and [Apply] refuses to edit anything until every
// such statement checks out. Spans travel a long way before they are applied —
// they are minted during discovery, hashed into mutant identities, written to
// reports, read back from caches — and a span that no longer covers what it
// covered when it was minted is the one failure mode that silently produces a
// wrong mutant instead of an error.
type Splice struct {
	// Span is the half-open byte range of the source this replaces. An empty
	// span is an insertion at that offset.
	Span mutation.Span
	// Original is the bytes the span is expected to cover.
	Original []byte
	// Replacement is written in their place. Empty deletes the span.
	Replacement []byte
}

// Apply performs every splice in one left-to-right pass and reports how
// offsets moved.
//
// The splices are applied in span order regardless of the order they were
// given in, so the result is a function of the set alone. They must not
// overlap: two edits to the same bytes have no well-defined composition, and
// picking one silently would make the output depend on discovery order. Nested
// rewrites are expressed by composing them into a single replacement before
// splicing — the order internal/interval's Forest.InnerFirst hands sites out
// in — not by handing Apply a span inside another span.
//
// Apply does not check [LinePreserving]. Whether a set of splices belongs on
// the lines it edits is a property of the guard form that produced them, and
// the forms assert it themselves; Apply is the mechanism, and it is also used
// for the few edits that legitimately add lines.
//
// The returned bytes never alias src.
func Apply(src []byte, splices []Splice) ([]byte, OffsetMap, error) {
	order, err := validateSplices(src, splices)
	if err != nil {
		return nil, OffsetMap{}, err
	}

	grown := int64(len(src))
	for _, s := range splices {
		grown += int64(len(s.Replacement)) - int64(s.Span.Len())
	}
	if grown > math.MaxUint32 {
		return nil, OffsetMap{}, &Error{
			Code:    CodeSpliceSpan,
			Message: fmt.Sprintf("spliced source would be %d bytes, past the 32-bit offset limit", grown),
		}
	}

	out := make([]byte, 0, grown)
	edits := make([]edit, 0, len(order))
	cursor := uint32(0)
	for _, i := range order {
		s := splices[i]
		out = append(out, src[cursor:s.Span.StartByte]...)
		outStart := uint32(len(out))
		out = append(out, s.Replacement...)
		edits = append(edits, edit{
			origStart: s.Span.StartByte,
			origEnd:   s.Span.EndByte,
			outStart:  outStart,
			outEnd:    uint32(len(out)),
		})
		cursor = s.Span.EndByte
	}
	out = append(out, src[cursor:]...)

	return out, OffsetMap{
		edits:  edits,
		srcLen: uint32(len(src)),
		outLen: uint32(len(out)),
	}, nil
}

// validateSplices checks every splice against src and returns the indices of
// the splices in application order.
func validateSplices(src []byte, splices []Splice) ([]int, error) {
	if uint64(len(src)) > math.MaxUint32 {
		return nil, &Error{
			Code:    CodeSpliceSpan,
			Message: fmt.Sprintf("source is %d bytes, past the 32-bit offset limit", len(src)),
		}
	}

	for i, s := range splices {
		if err := s.Span.Validate(); err != nil {
			return nil, &Error{
				Code:    CodeSpliceSpan,
				Message: fmt.Sprintf("splice %d has an invalid span", i),
				Err:     err,
			}
		}
		covered, err := s.Span.Slice(src)
		if err != nil {
			return nil, &Error{
				Code:    CodeSpliceSpan,
				Message: fmt.Sprintf("splice %d does not fit the source", i),
				Err:     err,
			}
		}
		if !bytes.Equal(covered, s.Original) {
			return nil, &Error{
				Code: CodeSpliceMismatch,
				Message: fmt.Sprintf("splice %d at %s covers %s, not %s",
					i, s.Span, quoteBytes(covered), quoteBytes(s.Original)),
			}
		}
	}

	order := make([]int, len(splices))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return splices[a].Span.Compare(splices[b].Span)
	})

	// Reach carries the furthest end seen so far rather than the previous end,
	// so that a span enclosing several later ones is caught against every one
	// of them and not just the first.
	reach, reachIdx := uint32(0), -1
	for k, i := range order {
		cur := splices[i].Span
		if k > 0 {
			prev := splices[order[k-1]].Span
			if cur == prev {
				return nil, &Error{
					Code:    CodeSpliceOverlap,
					Message: fmt.Sprintf("splices %d and %d both rewrite %s", order[k-1], i, cur),
				}
			}
			if cur.StartByte < reach {
				return nil, &Error{
					Code: CodeSpliceOverlap,
					Message: fmt.Sprintf("splice %d at %s overlaps splice %d at %s",
						i, cur, reachIdx, splices[reachIdx].Span),
				}
			}
		}
		if cur.EndByte >= reach {
			reach, reachIdx = cur.EndByte, i
		}
	}
	return order, nil
}

// quoteBytes renders a byte range for a diagnostic, shortened so that a
// mismatch on a long span stays one line.
func quoteBytes(b []byte) string {
	const limit = 48
	if len(b) <= limit {
		return fmt.Sprintf("%q", b)
	}
	return fmt.Sprintf("%q… (%d bytes)", b[:limit], len(b))
}

// An OffsetMap translates byte offsets between a source and its spliced
// output, in both directions.
//
// Every splice moves everything after it, so a span minted against the
// original bytes points at the wrong text the moment an earlier splice is
// applied. The map is what lets a caller keep working in original coordinates
// — reporting a mutant's position, or splicing an enclosing site after the
// sites nested inside it have already been rendered — without doing the
// arithmetic by hand at each call site.
//
// Offsets outside every replaced range translate exactly, and translate to the
// same byte: for such an offset o, output[ToOutput(o)] is src[o]. Offsets
// strictly inside a replaced range have no exact translation, because the
// bytes they addressed are gone; they translate to the start of the
// replacement, reported as inexact. Both directions are monotonically
// non-decreasing.
//
// The zero OffsetMap is the identity on empty input. Apply always returns a
// map covering the source it was given, even when there were no splices.
type OffsetMap struct {
	// edits is one entry per splice, ordered by offset. Both the original and
	// the output ranges are non-overlapping and ascending, which is what makes
	// the lookups binary searches.
	edits  []edit
	srcLen uint32
	outLen uint32
}

// edit records where one splice's bytes went.
type edit struct {
	origStart, origEnd uint32
	outStart, outEnd   uint32
}

// SrcLen returns the length of the source the map was built from.
func (m OffsetMap) SrcLen() uint32 { return m.srcLen }

// OutLen returns the length of the spliced output.
func (m OffsetMap) OutLen() uint32 { return m.outLen }

// Splices returns the number of splices the map records.
func (m OffsetMap) Splices() int { return len(m.edits) }

// ToOutput translates an original byte offset into an output byte offset.
//
// The boolean reports whether the translation is exact. It is false for an
// offset strictly inside replaced bytes — the returned offset is then the
// start of the replacement, which is the closest thing to the original
// position that still exists — and for an offset past the end of the source.
//
// An offset at the position of an inserted range translates past the insertion
// rather than before it, so that the byte it addressed keeps its identity.
func (m OffsetMap) ToOutput(off uint32) (uint32, bool) {
	if off > m.srcLen {
		return m.outLen, false
	}
	i := sort.Search(len(m.edits), func(i int) bool { return m.edits[i].origEnd > off })
	if i == len(m.edits) {
		return shift(off, int64(m.outLen)-int64(m.srcLen)), true
	}
	e := m.edits[i]
	if e.origStart < off {
		return e.outStart, false
	}
	return shift(off, int64(e.outStart)-int64(e.origStart)), true
}

// ToOriginal translates an output byte offset back into an original byte
// offset. It is the inverse of [OffsetMap.ToOutput] wherever an inverse
// exists; the boolean reports the same distinction, with an offset strictly
// inside a replacement translating to the start of the range it replaced.
//
// Round-tripping is exact for the bytes that survived the splice: for an
// original offset no splice covered, ToOriginal(ToOutput(o)) is o, and for an
// output offset outside every replacement, ToOutput(ToOriginal(p)) is p.
//
// Inside replaced or inserted text there is nothing to return to — those bytes
// are gone, or are new — and each direction answers with the nearest surviving
// anchor rather than with the other's inverse. The two are then not inverses
// of each other: a deletion maps both ends of the deleted range onto one
// output offset, and if an insertion begins at that same offset, the reverse
// lookup reports the insertion's origin rather than the deletion's start.
// Both answers are the closest surviving byte in their own direction, and both
// keep monotonicity; the map is a pair of monotone translations rather than a
// bijection, and callers that need an exact correspondence should ask about
// offsets outside the spans they spliced.
func (m OffsetMap) ToOriginal(off uint32) (uint32, bool) {
	if off > m.outLen {
		return m.srcLen, false
	}
	i := sort.Search(len(m.edits), func(i int) bool { return m.edits[i].outEnd > off })
	if i == len(m.edits) {
		return shift(off, int64(m.srcLen)-int64(m.outLen)), true
	}
	e := m.edits[i]
	if e.outStart < off {
		return e.origStart, false
	}
	return shift(off, int64(e.origStart)-int64(e.outStart)), true
}

// MapSpan translates a whole span into output coordinates.
//
// Both endpoints must translate exactly, which is the same as saying the span
// must not start or end inside replaced bytes. A span that encloses splices
// translates fine and grows or shrinks by their net effect: that is the case
// the nested-rewrite path depends on.
//
// Insertions on a boundary attach to the text before them, following
// [OffsetMap.ToOutput]: text inserted at the span's end offset falls inside the
// mapped span, and text inserted at its start offset falls outside.
func (m OffsetMap) MapSpan(s mutation.Span) (mutation.Span, error) {
	if err := s.Validate(); err != nil {
		return mutation.Span{}, &Error{Code: CodeSpliceSpan, Message: "span is invalid", Err: err}
	}
	if uint64(s.EndByte) > uint64(m.srcLen) {
		return mutation.Span{}, &Error{
			Code:    CodeSpliceSpan,
			Message: fmt.Sprintf("span %s is out of range for %d mapped bytes", s, m.srcLen),
		}
	}
	start, ok := m.ToOutput(s.StartByte)
	if !ok {
		return mutation.Span{}, &Error{
			Code:    CodeSpanStraddles,
			Message: fmt.Sprintf("span %s starts inside replaced bytes", s),
		}
	}
	end, ok := m.ToOutput(s.EndByte)
	if !ok {
		return mutation.Span{}, &Error{
			Code:    CodeSpanStraddles,
			Message: fmt.Sprintf("span %s ends inside replaced bytes", s),
		}
	}
	return mutation.Span{StartByte: start, EndByte: end}, nil
}

// shift applies a signed delta to an offset. Callers have already established
// that the result is in range.
func shift(off uint32, delta int64) uint32 {
	return uint32(int64(off) + delta)
}

// CountLines returns the number of line breaks in b.
//
// Only "\n" is counted. A CRLF file has exactly one "\n" per line break just
// as an LF file does, so this is the line count of the text under either
// convention — which is what makes it the right measure for the invariant
// below, on a tool that must not care which convention a source file uses.
func CountLines(b []byte) int {
	return bytes.Count(b, []byte{'\n'})
}

// LinePreserving reports whether applying these splices would leave every
// original byte on the line it started on.
//
// The line number of a byte is one plus the number of line breaks before it,
// so a splice shifts every later byte's line number by exactly
// CountLines(Replacement) - CountLines(Original), and those shifts accumulate.
// The set preserves line numbers exactly when every one of those differences
// is zero: sufficient because each splice contributes nothing, and necessary
// because the first non-zero difference moves every byte after it and no later
// splice can move them back — it can only shift bytes beyond itself.
//
// The predicate is therefore per-splice equality, not "the replacement holds
// no line break". The stronger reading is only the single-line case. A
// statement guard renders as `if __gm.M[7] { <flattened mutant> } else {
// <original bytes> }`, and when the statement it guards spans lines the
// original bytes in the else branch still span them, byte for byte: the
// replacement contains line breaks, and the rewrite is line-preserving anyway
// because it contains exactly as many as it replaced. Requiring a single-line
// replacement there would reject the very construction the invariant exists to
// permit.
//
// Callers assert this before [Apply]; see the package documentation for why
// line preservation is load-bearing rather than tidy.
func LinePreserving(splices []Splice) bool {
	for _, s := range splices {
		if CountLines(s.Original) != CountLines(s.Replacement) {
			return false
		}
	}
	return true
}
