// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/tempowner"
)

// TestStableNameIsDeterministicAndPrefixed pins the shape of the name itself.
//
// Everything the stable name buys depends on it being a function of the source
// root and nothing else — not of the clock, not of the process, not of how
// many runs came before. The prefix is load bearing twice over: the Cleanup
// guard refuses a directory whose name does not carry it, and the sweep
// collects the abandoned ones by it.
func TestStableNameIsDeterministicAndPrefixed(t *testing.T) {
	t.Parallel()

	const root = "/home/example/project"
	name := StableName(root)
	if again := StableName(root); again != name {
		t.Errorf("StableName(%q) is not a function of its argument: %q then %q", root, name, again)
	}
	if !strings.HasPrefix(name, DirPrefix) {
		t.Errorf("StableName(%q) = %q, want a name beginning with %q", root, name, DirPrefix)
	}
	digest := strings.TrimPrefix(name, DirPrefix)
	if len(digest) != stableNameHexLength {
		t.Errorf("StableName(%q) = %q, want %d hex characters after the prefix", root, name, stableNameHexLength)
	}
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		t.Errorf("the %q in %q is not lowercase hex (%v)", digest, name, err)
	}
	if other := StableName(root + "-two"); other == name {
		t.Errorf("StableName collapsed two source roots onto %q", name)
	}
}

// TestCreateUsesTheSameDirectoryNameForTheSameSourceRoot is the whole point of
// the stable name, stated as the property a consumer measures.
//
// The go command hashes the absolute directory of a package into every compile
// action id when -trimpath is not passed, and go-mutants deliberately does not
// pass it: -trimpath changes the program under test, and a test that reads
// runtime.Caller paths would behave differently in the snapshot than in the
// user's tree. So a snapshot at a fresh random path costs the shared build
// cache one full copy of the project's objects per run and hits nothing from
// the last one. Two runs of one root therefore have to land on one path.
func TestCreateUsesTheSameDirectoryNameForTheSameSourceRoot(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})
	elsewhere := t.TempDir()
	writeTree(t, elsewhere, map[string]string{"a.go": "package a\n"})
	dest := t.TempDir()

	first, firstStable := snapshotOnce(t, src, dest)
	second, secondStable := snapshotOnce(t, src, dest)
	if second != first {
		t.Errorf("two runs of %s landed in %s and %s, want one directory", src, first, second)
	}
	if !firstStable || !secondStable {
		t.Errorf("the two runs report StableDir %v and %v, want both true", firstStable, secondStable)
	}
	if want := filepath.Join(dest, StableName(absolutePath(t, src))); first != want {
		t.Errorf("the snapshot of %s landed in %s, want %s", src, first, want)
	}

	// A different root is a different name, or the two would fight over one
	// directory for no reason and each would keep evicting the other's copy.
	other, otherStable := snapshotOnce(t, elsewhere, dest)
	if other == first {
		t.Errorf("%s and %s share the snapshot directory %s", src, elsewhere, other)
	}
	if !otherStable {
		t.Error("the snapshot of an unrelated root did not get its stable name")
	}

	base := filepath.Base(first)
	if !strings.HasPrefix(base, DirPrefix) {
		t.Errorf("the snapshot directory is %q, want a name beginning with %q", base, DirPrefix)
	}
	if len(base) != len(DirPrefix)+stableNameHexLength {
		t.Errorf("the snapshot directory is %q, want %d characters", base, len(DirPrefix)+stableNameHexLength)
	}
}

// TestCreateFallsBackToARandomNameWhileTheStableDirectoryIsLive covers the one
// thing the stable name must never buy: two processes in one directory.
//
// A second run of the same root while the first is still going finds the
// stable name taken by a lock somebody holds, and takes a random name instead.
// It loses the build-cache hits and keeps the isolation, which is the only
// acceptable direction for that trade.
func TestCreateFallsBackToARandomNameWhileTheStableDirectoryIsLive(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})
	dest := t.TempDir()

	first, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := first.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup of the first snapshot: %v", cleanupErr)
		}
	})
	if !first.StableDir {
		t.Fatalf("the first snapshot landed in %s without the stable name", first.Dir())
	}

	second, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create beside a live snapshot of the same root: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := second.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup of the second snapshot: %v", cleanupErr)
		}
	})
	if second.Dir() == first.Dir() {
		t.Fatalf("two live snapshots of %s share the directory %s", src, second.Dir())
	}
	if second.StableDir {
		t.Error("the fallback directory reports itself as the stable one")
	}
	if got := filepath.Dir(second.Dir()); !pathsEqual(got, dest) {
		t.Errorf("the fallback landed under %s, want %s", got, dest)
	}
	if base := filepath.Base(second.Dir()); !strings.HasPrefix(base, DirPrefix) {
		t.Errorf("the fallback directory is %q, want a name beginning with %q", base, DirPrefix)
	}

	// Both are complete copies, and neither disturbed the other's.
	if second.WorkspaceDigest != first.WorkspaceDigest {
		t.Errorf("the fallback copy hashes %s, want %s", second.WorkspaceDigest, first.WorkspaceDigest)
	}
	if drifts, redigestErr := first.Redigest(); redigestErr != nil || len(drifts) != 0 {
		t.Errorf("the live snapshot drifted while the second was made: %v, %v", drifts, redigestErr)
	}
}

// TestCreateReplacesAnOrphanedStableDirectory is the rule that keeps a stable
// name from turning into a cache of trees.
//
// The directory a killed run left behind holds that run's tree, mutated in
// place by instrumentation and stale by however long the checkout has moved
// on. It is never adopted: the sweep collects it exactly as it collects any
// other orphan, and the copy is made again from the source. What is reused
// across runs is the path, so that the build cache recognises it — never the
// bytes, which only the workspace digest may speak for.
func TestCreateReplacesAnOrphanedStableDirectory(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})
	dest := t.TempDir()

	// The shape a SIGKILLed run leaves: the marker written, the lock file
	// present, and nobody holding it.
	orphan := filepath.Join(dest, StableName(absolutePath(t, src)))
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatalf("creating %s: %v", orphan, err)
	}
	owner, err := tempowner.Claim(orphan, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("claiming %s: %v", orphan, err)
	}
	if err = owner.Release(); err != nil {
		t.Fatalf("releasing %s: %v", orphan, err)
	}
	writeTree(t, filepath.Join(orphan, TreeName), map[string]string{"stale.go": "package stale\n"})
	sentinel := filepath.Join(orphan, TreeName, "stale.go")

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create over an orphaned stable directory: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup: %v", cleanupErr)
		}
	})

	if snap.Dir() != orphan {
		t.Errorf("Create landed in %s, want the swept %s", snap.Dir(), orphan)
	}
	if !snap.StableDir {
		t.Error("Create reports the reclaimed directory as a fallback")
	}
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("%s survived, so the orphan's tree was adopted rather than recopied (%v)", sentinel, statErr)
	}
	if got := relPaths(snap.Manifest); !slices.Equal(got, []string{"a.go"}) {
		t.Errorf("manifest paths = %v, want [a.go]", got)
	}
	if drifts, redigestErr := snap.Redigest(); redigestErr != nil || len(drifts) != 0 {
		t.Errorf("the recopied tree does not match its own manifest: %v, %v", drifts, redigestErr)
	}
}

// TestCreateLeavesAKeptStableDirectoryAlone holds the stable name to the
// promise KeepTemp makes.
//
// A kept directory is the answer to the one question a removed one cannot
// answer — what the tree a failing mutant ran in actually looked like — and a
// keep the next run overwrote because it wanted the name would be a keep in
// name only. So the next run takes a random name and leaves the evidence
// exactly where it was left.
func TestCreateLeavesAKeptStableDirectoryAlone(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n"})
	dest := t.TempDir()

	kept := filepath.Join(dest, StableName(absolutePath(t, src)))
	if err := os.Mkdir(kept, 0o700); err != nil {
		t.Fatalf("creating %s: %v", kept, err)
	}
	owner, err := tempowner.Claim(kept, time.Now().Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("claiming %s: %v", kept, err)
	}
	// Keep records the decision in the marker and releases the lock, which is
	// exactly the state a --keep-temp run exits in.
	if err = owner.Keep(); err != nil {
		t.Fatalf("keeping %s: %v", kept, err)
	}
	writeTree(t, filepath.Join(kept, TreeName), map[string]string{"evidence.go": "package evidence\n"})
	evidence := filepath.Join(kept, TreeName, "evidence.go")

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create beside a kept snapshot of the same root: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("Cleanup: %v", cleanupErr)
		}
	})

	if snap.Dir() == kept {
		t.Fatalf("Create took %s, the directory a keep preserved", kept)
	}
	if snap.StableDir {
		t.Error("the fallback directory reports itself as the stable one")
	}
	if base := filepath.Base(snap.Dir()); !strings.HasPrefix(base, DirPrefix) {
		t.Errorf("the fallback directory is %q, want a name beginning with %q", base, DirPrefix)
	}
	if _, statErr := os.Stat(evidence); statErr != nil {
		t.Errorf("Create removed the kept tree: %v", statErr)
	}
	if marker, markerErr := tempowner.ReadMarker(kept); markerErr != nil || !marker.Kept {
		t.Errorf("the kept marker of %s reads %+v (%v), want kept", kept, marker, markerErr)
	}
}

// snapshotOnce creates a snapshot, records where it landed and whether it got
// the stable name, and removes it again before returning.
//
// The removal is what makes two calls two successive runs rather than two live
// snapshots racing for one name — which is a different rule, tested
// separately.
func snapshotOnce(t *testing.T, src, dest string) (dir string, stable bool) {
	t.Helper()
	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	dir, stable = snap.Dir(), snap.StableDir
	if err = snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup of %s: %v", dir, err)
	}
	return dir, stable
}

// absolutePath is filepath.Abs with the test's error handling, for a test that
// has to name the directory Create will choose before Create chooses it.
func absolutePath(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	return abs
}
