// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"fmt"
)

var (
	ErrSpanReversed   = errors.New("mutation: span end byte precedes start byte")
	ErrSpanOutOfRange = errors.New("mutation: span is out of range for the source")
)

type Span struct {
	StartByte uint32
	EndByte   uint32
}

func NewSpan(start, end uint32) (Span, error) {
	s := Span{StartByte: start, EndByte: end}
	if err := s.Validate(); err != nil {
		return Span{}, err
	}
	return s, nil
}

func (s Span) Validate() error {
	if s.EndByte < s.StartByte {
		return fmt.Errorf("%w: [%d,%d)", ErrSpanReversed, s.StartByte, s.EndByte)
	}
	return nil
}

func (s Span) Len() uint32 {
	if s.EndByte < s.StartByte {
		return 0
	}
	return s.EndByte - s.StartByte
}

func (s Span) IsEmpty() bool { return s.Len() == 0 }

func (s Span) Contains(other Span) bool {
	return s.StartByte <= other.StartByte && other.EndByte <= s.EndByte
}

func (s Span) StrictlyContains(other Span) bool {
	return s.Contains(other) && s != other
}

func (s Span) Overlaps(other Span) bool {
	return s.StartByte < other.EndByte && other.StartByte < s.EndByte
}

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

func (s Span) Slice(src []byte) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if uint64(s.EndByte) > uint64(len(src)) {
		return nil, fmt.Errorf("%w: [%d,%d) in %d bytes", ErrSpanOutOfRange, s.StartByte, s.EndByte, len(src))
	}
	return src[s.StartByte:s.EndByte], nil
}

func (s Span) String() string {
	return fmt.Sprintf("[%d,%d)", s.StartByte, s.EndByte)
}
