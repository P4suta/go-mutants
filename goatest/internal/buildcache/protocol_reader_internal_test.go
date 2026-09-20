// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const (
	minimumBufioSize = 16
	pairOfBytes      = 2
	longValueRepeats = 1 << 12
)

func TestAProgressReaderRefusesAReaderThatNeverMovesForward(t *testing.T) {
	t.Parallel()
	stalled := stalledReader{}
	count, err := progressReader{reader: stalled}.Read(make([]byte, 1))
	if count != 0 || !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("a reader that returns nothing answered (%d, %v), want no progress", count, err)
	}
	empty := progressReader{reader: stalled}
	if count, err := empty.Read(nil); count != 0 || err != nil {
		t.Fatalf("a read of no bytes answered (%d, %v), want it left alone", count, err)
	}
	moving := progressReader{reader: strings.NewReader("ab")}
	destination := make([]byte, pairOfBytes)
	if count, err := moving.Read(destination); count != pairOfBytes || err != nil {
		t.Fatalf("a reader that moves answered (%d, %v), want two bytes", count, err)
	}
}

func TestOpeningAQuotedBodySkipsTheSpaceBeforeItAndRefusesAnythingElse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "a quoted value", input: `"abc"`},
		{name: "a quoted value behind spaces", input: "  \t\r\n" + `"abc"`},
		{name: "a value that is not quoted", input: "abc", want: "opens with"},
		{name: "a value that is a number", input: "12", want: "opens with"},
		{name: "nothing at all", input: "", want: "read cacheprog body"},
		{name: "nothing but space", input: "   ", want: "read cacheprog body"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, err := openQuoted(bufio.NewReader(strings.NewReader(test.input)))
			if test.want == "" {
				if err != nil || body == nil {
					t.Fatalf("openQuoted(%q) = (%v, %v), want a body", test.input, body, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("openQuoted(%q) reported %v, want it to say %q", test.input, err, test.want)
			}
			if body != nil {
				t.Errorf("a body it refused answered with %v, want none", body)
			}
		})
	}
}

func TestAQuotedBodyReadsUpToItsClosingQuoteAndNoFurther(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
		fails bool
	}{
		{name: "a short value", input: `abc"rest`, want: "abc"},
		{name: "an empty value", input: `"rest`, want: ""},
		{name: "a value that is never closed", input: "abc", fails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := &quotedReader{reader: bufio.NewReader(strings.NewReader(test.input))}
			var read bytes.Buffer
			_, err := io.Copy(&read, body)
			if test.fails {
				if err == nil {
					t.Fatalf("a value that is never closed read %q with no complaint", read.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if read.String() != test.want {
				t.Fatalf("the body read %q, want %q", read.String(), test.want)
			}
		})
	}
}

func TestAQuotedBodyReadsAValueLongerThanItsBuffer(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("abcd", longValueRepeats)
	body := &quotedReader{reader: bufio.NewReaderSize(strings.NewReader(value+`"rest`), minimumBufioSize)}
	var read bytes.Buffer
	if _, err := io.Copy(&read, body); err != nil {
		t.Fatal(err)
	}
	if read.String() != value {
		t.Fatalf("the body read %d bytes, want %d", read.Len(), len(value))
	}
}

func TestDrainingAQuotedBodyReadsItToItsClosingQuote(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReader(strings.NewReader(`abc"after`))
	body := &quotedReader{reader: reader}
	if err := body.drain(); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "after" {
		t.Fatalf("draining left %q, want what follows the closing quote", rest)
	}
}
