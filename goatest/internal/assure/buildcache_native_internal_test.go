// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/buildcache"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
)

var nativeCacheMoment = time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)

func nativeCacheMarker(run string) tempowner.Marker {
	return tempowner.Marker{RunID: run, Root: run}
}

func TestClaimingASharedNativeCacheTakesItOnceAndStandsDownAfterwards(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	base := filepath.Join(parent, "base")
	directory, owner, claimed, err := claimSharedNativeBuildCache(parent, base, nativeCacheMarker("run-a"), nativeCacheMoment)
	if err != nil || !claimed || owner == nil {
		t.Fatalf("claiming a free shared cache answered (%q, %t, %v)", directory, claimed, err)
	}
	if filepath.Base(directory) != buildcache.NativeSharedDirectoryName(base) {
		t.Fatalf("the shared cache is at %q, want the name the base hashes to", directory)
	}
	t.Cleanup(func() { _ = owner.Release() })

	again, second, taken, err := claimSharedNativeBuildCache(parent, base, nativeCacheMarker("run-b"), nativeCacheMoment)
	if err != nil {
		t.Fatalf("claiming a shared cache another run holds reported %v, want nothing", err)
	}
	if taken || second != nil || again != "" {
		t.Fatalf("a shared cache another run holds was claimed twice: (%q, %t)", again, taken)
	}
}

func TestClaimingASharedNativeCacheRefusesAParentItCannotWriteTo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.WriteFile(parent, []byte("this is not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	_, _, claimed, err := claimSharedNativeBuildCache(parent, filepath.Join(parent, "base"),
		nativeCacheMarker("run-a"), nativeCacheMoment)
	if err == nil || claimed {
		t.Fatalf("a parent that is a file was claimed: (%t, %v)", claimed, err)
	}
	if !strings.Contains(err.Error(), "create shared native build cache") {
		t.Errorf("the failure reads %v, want it to name what it could not create", err)
	}
}

func TestOpeningANativeCacheSharesItOrMakesOneOfItsOwn(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	base := filepath.Join(parent, "base")
	scratch := runScratch{id: "run-a", root: t.TempDir()}

	directory, owner, shared, _, err := openNativeBuildCache(base, scratch, nativeCacheMoment)
	if err != nil || !shared || owner == nil {
		t.Fatalf("opening a free native cache answered (%q, %t, %v)", directory, shared, err)
	}
	t.Cleanup(func() { _ = owner.Release() })
	if filepath.Base(directory) != buildcache.NativeSharedDirectoryName(base) {
		t.Fatalf("the shared cache is at %q, want the shared name", directory)
	}

	own, second, sharedAgain, _, err := openNativeBuildCache(base, runScratch{id: "run-b", root: scratch.root},
		nativeCacheMoment)
	if err != nil {
		t.Fatalf("opening a native cache another run holds reported %v", err)
	}
	t.Cleanup(func() { _ = second.Release() })
	if sharedAgain {
		t.Fatalf("a native cache another run holds was reported shared: %q", own)
	}
	if !strings.HasPrefix(filepath.Base(own), buildcache.NativeDirectoryPrefix) {
		t.Fatalf("the cache it made is at %q, want one of its own under the native prefix", own)
	}
	if own == directory {
		t.Fatal("the run that could not share made the same directory as the one that did")
	}
}

func TestOpeningANativeCacheReportsAParentItCannotWriteTo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.WriteFile(parent, []byte("this is not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	directory, owner, shared, _, err := openNativeBuildCache(filepath.Join(parent, "base"),
		runScratch{id: "run-a", root: root}, nativeCacheMoment)
	if err == nil {
		t.Fatalf("a parent that is a file was opened: %q", directory)
	}
	if directory != "" || owner != nil || shared {
		t.Errorf("a native cache it could not open answered (%q, %v, %t), want nothing", directory, owner, shared)
	}
}

func TestRemovingABuildCacheScratchTakesBackOnlyWhatItWasGiven(t *testing.T) {
	t.Parallel()
	if err := removeBuildCacheScratch(""); err != nil {
		t.Fatalf("removing no scratch at all reported %v, want nothing", err)
	}
	directory := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(directory, filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	if err := removeBuildCacheScratch(directory); err != nil {
		t.Fatalf("removing a scratch reported %v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("the scratch it says it removed is still there: %v", err)
	}
}
