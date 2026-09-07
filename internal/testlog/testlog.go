// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testlog reads the action log a Go test binary writes when it is
// given `-test.testlogfile`.
//
// The format is the testing package's, and it is not documented anywhere a
// consumer can point at: the flag's own help text says "for use only by
// cmd/go". So the shape is stated here, from the two ends of it in the Go
// source — testing/internal/testdeps writes it and cmd/go/internal/test reads
// it — and this package is where go-mutants' copy of that knowledge lives.
//
//	# test log\n          the header, written once when the log is opened
//	getenv GO_MUTANTS\n    one line per action: an operation, a space, a name
//	open /tmp/x\n
//	stat /tmp/y\n
//	chdir /tmp\n
//
// The four operations are the ones package os reports through
// internal/testlog: a variable that was read, a file that was opened or
// stat-ed, and a directory a process moved into. The name is everything after
// the first space and is written verbatim — the testing package drops a name
// that is empty or contains a newline rather than escaping it, so a line is
// always one action and a log is always readable line by line.
//
// Nothing here resolves or interprets a name. A relative path is relative to
// whatever the binary's working directory was, an environment variable name is
// a name, and what any of it means is the caller's question.
package testlog

import (
	"bytes"
	"errors"
	"io"
	"strings"
)

// Header is the first line of every test action log.
//
// testing/internal/testdeps writes it when the log is opened, before any test
// code runs, and cmd/go refuses a file that does not begin with it. It is the
// difference between a log a binary wrote and a file that happens to be in the
// way.
const Header = "# test log\n"

// An Op is one action a test binary reported.
//
// The vocabulary is package os's and is open the way [Parse] treats it: an
// operation this build has never heard of is kept exactly as it was written
// rather than dropped, because a Go release that reports a fifth kind of
// access would otherwise be answered with silence — and silence about an input
// reads as an input that was never consulted.
type Op string

// The operations testing/internal/testdeps writes today.
const (
	// OpGetenv is an environment variable the target read.
	OpGetenv Op = "getenv"
	// OpOpen is a file the target opened.
	OpOpen Op = "open"
	// OpStat is a file the target asked about without opening.
	OpStat Op = "stat"
	// OpChdir is a directory the target moved into. Every relative name after
	// it is relative to that directory, which is the one piece of state a
	// reader of the log has to carry.
	OpChdir Op = "chdir"
)

// An Entry is one line of the log: what was done, and to what.
//
// Name is verbatim. It may be relative, it may name a file that no longer
// exists, and it may name something outside the module — cmd/go decides which
// of those matter for its own cache, and this package decides nothing.
type Entry struct {
	Op   Op
	Name string
}

// A Log is one file's worth of entries.
type Log struct {
	// Entries are the actions the binary reported, in the order it reported
	// them. It is nil for a log that recorded none, which is a fact: a target
	// that consulted nothing at all consults nothing whatever changes around
	// it.
	Entries []Entry
	// Complete reports what the bytes can say: the header is there and the last
	// line is terminated. It is the test cmd/go applies to a file.
	//
	// It is half of the question and never the whole of it. The testing package
	// writes through a 4096-byte bufio.Writer that flushes whenever it fills,
	// as well as from the deferred m.after() — so a chatty binary that was
	// killed, that panicked past that call, or that called os.Exit leaves a log
	// ending at a line boundary that is a *prefix* of what it touched, and
	// nothing in the file says so. Only the caller knows how the process ended,
	// so only the caller can finish the test; internal/execute does exactly
	// that before it reports a log as complete to anybody.
	Complete bool
}

// ErrNoHeader reports a file that is not a test action log at all: it does not
// begin with [Header].
//
// It is the answer for an empty file too, and that is the ordinary case rather
// than a corner: the testing package creates the file when it parses its flags
// and writes the header into a buffer, so a binary killed before its first
// flush leaves a file that exists and holds nothing.
var ErrNoHeader = errors.New("the file does not begin with the test action log header")

// ErrUnsupported reports a target that does not accept `-test.testlogfile`.
//
// No standard Go test binary does that: the flag is the testing package's own,
// and go-mutants runs binaries it compiled itself. The sentinel exists for what
// the refusal *looks* like rather than for how likely it is — the standard flag
// package prints "flag provided but not defined" and exits 2, and exit 2 is a
// non-zero status, which is indistinguishable from a failing suite unless
// somebody looks. This is that look, so that a status of 2 can never be scored
// as a detection.
var ErrUnsupported = errors.New("the test binary does not accept -test.testlogfile")

// Parse reads one action log.
//
// A file with no header is [ErrNoHeader] and no log at all: the alternative
// would be to hand back an empty measurement, which is the same value as "this
// target touched nothing" and is what a consumer acts on.
//
// A final line the binary never terminated is dropped and reported through
// [Log.Complete]. Keeping it would present a truncated name as a name — a path
// cut off half way through is a different path, and it is exactly the input a
// consumer would then go and hash.
func Parse(r io.Reader) (Log, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Log{}, err
	}
	body, ok := bytes.CutPrefix(data, []byte(Header))
	if !ok {
		return Log{}, ErrNoHeader
	}
	// The header itself ends in a newline, so a log holding nothing but the
	// header is complete and empty: a binary that ran to the end and consulted
	// nothing.
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
