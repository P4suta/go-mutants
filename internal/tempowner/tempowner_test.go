// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestAcquireRefusesASecondHolderUntilTheFirstReleases is the whole liveness
// signal in one test: the lock, not the marker, is what says a directory still
// belongs to a running process.
//
// It takes the second lock through a second [Acquire] — a second open file — in
// this same process, because that is the case a shared advisory lock would get
// wrong and the case that matters: two concurrent Opens in one program must see
// each other's directories as live.
func TestAcquireRefusesASecondHolderUntilTheFirstReleases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := LockPath(dir)

	first, held, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquiring the lock: %v", err)
	}
	if !held {
		t.Fatal("the first Acquire did not take the lock")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the lock file was not created: %v", statErr)
	}

	second, held, err := Acquire(path)
	if err != nil {
		t.Fatalf("the refused Acquire reported an error: %v", err)
	}
	if held {
		t.Error("a second Acquire took a lock the first still holds")
		_ = second.Release()
	}
	if second != nil {
		t.Error("a refused Acquire returned a lock")
	}

	if err = first.Release(); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
	third, held, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquiring the released lock: %v", err)
	}
	if !held {
		t.Fatal("the lock was not free after Release")
	}
	if err = third.Release(); err != nil {
		t.Errorf("releasing the second holder: %v", err)
	}
	// Release is idempotent so that a Close path may release before removing
	// without tracking whether it already did.
	if err = third.Release(); err != nil {
		t.Errorf("a second Release reported an error: %v", err)
	}
}

// TestClaimWritesTheMarkerAndHoldsTheLock pins the pair a claimed directory
// carries: the lock a sweep tests for liveness, and the JSON a human reads.
func TestClaimWritesTheMarkerAndHoldsTheLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 9, 3, 10, 30, 0, 123456789, time.UTC)

	owner, err := Claim(dir, now)
	if err != nil {
		t.Fatalf("claiming the directory: %v", err)
	}
	if owner.Dir() != dir {
		t.Errorf("Dir() is %q, want %q", owner.Dir(), dir)
	}

	marker := readMarker(t, dir)
	if marker.Schema != Schema {
		t.Errorf("marker schema is %q, want %q", marker.Schema, Schema)
	}
	if marker.PID != os.Getpid() {
		t.Errorf("marker pid is %d, want %d", marker.PID, os.Getpid())
	}
	if !marker.Started.Equal(now) {
		t.Errorf("marker start is %s, want %s", marker.Started, now)
	}
	if marker.Kept {
		t.Error("a freshly claimed directory is marked kept")
	}
	// The JSON is for humans, so the timestamp is pinned as text as well as as
	// a value: a marker whose time cannot be read at a glance is a marker
	// nobody reads.
	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	if want := now.Format(time.RFC3339Nano); !strings.Contains(string(raw), want) {
		t.Errorf("marker %s does not carry the RFC3339Nano start %s", raw, want)
	}

	if _, held, lockErr := Acquire(LockPath(dir)); lockErr != nil || held {
		t.Errorf("the claimed directory's lock was free (held=%v, err=%v)", held, lockErr)
	}
	if err = owner.Release(); err != nil {
		t.Fatalf("releasing the claim: %v", err)
	}
	free, held, err := Acquire(LockPath(dir))
	if err != nil || !held {
		t.Fatalf("the lock was not free after Release (held=%v, err=%v)", held, err)
	}
	if err = free.Release(); err != nil {
		t.Errorf("releasing the test's own lock: %v", err)
	}
}

// TestClaimNamesAnOwnedDirectoryWithErrOwned pins the one failure a caller has
// to tell apart from the rest: a claim that lost to another process's lock is a
// directory that now belongs to that process, and a caller that had just made
// it must not remove it on the way out.
func TestClaimNamesAnOwnedDirectoryWithErrOwned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)

	first, err := Claim(dir, now)
	if err != nil {
		t.Fatalf("claiming the directory: %v", err)
	}
	defer func() {
		if releaseErr := first.Release(); releaseErr != nil {
			t.Errorf("releasing the first claim: %v", releaseErr)
		}
	}()

	second, err := Claim(dir, now.Add(time.Second))
	if err == nil {
		t.Fatal("a second Claim on a held directory succeeded")
	}
	if second != nil {
		t.Errorf("a failed Claim returned an owner: %+v", second)
	}
	if !errors.Is(err, ErrOwned) {
		t.Errorf("the failure is %v, want one that errors.Is ErrOwned", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the failure %q does not name the directory %q", err, dir)
	}
}

// TestKeepMarksTheDirectoryAndReleasesTheLock covers the deliberate keep: the
// directory outlives the process that made it, and says so in a way the next
// run's sweep obeys.
func TestKeepMarksTheDirectoryAndReleasesTheLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	owner, err := Claim(dir, time.Now())
	if err != nil {
		t.Fatalf("claiming the directory: %v", err)
	}
	if err = owner.Keep(); err != nil {
		t.Fatalf("keeping the directory: %v", err)
	}
	if marker := readMarker(t, dir); !marker.Kept {
		t.Error("Keep did not mark the directory kept")
	}
	lock, held, err := Acquire(LockPath(dir))
	if err != nil || !held {
		t.Fatalf("Keep did not release the lock (held=%v, err=%v)", held, err)
	}
	if err = lock.Release(); err != nil {
		t.Errorf("releasing the test's own lock: %v", err)
	}
	if err = owner.Release(); err != nil {
		t.Errorf("releasing a kept owner: %v", err)
	}
}

// TestSweepRemovesOnlyWhatItOwns is the sweep's whole contract in one table.
// Every row is a directory in one parent, swept in one call, because the rules
// only mean anything together: what makes "dead" safe to remove is that "live"
// and "kept" in the same parent are not.
func TestSweepRemovesOnlyWhatItOwns(t *testing.T) {
	parent := t.TempDir()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	// live is held for the whole sweep by a lock this test owns, which is
	// exactly what a running go-mutants process holds.
	live := makeDir(t, parent, "go-mutants-snap-live")
	claimAt(t, live, now.Add(-time.Minute))
	held, ok, err := Acquire(LockPath(live))
	if err != nil || !ok {
		t.Fatalf("holding the live directory's lock (held=%v, err=%v)", ok, err)
	}
	t.Cleanup(func() { _ = held.Release() })

	// dead carries a marker and a lock nobody holds: the owner is gone.
	dead := makeDir(t, parent, "go-mutants-snap-dead")
	claimAt(t, dead, now.Add(-time.Hour))
	writeFile(t, filepath.Join(dead, "payload.bin"), strings.Repeat("x", 512))

	// deadScratch proves the sweep answers to every prefix it is given, not
	// only the first.
	deadScratch := makeDir(t, parent, "go-mutants-api-dead")
	claimAt(t, deadScratch, now.Add(-time.Hour))

	// kept was preserved on purpose and is not the sweep's to remove, however
	// long ago its process exited.
	kept := makeDir(t, parent, "go-mutants-snap-kept")
	owner := claimAt(t, kept, now.Add(-72*time.Hour))
	if err = owner.Keep(); err != nil {
		t.Fatalf("keeping a directory: %v", err)
	}

	// legacy predates the marker entirely: the only thing known about it is
	// that nothing has touched it for a day.
	legacy := makeDir(t, parent, "go-mutants-snap-legacy")
	touch(t, legacy, now.Add(-25*time.Hour))

	// legacyYoung is the same shape a minute old, which is what a run started
	// by an older binary looks like while it is still running.
	legacyYoung := makeDir(t, parent, "go-mutants-snap-legacy-young")
	touch(t, legacyYoung, now.Add(-time.Minute))

	// Neither of these is go-mutants', whatever they are named.
	unrelated := makeDir(t, parent, "someone-elses-work")
	touch(t, unrelated, now.Add(-72*time.Hour))
	prefixedFile := filepath.Join(parent, "go-mutants-snap-notadirectory")
	writeFile(t, prefixedFile, "a file wearing the prefix")

	result, err := Sweep(parent, []string{"go-mutants-snap-", "go-mutants-api-"}, now)
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}

	removed := []string{dead, deadScratch, legacy}
	slices.Sort(removed)
	if got := slices.Clone(result.Removed); !slices.Equal(sorted(got), removed) {
		t.Errorf("Sweep removed %v, want %v", got, removed)
	}
	if result.Live != 1 {
		t.Errorf("Sweep counted %d live directories, want 1", result.Live)
	}
	if result.Kept != 1 {
		t.Errorf("Sweep counted %d kept directories, want 1", result.Kept)
	}
	if result.RemovedBytes < 512 {
		t.Errorf("Sweep reclaimed %d bytes, want at least the 512 it removed", result.RemovedBytes)
	}
	for _, gone := range removed {
		if _, statErr := os.Stat(gone); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("%s survived the sweep (%v)", gone, statErr)
		}
	}
	for _, survivor := range []string{live, kept, legacyYoung, unrelated, prefixedFile} {
		if _, statErr := os.Stat(survivor); statErr != nil {
			t.Errorf("%s did not survive the sweep: %v", survivor, statErr)
		}
	}
}

// TestSweepOfAMissingParentIsEmptyAndSucceeds keeps the first run on a machine
// from failing over a temporary directory nobody has written to yet.
func TestSweepOfAMissingParentIsEmptyAndSucceeds(t *testing.T) {
	t.Parallel()
	result, err := Sweep(filepath.Join(t.TempDir(), "not-there"), []string{"go-mutants-snap-"}, time.Now())
	if err != nil {
		t.Fatalf("sweeping a parent that does not exist: %v", err)
	}
	if len(result.Removed) != 0 || result.RemovedBytes != 0 || result.Live != 0 || result.Kept != 0 {
		t.Errorf("sweeping a missing parent reported %+v, want the zero result", result)
	}
}

// TestSweepReportsEveryFailureAndStillRemovesTheRest pins the loop's shape: a
// directory that cannot be removed is a diagnostic, not a reason to leave the
// next gigabyte on disk.
func TestSweepReportsEveryFailureAndStillRemovesTheRest(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	stubborn := makeDir(t, parent, "go-mutants-snap-stubborn")
	claimAt(t, stubborn, now.Add(-time.Hour))
	removable := makeDir(t, parent, "go-mutants-snap-removable")
	claimAt(t, removable, now.Add(-time.Hour))

	failure := errors.New("device is busy")
	sweep := sweeper{now: now, remove: func(path string) error {
		if path == stubborn {
			return failure
		}
		return os.RemoveAll(path)
	}}
	result, err := sweep.sweep(parent, []string{"go-mutants-snap-"})
	if !errors.Is(err, failure) {
		t.Fatalf("the sweep error is %v, want it to carry %v", err, failure)
	}
	if !strings.Contains(err.Error(), stubborn) {
		t.Errorf("the sweep error %v does not name %s", err, stubborn)
	}
	if !slices.Equal(result.Removed, []string{removable}) {
		t.Errorf("Sweep removed %v, want only %s", result.Removed, removable)
	}
	if _, statErr := os.Stat(stubborn); statErr != nil {
		t.Errorf("the stubborn directory is gone after a failed removal: %v", statErr)
	}
}

// TestSweepIgnoresAMalformedMarker keeps a half-written marker from making a
// dead directory immortal: the lock is the liveness signal, and an unreadable
// marker only means the directory was not deliberately kept.
func TestSweepIgnoresAMalformedMarker(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	now := time.Now()
	broken := makeDir(t, parent, "go-mutants-snap-broken")
	writeFile(t, MarkerPath(broken), "{not json")
	writeFile(t, LockPath(broken), "")

	result, err := Sweep(parent, []string{"go-mutants-snap-"}, now)
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if !slices.Equal(result.Removed, []string{broken}) {
		t.Errorf("Sweep removed %v, want %s", result.Removed, broken)
	}
}

func readMarker(t *testing.T, dir string) Marker {
	t.Helper()
	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	var marker Marker
	if err = json.Unmarshal(raw, &marker); err != nil {
		t.Fatalf("decoding the marker %s: %v", raw, err)
	}
	return marker
}

// claimAt claims dir, releases the lock so that the directory looks like one
// whose process has gone, and returns the owner for a test that wants to keep
// it afterwards.
func claimAt(t *testing.T, dir string, started time.Time) *Owner {
	t.Helper()
	owner, err := Claim(dir, started)
	if err != nil {
		t.Fatalf("claiming %s: %v", dir, err)
	}
	if err = owner.Release(); err != nil {
		t.Fatalf("releasing %s: %v", dir, err)
	}
	return owner
}

func makeDir(t *testing.T, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	return path
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func touch(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("setting the modification time of %s: %v", path, err)
	}
}

func sorted(paths []string) []string {
	slices.Sort(paths)
	return paths
}
