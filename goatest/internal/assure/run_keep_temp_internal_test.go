// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/keptledger"
	"github.com/P4suta/go-mutants/goatest/internal/report"
	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

var keepTempMoment = time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)

type failingTemporaryOwner struct {
	keepErr    error
	releaseErr error
	kept       int
	released   int
}

func (owner *failingTemporaryOwner) Keep() error {
	owner.kept++
	return owner.keepErr
}

func (owner *failingTemporaryOwner) Release() error {
	owner.released++
	return owner.releaseErr
}

func keepTempRecorder(sink *trace.MemorySink) *trace.Recorder {
	return trace.New(sink, func() time.Time { return keepTempMoment })
}

func recordedArtifacts(sink *trace.MemorySink) []trace.ArtifactRecord {
	var records []trace.ArtifactRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypeArtifact && event.Artifact != nil {
			records = append(records, *event.Artifact)
		}
	}
	return records
}

func TestKeepTempPreservesTheBaselineScratchAndSaysWhereItIs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		keep     bool
		removals int
		kept     bool
	}{
		{name: "removed by default", removals: 1},
		{name: "kept on request", keep: true, kept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			harness := newRunCoordinatorHarness(t)
			sink := harness.record()
			temporary := t.TempDir()
			result, err := harness.run(Options{KeepTemp: test.keep, TempDirectory: temporary})

			if err != nil || result.Verdict != report.VerdictAssured {
				t.Fatalf("run = (%+v, %v)", result, err)
			}
			if harness.scratchRemovals != test.removals {
				t.Fatalf("scratch removals = %d, want %d", harness.scratchRemovals, test.removals)
			}
			var want []trace.ArtifactRecord
			if test.kept {
				want = []trace.ArtifactRecord{
					{Kind: "baseline-scratch", Path: filepath.Join(harness.runScratch, "baseline-scratch")},
					{Kind: "run-scratch", Path: harness.runScratch},
				}
			}
			if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
				t.Fatalf("recorded artifacts = %+v, want %+v", got, want)
			}
		})
	}
}

func TestReleaseBaselineScratchSelectsExactlyKeepOrRemove(t *testing.T) {
	sentinel := errors.New("remove failed")
	removed := false
	remove := func(path string) error {
		removed = path == "baseline"
		return sentinel
	}
	if err := releaseBaselineScratch(Options{}, remove, "baseline"); !errors.Is(err, sentinel) || !removed {
		t.Fatalf("removed baseline scratch = (removed=%t, err=%v)", removed, err)
	}

	sink, recorder := newTraceRecording()
	removed = false
	if err := releaseBaselineScratch(Options{KeepTemp: true, Trace: recorder}, remove, "baseline"); err != nil || removed {
		t.Fatalf("kept baseline scratch = (removed=%t, err=%v)", removed, err)
	}
	want := []trace.ArtifactRecord{{Kind: artifactBaselineScratch, Path: "baseline"}}
	if got := recordedArtifacts(sink); !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline artifacts = %+v, want %+v", got, want)
	}
}

func TestReleaseBuildCacheRemovesAServingCacheScratch(t *testing.T) {
	directory := t.TempDir()
	cache := runBuildCache{plain: "program", scratch: directory}
	if err := releaseBuildCache(Options{}, cache, runScratch{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("released build cache scratch = %v", err)
	}
}

func TestReleaseBuildCacheReturnsNativeOwnerKeepFailure(t *testing.T) {
	cause := errors.New("native keep failed")
	owner := &failingTemporaryOwner{keepErr: cause}
	cache := runBuildCache{plain: "program", scratch: t.TempDir(), nativeOwner: owner}
	if err := releaseBuildCache(Options{KeepTemp: true}, cache, runScratch{}, keepTempMoment); !errors.Is(err, cause) || owner.kept != 1 {
		t.Fatalf("releaseBuildCache = %v, owner=%+v", err, owner)
	}
}

func TestReleasingARunScratchKeepsOrRemovesAndSaysWhichEitherWay(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		keep    bool
		removed bool
	}{
		{name: "a run that keeps its temporaries", keep: true},
		{name: "a run that does not", removed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sink := trace.NewMemorySink(0)
			root := t.TempDir()
			directory := filepath.Join(root, "run-scratch")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			removals := 0
			owner, err := tempowner.Claim(directory,
				tempowner.Marker{RunID: "run-a", Root: root}, keepTempMoment)
			if err != nil {
				t.Fatal(err)
			}
			scratch := runScratch{dir: directory, id: "run-a", root: root, owner: owner}
			options := Options{KeepTemp: test.keep, Trace: keepTempRecorder(sink)}
			releaseRunScratch(options, func(string) error { removals++; return nil }, scratch, keepTempMoment)
			if removed := removals == 1; removed != test.removed {
				t.Fatalf("%s removed=%t, want %t", test.name, removed, test.removed)
			}
			recorded := len(recordedArtifacts(sink)) != 0
			if recorded == test.removed {
				t.Fatalf("%s recorded artifacts=%t, want %t", test.name, recorded, !test.removed)
			}
		})
	}
	sink := trace.NewMemorySink(0)
	removals := 0
	releaseRunScratch(Options{Trace: keepTempRecorder(sink)},
		func(string) error { removals++; return nil }, runScratch{}, keepTempMoment)
	if removals != 0 || len(recordedArtifacts(sink)) != 0 {
		t.Fatalf("a scratch with no directory removed %d and recorded %d, want neither",
			removals, len(recordedArtifacts(sink)))
	}
}

func TestReleasingARunScratchReportsOwnerAndRemovalFailures(t *testing.T) {
	keepErr := errors.New("keep failed")
	keepOwner := &failingTemporaryOwner{keepErr: keepErr}
	var keepEvents []Event
	releaseRunScratch(Options{KeepTemp: true, Progress: func(event Event) { keepEvents = append(keepEvents, event) }},
		func(string) error { t.Fatal("kept scratch was removed"); return nil },
		runScratch{dir: "scratch", root: t.TempDir(), id: "run", owner: keepOwner}, keepTempMoment)
	if keepOwner.kept != 1 || len(keepEvents) != 1 || !strings.Contains(keepEvents[0].Detail, keepErr.Error()) {
		t.Fatalf("keep failure owner=%+v events=%+v", keepOwner, keepEvents)
	}

	releaseErr := errors.New("release failed")
	removeErr := errors.New("remove failed")
	releaseOwner := &failingTemporaryOwner{releaseErr: releaseErr}
	var releaseEvents []Event
	releaseRunScratch(Options{Progress: func(event Event) { releaseEvents = append(releaseEvents, event) }},
		func(path string) error {
			if path != "scratch" {
				t.Fatalf("remove path = %q", path)
			}
			return removeErr
		}, runScratch{dir: "scratch", owner: releaseOwner}, keepTempMoment)
	if releaseOwner.released != 1 || len(releaseEvents) != 2 || !strings.Contains(releaseEvents[0].Detail, releaseErr.Error()) || !strings.Contains(releaseEvents[1].Detail, removeErr.Error()) {
		t.Fatalf("release failure owner=%+v events=%+v", releaseOwner, releaseEvents)
	}
}

func TestRunScratchRecordsKeptOnlyWithRootAndIdentity(t *testing.T) {
	for _, test := range []struct {
		root string
		id   string
		want bool
	}{
		{root: "root", id: "run", want: true},
		{root: "root"},
		{id: "run"},
		{},
	} {
		if got := (runScratch{root: test.root, id: test.id}).recordsKept(); got != test.want {
			t.Errorf("recordsKept(root=%q, id=%q) = %t, want %t", test.root, test.id, got, test.want)
		}
	}
}

func TestReleasingAKeptBuildCacheNamesItsNativeProjectionOnlyWhereThereIsOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		native  string
		scratch runScratch
		kinds   []string
		ledger  bool
	}{
		{
			name:  "a cache with no native projection",
			kinds: []string{artifactBuildCacheScratch},
		},
		{
			name: "a native projection a ledger can record", native: "native",
			scratch: runScratch{root: "root", id: "run-a"},
			kinds:   []string{artifactBuildCacheScratch, artifactNativeCacheScratch}, ledger: true,
		},
		{
			name: "a native projection no ledger can record", native: "native",
			kinds: []string{artifactBuildCacheScratch, artifactNativeCacheScratch},
		},
		{
			name: "a native projection whose run has no identity", native: "native",
			scratch: runScratch{root: "root"},
			kinds:   []string{artifactBuildCacheScratch, artifactNativeCacheScratch},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sink := trace.NewMemorySink(0)
			scratch := test.scratch
			if scratch.root != "" {
				scratch.root = t.TempDir()
			}
			cache := runBuildCache{plain: "program", scratch: t.TempDir(), native: test.native}
			options := Options{KeepTemp: true, Trace: keepTempRecorder(sink)}
			if err := releaseBuildCache(options, cache, scratch, keepTempMoment); err != nil {
				t.Fatal(err)
			}
			var kinds []string
			for _, record := range recordedArtifacts(sink) {
				kinds = append(kinds, record.Kind)
			}
			if !reflect.DeepEqual(kinds, test.kinds) {
				t.Fatalf("%s recorded %q, want %q", test.name, kinds, test.kinds)
			}
			if scratch.root != "" {
				_, err := os.Stat(keptledger.Path(scratch.root))
				if exists := err == nil; exists != test.ledger {
					t.Fatalf("%s ledger exists=%t, want %t (stat error %v)", test.name, exists, test.ledger, err)
				}
			}
		})
	}
}

func TestRecordingKeptPathsWritesNoLedgerForNoPathAtAll(t *testing.T) {
	t.Parallel()
	sink := trace.NewMemorySink(0)
	root := t.TempDir()
	recordKept(Options{Trace: keepTempRecorder(sink)}, runScratch{root: root, id: "run-a"},
		artifactRunScratch, nil, keepTempMoment)
	if len(recordedArtifacts(sink)) != 0 {
		t.Fatalf("recording no path at all recorded %+v", recordedArtifacts(sink))
	}
	if _, err := os.Stat(keptledger.Path(root)); !os.IsNotExist(err) {
		t.Fatalf("recording no path at all wrote a ledger: %v", err)
	}
}
