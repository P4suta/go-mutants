// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	collectedEntrySize = 10
	twoEntriesOfTen    = 20
	oneHour            = time.Hour
)

type collectedEntry struct {
	action byte
	output byte
	aged   time.Duration
}

func storeCollectedEntries(t *testing.T, layer Layer, entries ...collectedEntry) {
	t.Helper()
	body := strings.Repeat("0", collectedEntrySize)
	for _, entry := range entries {
		outputID := key(entry.output)
		objectPath := layer.objectPath(outputID)
		writeStoredFile(t, objectPath, body)
		writeStoredFile(t, layer.actionPath(key(entry.action)), storedActionJSON(t, outputID))
		stamp := collectMoment.Add(-entry.aged)
		for _, path := range []string{objectPath, layer.actionPath(key(entry.action))} {
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writeStoredFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
}

func storedActionJSON(t *testing.T, outputID []byte) string {
	t.Helper()
	encoded, err := json.Marshal(actionRecord{
		Output: hex.EncodeToString(outputID), Size: collectedEntrySize, Time: collectMoment,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func survivingActions(t *testing.T, layer Layer) []string {
	t.Helper()
	actions, _, err := layer.list(layerHooks{}.resolved())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(actions))
	for _, action := range actions {
		names = append(names, action.name[:entryPrefixHexDigits])
	}
	slices.Sort(names)
	return names
}

func TestACollectionReadsTheOldestActionItHolds(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t,
		layer,
		collectedEntry{action: 0xaa, output: 1, aged: oneHour},
		collectedEntry{action: 0xbb, output: 2, aged: 2 * oneHour},
		collectedEntry{action: 0xcc, output: 3},
	)
	collected, err := layer.collectWithHooks(Policy{}, collectMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	oldest := collectMoment.Add(-2 * oneHour)
	if !collected.Before.Oldest.Equal(oldest) {
		t.Fatalf("the collection read %s as its oldest action, want %s", collected.Before.Oldest, oldest)
	}
	if !collected.After.Oldest.Equal(oldest) {
		t.Errorf("the collection left %s as its oldest action, want %s", collected.After.Oldest, oldest)
	}
}

func TestACollectionEvictsTheOldestActionFirst(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t,
		layer,
		collectedEntry{action: 0xaa, output: 1, aged: oneHour},
		collectedEntry{action: 0xbb, output: 2, aged: 2 * oneHour},
		collectedEntry{action: 0xcc, output: 3, aged: 2 * oneHour},
	)
	collected, err := layer.collectWithHooks(Policy{MaxBytes: twoEntriesOfTen}, collectMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if collected.RemovedActions != 1 {
		t.Fatalf("the collection removed %d actions, want the one it had to", collected.RemovedActions)
	}
	if got := survivingActions(t, layer); !slices.Equal(got, []string{"aa", "cc"}) {
		t.Fatalf("the collection left %q, want the oldest of the two written together taken first", got)
	}
}

func TestACollectionKeepsAnObjectAnotherActionStillNames(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t,
		layer,
		collectedEntry{action: 0xaa, output: 1, aged: 2 * oneHour},
		collectedEntry{action: 0xbb, output: 1},
	)
	collected, err := layer.collectWithHooks(
		Policy{TTL: oneHour}, collectMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if collected.RemovedActions != 1 {
		t.Fatalf("the collection removed %d actions, want the one past its time", collected.RemovedActions)
	}
	if collected.RemovedObjects != 0 || collected.RemovedBytes != 0 {
		t.Fatalf("the collection removed %d objects, want the one the surviving action still names kept",
			collected.RemovedObjects)
	}
	if _, err := os.Stat(layer.objectPath(key(1))); err != nil {
		t.Errorf("the object a surviving action names is gone: %v", err)
	}
}

func TestACollectionReleasesAnObjectOnceItsLastActionIsGone(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t,
		layer,
		collectedEntry{action: 0xaa, output: 1, aged: 2 * oneHour},
		collectedEntry{action: 0xbb, output: 1, aged: 2 * oneHour},
	)
	collected, err := layer.collectWithHooks(Policy{TTL: oneHour}, collectMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if collected.RemovedObjects != 1 || collected.RemovedBytes != collectedEntrySize {
		t.Fatalf("the collection removed %d objects of %d bytes, want the one nothing names any more",
			collected.RemovedObjects, collected.RemovedBytes)
	}
}

func TestACollectionReleasesAnObjectNoActionEverNamed(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	orphan := key(7)
	writeStoredFile(t, layer.objectPath(orphan), strings.Repeat("0", collectedEntrySize))
	collected, err := layer.collectWithHooks(Policy{}, collectMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if collected.RemovedObjects != 1 {
		t.Fatalf("the collection removed %d objects, want the one no action names", collected.RemovedObjects)
	}
	if _, err := os.Stat(layer.objectPath(orphan)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an object no action names survived: %v", err)
	}
}

func TestACollectionSparesAnEntryTheIdleWindowStillCovers(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		minIdle time.Duration
		now     time.Time
		aged    time.Duration
		spared  bool
	}{
		{name: "younger than the window", minIdle: oneHour, now: collectMoment, aged: oneHour - time.Second, spared: true},
		{name: "exactly as old as the window", minIdle: oneHour, now: collectMoment, aged: oneHour},
		{name: "older than the window", minIdle: oneHour, now: collectMoment, aged: oneHour + 1},
		{name: "no window at all", now: collectMoment, aged: 0},
		{name: "a clock that was never set", minIdle: oneHour, aged: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			writeStoredFile(t, layer.objectPath(key(7)), strings.Repeat("0", collectedEntrySize))
			stamp := collectMoment.Add(-test.aged)
			if err := os.Chtimes(layer.objectPath(key(7)), stamp, stamp); err != nil {
				t.Fatal(err)
			}
			collected, err := layer.collectWithHooks(Policy{MinIdle: test.minIdle}, test.now, layerHooks{})
			if err != nil {
				t.Fatal(err)
			}
			if spared := collected.RemovedObjects == 0; spared != test.spared {
				t.Fatalf("an object %s was spared=%t, want %t", test.name, spared, test.spared)
			}
		})
	}
}

func TestACollectionDropsAnActionExactlyAsOldAsItsTime(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		aged    time.Duration
		dropped bool
	}{
		{name: "one second inside its time", aged: oneHour - time.Second},
		{name: "exactly as old as its time", aged: oneHour, dropped: true},
		{name: "one second past its time", aged: oneHour + time.Second, dropped: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			storeCollectedEntries(t, layer, collectedEntry{action: 0xaa, output: 1, aged: test.aged})
			collected, err := layer.collectWithHooks(Policy{TTL: oneHour}, collectMoment, layerHooks{})
			if err != nil {
				t.Fatal(err)
			}
			if dropped := collected.RemovedActions == 1; dropped != test.dropped {
				t.Fatalf("an action %s was dropped=%t, want %t", test.name, dropped, test.dropped)
			}
		})
	}
}

func TestACollectionReportsWhatItCouldNotRemove(t *testing.T) {
	t.Parallel()
	failure := errors.New("the entry could not be removed")
	for _, test := range []struct {
		name      string
		half      string
		tolerated error
		want      string
	}{
		{name: "an action it cannot remove", half: actionsDirectory, want: "collect build cache action"},
		{name: "an object it cannot remove", half: objectsDirectory, want: "collect build cache object"},
		{name: "an action somebody removed first", half: actionsDirectory, tolerated: os.ErrNotExist},
		{name: "an object somebody removed first", half: objectsDirectory, tolerated: os.ErrNotExist},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			storeCollectedEntries(t, layer, collectedEntry{action: 0xaa, output: 1, aged: 2 * oneHour})
			answer := failure
			if test.tolerated != nil {
				answer = test.tolerated
			}
			hooks := layerHooks{remove: func(path string) error {
				if strings.Contains(path, string(filepath.Separator)+test.half+string(filepath.Separator)) {
					return answer
				}
				return os.Remove(path)
			}}
			_, err := layer.collectWithHooks(Policy{TTL: oneHour}, collectMoment, hooks)
			if test.tolerated != nil {
				if err != nil {
					t.Fatalf("a collection past %s reported %v, want nothing", test.name, err)
				}
				return
			}
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("collecting %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
		})
	}
}

func TestACollectionRefusesAPolicyBelowZero(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	for _, policy := range []Policy{{MaxBytes: -1}, {TTL: -1}, {MinIdle: -1}} {
		if _, err := layer.collectWithHooks(policy, collectMoment, layerHooks{}); err == nil {
			t.Errorf("policy %+v was accepted", policy)
		}
	}
}

func TestListingALayerWalksItInPathOrderHoweverTheDirectoryAnswers(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t,
		layer,
		collectedEntry{action: 0xaa, output: 1},
		collectedEntry{action: 0xbb, output: 2},
		collectedEntry{action: 0xcc, output: 3},
	)
	hooks := layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
		entries, err := os.ReadDir(path)
		slices.Reverse(entries)
		return entries, err
	}}
	actions, _, err := layer.list(hooks.resolved())
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(actions))
	for _, action := range actions {
		paths = append(paths, action.path)
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("the walk answered %q, want them in path order whatever order the directory gave", paths)
	}
}

func TestListingALayerSkipsWhatIsNotAnEntry(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t, layer, collectedEntry{action: 0xaa, output: 1})
	actionsRoot := filepath.Join(layer.Dir, actionsDirectory)
	writeStoredFile(t, filepath.Join(actionsRoot, "zz"), "")
	writeStoredFile(t, filepath.Join(actionsRoot, "aa", "not-hexadecimal"), "")
	if err := os.MkdirAll(filepath.Join(actionsRoot, "aa", "ffff"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	actions, _, err := layer.list(layerHooks{}.resolved())
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("the walk read %d actions, want the one entry among what is not one: %+v", len(actions), actions)
	}
	if actions[0].name != hex.EncodeToString(key(0xaa)) {
		t.Errorf("the walk read %q, want the entry it stored", actions[0].name)
	}
}

func TestListingALayerReportsEveryReadItCouldNotFinish(t *testing.T) {
	t.Parallel()
	failure := errors.New("the layer could not be read")
	for _, test := range []struct {
		name  string
		hooks layerHooks
	}{
		{
			name: "a half it cannot open",
			hooks: layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
				if strings.HasSuffix(path, actionsDirectory) {
					return nil, failure
				}
				return os.ReadDir(path)
			}},
		},
		{
			name: "a prefix it cannot open",
			hooks: layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
				if filepath.Base(filepath.Dir(path)) == objectsDirectory {
					return nil, failure
				}
				return os.ReadDir(path)
			}},
		},
		{
			name:  "an action it cannot read",
			hooks: layerHooks{readFile: func(string) ([]byte, error) { return nil, failure }},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			storeCollectedEntries(t, layer, collectedEntry{action: 0xaa, output: 1})
			if _, _, err := layer.list(test.hooks.resolved()); !errors.Is(err, failure) {
				t.Fatalf("listing %s reported %v, want %v", test.name, err, failure)
			}
		})
	}
}

func TestListingALayerTreatsAnActionThatVanishedAsEmpty(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t, layer, collectedEntry{action: 0xaa, output: 1})
	hooks := layerHooks{readFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }}
	actions, _, err := layer.list(hooks.resolved())
	if err != nil {
		t.Fatalf("listing past an action that vanished reported %v, want nothing", err)
	}
	if len(actions) != 1 || actions[0].output != "" {
		t.Fatalf("the walk read %+v, want the entry with no output read from it", actions)
	}
}

func TestListingALayerThatIsNotThereReadsNothing(t *testing.T) {
	t.Parallel()
	actions, objects, err := (Layer{Dir: filepath.Join(t.TempDir(), "absent")}).list(layerHooks{}.resolved())
	if err != nil || len(actions) != 0 || len(objects) != 0 {
		t.Fatalf("a layer that is not there listed (%+v, %+v, %v), want nothing", actions, objects, err)
	}
	if actions, _, err := (Layer{}).list(layerHooks{}.resolved()); err != nil || actions != nil {
		t.Fatalf("a layer with no directory listed (%+v, %v), want nothing", actions, err)
	}
}

type stubDirEntry struct {
	name    string
	dir     bool
	infoErr error
}

func (entry stubDirEntry) Name() string      { return entry.name }
func (entry stubDirEntry) IsDir() bool       { return entry.dir }
func (entry stubDirEntry) Type() os.FileMode { return 0 }
func (entry stubDirEntry) Info() (os.FileInfo, error) {
	if entry.infoErr != nil {
		return nil, entry.infoErr
	}
	return stubLayerInfo{}, nil
}

func TestListingALayerSkipsAPrefixThatVanishedUnderIt(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t, layer, collectedEntry{action: 0xaa, output: 1})
	hooks := layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
		if filepath.Base(filepath.Dir(path)) == actionsDirectory {
			return nil, os.ErrNotExist
		}
		return os.ReadDir(path)
	}}
	actions, objects, err := layer.list(hooks.resolved())
	if err != nil {
		t.Fatalf("listing past a prefix that vanished reported %v, want nothing", err)
	}
	if len(actions) != 0 || len(objects) != 1 {
		t.Fatalf("the walk read %d actions and %d objects, want none and one", len(actions), len(objects))
	}
}

func TestListingALayerAnswersForAnEntryItCannotDescribe(t *testing.T) {
	t.Parallel()
	failure := errors.New("the entry could not be described")
	for _, test := range []struct {
		name  string
		cause error
		read  int
	}{
		{name: "an entry removed under the walk", cause: os.ErrNotExist},
		{name: "an entry it cannot describe at all", cause: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			hooks := layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
				if filepath.Base(path) == actionsDirectory || filepath.Base(path) == objectsDirectory {
					return []os.DirEntry{stubDirEntry{name: "aa", dir: true}}, nil
				}
				return []os.DirEntry{stubDirEntry{name: hex.EncodeToString(key(1)), infoErr: test.cause}}, nil
			}}
			_, err := layer.walk(actionsDirectory, hooks.resolved())
			if errors.Is(test.cause, os.ErrNotExist) {
				if err != nil {
					t.Fatalf("walking past %s reported %v, want nothing", test.name, err)
				}
				return
			}
			if !errors.Is(err, failure) {
				t.Fatalf("walking %s reported %v, want %v", test.name, err, failure)
			}
		})
	}
}

func TestListingALayerReportsAnActionItCouldNotReadHoweverLateItComes(t *testing.T) {
	t.Parallel()
	failure := errors.New("the second action could not be read")
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t,
		layer,
		collectedEntry{action: 0xaa, output: 1},
		collectedEntry{action: 0xbb, output: 2},
	)
	hooks := layerHooks{readFile: func(path string) ([]byte, error) {
		if strings.Contains(path, hex.EncodeToString(key(0xbb))) {
			return nil, failure
		}
		return os.ReadFile(path)
	}}
	if _, _, err := layer.list(hooks.resolved()); !errors.Is(err, failure) {
		t.Fatalf("listing reported %v, want the failure of the action that is not the first", err)
	}
}

func TestListingALayerReadsNoOutputFromAnActionThatIsNotOne(t *testing.T) {
	t.Parallel()
	layer := Layer{Dir: t.TempDir()}
	writeStoredFile(t, layer.actionPath(key(0xaa)), `{"output":"`+hex.EncodeToString(key(1))+`","size":"twelve"}`)
	actions, _, err := layer.list(layerHooks{}.resolved())
	if err != nil {
		t.Fatalf("listing reported %v, want a record it cannot read left empty", err)
	}
	if len(actions) != 1 || actions[0].output != "" {
		t.Fatalf("the walk read %+v, want nothing read from a record that is not one", actions)
	}
}

func TestACollectionReportsAnActionItCouldNotRemoveUnderItsCeiling(t *testing.T) {
	t.Parallel()
	failure := errors.New("the entry could not be removed")
	layer := Layer{Dir: t.TempDir()}
	storeCollectedEntries(t,
		layer,
		collectedEntry{action: 0xaa, output: 1},
		collectedEntry{action: 0xbb, output: 2},
	)
	hooks := layerHooks{remove: func(string) error { return failure }}
	if _, err := layer.collectWithHooks(Policy{MaxBytes: collectedEntrySize}, collectMoment, hooks); !errors.Is(err, failure) {
		t.Fatalf("a collection under its ceiling reported %v, want %v", err, failure)
	}
}
