// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

type failingMarkerFile struct {
	writeErr error
	syncErr  error
	closeErr error
	closes   int
}

func (file *failingMarkerFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}

func (file *failingMarkerFile) Sync() error { return file.syncErr }

func (file *failingMarkerFile) Close() error {
	file.closes++
	return file.closeErr
}

func TestWriteAndSyncClosesTheMarkerWhateverStageRefusedIt(t *testing.T) {
	t.Parallel()
	failure := errors.New("stage failure")
	closeFailure := errors.New("close failure")
	for _, test := range []struct {
		name string
		file failingMarkerFile
		want []error
	}{
		{name: "every stage completes"},
		{name: "the write fails", file: failingMarkerFile{writeErr: failure}, want: []error{failure}},
		{name: "the sync fails", file: failingMarkerFile{syncErr: failure}, want: []error{failure}},
		{name: "the close fails", file: failingMarkerFile{closeErr: closeFailure}, want: []error{closeFailure}},
		{
			name: "the sync and the close fail",
			file: failingMarkerFile{syncErr: failure, closeErr: closeFailure},
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

func TestAcquireCarriesWhatTheLockAttemptAnswered(t *testing.T) {
	t.Parallel()
	failure := errors.New("cannot ask for the lock")
	for _, test := range []struct {
		name string
		held bool
		err  error
		want bool
	}{
		{name: "the lock is taken", held: true, want: true},
		{name: "somebody else holds it"},
		{name: "the attempt itself failed", err: failure},
		{name: "held and failed at once", held: true, err: failure, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), LockName)
			file, held, err := acquireWith(path, func(*os.File) (bool, error) { return test.held, test.err })
			if held != test.want {
				t.Fatalf("held = %t, want %t", held, test.want)
			}
			if test.err == nil && test.held {
				if file == nil || err != nil {
					t.Fatalf("acquire = (%v, %v), want the locked file", file, err)
				}
				_ = release(file)
				return
			}
			if file != nil || !errors.Is(err, test.err) && test.err != nil {
				t.Fatalf("acquire = (%v, %v), want no file and %v", file, err, test.err)
			}
		})
	}
}

func TestAcquireReportsALockItCannotOpen(t *testing.T) {
	t.Parallel()
	file, held, err := acquireWith(filepath.Join(t.TempDir(), "absent", LockName), func(*os.File) (bool, error) {
		t.Fatal("a lock that could not be opened was asked for anyway")
		return false, nil
	})
	if err == nil || held || file != nil {
		t.Fatalf("acquire in a directory that is not there = (%v, %t, %v)", file, held, err)
	}
}

func TestClaimReportsAMarkerItCannotWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(MarkerPath(dir), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	owner, err := Claim(dir, Marker{RunID: "run"}, time.Now())
	if err == nil || owner != nil || !strings.Contains(err.Error(), "marking ") {
		t.Fatalf("Claim over an occupied marker = (%v, %v)", owner, err)
	}
	second, err := Claim(dir, Marker{RunID: "run"}, time.Now())
	if err == nil || second != nil || !strings.Contains(err.Error(), "marking ") {
		t.Fatalf("the failed claim kept its lock: (%v, %v)", second, err)
	}
}

func TestKeepReportsAMarkerItCannotRewriteAndStillReleases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	owner, err := Claim(dir, Marker{RunID: "run"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(MarkerPath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(MarkerPath(dir), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := owner.Keep(); err == nil || !strings.Contains(err.Error(), "keeping ") {
		t.Fatalf("Keep over an occupied marker = %v", err)
	}
	second, err := Claim(dir, Marker{RunID: "run"}, time.Now())
	if err == nil || second != nil || !strings.Contains(err.Error(), "marking ") {
		t.Fatalf("a failed Keep kept its lock: (%v, %v)", second, err)
	}
}

func TestNoOwnerAnswersForItself(t *testing.T) {
	t.Parallel()
	var absent *Owner
	if got := absent.Dir(); got != "" {
		t.Fatalf("Dir of no owner = %q", got)
	}
	if err := absent.Release(); err != nil {
		t.Fatalf("Release of no owner = %v", err)
	}
	if err := absent.Keep(); err != nil {
		t.Fatalf("Keep of no owner = %v", err)
	}
}

func TestAnOwnerNamesTheDirectoryItHolds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	owner, err := Claim(dir, Marker{RunID: "run"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Release() }()
	if got := owner.Dir(); got != dir {
		t.Fatalf("Dir = %q, want %q", got, dir)
	}
}

func TestKeptByCarriesTheFailureItMetAndTheAnswerItDidNot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(MarkerPath(dir), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	kept, err := KeptBy(dir, "run")
	if err == nil || kept || strings.Contains(err.Error(), "reading the marker of") {
		t.Fatalf("KeptBy over a directory = (%t, %v), want the failure the operating system gave", kept, err)
	}
}

func TestReadMarkerCarriesWhatItCouldNotRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(MarkerPath(dir), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	marker, err := ReadMarker(dir)
	if err == nil || errors.Is(err, fs.ErrNotExist) || marker != (Marker{}) {
		t.Fatalf("ReadMarker over a directory = (%+v, %v)", marker, err)
	}
}

func TestSweepReportsAParentItCannotRead(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("not a directory"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	result, err := Sweep(parent, []string{"goatest-run-"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "reading "+parent) {
		t.Fatalf("Sweep over a file = (%+v, %v)", result, err)
	}
}

func TestSweepOfNoParentAtAllIsNotAFailure(t *testing.T) {
	t.Parallel()
	result, err := Sweep("", []string{"goatest-run-"}, time.Now())
	if err != nil || result.Live != 0 || len(result.Removed) != 0 {
		t.Fatalf("Sweep of no parent = (%+v, %v)", result, err)
	}
}

func TestAnUnclaimedDirectoryIsSparedUntilItIsOlderThanThePatience(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		age  time.Duration
		want verdict
	}{
		{name: "younger than the patience", age: UnclaimedMaxAge - time.Second, want: verdictSpared},
		{name: "exactly the patience", age: UnclaimedMaxAge, want: verdictAbandoned},
		{name: "older than the patience", age: UnclaimedMaxAge + time.Second, want: verdictAbandoned},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parent := t.TempDir()
			dir := filepath.Join(parent, "goatest-run-1")
			if err := os.MkdirAll(dir, filemode.ReadableDirectory); err != nil {
				t.Fatal(err)
			}
			modified := now.Add(-test.age)
			if err := os.Chtimes(dir, modified, modified); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			got, err := sweeper{now: now}.unclaimed(dir, entries[0])
			if err != nil || got != test.want {
				t.Fatalf("unclaimed = (%d, %v), want %d", got, err, test.want)
			}
		})
	}
}

func TestReadMarkerRefusesADocumentItCannotDecode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(MarkerPath(dir), []byte("{"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	marker, err := ReadMarker(dir)
	if err == nil || marker != (Marker{}) {
		t.Fatalf("ReadMarker of a torn document = (%+v, %v)", marker, err)
	}
}

func TestWriteMarkerReportsADirectoryItCannotWriteInto(t *testing.T) {
	t.Parallel()
	err := writeMarker(filepath.Join(t.TempDir(), "absent"), Marker{RunID: "run"})
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("writeMarker into a directory that is not there = %v", err)
	}
}

func TestSweepCarriesTheRemovalItCouldNotMake(t *testing.T) {
	t.Parallel()
	failure := errors.New("removal refused")
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	parent := t.TempDir()
	dir := filepath.Join(parent, "goatest-run-1")
	if err := os.MkdirAll(dir, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * UnclaimedMaxAge)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := sweeper{now: now, remove: func(string) error { return failure }}.sweep(parent, []string{"goatest-run-"})
	if err != nil || len(result.Removed) != 0 || len(result.Errors) != 1 || !errors.Is(result.Errors[0], failure) {
		t.Fatalf("sweep with a refused removal = (%+v, %v)", result, err)
	}
}

func TestSizeCountsTheFilesOfADirectoryAndNotTheDirectoriesThemselves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "file"), make([]byte, markerFileBytes), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if got := Size(dir); got != markerFileBytes {
		t.Fatalf("Size = %d, want the one file counted and no directory with it", got)
	}
}

const markerFileBytes = 7
