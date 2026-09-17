// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testlog reads the action log a Go test binary writes when it is given.
package testlog

import (
	"bytes"
	"errors"
	"io"
	"strings"
)

const Header = "# test log\n"

type Op string

const (
	OpGetenv Op = "getenv"
	OpOpen   Op = "open"
	OpStat   Op = "stat"
	OpChdir  Op = "chdir"
)

type Entry struct {
	Op   Op
	Name string
}

type Log struct {
	Entries  []Entry
	Complete bool
}

var ErrNoHeader = errors.New("the file does not begin with the test action log header")

var ErrUnsupported = errors.New("the test binary does not accept -test.testlogfile")

func Parse(r io.Reader) (Log, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Log{}, err
	}
	body, ok := bytes.CutPrefix(data, []byte(Header))
	if !ok {
		return Log{}, ErrNoHeader
	}
	log := Log{Complete: data[len(data)-1] == '\n'}
	for len(body) > 0 {
		line, rest, terminated := bytes.Cut(body, []byte("\n"))
		if !terminated {
			break
		}
		body = rest
		if len(line) == 0 {
			continue
		}
		op, name, _ := strings.Cut(string(line), " ")
		log.Entries = append(log.Entries, Entry{Op: Op(op), Name: name})
	}
	return log, nil
}
