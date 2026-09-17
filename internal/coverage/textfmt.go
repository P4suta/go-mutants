// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

const modePrefix = "mode: "

const scanBufferLimit = 1 << 20

type Block struct {
	File      string
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
	NumStmt   int
	Count     int
}

func (b Block) Covered() bool { return b.Count > 0 }

type Profile struct {
	Mode   string
	Blocks []Block
}

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

func parseBlock(line string, number int) (Block, error) {
	colon := strings.LastIndexByte(line, ':')
	if colon <= 0 || colon == len(line)-1 {
		return Block{}, malformed(number, strconv.Quote(line)+" is not a block record: no file separator")
	}
	file, rest := line[:colon], line[colon+1:]

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

func parseCount(text, what, line string, number int) (int, error) {
	value, err := strconv.Atoi(text)
	if err != nil || value < 0 {
		return 0, malformed(number, strconv.Quote(line)+" is not a block record: "+
			strconv.Quote(text)+" is not a "+what)
	}
	return value, nil
}

func malformed(number int, what string) error {
	if number <= 0 {
		return &Error{Code: CodeMalformedProfile, Message: what}
	}
	return &Error{
		Code:    CodeMalformedProfile,
		Message: "coverage profile line " + strconv.Itoa(number) + ": " + what,
	}
}
