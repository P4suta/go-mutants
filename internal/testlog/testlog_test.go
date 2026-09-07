// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testlog_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testlog"
)

// TestParseTestLog is the whole of the format, stated as the cases a real
// binary produces.
//
// The four that matter are the four a caller has to be able to tell apart: a
// log a binary finished writing, a log a killed binary left half written, a
// file that is not a log at all, and an operation this build has never heard
// of. The first two differ only in the final newline — which is exactly the
// test cmd/go applies before it trusts one — and the last must survive
// verbatim, because an engine that dropped an unknown operation would be
// answering a question about a Go release it does not know with silence.
func TestParseTestLog(t *testing.T) {
	t.Parallel()

	const header = "# test log\n"

	for _, test := range []struct {
		name     string
		input    string
		wantErr  error
		complete bool
		entries  []testlog.Entry
	}{{
		name:     "every operation the testing package writes",
		input:    header + "getenv HOME\nopen /tmp/x\nstat /tmp/y\nchdir /tmp\n",
		complete: true,
		entries: []testlog.Entry{
			{Op: testlog.OpGetenv, Name: "HOME"},
			{Op: testlog.OpOpen, Name: "/tmp/x"},
			{Op: testlog.OpStat, Name: "/tmp/y"},
			{Op: testlog.OpChdir, Name: "/tmp"},
		},
	}, {
		// A binary that ran to the end and consulted nothing. It is a
		// measurement and not a failure, so it is complete and empty.
		name:     "the header alone",
		input:    header,
		complete: true,
	}, {
		// What a killed or os.Exit-ed binary leaves: testing flushes the log
		// from m.after(), so a tree the supervisor tore down stops mid-line at
		// best. The half-written name is dropped rather than reported, because
		// a truncated path is not a path — and Complete is what says so.
		name:     "a line the binary never finished",
		input:    header + "getenv HOME\nopen /tmp/hal",
		complete: false,
		entries:  []testlog.Entry{{Op: testlog.OpGetenv, Name: "HOME"}},
	}, {
		name:     "an operation this build does not know",
		input:    header + "sniff /tmp/x\n",
		complete: true,
		entries:  []testlog.Entry{{Op: testlog.Op("sniff"), Name: "/tmp/x"}},
	}, {
		// The name is everything after the first space, verbatim. A file whose
		// name contains one is a file, and cutting on the last space instead
		// would rename it.
		name:     "a name with a space in it",
		input:    header + "open /tmp/two words.txt\n",
		complete: true,
		entries:  []testlog.Entry{{Op: testlog.OpOpen, Name: "/tmp/two words.txt"}},
	}, {
		// testing writes "\n" on every platform — the log is not a text file
		// the operating system rewrites — so a carriage return is part of the
		// name and is kept. Stripping it here would silently rename a file
		// somebody really did create with one.
		name:     "a carriage return is part of the name",
		input:    header + "open /tmp/x\r\n",
		complete: true,
		entries:  []testlog.Entry{{Op: testlog.OpOpen, Name: "/tmp/x\r"}},
	}, {
		name:    "a file that is not a log",
		input:   "PASS\nok  \tfixture.example/killable\t0.01s\n",
		wantErr: testlog.ErrNoHeader,
	}, {
		// What a binary that was killed before testing could flush anything
		// leaves behind: the file exists, created by m.before(), and holds
		// nothing at all.
		name:    "an empty file",
		input:   "",
		wantErr: testlog.ErrNoHeader,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			log, err := testlog.Parse(strings.NewReader(test.input))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Parse() error = %v, want %v", err, test.wantErr)
				}
				if log.Complete || len(log.Entries) != 0 {
					t.Errorf("Parse() = %+v beside an error, want nothing at all", log)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if log.Complete != test.complete {
				t.Errorf("Complete = %v, want %v: it is the final newline cmd/go itself checks for",
					log.Complete, test.complete)
			}
			if !slices.Equal(log.Entries, test.entries) {
				t.Errorf("Entries = %+v, want %+v", log.Entries, test.entries)
			}
		})
	}
}

// TestOperationsArePinned writes the four operation names out, because they are
// the ones package os hands the logger and a consumer switches on them.
func TestOperationsArePinned(t *testing.T) {
	t.Parallel()

	for name, pair := range map[string][2]string{
		"OpGetenv": {string(testlog.OpGetenv), "getenv"},
		"OpOpen":   {string(testlog.OpOpen), "open"},
		"OpStat":   {string(testlog.OpStat), "stat"},
		"OpChdir":  {string(testlog.OpChdir), "chdir"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}
	if testlog.Header != "# test log\n" {
		t.Errorf("Header = %q, want the line testing/internal/testdeps writes", testlog.Header)
	}
}
