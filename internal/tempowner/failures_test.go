// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// What this package does when the filesystem refuses.
//
// Every one of these is an error path the ordinary tests never reach, and every
// one of them decides something: whether a claim that could not be marked
// leaves a lock behind, whether a sweep that could not read a directory deletes
// it anyway, whether a failure is reported or swallowed. A package whose whole
// job is to decide what may be deleted has to be measured on the paths where it
// cannot see.
//
// The refusals are made with permissions rather than with a fake filesystem,
// because the question is what the operating system does: a mock would be a
// second implementation of the thing under test. Each one probes for its own
// enforcement and skips where a platform or a user is not stopped by it.
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

// readOnlyDir makes dir refuse new files, and skips the test where it cannot.
//
// A directory that refuses creation is how every "the marker could not be
// written" case below is produced. Root ignores the mode, and Windows does not
// express this permission at all, so both are skipped rather than asserted
// against -- a test that passed because nothing was enforced would be a test
// that proved nothing.
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

	// Proof that the mode is enforced here, before anything depends on it.
	probe := filepath.Join(dir, "probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err == nil {
		_ = os.Remove(probe)
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}
}

// readOnlyFile makes one existing file refuse writes, and skips the test where
// it cannot. It is [readOnlyDir]'s argument applied to a file that is already
// there, which a read-only directory does not cover.
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

// TestClaimSaysWhichHalfOfItFailed pins the two failures a claim can have, and
// they are different failures with different consequences.
//
// A lock that cannot be taken leaves nothing behind: there is no directory
// state to undo. A marker that cannot be written leaves a lock that must be
// released, because a claim that returned an error and kept the lock would make
// the directory immortal -- every later sweep would read it as live.
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
		// The lock file has to exist before the directory refuses creation, or
		// the lock is the half that fails and the marker is never reached.
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
		// And the lock is gone, which is the half a reader cannot see in the
		// error: a second claim would be refused by a lock nobody holds.
		_, held, err = Acquire(LockPath(dir))
		if err != nil {
			t.Fatalf("re-acquiring after a failed claim: %v", err)
		}
		if !held {
			t.Error("a claim that could not write its marker kept the lock")
		}
	})
}

// TestKeepReportsAFailureAndStillReleases is the same shape one level on.
//
// `Keep` is what a run calls when it wants the directory to survive, so a
// failure here is a directory that will be swept later. Releasing anyway is the
// decision: a lock held by a process that has given up is worse than a
// directory that is collected, because nothing will ever collect it.
func TestKeepReportsAFailureAndStillReleases(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	owner, err := Claim(dir, time.Now())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// The marker file itself, not the directory: Claim has already written it,
	// and a read-only *directory* still admits a write to a file that is
	// already in it.
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

// TestReadMarkerRefusesWhatIsNotOne keeps the two ways a marker is unreadable
// apart, because the sweep branches on exactly that difference: a marker that
// is *absent* sends a directory to the legacy age rule, and a marker that is
// *malformed* does not.
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

// TestSweepOfAParentItCannotReadIsAFailureRatherThanAnEmptyResult is the
// difference between "there is nothing here" and "I could not look".
//
// A missing parent is the first and is reported as an empty sweep, which is
// what the ordinary test pins. A parent that exists and cannot be read is the
// second, and a sweep that reported it as empty would tell a caller its
// temporary directories were already gone.
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

// TestSweepSparesADirectoryWhoseLockItCannotTake is the fail-closed rule the
// whole package exists for: what cannot be proved dead is left alone.
//
// A lock file that cannot be opened is not evidence of anything. The sweep
// reports the failure and spares the directory, because the alternative --
// treating "I could not ask" as "nobody answered" -- is how a running
// workspace gets deleted.
func TestSweepSparesADirectoryWhoseLockItCannotTake(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	dir := filepath.Join(parent, "go-mutants-unlockable")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("making the directory: %v", err)
	}
	// A marker that is present and does not say kept, so the verdict reaches
	// the lock rather than the legacy age rule.
	if err := os.WriteFile(MarkerPath(dir), []byte(`{"schema":1,"pid":1}`), 0o600); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}
	// A *directory* where the lock file goes: os.OpenFile refuses it, which is
	// the "the filesystem would not answer" case without a permission trick.
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

// TestTheLegacyRuleIsAnAgeAndTheBoundaryBelongsToTheOld pins the one comparison
// a directory with no marker is judged by.
//
// The rule spares what is *younger* than [LegacyMaxAge], so a directory exactly
// one cut-off old is collected. Which side of the line the boundary falls on is
// the only thing the comparison decides, and it is invisible from anywhere
// else: `<` and `<=` agree about every other age there is.
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
			// The clock is derived from the directory rather than the other
			// way round, and both halves of that matter. A fixed `now` keeps
			// the age from being the age plus however long the test took to
			// get here; reading the modification time *back* keeps it from
			// being the age plus whatever the filesystem rounded away, which on
			// a second-granularity one is up to a second. The boundary is the
			// whole subject, so an age that is approximately right is an age
			// that tests the other case.
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

// TestAcquireKeepsTheThreeAnswersApart is the contract the whole sweep rests
// on, asserted at the one function that produces it.
//
// "I took the lock", "somebody else holds it" and "the filesystem would not
// answer" are three different answers and only the first two are facts about an
// owner. A caller that read the third as the second would delete a running
// workspace; a caller that read it as the first would take a lock it does not
// hold.
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
		// The file is closed on the way out, which is what stops a refused
		// acquire from leaking a descriptor per swept directory.
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

// TestReleaseCarriesUpAnUnlockFailure is the other syscall, and the reason it
// is reported rather than swallowed.
//
// The close that follows drops the lock whatever the unlock did, so a caller
// could be told nothing went wrong and would usually be right. It is told
// anyway: an unlock that fails is a kernel disagreeing with this package about
// a descriptor it holds, and a run that swallowed that would be a run whose
// next sweep reads a directory nobody can explain.
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
	// And it is still idempotent: the second release has nothing to unlock and
	// says so with nil, whatever the first one reported.
	if err := lock.Release(); err != nil {
		t.Errorf("the second Release = %v, want nil", err)
	}
}

// TestCloseAfterPrefersTheCauseItWasGiven pins the rule the two syscall paths
// share, at the function that decides it.
//
// A close failure is real and is reported when there is nothing else to report.
// A cause wins over it, because the cause is what went wrong and the close is
// what this package did about it -- and a caller handed the close failure
// instead would be told the descriptor was the problem.
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

// TestLegacyReportsAnEntryItCannotStat is the directory that went away between
// the listing and the question about it.
//
// A sweep reads a directory's entries and then asks each one how old it is, and
// a run finishing in between is ordinary rather than exceptional. Vanished is
// spared and silent -- there is nothing left to remove and nothing to report --
// and every other failure is spared and reported, because "I could not ask" is
// never "nobody answered".
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

// TestDirectorySizeAnswersForATreeItCannotWalk keeps a measurement from being a
// reason not to reclaim a directory.
//
// The number is for a log line. A directory that cannot be walked at all, or a
// file that cannot be stat-ed inside one, is an answer of zero rather than a
// failure -- and never a panic, which is what a walk that read its error as a
// live entry would produce.
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

// openThenClose is a file that is already closed, so that closing it again
// fails the way a descriptor the operating system has taken back does.
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

// A failingEntry is an fs.DirEntry that cannot say anything about itself, which
// is what a directory removed between the listing and the question looks like.
type failingEntry struct{ err error }

func (e failingEntry) Name() string               { return "go-mutants-gone" }
func (e failingEntry) IsDir() bool                { return true }
func (e failingEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e failingEntry) Info() (fs.FileInfo, error) { return nil, e.err }

// TestClaimRefusesAClockItCannotWriteDown is the marker's own encoding failure,
// and the reason the lock does not outlive it.
//
// A Marker is four fields and three of them cannot fail to encode. The fourth
// is a time.Time, and RFC 3339 -- which is what encoding/json writes one as --
// has no year outside [0,9999], so a caller handing Claim a clock that far out
// gets an error from json.Marshal rather than a file. That is the only way this
// package's marshal can fail, and it is worth having a test for precisely
// because the failure arrives *between* the lock being taken and the marker
// being written: a Claim that returned the error and kept the lock would leave
// a directory that every later sweep reads as live and nothing ever releases.
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

	// And the lock went back. A second claim from this same process is the
	// proof: flock is per open file description, so a descriptor the first
	// claim had left open would refuse this one with ErrOwned.
	second, err := Claim(dir, time.Now())
	if err != nil {
		t.Fatalf("the claim after it = %v, want the lock to have been released", err)
	}
	t.Cleanup(func() { _ = second.Release() })
}

// TestDirectorySizeSkipsAFileItCannotStat is the other half of
// [TestDirectorySizeAnswersForATreeItCannotWalk]: not a tree the walk cannot
// enter, but one file inside a tree it can.
//
// A directory that is readable and not searchable is exactly that shape --
// os.ReadDir lists the names and lstat of any of them is refused -- and it is
// the case where reading the error as a live entry costs more than a wrong
// number: fs.DirEntry.Info returns a nil FileInfo beside its error, so a size
// added up without checking would panic inside the walk of a directory this
// package is about to reclaim.
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

	// Four, not twelve: the readable file is counted and the one that cannot be
	// stat-ed is passed over, which is what "best effort" has to mean here.
	if got := directorySize(dir); got != 4 {
		t.Errorf("directorySize = %d, want the one file it could stat", got)
	}
}

// unsearchableDir makes dir list its names and refuse to stat any of them, and
// skips the test where it cannot.
//
// It is [readOnlyDir]'s argument for the other permission bit: read without
// execute is what separates "which files are here" from "what is this file",
// and it is the only way to make fs.DirEntry.Info fail without racing a file
// removal.
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

	// Proof that both halves are enforced here, before anything depends on
	// either: the names are still readable, and stat-ing one is refused.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Skipf("this filesystem does not list an unsearchable directory: %v", err)
	}
	if _, err := entries[0].Info(); err == nil {
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}
}

// TestSweepSparesADirectoryWhoseLockItCannotRelease is the second half of the
// fail-closed rule [TestSweepSparesADirectoryWhoseLockItCannotTake] states.
//
// The lock is released *before* the removal rather than after it, because on
// Windows the open handle inside a directory is itself what would refuse the
// delete. That ordering puts one more syscall between "this directory is
// abandoned" and "remove it", and the answer to it has to be read the same way
// as the first: a lock this package cannot give back is a lock it does not know
// the state of, and a directory it does not know the state of is not one to
// delete. The failure is reported, the directory stays, and nothing is counted
// -- a spared directory is not a live one, and saying otherwise would put a
// number in Result that nothing on disk backs up.
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
