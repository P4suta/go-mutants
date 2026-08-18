// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"fmt"
)

// Errors returned when a span is not well formed or does not fit its source.
var (
	// ErrSpanReversed reports a span whose end byte precedes its start byte.
	ErrSpanReversed = errors.New("mutation: span end byte precedes start byte")
	// ErrSpanOutOfRange reports a span that reaches past the end of the
	// buffer it is being applied to.
	ErrSpanOutOfRange = errors.New("mutation: span is out of range for the source")
)

// Span is a half-open byte range [StartByte, EndByte) into one source file.
//
// Spans are byte offsets, never line/column pairs and never rune indices:
// go-mutants splices original bytes rather than pretty-printing an AST, so
// comments, alignment, and CRLF line endings survive a mutation untouched.
// Offsets are uint32 because a Go source file larger than 4 GiB is not a case
// worth carrying 64-bit arithmetic for, and the narrower type keeps Span
// comparable, copyable, and cheap inside the catalogue.
//
// The zero Span is the valid empty range at offset 0.
type Span struct {
	StartByte uint32
	EndByte   uint32
}

// NewSpan returns the half-open span [start, end), or an error when end
// precedes start. An empty span (start == end) is legal: it denotes an
// insertion point rather than a stretch of replaceable text.
func NewSpan(start, end uint32) (Span, error) {
	s := Span{StartByte: start, EndByte: end}
	if err := s.Validate(); err != nil {
		return Span{}, err
	}
	return s, nil
}

// Validate reports whether the span is well formed.
func (s Span) Validate() error {
	if s.EndByte < s.StartByte {
		return fmt.Errorf("%w: [%d,%d)", ErrSpanReversed, s.StartByte, s.EndByte)
	}
	return nil
}

// Len returns the number of bytes the span covers.
func (s Span) Len() uint32 {
	if s.EndByte < s.StartByte {
		return 0
	}
	return s.EndByte - s.StartByte
}

// IsEmpty reports whether the span covers no bytes.
func (s Span) IsEmpty() bool { return s.Len() == 0 }

// Contains reports whether other lies entirely within s. A span contains
// itself, and an empty span sitting on either boundary of s counts as
// contained.
func (s Span) Contains(other Span) bool {
	return s.StartByte <= other.StartByte && other.EndByte <= s.EndByte
}

// StrictlyContains reports whether other lies within s and is not s itself.
// The interval forest uses this to nest rewrite sites: equal spans are
// siblings at one site, strictly nested spans are parent and child.
func (s Span) StrictlyContains(other Span) bool {
	return s.Contains(other) && s != other
}

// Overlaps reports whether s and other share at least one byte. Empty spans
// overlap nothing, including each other.
func (s Span) Overlaps(other Span) bool {
	return s.StartByte < other.EndByte && other.StartByte < s.EndByte
}

// Compare orders spans by start byte, then by end byte. It returns a negative
// number when s sorts first, zero when the spans are equal, and a positive
// number otherwise, matching the convention of the cmp package.
func (s Span) Compare(other Span) int {
	switch {
	case s.StartByte != other.StartByte:
		if s.StartByte < other.StartByte {
			return -1
		}
		return 1
	case s.EndByte != other.EndByte:
		if s.EndByte < other.EndByte {
			return -1
		}
		return 1
	default:
		return 0
	}
}

// Slice returns the bytes src covers, without copying them. It fails rather
// than panicking when the span does not fit src, because spans travel through
// caches and report files and may outlive the source they were minted from.
func (s Span) Slice(src []byte) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if uint64(s.EndByte) > uint64(len(src)) {
		return nil, fmt.Errorf("%w: [%d,%d) in %d bytes", ErrSpanOutOfRange, s.StartByte, s.EndByte, len(src))
	}
	return src[s.StartByte:s.EndByte], nil
}

// String renders the span in half-open interval notation, for example
// "[12,20)".
func (s Span) String() string {
	return fmt.Sprintf("[%d,%d)", s.StartByte, s.EndByte)
}
