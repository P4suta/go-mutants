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

// mutantIDs are well-formed ids for the store tests: 64 lowercase hex
// characters, which is what the store insists on before it will name a file.
var mutantIDs = []string{
	strings.Repeat("a1", 32),
	strings.Repeat("b2", 32),
	strings.Repeat("c3", 32),
}

// open opens a cache rooted in a temporary directory, so that no test ever
// reaches the developer's own.
func open(t *testing.T, root string, ctx cache.Context) *cache.Cache {
	t.Helper()
	return openWithin(t, root, ctx, testTimeout)
}

// testTimeout is the per-mutant bound the store tests run under. It is
// comfortably above every duration they record, so that [cache.Entry.UsableUnder]
// is not what any of them is measuring — except the one that is.
const testTimeout = 10 * time.Second

// openWithin is [open] with a bound of the caller's choosing.
func openWithin(t *testing.T, root string, ctx cache.Context, timeout time.Duration) *cache.Cache {
	t.Helper()
	store, err := cache.Open(cache.Options{Root: root, Context: ctx, Timeout: timeout})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

// killedEntry is one settled outcome to store.
func killedEntry() cache.Entry {
	return cache.Entry{
		Outcome:    mutation.OutcomeKilled,
		DurationMS: 120,
		KilledBy:   "example.com/m/internal/alpha",
		Attempts:   1,
		OutputTail: "--- FAIL: TestAdd (0.00s)",
	}
}

// TestAStoredOutcomeComesBack is the round trip: everything a run needs to
// report a mutant without executing it survives the file.
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
	// The full key, not the truncation: it is the only field that can tell two
	// contexts sharing a directory apart. See [TestATruncationCollisionIsRefused].
	case entry.Key != store.Key():
		t.Errorf("the entry names key %q, want %q", entry.Key, store.Key())
	}
}

// TestAnUnknownMutantIsAnOrdinaryMiss covers the answer a cache gives most
// often, and the one that must never look like a failure.
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

// TestOneContextCannotReadAnother is the whole reason the key is a directory.
//
// A run that differs in anything the context covers looks somewhere else and
// finds nothing; nothing has to be invalidated, and there is no window in which
// yesterday's answer is still reachable.
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
	// And the first cache still has it: an edit does not invalidate, it moves
	// the question somewhere else.
	if _, found, _ := first.Lookup(mutantIDs[0]); !found {
		t.Error("the original context lost its own entry")
	}
}

// TestAnEntryThatIsNotAnEntryIsAMiss is the corruption contract: a truncated,
// misfiled, or out-of-date file is read as "measure it again" and reported,
// never adopted and never fatal.
func TestAnEntryThatIsNotAnEntryIsAMiss(t *testing.T) {
	t.Parallel()

	// {key}, {context} and {id} are filled in with this store's own values, so
	// that every field a case does not deliberately break is correct and the
	// refusal can only have come from the one it did.
	cases := map[string]string{
		"truncated JSON":  `{"version":2,"outcome":"kil`,
		"not JSON at all": "the antivirus quarantined this file",
		"a future version": `{"version":3,"key":"{key}","context":"{context}","id":"{id}","outcome":"killed",` +
			`"duration_ms":1,"timeout_ms":10000,"attempts":1}`,
		// The version this build wrote before the full key was recorded. It is
		// refused as the version miss it is rather than as a key mismatch,
		// which would read as a collision that never happened.
		"a version 1 entry left by an older build": `{"version":1,"context":"{context}","id":"{id}",` +
			`"outcome":"killed","duration_ms":1,"timeout_ms":10000,"attempts":1}`,
		"another mutant's outcome": `{"version":2,"key":"{key}","context":"{context}","id":"` +
			`0000000000000000000000000000000000000000000000000000000000000000","outcome":"killed",` +
			`"duration_ms":1,"timeout_ms":10000,"attempts":1}`,
		// A file misfiled by hand: the directory says one context and the
		// document says another.
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

// TestATruncationCollisionIsRefused is the one thing the directory layout
// cannot do for itself, and the failure this package must never have.
//
// A context directory is named by the first [cache.ContextKeyLength] characters
// of the key, so two contexts that agree over that prefix and disagree
// afterwards are filed in one directory. Nothing about the path distinguishes
// them, and neither does the truncated `context` field, because the collision
// is precisely that the two truncations are equal — so the entry has to carry
// the full key and the read has to compare it.
//
// A real collision needs about 2^32 contexts on one machine and cannot be
// produced in a test, so the entry is written by hand: the same directory, the
// same truncated context, and a full key that is one character different. That
// is exactly what the second of two colliding runs would find.
func TestATruncationCollisionIsRefused(t *testing.T) {
	t.Parallel()

	store := open(t, t.TempDir(), baseContext())
	id := mutantIDs[0]
	if err := store.Put(id, killedEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The other context's key: identical through the truncation that names the
	// directory, different in the half the directory name threw away.
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

// TestCacheableIsTheWholeRule pins the three outcomes a later run may reuse and
// the three it may not. It is the rule the whole store's soundness rests on, so
// it is stated here as a table rather than left implicit in the callers.
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

// TestAnEntryIsOnlyEvidenceAboutARunWithACompatibleBound is the whole of what
// keeping the timeout out of the key costs, and what buys the soundness back.
//
// It matters because a derived bound is max(10s, slowest baseline × 5) — a
// wall-clock measurement — so on any project whose tests take more than two
// seconds it is a slightly different number every run. Keying on it would give
// every run its own empty directory; judging each entry against it instead
// keeps the whole cache reachable and refuses exactly the entries that would
// have been wrong.
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

// TestPutRefusesWhatNoRunMayReuse checks that a caller getting the rule wrong is
// told rather than quietly ignored: a Put that silently did nothing would make
// the bug invisible.
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

// TestAnIDThatIsNotAnIDNamesNoFile is the path-traversal guard. The alphabet the
// check accepts has no separator, no dot and no drive letter in it, so an entry
// can only ever be a file in its own directory.
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

// TestALongOutputTailIsTruncated keeps one pathological test suite from filling
// somebody's cache directory, and keeps the end of the output — where the
// failing assertion is.
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

// TestOpenRefusesAWorkspaceThatBelongsToSomethingElse is the ownership marker
// doing its job. The cache shares a directory with the run history and with
// every other tool on the machine, and a truncated workspace key is the one
// thing that could put two projects in one directory.
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

// TestOpenClaimsTheSameMarkerTheHistoryDoes proves the two stores share one
// directory and one claim rather than two implementations of the same dance.
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
	// A second open of the same workspace is not a collision: the marker
	// already names it, which is the answer the claim was asking for.
	if _, err = cache.Open(cache.Options{Root: root, Context: ctx}); err != nil {
		t.Errorf("re-opening the same workspace failed: %v", err)
	}
}

// TestRootResolvesUnderTheOperatingSystemCache checks the two shapes
// `cache.directory` can take, without depending on which directory this machine
// calls its cache.
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
	// internal/config has already refused an escaping directory by the time a
	// run gets here, and this refuses it again rather than trusting that.
	if _, err = cache.Root("../elsewhere"); err == nil {
		t.Error("a directory climbing out of the cache root was accepted")
	}
}

// write puts a file on disk, creating its directory, and fails the test if it
// cannot.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
