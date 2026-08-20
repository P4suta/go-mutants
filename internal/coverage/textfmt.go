// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// modePrefix opens the first line of every textfmt document.
const modePrefix = "mode: "

// scanBufferLimit is the longest line [ParseTextfmt] will read.
//
// bufio.Scanner defaults to 64 KiB and reports anything longer as an error
// rather than truncating, which would turn one very long import path into an
// unreadable profile. A block record is a path plus two dozen characters, so a
// mebibyte is far past any real one and still bounded — the input is a file the
// toolchain wrote a moment ago, not something to be defensive about, but an
// unbounded buffer is never the right answer to "how long can a line be".
const scanBufferLimit = 1 << 20

// A Block is one statement block of a coverage profile.
//
// The columns are parsed and carried because the format has them and dropping a
// field while reading is how a parser stops being a parser. Nothing in this
// package looks at them; see the package documentation for why the mapping is
// line-only.
type Block struct {
	// File is the block's file, exactly as the profile spells it: the owning
	// package's import path, a slash, and the file's base name — for example
	// "example.com/m/internal/alpha/alpha.go". It is not a path on this
	// machine and must not be opened.
	File string
	// StartLine and StartCol are the 1-based coordinates the block opens at.
	StartLine int
	StartCol  int
	// EndLine and EndCol are where it closes. EndLine is never less than
	// StartLine.
	EndLine int
	EndCol  int
	// NumStmt is how many statements the block holds.
	NumStmt int
	// Count is how many times the block was reached. In `set` mode — what
	// `go test -cover` produces by default, and what go-mutants collects — it
	// is 0 or 1, and only "greater than zero" is ever asked of it.
	Count int
}

// Covered reports whether the block was reached at all.
func (b Block) Covered() bool { return b.Count > 0 }

// A Profile is one parsed `go tool covdata textfmt` document.
type Profile struct {
	// Mode is the counter mode the data was collected in: "set", "count", or
	// "atomic". It is kept because a document that does not declare one is
	// malformed, and because a reader has to be able to tell a merged profile's
	// mode without re-parsing it.
	Mode string
	// Blocks are the block records, in the order the document listed them.
	// Nothing here sorts them: the order is the toolchain's, and preserving it
	// keeps a parsed profile comparable to the file it came from.
	Blocks []Block
}

// ParseTextfmt reads a `go tool covdata textfmt` document.
//
// The format is the same one `go test -coverprofile` has written since Go 1.2,
// and it is two things: a `mode: <name>` line, then one block record per line —
//
//	<import path>/<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStmt> <count>
//
// The file name is separated from the coordinates by the *last* colon on the
// line rather than the first. That is not defensiveness about import paths,
// which cannot contain one: it is what the Go toolchain's own parser does, and
// a reader that disagreed with the writer about where a record starts would
// misread exactly the documents that are hardest to debug.
//
// Blank lines are skipped, which makes a trailing newline — and a file
// concatenated from two profiles by hand — readable rather than a failure.
// Anything else that is not a block record is [CodeMalformedProfile], with the
// line number, because a profile that is silently half-read is a coverage
// mapping that silently loses kills.
func ParseTextfmt(r io.Reader) (Profile, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), scanBufferLimit)

	var profile Profile
	number := 0
	for scanner.Scan() {
		number++
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if profile.Mode == "" {
			mode, ok := strings.CutPrefix(line, modePrefix)
			if !ok || strings.TrimSpace(mode) == "" {
				return Profile{}, malformed(number,
					"the first line is "+strconv.Quote(line)+", not a "+strconv.Quote("mode: <name>")+" header")
			}
			profile.Mode = strings.TrimSpace(mode)
			continue
		}
		block, err := parseBlock(line, number)
		if err != nil {
			return Profile{}, err
		}
		profile.Blocks = append(profile.Blocks, block)
	}
	if err := scanner.Err(); err != nil {
		return Profile{}, &Error{
			Code:    CodeMalformedProfile,
			Message: "the coverage profile could not be read",
			Err:     err,
		}
	}
	if profile.Mode == "" {
		return Profile{}, malformed(0, "the coverage profile is empty: not even a "+
			strconv.Quote("mode: <name>")+" header")
	}
	return profile, nil
}

// parseBlock reads one block record.
func parseBlock(line string, number int) (Block, error) {
	colon := strings.LastIndexByte(line, ':')
	if colon <= 0 || colon == len(line)-1 {
		return Block{}, malformed(number, strconv.Quote(line)+" is not a block record: no file separator")
	}
	file, rest := line[:colon], line[colon+1:]

	// "<positions> <numStmt> <count>". Split from the right, because the
	// positions are one field and the two counters are the last two.
	fields := strings.Fields(rest)
	if len(fields) != 3 {
		return Block{}, malformed(number, strconv.Quote(line)+
			" is not a block record: expected <line>.<col>,<line>.<col> <statements> <count>")
	}

	start, end, ok := strings.Cut(fields[0], ",")
	if !ok {
		return Block{}, malformed(number, strconv.Quote(line)+" is not a block record: no comma between the positions")
	}
	startLine, startCol, err := parsePosition(start, line, number)
	if err != nil {
		return Block{}, err
	}
	endLine, endCol, err := parsePosition(end, line, number)
	if err != nil {
		return Block{}, err
	}
	numStmt, err := parseCount(fields[1], "statement count", line, number)
	if err != nil {
		return Block{}, err
	}
	count, err := parseCount(fields[2], "execution count", line, number)
	if err != nil {
		return Block{}, err
	}
	// A block that ends before it begins would make every interval test below
	// answer wrongly, silently, and only for the mutants that happen to sit
	// near it.
	if endLine < startLine {
		return Block{}, malformed(number, strconv.Quote(line)+" ends on line "+strconv.Itoa(endLine)+
			", before it starts on line "+strconv.Itoa(startLine))
	}
	return Block{
		File:      file,
		StartLine: startLine,
		StartCol:  startCol,
		EndLine:   endLine,
		EndCol:    endCol,
		NumStmt:   numStmt,
		Count:     count,
	}, nil
}

// parsePosition reads a "<line>.<column>" pair. Both are 1-based in the format,
// so zero is refused rather than accepted as a coordinate no editor could show.
func parsePosition(position, line string, number int) (int, int, error) {
	text, column, ok := strings.Cut(position, ".")
	if !ok {
		return 0, 0, malformed(number, strconv.Quote(line)+" is not a block record: "+
			strconv.Quote(position)+" is not a <line>.<column> position")
	}
	lineNumber, err := strconv.Atoi(text)
	if err != nil || lineNumber < 1 {
		return 0, 0, malformed(number, strconv.Quote(line)+" is not a block record: "+
			strconv.Quote(text)+" is not a line number")
	}
	columnNumber, err := strconv.Atoi(column)
	if err != nil || columnNumber < 1 {
		return 0, 0, malformed(number, strconv.Quote(line)+" is not a block record: "+
			strconv.Quote(column)+" is not a column number")
	}
	return lineNumber, columnNumber, nil
}

// parseCount reads one of the two trailing counters, which are non-negative.
func parseCount(text, what, line string, number int) (int, error) {
	value, err := strconv.Atoi(text)
	if err != nil || value < 0 {
		return 0, malformed(number, strconv.Quote(line)+" is not a block record: "+
			strconv.Quote(text)+" is not a "+what)
	}
	return value, nil
}

// malformed builds the parse failure, naming the line it happened on. Line 0
// means "the document as a whole", which is the one complaint that is not about
// a particular line.
func malformed(number int, what string) error {
	if number <= 0 {
		return &Error{Code: CodeMalformedProfile, Message: what}
	}
	return &Error{
		Code:    CodeMalformedProfile,
		Message: "coverage profile line " + strconv.Itoa(number) + ": " + what,
	}
}
