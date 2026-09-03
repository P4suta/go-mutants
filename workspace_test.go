// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants_test

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

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/tempowner"
)

// TestOpenOwnsEveryTemporaryDirectory is the first half of the promise that
// nothing a run writes outlives it: every top-level directory Open creates
// carries a marker saying whose it is and holds a lock saying it is still in
// use, so that another run can tell a live directory from an abandoned one
// without guessing at process ids.
func TestOpenOwnsEveryTemporaryDirectory(t *testing.T) {
	root := copyFixture(t, "simple")
	parent := t.TempDir()

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}

	directories := temporaryDirectories(t, parent)
	if len(directories) != 2 {
		t.Fatalf("Open created %v, want a snapshot directory and a scratch directory", directories)
	}
	for _, directory := range directories {
		marker := readOwnerMarker(t, directory)
		if marker.Schema != tempowner.Schema {
			t.Errorf("%s carries schema %q, want %q", directory, marker.Schema, tempowner.Schema)
		}
		if marker.PID != os.Getpid() {
			t.Errorf("%s names pid %d, want %d", directory, marker.PID, os.Getpid())
		}
		if marker.Kept {
			t.Errorf("%s is marked kept without KeepTemp", directory)
		}
		if lock, held, lockErr := tempowner.Acquire(tempowner.LockPath(directory)); lockErr != nil || held {
			t.Errorf("%s was not locked while the workspace was open (held=%v, err=%v)", directory, held, lockErr)
			_ = lock.Release()
		}
	}

	if err = workspace.Close(); err != nil {
		t.Fatalf("closing workspace: %v", err)
	}
	if entries, readErr := os.ReadDir(parent); readErr != nil || len(entries) != 0 {
		t.Errorf("Close left %v behind (err=%v)", entries, readErr)
	}
	if preserved := workspace.Preserved(); len(preserved) != 0 {
		t.Errorf("Preserved() is %v after a Close that removed everything, want nothing", preserved)
	}
}

// TestOpenSweepsDeadTemporaryDirectoriesAndSparesTheRest is the second half:
// what a killed process could not remove is removed by the next run, and
// nothing else in the temporary directory is.
func TestOpenSweepsDeadTemporaryDirectoriesAndSparesTheRest(t *testing.T) {
	root := copyFixture(t, "simple")
	parent := t.TempDir()

	dead := filepath.Join(parent, "go-mutants-snap-dead")
	claimAndAbandon(t, dead)
	deadScratch := filepath.Join(parent, "go-mutants-api-dead")
	claimAndAbandon(t, deadScratch)

	live := filepath.Join(parent, "go-mutants-snap-live")
	claimAndAbandon(t, live)
	lock, held, err := tempowner.Acquire(tempowner.LockPath(live))
	if err != nil || !held {
		t.Fatalf("holding a live directory's lock (held=%v, err=%v)", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	unrelated := filepath.Join(parent, "not-go-mutants")
	if err = os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	swept := workspace.Swept()
	if want := []string{deadScratch, dead}; !slices.Equal(sortedPaths(slices.Clone(swept.Removed)), sortedPaths(want)) {
		t.Errorf("Swept().Removed is %v, want %v", swept.Removed, want)
	}
	if swept.Live != 1 {
		t.Errorf("Swept().Live is %d, want 1", swept.Live)
	}
	for _, gone := range []string{dead, deadScratch} {
		if _, statErr := os.Stat(gone); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("%s survived Open's sweep (%v)", gone, statErr)
		}
	}
	for _, survivor := range []string{live, unrelated} {
		if _, statErr := os.Stat(survivor); statErr != nil {
			t.Errorf("Open's sweep removed %s: %v", survivor, statErr)
		}
	}
}

// TestKeepTempPreservesTheTemporaryDirectories covers the deliberate keep. It
// is the escape hatch for the one case the sweep would otherwise make
// impossible — looking at the tree a failing mutant ran in — and it has to be
// deliberate in a way the next run can read, or the directory it leaves behind
// is exactly the garbage this change removes.
func TestKeepTempPreservesTheTemporaryDirectories(t *testing.T) {
	root := copyFixture(t, "simple")
	parent := t.TempDir()

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		KeepTemp:      true,
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	created := temporaryDirectories(t, parent)
	if len(created) != 2 {
		t.Fatalf("Open created %v, want a snapshot directory and a scratch directory", created)
	}
	if err = workspace.Close(); err != nil {
		t.Fatalf("closing workspace: %v", err)
	}

	preserved := workspace.Preserved()
	if !slices.Equal(sortedPaths(slices.Clone(preserved)), sortedPaths(slices.Clone(created))) {
		t.Errorf("Preserved() is %v, want %v", preserved, created)
	}
	for _, directory := range preserved {
		if !filepath.IsAbs(directory) {
			t.Errorf("Preserved() names %q, which is not an absolute path", directory)
		}
		if _, statErr := os.Stat(directory); statErr != nil {
			t.Errorf("KeepTemp did not keep %s: %v", directory, statErr)
		}
		if marker := readOwnerMarker(t, directory); !marker.Kept {
			t.Errorf("%s was kept without being marked kept", directory)
		}
		lock, held, lockErr := tempowner.Acquire(tempowner.LockPath(directory))
		if lockErr != nil || !held {
			t.Errorf("%s still holds its lock after Close (held=%v, err=%v)", directory, held, lockErr)
			continue
		}
		if err = lock.Release(); err != nil {
			t.Errorf("releasing the test's own lock: %v", err)
		}
	}

	// A kept directory is not an orphan, and the next run has to agree: a keep
	// that the following Open swept away would be a keep in name only.
	second, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		t.Fatalf("opening a second workspace: %v", err)
	}
	if swept := second.Swept(); swept.Kept != len(created) || len(swept.Removed) != 0 {
		t.Errorf("the second Open swept %+v, want %d kept and nothing removed", swept, len(created))
	}
	if err = second.Close(); err != nil {
		t.Fatalf("closing the second workspace: %v", err)
	}
	for _, directory := range created {
		if _, statErr := os.Stat(directory); statErr != nil {
			t.Errorf("a later run removed the kept directory %s: %v", directory, statErr)
		}
	}
}

// claimAndAbandon leaves a directory in the state a SIGKILLed run leaves: the
// marker written, the lock file present, and nobody holding it.
func claimAndAbandon(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	owner, err := tempowner.Claim(dir, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("claiming %s: %v", dir, err)
	}
	if err = owner.Release(); err != nil {
		t.Fatalf("releasing %s: %v", dir, err)
	}
}

// temporaryDirectories returns the absolute paths of every go-mutants
// directory directly under parent, sorted.
func temporaryDirectories(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading %s: %v", parent, err)
	}
	var directories []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "go-mutants-") {
			directories = append(directories, filepath.Join(parent, entry.Name()))
		}
	}
	return sortedPaths(directories)
}

func readOwnerMarker(t *testing.T, dir string) tempowner.Marker {
	t.Helper()
	raw, err := os.ReadFile(tempowner.MarkerPath(dir))
	if err != nil {
		t.Fatalf("reading the owner marker of %s: %v", dir, err)
	}
	var marker tempowner.Marker
	if err = json.Unmarshal(raw, &marker); err != nil {
		t.Fatalf("decoding the owner marker %s: %v", raw, err)
	}
	return marker
}

func sortedPaths(paths []string) []string {
	slices.Sort(paths)
	return paths
}

// TestKeepTempThatCannotBeRecordedRemovesRatherThanLeaks holds KeepTemp to the
// same standard on the way out. A directory whose keep could not be written
// into its marker is exactly what the next Open would sweep as an orphan, so
// Close does not report it preserved and does not leave it for that sweep: it
// removes it, says why, and Preserved names nothing.
func TestKeepTempThatCannotBeRecordedRemovesRatherThanLeaks(t *testing.T) {
	root := copyFixture(t, "simple")
	parent := t.TempDir()

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		KeepTemp:      true,
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	created := temporaryDirectories(t, parent)
	if len(created) != 2 {
		t.Fatalf("Open created %v, want a snapshot directory and a scratch directory", created)
	}
	for _, directory := range created {
		marker := tempowner.MarkerPath(directory)
		if err = os.Remove(marker); err != nil {
			t.Fatalf("removing the marker of %s: %v", directory, err)
		}
		if err = os.Mkdir(marker, 0o755); err != nil {
			t.Fatalf("obstructing the marker of %s: %v", directory, err)
		}
	}

	if err = workspace.Close(); err == nil {
		t.Fatal("Close succeeded although no keep could be recorded")
	}
	if preserved := workspace.Preserved(); len(preserved) != 0 {
		t.Errorf("Preserved() is %v after a keep that could not be recorded, want nothing", preserved)
	}
	if left := temporaryDirectories(t, parent); len(left) != 0 {
		t.Errorf("Close left %v behind after a keep it could not record", left)
	}
}
