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

type Splice struct {
	Span        mutation.Span
	Original    []byte
	Replacement []byte
	Origin      string
}

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

	reach, reachIdx := uint32(0), -1
	for k, i := range order {
		cur := splices[i].Span
		if k > 0 {
			prev := splices[order[k-1]].Span
			if cur == prev {
				return nil, &Error{
					Code: CodeSpliceOverlap,
					Message: fmt.Sprintf("%s and %s both rewrite %s",
						spliceName(splices, order[k-1]), spliceName(splices, i), cur),
				}
			}
			if cur.StartByte < reach {
				return nil, &Error{
					Code: CodeSpliceOverlap,
					Message: fmt.Sprintf("%s at %s overlaps %s at %s",
						spliceName(splices, i), cur, spliceName(splices, reachIdx), splices[reachIdx].Span),
				}
			}
		}
		if cur.EndByte >= reach {
			reach, reachIdx = cur.EndByte, i
		}
	}
	return order, nil
}

func quoteBytes(b []byte) string {
	const limit = 48
	if len(b) <= limit {
		return fmt.Sprintf("%q", b)
	}
	return fmt.Sprintf("%q… (%d bytes)", b[:limit], len(b))
}

type OffsetMap struct {
	edits  []edit
	srcLen uint32
	outLen uint32
}

type edit struct {
	origStart, origEnd uint32
	outStart, outEnd   uint32
}

func (m OffsetMap) SrcLen() uint32 { return m.srcLen }

func (m OffsetMap) OutLen() uint32 { return m.outLen }

func (m OffsetMap) Splices() int { return len(m.edits) }

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

func shift(off uint32, delta int64) uint32 {
	return uint32(int64(off) + delta)
}

func CountLines(b []byte) int {
	return bytes.Count(b, []byte{'\n'})
}

func LinePreserving(splices []Splice) bool {
	for _, s := range splices {
		if CountLines(s.Original) != CountLines(s.Replacement) {
			return false
		}
	}
	return true
}

func spliceName(splices []Splice, i int) string {
	if splices[i].Origin == "" {
		return fmt.Sprintf("splice %d", i)
	}
	return fmt.Sprintf("splice %d (%s)", i, splices[i].Origin)
}
