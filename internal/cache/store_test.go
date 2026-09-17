// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testsupport"
)

var mutantIDs = []string{
	strings.Repeat("a1", 32),
	strings.Repeat("b2", 32),
	strings.Repeat("c3", 32),
}

func open(t *testing.T, root string, ctx cache.Context) *cache.Cache {
	t.Helper()
	return openWithin(t, root, ctx, testTimeout)
}

const testTimeout = 10 * time.Second

func openWithin(t *testing.T, root string, ctx cache.Context, timeout time.Duration) *cache.Cache {
	t.Helper()
	store, err := cache.Open(cache.Options{Root: root, Context: ctx, Timeout: timeout})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func killedEntry() cache.Entry {
	return cache.Entry{
		Outcome:    mutation.OutcomeKilled,
		DurationMS: 120,
		KilledBy:   "example.com/m/internal/alpha",
		Attempts:   1,
		OutputTail: "--- FAIL: TestAdd (0.00s)",
	}
}

func TestAStoredOutcomeComesBack(t *testing.T) {
	t.Parallel()

	store := open(t, t.TempDir(), baseContext())
	if err := store.Put(mutantIDs[0], killedEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entry, found, err := store.Lookup(mutantIDs[0])
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !found {
		t.Fatal("the outcome just stored was not found")
	}
	want := killedEntry()
	switch {
	case entry.Outcome != want.Outcome:
		t.Errorf("outcome = %s, want %s", entry.Outcome, want.Outcome)
	case entry.DurationMS != want.DurationMS:
		t.Errorf("duration = %dms, want %dms", entry.DurationMS, want.DurationMS)
	case entry.KilledBy != want.KilledBy:
		t.Errorf("killed_by = %q, want %q", entry.KilledBy, want.KilledBy)
	case entry.Attempts != want.Attempts:
		t.Errorf("attempts = %d, want %d", entry.Attempts, want.Attempts)
	case entry.OutputTail != want.OutputTail:
		t.Errorf("output_tail = %q, want %q", entry.OutputTail, want.OutputTail)
	case entry.ID != mutantIDs[0]:
		t.Errorf("the entry names mutant %q, want %q", entry.ID, mutantIDs[0])
	case entry.Context != store.ContextKey():
		t.Errorf("the entry names context %q, want %q", entry.Context, store.ContextKey())
	case entry.Key != store.Key():
		t.Errorf("the entry names key %q, want %q", entry.Key, store.Key())
	}
}

func TestAnUnknownMutantIsAnOrdinaryMiss(t *testing.T) {
	t.Parallel()

	store := open(t, t.TempDir(), baseContext())
	entry, found, err := store.Lookup(mutantIDs[0])
	if err != nil {
		t.Errorf("a miss reported an error: %v", err)
	}
	if found {
		t.Errorf("an empty cache answered with %+v", entry)
	}
}

func TestOneContextCannotReadAnother(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := open(t, root, baseContext())
	if err := first.Put(mutantIDs[0], killedEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	moved := baseContext()
	moved.WorkspaceDigest = strings.Repeat("fe", 32)
	second := open(t, root, moved)
	if second.ContextKey() == first.ContextKey() {
		t.Fatal("two workspaces landed on one context key")
	}
	if _, found, err := second.Lookup(mutantIDs[0]); found || err != nil {
		t.Errorf("the edited workspace read the old outcome (found=%t, err=%v)", found, err)
	}
	if _, found, _ := first.Lookup(mutantIDs[0]); !found {
		t.Error("the original context lost its own entry")
	}
}

func TestAnEntryThatIsNotAnEntryIsAMiss(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"truncated JSON":  `{"version":2,"outcome":"kil`,
		"not JSON at all": "the antivirus quarantined this file",
		"a future version": `{"version":3,"key":"{key}","context":"{context}","id":"{id}","outcome":"killed",` +
			`"duration_ms":1,"timeout_ms":10000,"attempts":1}`,
		"a version 1 entry left by an older build": `{"version":1,"context":"{context}","id":"{id}",` +
			`"outcome":"killed","duration_ms":1,"timeout_ms":10000,"attempts":1}`,
		"another mutant's outcome": `{"version":2,"key":"{key}","context":"{context}","id":"` +
			`0000000000000000000000000000000000000000000000000000000000000000","outcome":"killed",` +
			`"duration_ms":1,"timeout_ms":10000,"attempts":1}`,
		"another context's outcome": `{"version":2,"key":"{key}","context":"0000000000000000","id":"{id}",` +
			`"outcome":"killed","duration_ms":1,"timeout_ms":10000,"attempts":1}`,
		"an outcome no run may reuse": `{"version":2,"key":"{key}","context":"{context}","id":"{id}",` +
			`"outcome":"inconclusive","duration_ms":1,"timeout_ms":10000,"attempts":2}`,
		"a measurement that never happened": `{"version":2,"key":"{key}","context":"{context}","id":"{id}",` +
			`"outcome":"killed","duration_ms":1,"timeout_ms":10000,"attempts":0}`,
		"a measurement made under no bound at all": `{"version":2,"key":"{key}","context":"{context}","id":"{id}",` +
			`"outcome":"killed","duration_ms":1,"timeout_ms":0,"attempts":1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := open(t, t.TempDir(), baseContext())
			id := mutantIDs[0]
			content := strings.NewReplacer(
				"{key}", store.Key(),
				"{context}", store.ContextKey(),
				"{id}", id,
			).Replace(body)
			write(t, filepath.Join(store.Dir(), id+".json"), content)

			entry, found, err := store.Lookup(id)
			if found {
				t.Errorf("a corrupt entry was adopted as %+v", entry)
			}
			if err == nil {
				t.Fatal("a corrupt entry was not reported")
			}
			if code := cache.CodeOf(err); code != cache.CodeCorruptEntry {
				t.Errorf("code = %q, want %q (%v)", code, cache.CodeCorruptEntry, err)
			}
		})
	}
}

func TestATruncationCollisionIsRefused(t *testing.T) {
	t.Parallel()

	store := open(t, t.TempDir(), baseContext())
	id := mutantIDs[0]
	if err := store.Put(id, killedEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	other := store.Key()
	colliding := other[:cache.ContextKeyLength] +
		strings.Repeat("0", cache.KeyHexLength-cache.ContextKeyLength)
	if colliding == other {
		t.Fatalf("the constructed key %s is not a different one", colliding)
	}
	if got := colliding[:cache.ContextKeyLength]; got != store.ContextKey() {
		t.Fatalf("the constructed key is filed under %s, want %s", got, store.ContextKey())
	}
	write(t, filepath.Join(store.Dir(), id+".json"),
		`{"version":2,"key":"`+colliding+`","context":"`+store.ContextKey()+`","id":"`+id+`",`+
			`"outcome":"survived","duration_ms":1,"timeout_ms":10000,"attempts":1}`)

	entry, found, err := store.Lookup(id)
	if found {
		t.Errorf("a colliding context's outcome was adopted as %+v", entry)
	}
	if err == nil {
		t.Fatal("a colliding context's outcome was not reported")
	}
	if code := cache.CodeOf(err); code != cache.CodeCorruptEntry {
		t.Errorf("code = %q, want %q (%v)", code, cache.CodeCorruptEntry, err)
	}
}

func TestCacheableIsTheWholeRule(t *testing.T) {
	t.Parallel()

	want := map[mutation.Outcome]bool{
		mutation.OutcomeKilled:       true,
		mutation.OutcomeSurvived:     true,
		mutation.OutcomeTimedOut:     true,
		mutation.OutcomeInconclusive: false,
		mutation.OutcomeErrored:      false,
		mutation.OutcomeNotRun:       false,
	}
	for _, outcome := range mutation.Outcomes() {
		expected, listed := want[outcome]
		if !listed {
			t.Fatalf("the outcome %s is not in the table: decide whether it may be reused", outcome)
		}
		if got := cache.Cacheable(outcome); got != expected {
			t.Errorf("Cacheable(%s) = %t, want %t", outcome, got, expected)
		}
	}
}

func TestAnEntryIsOnlyEvidenceAboutARunWithACompatibleBound(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		entry   cache.Entry
		measure time.Duration
		bound   time.Duration
		want    bool
	}{
		{
			name:    "a kill well inside a slightly smaller bound",
			entry:   cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 200, Attempts: 1},
			measure: 30 * time.Second, bound: 29 * time.Second, want: true,
		},
		{
			name:    "a kill that would not have fitted",
			entry:   cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 9000, Attempts: 1},
			measure: 30 * time.Second, bound: 8 * time.Second, want: false,
		},
		{
			name:    "a survivor exactly at the new bound",
			entry:   cache.Entry{Outcome: mutation.OutcomeSurvived, DurationMS: 8000, Attempts: 1},
			measure: 30 * time.Second, bound: 8 * time.Second, want: true,
		},
		{
			name:    "a confirmed timeout under a tighter bound still times out",
			entry:   cache.Entry{Outcome: mutation.OutcomeTimedOut, DurationMS: 20000, Attempts: 2},
			measure: 10 * time.Second, bound: 9 * time.Second, want: true,
		},
		{
			name:    "a confirmed timeout might have finished under a larger one",
			entry:   cache.Entry{Outcome: mutation.OutcomeTimedOut, DurationMS: 20000, Attempts: 2},
			measure: 10 * time.Second, bound: 11 * time.Second, want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writer := openWithin(t, root, baseContext(), c.measure)
			if err := writer.Put(mutantIDs[0], c.entry); err != nil {
				t.Fatalf("Put: %v", err)
			}
			reader := openWithin(t, root, baseContext(), c.bound)
			got, found, err := reader.Lookup(mutantIDs[0])
			if err != nil {
				t.Fatalf("a bound mismatch was reported as a problem: %v", err)
			}
			if found != c.want {
				t.Errorf("an entry measured under %s was adopted under %s = %t, want %t",
					c.measure, c.bound, found, c.want)
			}
			if found && got.Timeout() != c.measure {
				t.Errorf("the entry records a bound of %s, want %s", got.Timeout(), c.measure)
			}
		})
	}
}

func TestPutRefusesWhatNoRunMayReuse(t *testing.T) {
	t.Parallel()

	store := open(t, t.TempDir(), baseContext())
	for _, outcome := range []mutation.Outcome{
		mutation.OutcomeInconclusive,
		mutation.OutcomeErrored,
		mutation.OutcomeNotRun,
	} {
		entry := killedEntry()
		entry.Outcome = outcome
		err := store.Put(mutantIDs[0], entry)
		if err == nil {
			t.Fatalf("Put stored a %s outcome", outcome)
		}
		if code := cache.CodeOf(err); code != cache.CodeEntryNotWritten {
			t.Errorf("code = %q, want %q (%v)", code, cache.CodeEntryNotWritten, err)
		}
		if _, found, _ := store.Lookup(mutantIDs[0]); found {
			t.Fatalf("a %s outcome reached the disk", outcome)
		}
	}
}

func TestAnIDThatIsNotAnIDNamesNoFile(t *testing.T) {
	t.Parallel()

	store := open(t, t.TempDir(), baseContext())
	for _, id := range []string{
		"",
		"../../../etc/passwd",
		strings.Repeat("a", 63),
		strings.Repeat("A", 64),
		filepath.Join("..", strings.Repeat("a", 60)),
	} {
		if err := store.Put(id, killedEntry()); err == nil {
			t.Errorf("Put accepted %q as a mutant id", id)
		}
		if _, _, err := store.Lookup(id); err == nil {
			t.Errorf("Lookup accepted %q as a mutant id", id)
		}
	}
}

func TestALongOutputTailIsTruncated(t *testing.T) {
	t.Parallel()

	store := open(t, t.TempDir(), baseContext())
	entry := killedEntry()
	entry.OutputTail = strings.Repeat("x", cache.MaxOutputTail*2) + "the assertion that failed"
	if err := store.Put(mutantIDs[0], entry); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stored, found, err := store.Lookup(mutantIDs[0])
	if err != nil || !found {
		t.Fatalf("Lookup: found=%t err=%v", found, err)
	}
	if len(stored.OutputTail) > cache.MaxOutputTail*2 {
		t.Errorf("the stored tail is %d bytes, far past the %d cap", len(stored.OutputTail), cache.MaxOutputTail)
	}
	if !strings.HasSuffix(stored.OutputTail, "the assertion that failed") {
		t.Error("truncation kept the beginning of the output rather than the end")
	}
	if !strings.Contains(stored.OutputTail, "truncated") {
		t.Error("the truncated tail does not say that it was truncated")
	}
}

func TestOpenRefusesAWorkspaceThatBelongsToSomethingElse(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := baseContext()
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(ctx.WorkspaceDigest))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating the workspace directory: %v", err)
	}
	write(t, filepath.Join(dir, report.MarkerFileName),
		"go-mutants-workspace-v1\n"+strings.Repeat("ff", 32)+"\n")

	_, err := cache.Open(cache.Options{Root: root, Context: ctx})
	if err == nil {
		t.Fatal("the cache claimed a directory belonging to another workspace")
	}
	if code := cache.CodeOf(err); code != cache.CodeUnavailable {
		t.Errorf("code = %q, want %q (%v)", code, cache.CodeUnavailable, err)
	}
	if !strings.Contains(err.Error(), string(report.CodeForeignWorkspace)) {
		t.Errorf("the refusal does not carry the marker's own code %s: %v", report.CodeForeignWorkspace, err)
	}
}

func TestOpenClaimsTheSameMarkerTheHistoryDoes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := baseContext()
	store := open(t, root, ctx)

	workspace := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(ctx.WorkspaceDigest))
	digest, err := report.ReadMarker(workspace)
	if err != nil {
		t.Fatalf("ReadMarker: %v", err)
	}
	if digest != ctx.WorkspaceDigest {
		t.Errorf("the marker names %s, want %s", digest, ctx.WorkspaceDigest)
	}
	want := filepath.Join(workspace, cache.OutcomesDirName, store.ContextKey())
	if store.Dir() != want {
		t.Errorf("entries are filed in %s, want %s", store.Dir(), want)
	}
	if _, err = cache.Open(cache.Options{Root: root, Context: ctx}); err != nil {
		t.Errorf("re-opening the same workspace failed: %v", err)
	}
}

func TestRootResolvesUnderTheOperatingSystemCache(t *testing.T) {
	base := testsupport.CacheDir(t)

	root, err := cache.Root("")
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if want := filepath.Join(base, report.DirName); root != want {
		t.Errorf("the default root is %s, want %s", root, want)
	}
	moved, err := cache.Root("team/cache")
	if err != nil {
		t.Fatalf("Root with a directory: %v", err)
	}
	if want := filepath.Join(base, "team", "cache"); moved != want {
		t.Errorf("the configured root is %s, want %s", moved, want)
	}
	if _, err = cache.Root("../elsewhere"); err == nil {
		t.Error("a directory climbing out of the cache root was accepted")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestAnEntryIsOnlyEvidenceAboutARunWithACompatibleMemoryBound(t *testing.T) {
	t.Parallel()

	const (
		gib = 1 << 30
		mib = 1 << 20
	)
	cases := []struct {
		name    string
		entry   cache.Entry
		measure int64
		bound   int64
		want    bool
	}{
		{
			name:    "a memory kill under the same bound",
			entry:   cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 200, Attempts: 1, MemoryExceeded: true},
			measure: 256 * mib, bound: 256 * mib, want: true,
		},
		{
			name:    "a memory kill under a tighter bound would still have been one",
			entry:   cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 200, Attempts: 1, MemoryExceeded: true},
			measure: 256 * mib, bound: 128 * mib, want: true,
		},
		{
			name:    "a memory kill might have finished under a larger bound",
			entry:   cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 200, Attempts: 1, MemoryExceeded: true},
			measure: 256 * mib, bound: 8 * gib, want: false,
		},
		{
			name:    "a memory kill says nothing about a run with no bound at all",
			entry:   cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 200, Attempts: 1, MemoryExceeded: true},
			measure: 256 * mib, bound: 0, want: false,
		},
		{
			name:    "a survivor under a larger bound survives again",
			entry:   cache.Entry{Outcome: mutation.OutcomeSurvived, DurationMS: 200, Attempts: 1},
			measure: 256 * mib, bound: 8 * gib, want: true,
		},
		{
			name:    "a survivor might have been killed by a smaller bound",
			entry:   cache.Entry{Outcome: mutation.OutcomeSurvived, DurationMS: 200, Attempts: 1},
			measure: 8 * gib, bound: 256 * mib, want: false,
		},
		{
			name:    "a confirmed timeout might have been killed by a smaller bound",
			entry:   cache.Entry{Outcome: mutation.OutcomeTimedOut, DurationMS: 200, Attempts: 2},
			measure: 8 * gib, bound: 256 * mib, want: false,
		},
		{
			name:    "an ordinary kill is evidence about any bound",
			entry:   cache.Entry{Outcome: mutation.OutcomeKilled, DurationMS: 200, Attempts: 1},
			measure: 8 * gib, bound: 256 * mib, want: true,
		},
		{
			name:    "an entry measured with no bound is judged as it always was",
			entry:   cache.Entry{Outcome: mutation.OutcomeSurvived, DurationMS: 200, Attempts: 1},
			measure: 0, bound: 256 * mib, want: true,
		},
		{
			name:    "and so is a run with no bound reading one",
			entry:   cache.Entry{Outcome: mutation.OutcomeSurvived, DurationMS: 200, Attempts: 1},
			measure: 0, bound: 0, want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writer := openBounded(t, root, baseContext(), c.measure)
			if err := writer.Put(mutantIDs[0], c.entry); err != nil {
				t.Fatalf("Put: %v", err)
			}
			reader := openBounded(t, root, baseContext(), c.bound)
			got, found, err := reader.Lookup(mutantIDs[0])
			if err != nil {
				t.Fatalf("a bound mismatch was reported as a problem: %v", err)
			}
			if found != c.want {
				t.Errorf("an entry measured under %d bytes was adopted under %d = %t, want %t",
					c.measure, c.bound, found, c.want)
			}
			if found && got.MemoryBytes != c.measure {
				t.Errorf("the entry records a memory bound of %d, want %d", got.MemoryBytes, c.measure)
			}
		})
	}
}

func openBounded(t *testing.T, root string, ctx cache.Context, memory int64) *cache.Cache {
	t.Helper()
	store, err := cache.Open(cache.Options{
		Root: root, Context: ctx, Timeout: testTimeout, MemoryLimit: memory,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func TestTheMemoryBoundIsRecordedOnTheEntryAndNotInTheKey(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	const small, large = 256 << 20, 8 << 30

	writer := openBounded(t, root, baseContext(), small)
	if err := writer.Put(mutantIDs[0], killedEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	reader := openBounded(t, root, baseContext(), large)
	if writer.Key() != reader.Key() {
		t.Errorf("two runs differing only in their memory bound have different keys:\n%s\n%s",
			writer.Key(), reader.Key())
	}
	got, found, err := reader.Lookup(mutantIDs[0])
	if err != nil || !found {
		t.Fatalf("Lookup = %v, %t, %v; an ordinary kill is evidence about any bound", got, found, err)
	}
	if got.MemoryBytes != small {
		t.Errorf("the entry records %d, want the %d it was measured under", got.MemoryBytes, small)
	}
}

func TestAStoredMemoryKillKeepsWhatItCost(t *testing.T) {
	t.Parallel()

	const bound = 256 << 20
	root := t.TempDir()
	writer := openBounded(t, root, baseContext(), bound)

	entry := killedEntry()
	entry.MemoryExceeded = true
	entry.PeakMemory = 300 << 20
	if err := writer.Put(mutantIDs[0], entry); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reader := openBounded(t, root, baseContext(), bound)
	got, found, err := reader.Lookup(mutantIDs[0])
	if err != nil || !found {
		t.Fatalf("Lookup = %v, %t, %v", got, found, err)
	}
	if !got.MemoryExceeded {
		t.Error("the adopted entry does not say the bound settled it")
	}
	if got.PeakMemory != entry.PeakMemory {
		t.Errorf("PeakMemory = %d, want the %d the measuring run recorded", got.PeakMemory, entry.PeakMemory)
	}
	if got.MemoryBytes != bound {
		t.Errorf("MemoryBytes = %d, want the %d it was measured under", got.MemoryBytes, bound)
	}
}

func TestAnEntryCannotClaimAPeakItCouldNotHaveReached(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writer := openBounded(t, root, baseContext(), 256<<20)

	negative := killedEntry()
	negative.PeakMemory = -1
	if err := writer.Put(mutantIDs[0], negative); err == nil {
		t.Error("an entry claiming a negative peak was stored")
	}
}
