// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func readOnlyDir(t *testing.T, dir string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows directory mode does not refuse file creation the way this test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a write fail")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("making %s read-only: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	probe := filepath.Join(dir, "probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err == nil {
		_ = os.Remove(probe)
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}
}

func readOnlyFile(t *testing.T, path string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows file mode does not refuse a write the way this test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a write fail")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("making %s read-only: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if err := os.WriteFile(path, []byte("probe"), 0o600); err == nil {
		t.Skip("this filesystem does not enforce the file mode this test needs")
	}
}

func TestClaimSaysWhichHalfOfItFailed(t *testing.T) {
	t.Parallel()

	t.Run("the lock cannot be taken", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(t.TempDir(), "not-there")
		owner, err := Claim(missing, time.Now())
		if err == nil {
			t.Fatalf("Claim of a directory that does not exist returned %+v", owner)
		}
		if owner != nil {
			t.Errorf("a failed claim returned an owner: %+v", owner)
		}
		if !strings.Contains(err.Error(), "locking "+missing) {
			t.Errorf("the failure does not say which half failed: %v", err)
		}
	})

	t.Run("the marker cannot be written", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		lock, held, err := Acquire(LockPath(dir))
		if err != nil || !held {
			t.Fatalf("seeding the lock file: held=%v err=%v", held, err)
		}
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Fatalf("releasing the seeded lock: %v", releaseErr)
		}
		readOnlyDir(t, dir)

		owner, err := Claim(dir, time.Now())
		if err == nil {
			t.Fatalf("Claim into a directory that refuses files returned %+v", owner)
		}
		if owner != nil {
			t.Errorf("a failed claim returned an owner: %+v", owner)
		}
		if !strings.Contains(err.Error(), "marking "+dir) {
			t.Errorf("the failure does not say which half failed: %v", err)
		}
		_, held, err = Acquire(LockPath(dir))
		if err != nil {
			t.Fatalf("re-acquiring after a failed claim: %v", err)
		}
		if !held {
			t.Error("a claim that could not write its marker kept the lock")
		}
	})
}

func TestKeepReportsAFailureAndStillReleases(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	owner, err := Claim(dir, time.Now())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	t.Cleanup(func() { _ = owner.Release() })
	readOnlyFile(t, MarkerPath(dir))

	if err := owner.Keep(); err == nil {
		t.Fatal("Keep into a directory that refuses files returned no error")
	} else if !strings.Contains(err.Error(), "keeping "+dir) {
		t.Errorf("the failure does not name what it was doing: %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Errorf("releasing after a failed Keep: %v", err)
	}
}

func TestReadMarkerRefusesWhatIsNotOne(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := ReadMarker(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadMarker of a directory with no marker = %v, want a not-exist error", err)
	}

	if err := os.WriteFile(MarkerPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing a malformed marker: %v", err)
	}
	marker, err := ReadMarker(dir)
	if err == nil {
		t.Fatalf("ReadMarker of a malformed marker returned %+v", marker)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a malformed marker is reported as an absent one: %v", err)
	}
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("the failure is not the decoder's: %v", err)
	}
	if marker != (Marker{}) {
		t.Errorf("a failed read returned %+v, want nothing at all", marker)
	}
}

func TestSweepOfAParentItCannotReadIsAFailureRatherThanAnEmptyResult(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows directory mode does not refuse a listing the way this test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a listing fail")
	}
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("making the parent: %v", err)
	}
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("making the parent unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if entries, err := os.ReadDir(parent); err == nil {
		t.Skipf("this filesystem lists an unreadable directory (%d entries)", len(entries))
	}

	result, err := Sweep(parent, []string{"go-mutants-"}, time.Now())
	if err == nil {
		t.Fatalf("Sweep of an unreadable parent returned %+v and no error", result)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an unreadable parent is reported as a missing one: %v", err)
	}
	if !strings.Contains(err.Error(), "reading "+parent) {
		t.Errorf("the failure does not name the parent: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Errorf("a sweep that could not look removed %v", result.Removed)
	}
}

func TestSweepSparesADirectoryWhoseLockItCannotTake(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	dir := filepath.Join(parent, "go-mutants-unlockable")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("making the directory: %v", err)
	}
	if err := os.WriteFile(MarkerPath(dir), []byte(`{"schema":1,"pid":1}`), 0o600); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}
	if err := os.Mkdir(LockPath(dir), 0o700); err != nil {
		t.Fatalf("making the lock path a directory: %v", err)
	}

	result, err := Sweep(parent, []string{"go-mutants-"}, time.Now())
	if err == nil {
		t.Fatal("Sweep over a directory whose lock it could not take reported no failure")
	}
	if !strings.Contains(err.Error(), "locking "+dir) {
		t.Errorf("the failure does not name what it could not do: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Errorf("the sweep removed %v; a directory it could not ask about is spared", result.Removed)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("the spared directory is gone: %v", statErr)
	}
}

func TestTheLegacyRuleIsAnAgeAndTheBoundaryBelongsToTheOld(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		age     time.Duration
		removed bool
	}{
		{name: "younger than the cut-off", age: LegacyMaxAge - time.Minute},
		{name: "exactly the cut-off", age: LegacyMaxAge, removed: true},
		{name: "older than the cut-off", age: LegacyMaxAge + time.Minute, removed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parent := t.TempDir()
			dir := filepath.Join(parent, "go-mutants-legacy")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("making the directory: %v", err)
			}
			aged := time.Now().Add(-tc.age)
			if err := os.Chtimes(dir, aged, aged); err != nil {
				t.Fatalf("ageing the directory: %v", err)
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("reading the directory back: %v", err)
			}
			now := info.ModTime().Add(tc.age)

			result, err := Sweep(parent, []string{"go-mutants-"}, now)
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if removed := len(result.Removed) == 1; removed != tc.removed {
				t.Errorf("removed = %v, want %v: %+v", removed, tc.removed, result)
			}
		})
	}
}

func TestAcquireKeepsTheThreeAnswersApart(t *testing.T) {
	t.Parallel()

	t.Run("the file cannot be opened", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(t.TempDir(), "not-there", "owner.lock")
		lock, held, err := Acquire(missing)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Acquire of a path under a missing directory = %v, want a not-exist error", err)
		}
		if held {
			t.Error("a lock nothing opened is reported as held")
		}
		if lock != nil {
			t.Error("a failed acquire returned a lock")
		}
	})

	t.Run("the lock cannot be taken", func(t *testing.T) {
		t.Parallel()

		refused := errors.New("this filesystem does not do locks")
		path := filepath.Join(t.TempDir(), "owner.lock")
		lock, held, err := acquire(path,
			func(*os.File) (bool, error) { return false, refused }, unlockAdvisory)
		if !errors.Is(err, refused) {
			t.Fatalf("Acquire = %v, want the syscall's own failure", err)
		}
		if held {
			t.Error("a lock the syscall refused is reported as held")
		}
		if lock != nil {
			t.Error("a failed acquire returned a lock")
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("the lock file is gone: %v", statErr)
		}
	})

	t.Run("somebody else holds it", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "owner.lock")
		first, held, err := Acquire(path)
		if err != nil || !held {
			t.Fatalf("the first acquire: held=%v err=%v", held, err)
		}
		t.Cleanup(func() { _ = first.Release() })

		second, held, err := Acquire(path)
		if err != nil {
			t.Fatalf("a contended acquire is not a failure: %v", err)
		}
		if held {
			t.Error("two holders of one lock")
		}
		if second != nil {
			t.Error("a refused acquire returned a lock")
		}
	})
}

func TestReleaseCarriesUpAnUnlockFailure(t *testing.T) {
	t.Parallel()

	refused := errors.New("the kernel would not unlock it")
	lock, held, err := acquire(filepath.Join(t.TempDir(), "owner.lock"),
		tryAdvisoryLock, func(*os.File) error { return refused })
	if err != nil || !held {
		t.Fatalf("Acquire: held=%v err=%v", held, err)
	}
	if err := lock.Release(); !errors.Is(err, refused) {
		t.Errorf("Release = %v, want the syscall's own failure", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("the second Release = %v, want nil", err)
	}
}

func TestCloseAfterPrefersTheCauseItWasGiven(t *testing.T) {
	t.Parallel()

	cause := errors.New("the thing that actually went wrong")

	closed := openThenClose(t)
	if err := closeAfter(closed, cause); !errors.Is(err, cause) {
		t.Errorf("closeAfter with a cause = %v, want the cause", err)
	}

	closed = openThenClose(t)
	err := closeAfter(closed, nil)
	if err == nil {
		t.Fatal("closeAfter of an already-closed file with no cause returned nil")
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("closeAfter = %v, want the close failure", err)
	}

	open, err := os.CreateTemp(t.TempDir(), "close")
	if err != nil {
		t.Fatalf("opening a file: %v", err)
	}
	if err := closeAfter(open, nil); err != nil {
		t.Errorf("closeAfter of a file that closes cleanly = %v, want nil", err)
	}
}

func TestLegacyReportsAnEntryItCannotStat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		err      error
		reported bool
	}{
		{name: "the directory vanished", err: fs.ErrNotExist},
		{name: "the filesystem refused", err: errors.New("I/O error"), reported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := sweeper{now: time.Now()}.legacy("/tmp/gone", failingEntry{err: tc.err})
			if got != verdictSpared {
				t.Errorf("verdict = %v, want it spared", got)
			}
			if reported := err != nil; reported != tc.reported {
				t.Errorf("reported = %v (%v), want %v", reported, err, tc.reported)
			}
			if tc.reported && !strings.Contains(err.Error(), "reading /tmp/gone") {
				t.Errorf("the failure does not name the directory: %v", err)
			}
		})
	}
}

func TestDirectorySizeAnswersForATreeItCannotWalk(t *testing.T) {
	t.Parallel()

	if got := directorySize(filepath.Join(t.TempDir(), "not-there")); got != 0 {
		t.Errorf("directorySize of a missing tree = %d, want 0", got)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eight"), []byte("12345678"), 0o600); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "inner"), 0o700); err != nil {
		t.Fatalf("making a directory: %v", err)
	}
	if got := directorySize(dir); got != 8 {
		t.Errorf("directorySize = %d, want the one file's eight bytes", got)
	}
}

func openThenClose(t *testing.T) *os.File {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatalf("opening a file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing it: %v", err)
	}
	return file
}

type failingEntry struct{ err error }

func (e failingEntry) Name() string               { return "go-mutants-gone" }
func (e failingEntry) IsDir() bool                { return true }
func (e failingEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e failingEntry) Info() (fs.FileInfo, error) { return nil, e.err }

func TestClaimRefusesAClockItCannotWriteDown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	owner, err := Claim(dir, time.Date(12345, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatalf("Claim with an unwritable clock = %v, want a refusal", owner)
	}
	if owner != nil {
		t.Error("a failed claim returned an owner")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("Claim = %v, want the directory named in it", err)
	}
	if _, statErr := os.Stat(MarkerPath(dir)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a marker was written anyway: %v", statErr)
	}

	second, err := Claim(dir, time.Now())
	if err != nil {
		t.Fatalf("the claim after it = %v, want the lock to have been released", err)
	}
	t.Cleanup(func() { _ = second.Release() })
}

func TestDirectorySizeSkipsAFileItCannotStat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatalf("making a directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inner, "eight"), []byte("12345678"), 0o600); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "four"), []byte("1234"), 0o600); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	unsearchableDir(t, inner)

	if got := directorySize(dir); got != 4 {
		t.Errorf("directorySize = %d, want the one file it could stat", got)
	}
}

func unsearchableDir(t *testing.T, dir string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows directory mode does not separate listing from stat the way this test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a stat fail")
	}
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatalf("making %s unsearchable: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Skipf("this filesystem does not list an unsearchable directory: %v", err)
	}
	if _, err := entries[0].Info(); err == nil {
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}
}

func TestSweepSparesADirectoryWhoseLockItCannotRelease(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	dir := makeDir(t, parent, "go-mutants-snap-stuck")
	claimAt(t, dir, now.Add(-time.Hour))

	refused := errors.New("the kernel would not unlock it")
	sweep := sweeper{
		now: now,
		remove: func(path string) error {
			t.Errorf("%s was removed after its lock would not come back", path)
			return nil
		},
		acquire: func(path string) (*Lock, bool, error) {
			return acquire(path, tryAdvisoryLock, func(*os.File) error { return refused })
		},
	}
	result, err := sweep.sweep(parent, []string{"go-mutants-snap-"})
	if !errors.Is(err, refused) {
		t.Fatalf("the sweep error is %v, want it to carry the unlock's own failure", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the sweep error %v does not name %s", err, dir)
	}
	if len(result.Removed) != 0 || result.Live != 0 || result.Kept != 0 {
		t.Errorf("Result = %+v, want nothing counted for a directory nothing was decided about", result)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("the directory is gone: %v", statErr)
	}
}
