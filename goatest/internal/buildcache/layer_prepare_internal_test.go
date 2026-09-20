// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestALayerWithNoDirectorySaysSoRatherThanAskingTheFilesystem(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"prepare": (Layer{}).prepareWithHooks(layerHooks{}),
		"ensure":  (Layer{}).ensureWithHooks(layerHooks{}),
	} {
		if err == nil {
			t.Fatalf("%s of a layer with no directory reported nothing", name)
		}
		if !strings.Contains(err.Error(), "has no directory") {
			t.Errorf("%s of a layer with no directory reported %v, want it to name the missing directory", name, err)
		}
	}
}

func TestPreparingALayerReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the step failed")
	for _, test := range []struct {
		name  string
		hooks func(dir string) layerHooks
		want  string
	}{
		{
			name: "a directory it cannot read",
			hooks: func(string) layerHooks {
				return layerHooks{readDir: func(string) ([]os.DirEntry, error) { return nil, failure }}
			},
			want: "read build cache layer",
		},
		{
			name: "a directory it cannot create",
			hooks: func(string) layerHooks {
				return layerHooks{mkdirAll: func(string, os.FileMode) error { return failure }}
			},
			want: "create build cache layer",
		},
		{
			name: "a marker it cannot write",
			hooks: func(string) layerHooks {
				return layerHooks{createTemporary: func(string, string) (layerWritableFile, error) {
					return nil, failure
				}}
			},
			want: "prepare build cache layer",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			err := (Layer{Dir: dir}).prepareWithHooks(test.hooks(dir))
			if !errors.Is(err, failure) {
				t.Fatalf("preparing %s reported %v, want %v", test.name, err, failure)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("preparing %s reported %v, want it to name %q", test.name, err, test.want)
			}
		})
	}
}

func TestClaimingALayerSweepsAMarkerTemporaryAndNothingElse(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir(), Touch: ScratchTouchInterval}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	temporary := markerTemporaryPrefix + "1234" + markerTemporarySuffix
	if err := os.WriteFile(filepath.Join(layer.Dir, temporary), nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	var removed []string
	hooks := layerHooks{
		now:    func() time.Time { return collectMoment.Add(layer.MinIdle() * 2) },
		stat:   func(string) (fs.FileInfo, error) { return stubLayerInfo{modified: collectMoment}, nil },
		remove: func(path string) error { removed = append(removed, filepath.Base(path)); return nil },
	}
	if err := layer.claim(hooks.resolved()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{temporary}) {
		t.Fatalf("claiming the layer removed %q, want the marker temporary alone", removed)
	}
}

func TestClaimingALayerRefusesADirectoryGoatestDidNotWrite(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	temporary := markerTemporaryPrefix + "1234" + markerTemporarySuffix
	for _, name := range []string{temporary, "somebody-elses-file"} {
		if err := os.WriteFile(filepath.Join(layer.Dir, name), nil, filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	}
	err := layer.Prepare()
	if err == nil {
		t.Fatal("a directory holding a file goatest did not write was claimed")
	}
	if !strings.Contains(err.Error(), "somebody-elses-file") {
		t.Errorf("the refusal reads %v, want it to name the file it found", err)
	}
	if _, statErr := os.Stat(filepath.Join(layer.Dir, MarkerName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("the refused directory was marked as a build cache anyway")
	}
}

func TestClaimingALayerStopsAtASweepItCouldNotFinish(t *testing.T) {
	t.Parallel()
	failure := errors.New("the temporary could not be removed")
	layer := Layer{Dir: t.TempDir()}
	temporary := markerTemporaryPrefix + "1234" + markerTemporarySuffix
	if err := os.WriteFile(filepath.Join(layer.Dir, temporary), nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		hooks  layerHooks
		want   string
		failed bool
	}{
		{
			name:  "a temporary it cannot inspect",
			hooks: layerHooks{stat: func(string) (fs.FileInfo, error) { return nil, failure }},
			want:  "read build cache layer", failed: true,
		},
		{
			name: "a temporary it cannot remove",
			hooks: layerHooks{
				now:    func() time.Time { return collectMoment.Add(layer.MinIdle() * 2) },
				stat:   func(string) (fs.FileInfo, error) { return stubLayerInfo{modified: collectMoment}, nil },
				remove: func(string) error { return failure },
			},
			want: "remove build cache marker temporary", failed: true,
		},
		{
			name: "a temporary another run may still be writing",
			hooks: layerHooks{
				now:  func() time.Time { return collectMoment },
				stat: func(string) (fs.FileInfo, error) { return stubLayerInfo{modified: collectMoment}, nil },
				remove: func(string) error {
					t.Error("a temporary younger than the idle window was removed")
					return nil
				},
			},
		},
		{
			name: "a temporary somebody else removed first",
			hooks: layerHooks{
				now:    func() time.Time { return collectMoment.Add(layer.MinIdle() * 2) },
				stat:   func(string) (fs.FileInfo, error) { return stubLayerInfo{modified: collectMoment}, nil },
				remove: func(string) error { return os.ErrNotExist },
			},
		},
		{
			name:  "a temporary that was gone before it was inspected",
			hooks: layerHooks{stat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := layer.claim(test.hooks.resolved())
			if !test.failed {
				if err != nil {
					t.Fatalf("claiming past %s reported %v, want nothing", test.name, err)
				}
				return
			}
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("claiming %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
		})
	}
}

func TestAMarkerTemporaryIsSweptOnlyOnceItIsAsOldAsTheIdleWindow(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir(), Touch: ScratchTouchInterval}
	for _, test := range []struct {
		name  string
		aged  time.Duration
		swept bool
	}{
		{name: "one nanosecond younger than the window", aged: layer.MinIdle() - 1},
		{name: "exactly as old as the window", aged: layer.MinIdle(), swept: true},
		{name: "one nanosecond older than the window", aged: layer.MinIdle() + 1, swept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			swept := false
			hooks := layerHooks{
				now:    func() time.Time { return collectMoment },
				stat:   func(string) (fs.FileInfo, error) { return stubLayerInfo{modified: collectMoment.Add(-test.aged)}, nil },
				remove: func(string) error { swept = true; return nil },
			}
			if err := layer.sweepMarkerTemporary(markerTemporaryPrefix+"1"+markerTemporarySuffix, hooks.resolved()); err != nil {
				t.Fatal(err)
			}
			if swept != test.swept {
				t.Fatalf("a temporary %s was swept=%t, want %t", test.name, swept, test.swept)
			}
		})
	}
}
