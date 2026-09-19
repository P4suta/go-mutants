// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/buildcache"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
)

func TestOpenRunBuildCacheReportsAndCleansEveryFailedStage(t *testing.T) {
	cause := errors.New("stage failed")
	for _, stage := range []string{"scratch prepare", "fallback directory", "plain program", "persisting program", "native cache"} {
		t.Run(stage, func(t *testing.T) {
			runDirectory := t.TempDir()
			prepareCalls := 0
			dependencies := runBuildCacheOpenDependencies{
				prepare: func(buildcache.Layer) error {
					prepareCalls++
					if stage == "scratch prepare" && prepareCalls == 2 {
						return cause
					}
					return nil
				},
				mkdir: func(path string) error {
					if stage == "fallback directory" {
						return cause
					}
					return os.Mkdir(path, filemode.PrivateDirectory)
				},
				program: func(options buildcache.ProgramOptions) (string, error) {
					if stage == "plain program" && !options.Persist || stage == "persisting program" && options.Persist {
						return "", cause
					}
					if options.Persist {
						return "persisting", nil
					}
					return "plain", nil
				},
				openNative: func(string, runScratch, time.Time) (string, *tempowner.Owner, bool, tempowner.Result, error) {
					if stage == "native cache" {
						return "", nil, false, tempowner.Result{}, cause
					}
					return "native", nil, true, tempowner.Result{}, nil
				},
				now: func() time.Time { return nativeCacheMoment },
			}
			cache, err := openRunBuildCacheWith("goatest", "base", "source", runScratch{dir: runDirectory}, 17, dependencies)
			if stage == "native cache" {
				if err != nil || !cache.serves() || cache.projection == nil || !cache.projection.attempted || !errors.Is(cache.projection.err, cause) {
					t.Fatalf("native failure = (%+v, %v)", cache, err)
				}
				return
			}
			if !errors.Is(err, cause) || cache.serves() {
				t.Fatalf("%s failure = (%+v, %v)", stage, cache, err)
			}
			entries, readErr := os.ReadDir(runDirectory)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("%s scratch = (%v, %v), want empty", stage, entries, readErr)
			}
		})
	}
}

func nativeCacheTestOwner(t *testing.T) *tempowner.Owner {
	t.Helper()
	owner, err := tempowner.Claim(t.TempDir(), nativeCacheMarker(t.Name()), nativeCacheMoment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Release() })
	return owner
}

func nativeOpenDependencies(owner *tempowner.Owner) nativeBuildCacheOpenDependencies {
	return nativeBuildCacheOpenDependencies{
		sweep: func(string, []string, time.Time) (tempowner.Result, error) {
			return tempowner.Result{Removed: []string{"old-native"}}, nil
		},
		claimShared: func(string, string, tempowner.Marker, time.Time) (string, *tempowner.Owner, bool, error) {
			return "shared-native", owner, true, nil
		},
		mkdirTemp: func(string, string) (string, error) { return "private-native", nil },
		claim:     func(string, tempowner.Marker, time.Time) (*tempowner.Owner, error) { return owner, nil },
		remove:    func(string) error { return nil },
	}
}

func TestOpenNativeBuildCacheCarriesSweepFailuresOnBothSuccessRoutes(t *testing.T) {
	cause := errors.New("sweep failed")
	for _, shared := range []bool{true, false} {
		t.Run(map[bool]string{true: "shared", false: "private"}[shared], func(t *testing.T) {
			owner := nativeCacheTestOwner(t)
			dependencies := nativeOpenDependencies(owner)
			dependencies.sweep = func(string, []string, time.Time) (tempowner.Result, error) {
				return tempowner.Result{Removed: []string{"old-native"}}, cause
			}
			dependencies.claimShared = func(string, string, tempowner.Marker, time.Time) (string, *tempowner.Owner, bool, error) {
				return "shared-native", owner, shared, nil
			}
			directory, gotOwner, gotShared, swept, err := openNativeBuildCacheWith("base", runScratch{id: "run"}, nativeCacheMoment, dependencies)
			wantDirectory := "shared-native"
			if !shared {
				wantDirectory = "private-native"
			}
			if err != nil || directory != wantDirectory || gotOwner != owner || gotShared != shared || len(swept.Errors) != 1 || !errors.Is(swept.Errors[0], cause) {
				t.Fatalf("open = (%q, %p, %t, %+v, %v)", directory, gotOwner, gotShared, swept, err)
			}
		})
	}

	owner := nativeCacheTestOwner(t)
	dependencies := nativeOpenDependencies(owner)
	directory, gotOwner, shared, swept, err := openNativeBuildCacheWith("base", runScratch{id: "run"}, nativeCacheMoment, dependencies)
	if err != nil || directory != "shared-native" || gotOwner != owner || !shared || len(swept.Errors) != 0 {
		t.Fatalf("clean shared open = (%q, %p, %t, %+v, %v)", directory, gotOwner, shared, swept, err)
	}
}

func TestOpenNativeBuildCacheReportsEveryFailedStage(t *testing.T) {
	cause := errors.New("stage failed")
	sweepCause := errors.New("sweep failed")
	for _, stage := range []string{"shared claim", "private directory", "private claim"} {
		t.Run(stage, func(t *testing.T) {
			owner := nativeCacheTestOwner(t)
			dependencies := nativeOpenDependencies(owner)
			dependencies.sweep = func(string, []string, time.Time) (tempowner.Result, error) {
				return tempowner.Result{Removed: []string{"old-native"}}, sweepCause
			}
			dependencies.claimShared = func(string, string, tempowner.Marker, time.Time) (string, *tempowner.Owner, bool, error) {
				if stage == "shared claim" {
					return "", nil, false, cause
				}
				return "", nil, false, nil
			}
			dependencies.mkdirTemp = func(string, string) (string, error) {
				if stage == "private directory" {
					return "", cause
				}
				return "private-native", nil
			}
			removed := ""
			dependencies.remove = func(path string) error {
				removed = path
				return nil
			}
			dependencies.claim = func(string, tempowner.Marker, time.Time) (*tempowner.Owner, error) {
				if stage == "private claim" {
					return nil, cause
				}
				return owner, nil
			}
			directory, gotOwner, shared, swept, err := openNativeBuildCacheWith("base", runScratch{id: "run"}, nativeCacheMoment, dependencies)
			if directory != "" || gotOwner != nil || shared || !errors.Is(err, cause) || !errors.Is(err, sweepCause) || !slices.Equal(swept.Removed, []string{"old-native"}) {
				t.Fatalf("%s failure = (%q, %p, %t, %+v, %v)", stage, directory, gotOwner, shared, swept, err)
			}
			if stage == "private claim" && removed != "private-native" {
				t.Fatalf("removed private directory = %q", removed)
			}
		})
	}
}

func TestClaimSharedNativeBuildCacheReportsAClaimFailure(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	base := filepath.Join(parent, "base")
	directory := filepath.Join(parent, buildcache.NativeSharedDirectoryName(base))
	if err := os.MkdirAll(filepath.Join(directory, tempowner.LockName), filemode.PrivateDirectory); err != nil {
		t.Fatal(err)
	}
	gotDirectory, owner, claimed, err := claimSharedNativeBuildCache(parent, base, nativeCacheMarker("run"), nativeCacheMoment)
	if gotDirectory != "" || owner != nil || claimed || err == nil || !strings.Contains(err.Error(), "claim shared native build cache") {
		t.Fatalf("claim failure = (%q, %v, %t, %v)", gotDirectory, owner, claimed, err)
	}
}

func TestRemoveBuildCacheScratchStopsAtEveryBoundary(t *testing.T) {
	cause := errors.New("remove failed")
	calls := 0
	remove := func(path string) error {
		calls++
		if path != "scratch" {
			t.Fatalf("remove path = %q", path)
		}
		return cause
	}
	if err := removeBuildCacheScratchWith("", remove); err != nil || calls != 0 {
		t.Fatalf("empty removal = (%v, calls %d)", err, calls)
	}
	if err := removeBuildCacheScratchWith("scratch", remove); !errors.Is(err, cause) || calls != 1 || !strings.Contains(err.Error(), "remove build cache scratch") {
		t.Fatalf("failed removal = (%v, calls %d)", err, calls)
	}
}

func TestReportRunBuildCacheCollectionDistinguishesEveryOutcome(t *testing.T) {
	cause := errors.New("collect failed")
	for _, test := range []struct {
		name      string
		collected buildcache.Collected
		ran       bool
		err       error
		kind      string
	}{
		{name: "error", err: cause, kind: "build-cache-unavailable"},
		{name: "actions and objects", ran: true, collected: buildcache.Collected{RemovedActions: 1, RemovedObjects: 1}, kind: "build-cache-collected"},
		{name: "objects", ran: true, collected: buildcache.Collected{RemovedObjects: 1}, kind: "build-cache-collected"},
		{name: "ran without removals", ran: true},
		{name: "did not run with apparent removals", collected: buildcache.Collected{RemovedActions: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []Event
			reportRunBuildCacheCollection(Options{Progress: func(event Event) { events = append(events, event) }}, test.collected, test.ran, test.err)
			if test.kind == "" {
				if len(events) != 0 {
					t.Fatalf("events = %+v, want none", events)
				}
				return
			}
			if len(events) != 1 || events[0].Kind != test.kind {
				t.Fatalf("events = %+v, want %q", events, test.kind)
			}
		})
	}
}

func TestNativeCollectionDecisionsCoverEveryBoundary(t *testing.T) {
	cause := errors.New("projection failed")
	for _, test := range []struct {
		name     string
		maxBytes int64
		err      error
		disabled bool
		want     bool
	}{
		{name: "ready", maxBytes: 1, want: true},
		{name: "zero bound"},
		{name: "negative bound", maxBytes: -1},
		{name: "projection error", maxBytes: 1, err: cause},
		{name: "disabled", maxBytes: 1, disabled: true},
		{name: "error and disabled", maxBytes: 1, err: cause, disabled: true},
	} {
		if got := nativeCollectionReady(test.maxBytes, test.err, test.disabled); got != test.want {
			t.Errorf("%s ready = %t, want %t", test.name, got, test.want)
		}
	}
	moment := nativeCacheMoment
	for _, test := range []struct {
		name  string
		force bool
		last  time.Time
		want  bool
	}{
		{name: "forced", force: true, last: moment, want: true},
		{name: "never collected", want: true},
		{name: "exact interval", last: moment.Add(-buildcache.NativeCollectInterval), want: true},
		{name: "past interval", last: moment.Add(-buildcache.NativeCollectInterval - time.Nanosecond), want: true},
		{name: "before interval", last: moment.Add(-buildcache.NativeCollectInterval + time.Nanosecond)},
	} {
		if got := nativeCollectionDue(test.force, test.last, moment); got != test.want {
			t.Errorf("%s due = %t, want %t", test.name, got, test.want)
		}
	}
}

type failingBuildCacheWorkspace struct {
	err      error
	commands []gomutants.Command
}

func (workspace *failingBuildCacheWorkspace) Exec(_ context.Context, command gomutants.Command) (gomutants.CommandResult, error) {
	workspace.commands = append(workspace.commands, command)
	return gomutants.CommandResult{}, workspace.err
}

func TestBuildCacheWorkspacePropagatesErrorsAndMarksOnlyPersistentCommands(t *testing.T) {
	cause := errors.New("exec failed")
	projection := &nativeCacheProjection{}
	cache := runBuildCache{plain: "plain", persisting: "persisting", native: "native", projection: projection}
	workspace := &failingBuildCacheWorkspace{err: cause}
	wrapper := buildCacheWorkspace{
		workspace: workspace, nonPersisting: cache.environment(), persisting: cache.persistingEnvironment(), cache: cache,
	}
	if _, err := wrapper.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "build", "./..."}}); !errors.Is(err, cause) {
		t.Fatalf("persistent error = %v", err)
	}
	if projection.generation != 1 {
		t.Fatalf("persistent generation = %d, want 1", projection.generation)
	}
	if _, err := wrapper.Exec(t.Context(), gomutants.Command{Argv: []string{"binary", "-test.run=TestValue"}}); !errors.Is(err, cause) {
		t.Fatalf("non-persistent error = %v", err)
	}
	if projection.generation != 1 {
		t.Fatalf("non-persistent generation = %d, want unchanged", projection.generation)
	}
}

func TestBuildCacheCommandHelpersKeepExactBoundaries(t *testing.T) {
	for _, argv := range [][]string{{"go", "-C=directory"}, {"go", "-C", "directory"}} {
		if nativeExecutionCommand(argv) || persistingCommand(argv) {
			t.Errorf("incomplete command %q was classified", argv)
		}
	}
	for _, test := range []struct {
		argv []string
		want int
	}{
		{want: 0},
		{argv: []string{"go"}, want: 1},
		{argv: []string{"go", "test"}, want: 1},
		{argv: []string{"go", "-C"}, want: 3},
		{argv: []string{"go", "-C=directory"}, want: 2},
	} {
		if got := goSubcommandIndex(test.argv); got != test.want {
			t.Errorf("goSubcommandIndex(%q) = %d, want %d", test.argv, got, test.want)
		}
	}
	if got := argumentSeparator([]string{"-args", "value"}); got != 0 {
		t.Fatalf("leading argument separator = %d, want 0", got)
	}
}

func TestOverlayEnvironmentPreservesMalformedEntriesExactly(t *testing.T) {
	if got := overlayEnvironment([]string{"BROKEN"}, []string{"BROKEN"}); !slices.Equal(got, []string{"BROKEN", "BROKEN"}) {
		t.Fatalf("malformed overlay = %q", got)
	}
	if got := overlayEnvironment([]string{"BROKEN"}, []string{"BROKEN=value"}); !slices.Equal(got, []string{"BROKEN", "BROKEN=value"}) {
		t.Fatalf("malformed existing environment = %q", got)
	}
}

func TestUnwrapBuildCacheWorkspaceReturnsOnlyAConcreteWrappersInnerWorkspace(t *testing.T) {
	inner := &recordingWorkspace{}
	concrete := buildCacheWorkspace{workspace: inner}
	if got := unwrapBuildCacheWorkspace(concrete); got != CommandWorkspace(inner) {
		t.Fatalf("concrete unwrap = %T", got)
	}
	pointer := &buildCacheWorkspace{workspace: inner}
	if got := unwrapBuildCacheWorkspace(pointer); got != CommandWorkspace(pointer) {
		t.Fatalf("pointer unwrap = %T, want the original pointer", got)
	}
}

func TestCollectBaseDoesNotTouchAConfiguredLayerWhenTheCacheDoesNotServe(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	collected, ran, err := (runBuildCache{base: filepath.Join(blocked, "cache")}).collectBase(buildcache.Policy{MaxBytes: 1}, nativeCacheMoment)
	if err != nil || ran || collected != (buildcache.Collected{}) {
		t.Fatalf("unserved collection = (%+v, %t, %v)", collected, ran, err)
	}
}

func TestPreparationWithoutProjectionUsesTheNativeEnvironmentDirectly(t *testing.T) {
	cache := runBuildCache{plain: "plain", persisting: "persisting", native: "native"}
	if got := cache.preparationEnvironment(); !slices.Equal(got, []string{"GOCACHE=native", "GOCACHEPROG="}) {
		t.Fatalf("preparation environment = %q", got)
	}
}

func TestSeedNativeRecordsSuccessAndLeavesFailureUnseeded(t *testing.T) {
	base := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	success := &nativeCacheProjection{generation: 7}
	cache := runBuildCache{plain: "plain", base: base, native: t.TempDir(), projection: success}
	if !cache.seedNative() || !success.attempted || success.err != nil || success.seededGeneration != 7 || success.lastCollect.IsZero() {
		t.Fatalf("successful seed = %+v", success)
	}

	failure := &nativeCacheProjection{generation: 9}
	cache = runBuildCache{plain: "plain", native: "native", projection: failure}
	if cache.seedNative() || !failure.attempted || failure.err == nil || failure.seededGeneration != 0 || !failure.lastCollect.IsZero() {
		t.Fatalf("failed seed = %+v", failure)
	}
}

func TestBeginNativeDisablesAProjectionAfterRefreshFailure(t *testing.T) {
	projection := &nativeCacheProjection{attempted: true, generation: 2, seededGeneration: 1, beforeDrain: releaseUntrackedNativeExecution}
	projection.once.Do(func() {})
	cache := runBuildCache{plain: "plain", native: "native", projection: projection}
	release, admitted := cache.beginNative()
	if admitted || release != nil || !projection.disabled || projection.refreshErr == nil {
		t.Fatalf("refresh failure = (release %v, admitted %t, projection %+v)", release != nil, admitted, projection)
	}
}

func TestBeginNativeRefreshDoesNotForceAnImmediateCollection(t *testing.T) {
	base := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	body := "compiled archive"
	output := buildCacheTestKey(0x31)
	if _, err := (buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}).Put(
		buildCacheTestKey(1), output, strings.NewReader(body), int64(len(body)), nativeCacheMoment,
	); err != nil {
		t.Fatal(err)
	}
	native := t.TempDir()
	projection := &nativeCacheProjection{
		attempted: true, generation: 2, seededGeneration: 1, lastCollect: nativeCacheMoment,
		beforeDrain: releaseUntrackedNativeExecution,
	}
	projection.once.Do(func() {})
	cache := runBuildCache{plain: "plain", base: base, native: native, projection: projection, maxBytes: 1}
	release, admitted := cache.beginNative()
	if !admitted || release == nil || projection.disabled || projection.refreshErr != nil || projection.collected.RemovedBytes != 0 {
		t.Fatalf("refresh = (release %v, admitted %t, projection %+v)", release != nil, admitted, projection)
	}
	release()
	name := hex.EncodeToString(output)
	if _, err := os.Stat(filepath.Join(native, name[:2], name+"-d")); err != nil {
		t.Fatalf("uncollected refreshed object = %v", err)
	}
}

func TestRecordNativePersistenceDoesNotAdvanceAFailedSeed(t *testing.T) {
	cause := errors.New("persist failed")
	projection := &nativeCacheProjection{
		seed:      buildcache.NativeSeed{Actions: 2, Objects: 3, Bytes: 5, Skipped: 7},
		persisted: buildcache.NativePersisted{Actions: 11, Objects: 13, Bytes: 17, Skipped: 19},
		collected: buildcache.NativeCollected{BeforeBytes: 23, AfterBytes: 29},
	}
	seed := projection.seed
	persisted := buildcache.NativePersisted{Actions: 31, Objects: 37, Bytes: 41, Skipped: 43, Deferred: true}
	recordNativePersistence(projection, seed, persisted, cause)
	if !errors.Is(projection.persistErr, cause) || projection.seed.Actions != seed.Actions || projection.seed.Objects != seed.Objects ||
		projection.seed.Bytes != seed.Bytes || projection.seed.Skipped != seed.Skipped ||
		projection.collected.BeforeBytes != 23 || projection.collected.AfterBytes != 29 {
		t.Fatalf("failed persistence advanced the seed: %+v", projection)
	}
	if projection.persisted.Actions != 42 || projection.persisted.Objects != 50 || projection.persisted.Bytes != 58 ||
		projection.persisted.Skipped != 62 || !projection.persisted.Deferred {
		t.Fatalf("failed persistence was not measured: %+v", projection.persisted)
	}
}

func TestCollectNativeLockedAccumulatesEveryRemoval(t *testing.T) {
	base := t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	layers := buildcache.Layers{Base: buildcache.Layer{Dir: base}, Persist: true}
	for index := byte(1); index <= 2; index++ {
		if _, err := layers.Put(buildCacheTestKey(index), buildCacheTestKey(index+0x30), strings.NewReader("0123456789"), 10, nativeCacheMoment); err != nil {
			t.Fatal(err)
		}
	}
	native := t.TempDir()
	if _, err := buildcache.SeedNative(base, native, nativeCacheMoment); err != nil {
		t.Fatal(err)
	}
	projection := &nativeCacheProjection{}
	cache := runBuildCache{native: native, projection: projection, maxBytes: 10}
	cache.collectNativeLocked(true, nativeCacheMoment)
	if projection.collectErr != nil || projection.collected.RemovedActions <= 0 || projection.collected.RemovedObjects <= 0 || projection.collected.RemovedBytes <= 0 {
		t.Fatalf("native collection = %+v, err %v", projection.collected, projection.collectErr)
	}
}

func TestBuildCacheSummaryDistinguishesSweepAndProjectionStates(t *testing.T) {
	scratch := t.TempDir()
	if err := (buildcache.Layer{Dir: scratch}).Prepare(); err != nil {
		t.Fatal(err)
	}
	base := runBuildCache{plain: "plain", scratch: scratch}
	plain := base.summarize()
	if plain == "" {
		t.Fatal("plain summary is empty")
	}
	if got := (runBuildCache{plain: "plain", scratch: scratch, projection: &nativeCacheProjection{}}).summarize(); got != plain {
		t.Fatalf("unattempted projection summary = %q, want %q", got, plain)
	}
	for _, test := range []struct {
		name   string
		sweep  tempowner.Result
		marker string
	}{
		{name: "removed", sweep: tempowner.Result{Removed: []string{"old"}}, marker: "native-sweep-removed=1"},
		{name: "error", sweep: tempowner.Result{Errors: []error{errors.New("sweep failed")}}, marker: "native-sweep-removed=0"},
		{name: "both", sweep: tempowner.Result{Removed: []string{"old"}, Errors: []error{errors.New("sweep failed")}}, marker: "errors=1"},
	} {
		cache := base
		cache.nativeSweep = test.sweep
		if got := cache.summarize(); !strings.Contains(got, test.marker) {
			t.Errorf("%s summary = %q, want %q", test.name, got, test.marker)
		}
	}
	for _, test := range []struct {
		name       string
		projection *nativeCacheProjection
		status     string
	}{
		{name: "ready", projection: &nativeCacheProjection{attempted: true}, status: "native-seed=ready"},
		{name: "refresh", projection: &nativeCacheProjection{attempted: true, refreshErr: errors.New("refresh")}, status: "native-seed=refresh-failed"},
		{name: "collect", projection: &nativeCacheProjection{attempted: true, collectErr: errors.New("collect")}, status: "native-seed=collection-failed"},
		{name: "persist", projection: &nativeCacheProjection{attempted: true, persistErr: errors.New("persist")}, status: "native-seed=persistence-failed"},
		{name: "refresh precedence", projection: &nativeCacheProjection{attempted: true, refreshErr: errors.New("refresh"), collectErr: errors.New("collect"), persistErr: errors.New("persist")}, status: "native-seed=refresh-failed"},
		{name: "collect precedence", projection: &nativeCacheProjection{attempted: true, collectErr: errors.New("collect"), persistErr: errors.New("persist")}, status: "native-seed=collection-failed"},
	} {
		cache := base
		cache.projection = test.projection
		if got := cache.summarize(); !strings.Contains(got, test.status) {
			t.Errorf("%s summary = %q, want %q", test.name, got, test.status)
		}
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a cache"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if got := (runBuildCache{plain: "plain", scratch: blocked}).summarize(); got != "" {
		t.Fatalf("failed summary = %q, want empty", got)
	}
}

func TestPreparedMutationProbeFallsBackWithAnUnavailableProjection(t *testing.T) {
	projection := &nativeCacheProjection{attempted: true, err: errors.New("projection failed")}
	projection.once.Do(func() {})
	underlying := &mutationUnitSession{}
	wrapped := withNativeBuildCache(underlying, runBuildCache{
		plain: "plain", fallback: "fallback", native: "native", projection: projection,
	})
	if _, err := wrapped.Probe(t.Context(), gomutants.ProbeRequest{Env: []string{"RESOURCE=ready"}}); err != nil {
		t.Fatal(err)
	}
	if got := underlying.probeRequests()[0].Env; !slices.Equal(got, []string{"RESOURCE=ready", "GOCACHE=fallback", "GOCACHEPROG=plain"}) {
		t.Fatalf("probe fallback environment = %q", got)
	}
}

func TestBuildCacheCloseDoesNothingUnlessTheCacheServes(t *testing.T) {
	scratch := t.TempDir()
	owner := &failingTemporaryOwner{releaseErr: errors.New("must not release")}
	cache := runBuildCache{scratch: scratch, nativeOwner: owner}
	if err := cache.close(false); err != nil {
		t.Fatal(err)
	}
	if owner.released != 0 {
		t.Fatalf("unserved owner releases = %d", owner.released)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("unserved scratch = %v, want preserved", err)
	}
}

func TestSharedBuildCacheCloseCollectsOnlyWhenDue(t *testing.T) {
	withoutProjection := runBuildCache{
		plain: "plain", scratch: t.TempDir(), native: t.TempDir(), nativeShared: true,
	}
	if err := withoutProjection.close(false); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		lastCollect time.Time
		wantFailure bool
	}{
		{name: "due", wantFailure: true},
		{name: "recent", lastCollect: time.Now()},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := filepath.Join(t.TempDir(), "native")
			if err := os.WriteFile(native, []byte("not a directory"), filemode.PrivateFile); err != nil {
				t.Fatal(err)
			}
			projection := &nativeCacheProjection{lastCollect: test.lastCollect}
			cache := runBuildCache{
				plain: "plain", scratch: t.TempDir(), native: native, nativeShared: true,
				projection: projection, maxBytes: 1,
			}
			if err := cache.close(false); err != nil {
				t.Fatal(err)
			}
			if failed := projection.collectErr != nil && projection.disabled; failed != test.wantFailure {
				t.Fatalf("collection failed=%t, want %t; projection %+v", failed, test.wantFailure, projection)
			}
		})
	}
}
