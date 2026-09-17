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
		name:     "the header alone",
		input:    header,
		complete: true,
	}, {
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
		name:     "a name with a space in it",
		input:    header + "open /tmp/two words.txt\n",
		complete: true,
		entries:  []testlog.Entry{{Op: testlog.OpOpen, Name: "/tmp/two words.txt"}},
	}, {
		name:     "a carriage return is part of the name",
		input:    header + "open /tmp/x\r\n",
		complete: true,
		entries:  []testlog.Entry{{Op: testlog.OpOpen, Name: "/tmp/x\r"}},
	}, {
		name:     "a blank line between actions",
		input:    header + "getenv HOME\n\nopen /tmp/x\n",
		complete: true,
		entries: []testlog.Entry{
			{Op: testlog.OpGetenv, Name: "HOME"},
			{Op: testlog.OpOpen, Name: "/tmp/x"},
		},
	}, {
		name:    "a file that is not a log",
		input:   "PASS\nok  \tfixture.example/killable\t0.01s\n",
		wantErr: testlog.ErrNoHeader,
	}, {
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

func TestParseCarriesUpAReadFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("the file went away")
	log, err := testlog.Parse(failingReader{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("Parse() error = %v, want %v", err, want)
	}
	if log.Complete || len(log.Entries) != 0 {
		t.Errorf("Parse() = %+v beside an error, want nothing at all", log)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
