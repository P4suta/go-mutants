// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"os"
	"path/filepath"
	"slices"
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

func TestABuildCacheEnvironmentNamesAFallbackOnlyWhereThereIsOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		cache    runBuildCache
		plain    []string
		persists []string
	}{
		{
			name:     "a cache with a fallback directory",
			cache:    runBuildCache{plain: "plain", persisting: "persisting", fallback: "/cache/go-build"},
			plain:    []string{goCacheVariable + "=/cache/go-build", cacheProgramVariable + "=plain"},
			persists: []string{goCacheVariable + "=/cache/go-build", cacheProgramVariable + "=persisting"},
		},
		{
			name:     "a cache with none",
			cache:    runBuildCache{plain: "plain", persisting: "persisting"},
			plain:    []string{cacheProgramVariable + "=plain"},
			persists: []string{cacheProgramVariable + "=persisting"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.cache.environment(); !slices.Equal(got, test.plain) {
				t.Errorf("%s answers %q, want %q", test.name, got, test.plain)
			}
			if got := test.cache.persistingEnvironment(); !slices.Equal(got, test.persists) {
				t.Errorf("%s persists with %q, want %q", test.name, got, test.persists)
			}
		})
	}
	var none runBuildCache
	if none.environment() != nil || none.persistingEnvironment() != nil || none.nativeEnvironment() != nil {
		t.Fatal("a cache that serves nothing named an environment")
	}
}

func TestANativeEnvironmentTurnsTheProgramOffOnlyWhereThereIsANativeCache(t *testing.T) {
	t.Parallel()
	with := runBuildCache{plain: "plain", native: "/cache/native"}
	want := []string{goCacheVariable + "=/cache/native", cacheProgramVariable + "="}
	if got := with.nativeEnvironment(); !slices.Equal(got, want) {
		t.Fatalf("a cache with a native projection answers %q, want %q", got, want)
	}
	without := runBuildCache{plain: "plain"}
	if got := without.nativeEnvironment(); !slices.Equal(got, []string{cacheProgramVariable + "=plain"}) {
		t.Fatalf("a cache with no native projection answers %q, want the program alone", got)
	}
}

func TestAPlanReadsTheMomentItWasGivenOrTheOneItRuns(t *testing.T) {
	t.Parallel()
	given := planMoment(Options{Now: func() time.Time { return nativeCacheMoment }})
	if !given.Equal(nativeCacheMoment) {
		t.Fatalf("a plan read %s, want the moment it was given", given)
	}
	if planMoment(Options{}).IsZero() {
		t.Fatal("a plan with no clock read no moment at all")
	}
}

func TestACacheThatServesNothingSeedsNothingAndCompilesNothingPersistently(t *testing.T) {
	t.Parallel()
	var none runBuildCache
	if none.serves() || none.seedNative() || none.needsPersistentCompile() {
		t.Fatal("a cache that serves nothing claimed to seed or to need a persistent compile")
	}
	noNative := runBuildCache{plain: "plain"}
	if !noNative.serves() || noNative.seedNative() {
		t.Fatalf("a cache with no native projection seeds=%t, want it not to", noNative.seedNative())
	}
	if !noNative.needsPersistentCompile() {
		t.Fatal("a serving cache that seeds nothing does not need a persistent compile")
	}
}

func TestMarkingANativeCacheDirtyCountsOnlyWhereThereIsOneToMark(t *testing.T) {
	t.Parallel()
	projection := &nativeCacheProjection{}
	for _, test := range []struct {
		name  string
		cache runBuildCache
		marks bool
	}{
		{
			name:  "a cache with a projection and a native directory",
			cache: runBuildCache{plain: "plain", native: "/cache/native", projection: projection}, marks: true,
		},
		{
			name:  "a cache with a projection and no native directory",
			cache: runBuildCache{plain: "plain", projection: projection},
		},
		{
			name:  "a cache with a native directory and no projection",
			cache: runBuildCache{plain: "plain", native: "/cache/native"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection.mutex.Lock()
			before := projection.generation
			projection.mutex.Unlock()
			test.cache.markNativeDirty()
			projection.mutex.Lock()
			after := projection.generation
			projection.mutex.Unlock()
			if marked := after == before+1; marked != test.marks {
				t.Fatalf("%s moved the generation from %d to %d, want marked=%t",
					test.name, before, after, test.marks)
			}
		})
	}
}
