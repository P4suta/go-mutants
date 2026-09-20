// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/advisorylock"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestHoldingACollectionAlwaysAnswersWithAReleaseThatCanBeCalled(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		layer func(t *testing.T) Layer
		fails bool
	}{
		{
			name:  "a layer with no directory",
			layer: func(*testing.T) Layer { return Layer{} },
		},
		{
			name: "a directory that is a file",
			layer: func(t *testing.T) Layer {
				path := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(path, nil, filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
				return Layer{Dir: path}
			},
			fails: true,
		},
		{
			name: "a marker another descriptor already holds",
			layer: func(t *testing.T) Layer {
				layer := Layer{Dir: t.TempDir()}
				file, err := os.OpenFile(layer.collectionMarkerPath(),
					os.O_CREATE|os.O_RDWR, filemode.ReadableFile)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = advisorylock.Release(file); _ = file.Close() })
				locked, err := advisorylock.Try(file)
				if err != nil || !locked {
					t.Fatalf("the test could not take the lock first: (%t, %v)", locked, err)
				}
				return layer
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			release, held, err := test.layer(t).HoldCollection()
			if held {
				t.Fatalf("%s reported the collection lock held", test.name)
			}
			if (err != nil) != test.fails {
				t.Fatalf("%s answered error %v, want failure=%t", test.name, err, test.fails)
			}
			if release == nil {
				t.Fatal("the release is nil; a caller that defers it panics")
			}
			if releaseErr := release(); releaseErr != nil {
				t.Errorf("releasing a lock that was never held reported %v, want nothing", releaseErr)
			}
		})
	}
}

func TestHoldingACollectionReportsALockItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the lock could not be taken")
	layer := Layer{Dir: t.TempDir()}
	release, held, err := layer.holdCollectionWithHooks(layerHooks{
		lockFile: func(*os.File) (bool, error) { return false, failure },
	})
	if !errors.Is(err, failure) {
		t.Fatalf("holding a lock that failed reported %v, want %v", err, failure)
	}
	if held {
		t.Error("a lock that could not be taken was reported held")
	}
	if !strings.Contains(err.Error(), "lock build cache collection") {
		t.Errorf("the failure reads %v, want it to name the lock it could not take", err)
	}
	if release == nil {
		t.Fatal("the release is nil; a caller that defers it panics")
	}
	if releaseErr := release(); releaseErr != nil {
		t.Errorf("releasing a lock that was never taken reported %v, want nothing", releaseErr)
	}
}

func TestReleasingACollectionLockTwiceReportsTheSecondRelease(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	release, held, err := layer.HoldCollection()
	if err != nil || !held {
		t.Fatalf("HoldCollection = (%t, %v), want the lock held", held, err)
	}
	if releaseErr := release(); releaseErr != nil {
		t.Fatalf("the first release reported %v, want nothing", releaseErr)
	}
	releaseErr := release()
	if releaseErr == nil {
		t.Fatal("releasing a lock whose file is closed reported nothing")
	}
	if !strings.Contains(releaseErr.Error(), "release build cache collection lock") {
		t.Errorf("the second release reported %v, want it to name the release that failed", releaseErr)
	}
}

func TestALockedCollectionStandsDownWhenAnotherRunHoldsTheLock(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	collected, ran, err := layer.collectLockedWithHooks(Policy{MaxBytes: 1}, 0, collectMoment, layerHooks{
		lockFile: func(*os.File) (bool, error) { return false, nil },
		readDir: func(string) ([]os.DirEntry, error) {
			t.Error("a collection ran without the lock")
			return nil, nil
		},
	})
	if err != nil || ran || collected != (Collected{}) {
		t.Fatalf("collection = (%+v, %t, %v), want it stood down for the run that holds the lock", collected, ran, err)
	}
}

func TestALockedCollectionRefusesAPolicyItCannotHonour(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	for _, policy := range []Policy{{MaxBytes: -1}, {TTL: -1}, {MinIdle: -1}} {
		collected, ran, err := layer.CollectLocked(policy, 0, collectMoment)
		if err == nil {
			t.Fatalf("policy %+v was accepted", policy)
		}
		if ran || collected != (Collected{}) {
			t.Errorf("a refused policy reported ran=%t and %+v, want nothing collected", ran, collected)
		}
	}
}

func TestALockedCollectionStandsDownWhenOneRanWithinTheInterval(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	layers := Layers{Scratch: layer}
	if _, err := layers.Put(collectKey(1), collectKey(2), strings.NewReader("0123456789"), 10, collectMoment); err != nil {
		t.Fatal(err)
	}
	marker := layer.collectionMarkerPath()
	if err := os.WriteFile(marker, nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	recent := collectMoment.Add(-time.Second)
	if err := os.Chtimes(marker, recent, recent); err != nil {
		t.Fatal(err)
	}
	policy := Policy{MaxBytes: 1, TTL: time.Nanosecond}
	collected, ran, err := layer.CollectLocked(policy, time.Minute, collectMoment)
	if err != nil {
		t.Fatal(err)
	}
	if ran || collected != (Collected{}) {
		t.Fatalf("a collection within the interval reported ran=%t and %+v, want it stood down", ran, collected)
	}
	if _, statErr := os.Stat(layer.actionPath(collectKey(1))); statErr != nil {
		t.Errorf("the entry was collected anyway: %v", statErr)
	}
}

func TestALockedCollectionReportsWhatTheCollectionItselfCouldNotDo(t *testing.T) {
	t.Parallel()
	failure := errors.New("the layer could not be listed")
	layer := Layer{Dir: t.TempDir()}
	collected, ran, err := layer.collectLockedWithHooks(Policy{MaxBytes: 1}, 0, collectMoment, layerHooks{
		readDir: func(string) ([]os.DirEntry, error) { return nil, failure },
	})
	if !errors.Is(err, failure) {
		t.Fatalf("collection error = %v, want %v", err, failure)
	}
	if ran || collected != (Collected{}) {
		t.Errorf("a collection that failed reported ran=%t and %+v, want nothing", ran, collected)
	}
}

func TestAnIntervalThatDisablesTheCheckAsksTheFilesystemNothing(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	for _, test := range []struct {
		name     string
		interval time.Duration
		now      time.Time
	}{
		{name: "an interval of nothing", interval: 0, now: collectMoment},
		{name: "an interval below nothing", interval: -time.Minute, now: collectMoment},
		{name: "a clock that was never set", interval: time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			hooks := layerHooks{stat: func(path string) (fs.FileInfo, error) {
				t.Errorf("%s asked the filesystem for %s", test.name, path)
				return nil, os.ErrNotExist
			}}
			if layer.collectedRecently(test.interval, test.now, hooks.resolved()) {
				t.Fatalf("%s reported a collection within the interval", test.name)
			}
		})
	}
}

func TestACollectionExactlyOneIntervalOldIsNoLongerRecent(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	interval := time.Minute
	for _, test := range []struct {
		name   string
		marked time.Time
		recent bool
	}{
		{name: "one nanosecond inside the interval", marked: collectMoment.Add(-interval + 1), recent: true},
		{name: "exactly one interval ago", marked: collectMoment.Add(-interval)},
		{name: "one nanosecond past the interval", marked: collectMoment.Add(-interval - 1)},
		{name: "at this very moment", marked: collectMoment, recent: true},
		{name: "a marker written after this moment", marked: collectMoment.Add(time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			hooks := layerHooks{stat: func(string) (fs.FileInfo, error) {
				return stubLayerInfo{modified: test.marked}, nil
			}}
			if recent := layer.collectedRecently(interval, collectMoment, hooks.resolved()); recent != test.recent {
				t.Fatalf("a collection marked %s reads recent=%t, want %t", test.name, recent, test.recent)
			}
		})
	}
	missing := layerHooks{stat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }}
	if layer.collectedRecently(interval, collectMoment, missing.resolved()) {
		t.Error("a layer that never collected reads as recently collected")
	}
}
