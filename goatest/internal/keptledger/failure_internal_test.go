// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package keptledger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/advisorylock"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const lockAttemptBudget = 4 * int(lockPatience/lockPoll)

func ledgerEntry(path string, keptAt time.Time) Entry {
	return Entry{Path: path, RunID: "run", KeptAt: keptAt, Bytes: 1}
}

func occupiedPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(path, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReportsAPathItCannotRead(t *testing.T) {
	t.Parallel()
	path := occupiedPath(t, "kept-temp-v1.json")
	ledger, err := Load(path)
	if err == nil || ledger.Schema != "" || strings.Contains(err.Error(), "goatest: read ") {
		t.Fatalf("Load of a directory = (%+v, %v), want the failure the operating system gave", ledger, err)
	}
}

func TestUpdateCarriesEveryFailureOfTheStepsItTakes(t *testing.T) {
	t.Parallel()
	mutationFailure := errors.New("the caller refused the ledger")
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T) string
		mutate  func(*Ledger) error
		message string
	}{
		{
			name: "a directory it cannot make",
			prepare: func(t *testing.T) string {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, ".goatest"), []byte("file"), filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(root, ".goatest", FileName)
			},
			message: filepath.Join(".goatest") + ": ",
		},
		{
			name: "a lock it cannot open",
			prepare: func(t *testing.T) string {
				root := t.TempDir()
				path := filepath.Join(root, FileName)
				if err := os.MkdirAll(path+lockSuffix, filemode.ReadableDirectory); err != nil {
					t.Fatal(err)
				}
				return path
			},
			message: "is a directory",
		},
		{
			name: "a ledger it cannot read",
			prepare: func(t *testing.T) string {
				root := t.TempDir()
				path := filepath.Join(root, FileName)
				if err := os.WriteFile(path, []byte("{"), filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
				return path
			},
			message: "read ",
		},
		{
			name:    "a caller that refuses",
			prepare: func(t *testing.T) string { return filepath.Join(t.TempDir(), FileName) },
			mutate:  func(*Ledger) error { return mutationFailure },
			message: mutationFailure.Error(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutate := test.mutate
			if mutate == nil {
				mutate = func(*Ledger) error { return nil }
			}
			err := Update(test.prepare(t), mutate)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Update = %v, want %q", err, test.message)
			}
		})
	}
}

func TestUpdateWritesNothingOnlyWhenItStartedAndFinishedEmpty(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		recorded []Entry
		mutate   func(*Ledger) error
		wantFile bool
	}{
		{name: "empty before and after", mutate: func(*Ledger) error { return nil }},
		{
			name: "empty before, written after",
			mutate: func(ledger *Ledger) error {
				ledger.Entries = []Entry{ledgerEntry("/tmp/a", time.Unix(1, 0))}
				return nil
			},
			wantFile: true,
		},
		{
			name:     "written before, empty after",
			recorded: []Entry{ledgerEntry("/tmp/a", time.Unix(1, 0))},
			mutate:   func(ledger *Ledger) error { ledger.Entries = nil; return nil },
			wantFile: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), FileName)
			if len(test.recorded) != 0 {
				if err := Save(path, Ledger{Entries: test.recorded}); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := Append(path, test.recorded...); err != nil {
					t.Fatal(err)
				}
			}
			if err := Update(path, test.mutate); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(path)
			if written := err == nil; written != test.wantFile {
				t.Fatalf("ledger written = %t, want %t (%v)", written, test.wantFile, err)
			}
		})
	}
}

func TestLockGivesUpAfterExactlyItsPatience(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	attempts := 0
	waits := 0
	release, err := lockWithWait(path, func(time.Duration) {
		waits++
		if waits > lockAttemptBudget {
			t.Fatalf("the lock waited %d times without giving up", waits)
		}
	}, func(*os.File) (bool, error) {
		attempts++
		return false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "is held by another process") {
		if release != nil {
			release()
		}
		t.Fatalf("lock over a permanently held file = %v", err)
	}
	if want := int(lockPatience/lockPoll) + 1; attempts != want {
		t.Fatalf("lock attempts = %d, want %d", attempts, want)
	}
}

func TestLockReportsAnAttemptItCouldNotMake(t *testing.T) {
	t.Parallel()
	failure := errors.New("cannot ask for the lock")
	path := filepath.Join(t.TempDir(), FileName)
	release, err := lockWithWait(path, func(time.Duration) { t.Fatal("a failed attempt waited") },
		func(*os.File) (bool, error) { return false, failure })
	if !errors.Is(err, failure) {
		if release != nil {
			release()
		}
		t.Fatalf("lock = %v, want %v", err, failure)
	}
}

func TestLockReleasesWhatItTook(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	release, err := lockWithWait(path, func(time.Duration) { t.Fatal("an uncontended lock waited") }, advisorylock.Try)
	if err != nil {
		t.Fatal(err)
	}
	release()
	second, err := lockWithWait(path, func(time.Duration) { t.Fatal("the released lock was still held") }, advisorylock.Try)
	if err != nil {
		t.Fatal(err)
	}
	second()
}

func TestSaveWritesAnEmptyLedgerAsAnEmptyList(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, Ledger{}); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(stored)); got != `{"schema":"`+Schema+`","entries":[]}` {
		t.Fatalf("stored empty ledger = %s", got)
	}
}

func TestSaveReportsADirectoryItCannotMake(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nested"), []byte("file"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	err := Save(filepath.Join(root, "nested", FileName), Ledger{})
	if err == nil || !strings.Contains(err.Error(), "nested: ") {
		t.Fatalf("Save under a file = %v, want the directory it could not make named", err)
	}
}

type failingLedgerFile struct {
	writeErr error
	syncErr  error
	closeErr error
	closes   int
}

func (file *failingLedgerFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *failingLedgerFile) Sync() error { return file.syncErr }

func (file *failingLedgerFile) Close() error {
	file.closes++
	return file.closeErr
}

func TestWriteAndSyncClosesTheFileWhateverStageRefusedIt(t *testing.T) {
	t.Parallel()
	failure := errors.New("stage failure")
	closeFailure := errors.New("close failure")
	for _, test := range []struct {
		name string
		file failingLedgerFile
		want []error
	}{
		{name: "every stage completes"},
		{name: "the write fails", file: failingLedgerFile{writeErr: failure}, want: []error{failure}},
		{name: "the sync fails", file: failingLedgerFile{syncErr: failure}, want: []error{failure}},
		{name: "the close fails", file: failingLedgerFile{closeErr: closeFailure}, want: []error{closeFailure}},
		{
			name: "the write and the close fail",
			file: failingLedgerFile{writeErr: failure, closeErr: closeFailure},
			want: []error{failure, closeFailure},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := test.file
			err := writeAndSync(&file, []byte("{}\n"))
			for _, want := range test.want {
				if !errors.Is(err, want) {
					t.Fatalf("writeAndSync = %v, want %v", err, want)
				}
			}
			if len(test.want) == 0 && err != nil {
				t.Fatalf("writeAndSync = %v, want no failure", err)
			}
			if file.closes != 1 {
				t.Fatalf("closes = %d, want the file closed exactly once", file.closes)
			}
		})
	}
}

func TestCompareEntriesOrdersByTimeThenPath(t *testing.T) {
	t.Parallel()
	earlier := time.Unix(1, 0).UTC()
	later := time.Unix(2, 0).UTC()
	for _, test := range []struct {
		name   string
		first  Entry
		second Entry
		want   int
	}{
		{name: "earlier first", first: ledgerEntry("/z", earlier), second: ledgerEntry("/a", later), want: -1},
		{name: "later last", first: ledgerEntry("/a", later), second: ledgerEntry("/z", earlier), want: 1},
		{name: "same time, by path", first: ledgerEntry("/a", earlier), second: ledgerEntry("/z", earlier), want: -1},
		{name: "same time and path", first: ledgerEntry("/a", earlier), second: ledgerEntry("/a", earlier)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareEntries(test.first, test.second); got != test.want {
				t.Fatalf("compareEntries = %d, want %d", got, test.want)
			}
		})
	}
}

func TestTheStoredLedgerIsTheDocumentItDecodesBackTo(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	entries := []Entry{ledgerEntry("/tmp/b", time.Unix(2, 0).UTC()), ledgerEntry("/tmp/a", time.Unix(1, 0).UTC())}
	if err := Save(path, Ledger{Entries: entries}); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Ledger
	if err := json.Unmarshal(stored, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Entries) != 2 || decoded.Entries[0].Path != "/tmp/a" {
		t.Fatalf("stored ledger = %+v, want the earliest entry first", decoded)
	}
}
