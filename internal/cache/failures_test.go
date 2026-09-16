// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// The failure half of the store and of its three maintenance commands.
//
// A cache never fails a run -- every failure here is the caller's to fail open
// on -- but "fails open" is a claim about which diagnostic a caller is handed,
// and a cache that answered the wrong one would send somebody to look at their
// disk for a mistake in their configuration. So each of these says which.

// errStaged is what a seam returns when a test wants the call to fail.
var errStaged = errors.New("the operating system refused")

// swapSeam replaces one seam for the length of a test and puts it back.
func swapSeam[T any](t *testing.T, seam *T, with T) {
	t.Helper()
	was := *seam
	*seam = with
	t.Cleanup(func() { *seam = was })
}

// validContext is a context that will produce a key.
func validContext() Context {
	return Context{
		ToolVersion:      "0.1.0-dev",
		ToolDigest:       strings.Repeat("11", 32),
		ToolchainVersion: "go1.26.5",
		WorkspaceDigest:  strings.Repeat("ab", 32),
		CatalogDigest:    strings.Repeat("cd", 32),
		TestCommand:      []string{"go", "test", "./..."},
	}
}

// assertCode fails unless err carries the code.
func assertCode(t *testing.T, err error, want Code) {
	t.Helper()
	var coded *Error
	if !errors.As(err, &coded) {
		t.Fatalf("the failure is not this package's: %v", err)
	}
	if coded.Code != want {
		t.Fatalf("code = %s, want %s (%v)", coded.Code, want, err)
	}
}

// TestRootNeedsSomewhereToKeepOutcomes covers the one thing a cache root cannot
// be derived without.
//
// It is not a failure a user can be blamed for and it is not one they can be
// left guessing about either: a machine with no cache directory has nowhere to
// keep outcomes at all, which is a different problem from a directory that
// could not be written.
func TestRootNeedsSomewhereToKeepOutcomes(t *testing.T) {
	// Not parallel: the environment is the process's.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	if _, err := os.UserCacheDir(); err == nil {
		t.Skip("this platform names a cache directory without an environment to read it from")
	}

	_, err := Root("")
	assertCode(t, err, CodeUnavailable)
	if !strings.Contains(err.Error(), "nowhere to keep outcomes") {
		t.Errorf("the failure does not say what is missing: %v", err)
	}

	// And Open carries it up rather than rewriting it: the caller has to be
	// able to tell "there is no cache directory on this machine" from "the
	// cache directory could not be claimed".
	_, err = Open(Options{Context: validContext()})
	assertCode(t, err, CodeUnavailable)
	if !strings.Contains(err.Error(), "nowhere to keep outcomes") {
		t.Errorf("Open rewrote the failure: %v", err)
	}
}

// TestOpenRefusesAContextThatCouldNotIdentifyARun is the failure that is a
// caller bug rather than a user's problem.
//
// It costs a run its cache and nothing else, which is why it is reported with
// the context's own code rather than with the store's: a key that would not
// identify this run is not a cache that is unavailable.
func TestOpenRefusesAContextThatCouldNotIdentifyARun(t *testing.T) {
	t.Parallel()

	incomplete := validContext()
	incomplete.CatalogDigest = ""

	_, err := Open(Options{Root: t.TempDir(), Context: incomplete})
	assertCode(t, err, CodeInvalidContext)
	if !strings.Contains(err.Error(), "catalogue digest") {
		t.Errorf("the failure does not name the missing field: %v", err)
	}
}

// TestOpenReportsADirectoryItCannotCreate is the last thing Open does, and the
// one a read-only cache root produces.
func TestOpenReportsADirectoryItCannotCreate(t *testing.T) {
	t.Parallel()

	// The workspace directory is claimed first, so it is staged with a marker
	// of its own and then made read-only: the claim reads the marker it
	// already agrees with, and the outcomes directory underneath it is what
	// cannot be made.
	root := t.TempDir()
	ctx := validContext()
	workspace := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(ctx.WorkspaceDigest))
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("staging the workspace: %v", err)
	}
	if _, err := (report.History{Root: root}).Claim(ctx.WorkspaceDigest); err != nil {
		t.Fatalf("claiming the workspace: %v", err)
	}
	unwritableDir(t, workspace)

	_, err := Open(Options{Root: root, Context: ctx})
	assertCode(t, err, CodeUnavailable)
	if !strings.Contains(err.Error(), "could not be created") {
		t.Errorf("the failure names the wrong step: %v", err)
	}
}

// TestToolDigestNamesThisBuildOrSaysWhyItCannot covers the digest that stops a
// rebuilt go-mutants from adopting its predecessor's answers.
//
// Both halves are seams, because a process that cannot locate or read its own
// binary is not a filesystem a test may build -- and what happens when it
// cannot is the difference between a run with no cache and a run caching under
// a key that names nothing.
func TestToolDigestNamesThisBuildOrSaysWhyItCannot(t *testing.T) {
	t.Run("the executable that cannot be located", func(t *testing.T) {
		swapSeam(t, &executablePath, func() (string, error) { return "", errStaged })
		_, err := ToolDigest()
		assertCode(t, err, CodeExecutableUnreadable)
		if !errors.Is(err, errStaged) {
			t.Errorf("the failure does not carry the one that was staged: %v", err)
		}
		if !strings.Contains(err.Error(), "could not be located") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
	})

	t.Run("the executable that cannot be read", func(t *testing.T) {
		swapSeam(t, &readExecutable, func(string) ([]byte, error) { return nil, errStaged })
		_, err := ToolDigest()
		assertCode(t, err, CodeExecutableUnreadable)
		if !strings.Contains(err.Error(), "could not be read") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
	})

	t.Run("and the digest of what it did read", func(t *testing.T) {
		swapSeam(t, &executablePath, func() (string, error) { return "/proc/self/exe", nil })
		swapSeam(t, &readExecutable, func(string) ([]byte, error) { return []byte("bytes"), nil })
		got, err := ToolDigest()
		if err != nil {
			t.Fatalf("ToolDigest: %v", err)
		}
		if want := mutation.Digest([]byte("bytes")); got != want {
			t.Errorf("ToolDigest = %q, want the digest of what it read", got)
		}
	})
}

// TestLookupTellsAMissApartFromAnEntryItCouldNotRead is the three states the
// contract promises.
//
// A caller that treated every error as fatal would have misread it -- but a
// cache directory somebody's antivirus is quietly corrupting must not present
// as a permanently cold one either, which is why the second state exists at
// all.
func TestLookupTellsAMissApartFromAnEntryItCouldNotRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := Open(Options{Root: root, Context: validContext(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := strings.Repeat("a1", 32)

	t.Run("an id that is not one names no file", func(t *testing.T) {
		t.Parallel()

		_, found, err := store.Lookup("not-an-id")
		assertCode(t, err, CodeInvalidContext)
		if found {
			t.Error("a lookup that failed reported a hit")
		}
	})

	t.Run("an entry that is not readable", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(store.Dir(), id+entrySuffix)
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatalf("staging the entry: %v", err)
		}
		unreadableFile(t, path)

		_, found, err := store.Lookup(id)
		assertCode(t, err, CodeCorruptEntry)
		if found {
			t.Error("an unreadable entry was reported as a hit")
		}
		if !strings.Contains(err.Error(), "measured again") {
			t.Errorf("the failure does not say what happens next: %v", err)
		}
	})
}

// TestPutReportsEveryWayAnEntryCanFailToBeWritten covers the write that must be
// whole or absent.
//
// A correctly named file holding half a JSON document is the failure that would
// turn one interrupted run into a permanently poisoned cache, so the write goes
// through a temporary file and a rename -- and each step of that says which one
// it was.
func TestPutReportsEveryWayAnEntryCanFailToBeWritten(t *testing.T) {
	id := strings.Repeat("a1", 32)
	entry := func() Entry {
		return Entry{Outcome: mutation.OutcomeKilled, DurationMS: 5, Attempts: 1}
	}
	openOne := func(t *testing.T) *Cache {
		t.Helper()
		store, err := Open(Options{Root: t.TempDir(), Context: validContext(), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return store
	}

	t.Run("an id that is not one", func(t *testing.T) {
		assertCode(t, openOne(t).Put("nope", entry()), CodeInvalidContext)
	})

	t.Run("a temporary file that cannot be created", func(t *testing.T) {
		store := openOne(t)
		unwritableDir(t, store.Dir())
		err := store.Put(id, entry())
		assertCode(t, err, CodeEntryNotWritten)
		if !strings.Contains(err.Error(), "temporary file") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
	})

	t.Run("a rename that cannot land", func(t *testing.T) {
		// A directory where the entry has to go. The temporary file is made
		// beside it and the rename onto it is refused, retried, and refused
		// again -- which is the one failing path that is real rather than
		// staged.
		store := openOne(t)
		if err := os.Mkdir(filepath.Join(store.Dir(), id+entrySuffix), 0o700); err != nil {
			t.Fatalf("staging the obstruction: %v", err)
		}
		err := store.Put(id, entry())
		assertCode(t, err, CodeEntryNotWritten)
		if !strings.Contains(err.Error(), "moved into place") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
		// And the temporary file went with it, rather than being left for the
		// next sweep to wonder about.
		left, readErr := os.ReadDir(store.Dir())
		if readErr != nil {
			t.Fatalf("listing the directory: %v", readErr)
		}
		for _, e := range left {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("a temporary file was left behind: %s", e.Name())
			}
		}
	})

	for _, test := range []struct {
		name string
		seam func(t *testing.T)
	}{{
		name: "bytes that cannot be written",
		seam: func(t *testing.T) {
			swapSeam(t, &writeTemp, func(*os.File, []byte) (int, error) { return 0, errStaged })
		},
	}, {
		name: "a flush that fails",
		seam: func(t *testing.T) {
			swapSeam(t, &syncTemp, func(*os.File) error { return errStaged })
		},
	}, {
		name: "a close that fails",
		seam: func(t *testing.T) {
			swapSeam(t, &closeTemp, func(f *os.File) error { _ = f.Close(); return errStaged })
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			store := openOne(t)
			test.seam(t)
			err := store.Put(id, entry())
			assertCode(t, err, CodeEntryNotWritten)
			// The failure that happened, not the one that happened next: a
			// close error reported in place of a write error would tell a
			// reader the bytes reached the disk.
			if !errors.Is(err, errStaged) {
				t.Errorf("Put = %v, want the staged failure", err)
			}
			if _, statErr := os.Stat(filepath.Join(store.Dir(), id+entrySuffix)); statErr == nil {
				t.Error("an entry that was never written is on disk under its own name")
			}
		})
	}
}

// TestPutRefusesAnEntryThatCouldNotHaveBeenMeasured is the shape check on the
// way out rather than on the way in.
func TestPutRefusesAnEntryThatCouldNotHaveBeenMeasured(t *testing.T) {
	t.Parallel()

	store, err := Open(Options{Root: t.TempDir(), Context: validContext(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bad := Entry{Outcome: mutation.OutcomeKilled, DurationMS: -1, Attempts: 1}
	putErr := store.Put(strings.Repeat("a1", 32), bad)
	assertCode(t, putErr, CodeEntryNotWritten)
	if !strings.Contains(putErr.Error(), "could have happened") {
		t.Errorf("the failure names the wrong reason: %v", putErr)
	}
}

// TestPutRefusesADivergenceBesideAnOutcomeALoopCannotProduce is the twin of the
// memory rule, and it is a refusal at the point of writing for the same reason.
//
// A counted loop past its ceiling ends the process, so the only outcome it can
// produce is the one a mutant that does not return gets. An entry saying a loop
// settled a kill or a survival describes a measurement that both ended itself
// and finished, and the contradiction is exactly the kind a consumer reads
// straight past: `explain` on a warm run would report which loop ran away from
// a mutant the suite caught with an assertion.
//
// The round trip is asserted beside it, because a field that is refused when
// wrong and dropped when right is a field nothing carries.
func TestPutRefusesADivergenceBesideAnOutcomeALoopCannotProduce(t *testing.T) {
	t.Parallel()

	store, err := Open(Options{Root: t.TempDir(), Context: validContext(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := strings.Repeat("a1", 32)

	for _, outcome := range []mutation.Outcome{mutation.OutcomeKilled, mutation.OutcomeSurvived} {
		bad := Entry{Outcome: outcome, DurationMS: 20, Attempts: 1, TimeoutMS: 1000, Diverged: true}
		putErr := store.Put(id, bad)
		assertCode(t, putErr, CodeEntryNotWritten)
		if !strings.Contains(putErr.Error(), "counted loop") {
			t.Errorf("the failure beside %s names the wrong reason: %v", outcome, putErr)
		}
		if _, found, _ := store.Lookup(id); found {
			t.Fatalf("a %s outcome claiming a divergence reached the disk", outcome)
		}
	}

	good := Entry{Outcome: mutation.OutcomeTimedOut, DurationMS: 20, Attempts: 1, TimeoutMS: 1000, Diverged: true}
	if putErr := store.Put(id, good); putErr != nil {
		t.Fatalf("Put of a divergence: %v", putErr)
	}
	back, found, err := store.Lookup(id)
	if err != nil || !found {
		t.Fatalf("Lookup after Put: found=%v err=%v", found, err)
	}
	if !back.Diverged {
		t.Error("the entry came back without the fact that makes a warm run able to explain it")
	}
}

// TestACacheKnowsWhereItIs pins the three accessors a caller reads a handle
// with, because each of them names a directory somebody will be told about.
func TestACacheKnowsWhereItIs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := validContext()
	store, err := Open(Options{Root: root, Context: ctx, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store.Root() != root {
		t.Errorf("Root() = %q, want %q", store.Root(), root)
	}
	if !strings.HasPrefix(store.Dir(), root) {
		t.Errorf("Dir() = %q, want it under %q", store.Dir(), root)
	}
	key, err := ctx.Key()
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if store.Key() != key {
		t.Errorf("Key() = %q, want the context's own", store.Key())
	}
	if !strings.HasSuffix(filepath.Dir(store.Dir()+"/x"), key[:ContextKeyLength]) {
		t.Errorf("Dir() = %q, want it filed under the context key", store.Dir())
	}
}

// staged builds a cache root holding one owned workspace with one stored
// outcome, and returns the root, the workspace directory and the context
// directory the entry is in.
func staged(t *testing.T) (root, workspace, context string) {
	t.Helper()

	root = t.TempDir()
	ctx := validContext()
	store, err := Open(Options{Root: root, Context: ctx, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entry := Entry{Outcome: mutation.OutcomeKilled, DurationMS: 5, Attempts: 1}
	if err = store.Put(strings.Repeat("a1", 32), entry); err != nil {
		t.Fatalf("Put: %v", err)
	}
	context = store.Dir()
	workspace = filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(ctx.WorkspaceDigest))
	return root, workspace, context
}

// TestTheThreeCommandsStopAtADirectoryTheyCannotRead is the rule all three
// share, asserted once per directory they walk.
//
// A survey that silently left out what it could not list would report a cache
// smaller than it is, and a sweep built on that report would leave the rest
// behind while saying it had finished. Both are worse than a failure.
func TestTheThreeCommandsStopAtADirectoryTheyCannotRead(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		pick  func(root, workspace, context string) string
		blind func(t *testing.T, dir string)
	}{{
		name:  "the workspaces directory",
		pick:  func(root, _, _ string) string { return filepath.Join(root, report.WorkspacesDirName) },
		blind: unreadableDir,
	}, {
		name:  "one workspace's outcomes directory",
		pick:  func(_, workspace, _ string) string { return filepath.Join(workspace, OutcomesDirName) },
		blind: unreadableDir,
	}, {
		name:  "one context directory",
		pick:  func(_, _, context string) string { return context },
		blind: unreadableDir,
	}, {
		name:  "one context directory whose entries cannot be stat-ed",
		pick:  func(_, _, context string) string { return context },
		blind: unsearchableDir,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			for _, command := range []struct {
				name string
				run  func(root string) error
			}{
				{name: "status", run: func(root string) error { _, err := Status(root); return err }},
				{name: "gc", run: func(root string) error { _, err := GC(root, time.Now()); return err }},
				{name: "clean", run: func(root string) error { _, err := Clean(root); return err }},
			} {
				t.Run(command.name, func(t *testing.T) {
					t.Parallel()

					root, workspace, context := staged(t)
					test.blind(t, test.pick(root, workspace, context))
					assertCode(t, command.run(root), CodeScanFailed)
				})
			}
		})
	}
}

// TestAStatusOfNothingIsAnEmptyListAndNotAMissingOne pins the shape of a survey
// nobody has cached anything for.
//
// The difference is what a `--json` reader sees: `[]` is "this machine has
// cached nothing", and `null` is a field the writer forgot. A tool reading the
// second has to guess.
func TestAStatusOfNothingIsAnEmptyListAndNotAMissingOne(t *testing.T) {
	t.Parallel()

	survey, err := Status(t.TempDir())
	if err != nil {
		t.Fatalf("Status of a machine that has cached nothing: %v", err)
	}
	if survey.Skipped == nil {
		t.Error("Skipped is nil, want an empty list")
	}
	if survey.Workspaces == nil {
		t.Error("Workspaces is nil, want an empty list")
	}
	encoded, err := json.Marshal(survey)
	if err != nil {
		t.Fatalf("encoding the survey: %v", err)
	}
	if strings.Contains(string(encoded), "null") {
		t.Errorf("the survey encodes a null: %s", encoded)
	}

	// A root that is not there at all is the same answer, and a root that is
	// no root at all is a failure rather than an empty one.
	if _, err = GC(filepath.Join(t.TempDir(), "never-used"), time.Now()); err != nil {
		t.Errorf("GC of a never-used root: %v", err)
	}
	_, _, err = walk("")
	assertCode(t, err, CodeScanFailed)
}

// TestASurveyCountsAContextOnlyWhenSomethingIsInIt is the boundary of the
// context tally.
//
// An empty context directory is a directory a sweep pruned or a run made and
// never filled. Counting it would report a cache holding contexts with nothing
// in them, which is a number nobody can act on.
func TestASurveyCountsAContextOnlyWhenSomethingIsInIt(t *testing.T) {
	t.Parallel()

	root, workspace, _ := staged(t)
	empty := filepath.Join(workspace, OutcomesDirName, "0000000000000000")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatalf("staging an empty context: %v", err)
	}

	survey, err := Status(root)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(survey.Workspaces) != 1 {
		t.Fatalf("Status found %d workspaces, want one", len(survey.Workspaces))
	}
	if got := survey.Workspaces[0].Contexts; got != 1 {
		t.Errorf("Contexts = %d, want the one that holds an entry", got)
	}
	if got := survey.Workspaces[0].Entries; got != 1 {
		t.Errorf("Entries = %d, want one", got)
	}
}

// TestASweepReportsWhichWorkspacesItTouched pins the flag a sweep counts
// workspaces by.
//
// A workspace nothing was removed from is not one the sweep touched, and
// counting it would tell somebody their cache had been swept when it had not.
func TestASweepReportsWhichWorkspacesItTouched(t *testing.T) {
	t.Parallel()

	root, _, _ := staged(t)

	// Nothing is old enough, so nothing is removed and no workspace is
	// counted.
	sweep, err := GC(root, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if sweep.Workspaces != 0 || sweep.Entries != 0 {
		t.Errorf("a sweep that removed nothing reported %+v", sweep)
	}

	// Everything is, so the one workspace is counted once.
	sweep, err = GC(root, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if sweep.Workspaces != 1 {
		t.Errorf("Workspaces = %d, want the one it swept", sweep.Workspaces)
	}
	if sweep.Entries != 1 || sweep.Contexts != 1 {
		t.Errorf("Sweep = %+v, want one entry and its emptied context", sweep)
	}
	if sweep.Bytes <= 0 {
		t.Errorf("Bytes = %d, want what the removed entry took up", sweep.Bytes)
	}
}

// TestSkippedAndOwnedDirectoriesAreBothReportedInNameOrder pins the ordering of
// both lists a survey carries.
//
// The names are hashes and therefore arbitrary -- but arbitrary and stable, so
// two runs of `cache status` over an unchanged cache produce the same output
// and can be diffed. A list in filesystem order cannot be.
func TestSkippedAndOwnedDirectoriesAreBothReportedInNameOrder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(root, report.WorkspacesDirName)
	for _, name := range []string{"zzz-no-marker", "aaa-no-marker", "mmm-no-marker"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}
	for _, digest := range []string{strings.Repeat("ff", 32), strings.Repeat("11", 32)} {
		if _, err := (report.History{Root: root}).Claim(digest); err != nil {
			t.Fatalf("claiming %s: %v", digest, err)
		}
	}

	owned, skipped, err := walk(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if !sortedBy(len(skipped), func(i int) string { return skipped[i].Name }) {
		t.Errorf("the skipped list is not in name order: %+v", skipped)
	}
	if !sortedBy(len(owned), func(i int) string { return owned[i].Key }) {
		t.Errorf("the owned list is not in name order: %+v", owned)
	}
	if len(skipped) != 3 || len(owned) != 2 {
		t.Errorf("walk found %d skipped and %d owned, want three and two", len(skipped), len(owned))
	}
	for _, row := range skipped {
		if row.Reason == "" {
			t.Errorf("%s was skipped for no stated reason", row.Name)
		}
	}
}

// sortedBy reports whether n values are in ascending order.
func sortedBy(n int, at func(int) string) bool {
	for i := 1; i < n; i++ {
		if at(i-1) > at(i) {
			return false
		}
	}
	return true
}

// TestEnabledIsEitherDirection pins the one line a caller branches on.
//
// Resolve only ever produces decisions whose two halves agree -- reading
// somebody else's answers and writing your own are the same promise about the
// same command -- but Enabled is exported and answers about the value it is
// given, and a caller assembling a half-enabled decision is asking whether the
// cache does anything at all, not whether it does everything.
func TestEnabledIsEitherDirection(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		decision Decision
		want     bool
	}{
		{decision: Decision{}, want: false},
		{decision: Decision{Read: true}, want: true},
		{decision: Decision{Write: true}, want: true},
		{decision: Decision{Read: true, Write: true}, want: true},
	} {
		if got := test.decision.Enabled(); got != test.want {
			t.Errorf("Decision{Read:%v, Write:%v}.Enabled() = %v, want %v",
				test.decision.Read, test.decision.Write, got, test.want)
		}
	}
}

// TestCurrentEnvReadsThisProcess is the one caller of EnvFrom that does not
// state its own environment.
func TestCurrentEnvReadsThisProcess(t *testing.T) {
	t.Parallel()

	got := CurrentEnv()
	if got == nil {
		t.Fatal("CurrentEnv answered nothing at all")
	}
	if len(got) != len(keyEnv) {
		t.Errorf("CurrentEnv read %d variables, want the %d in the key", len(got), len(keyEnv))
	}
	for _, name := range keyEnv {
		if _, ok := got[name]; !ok {
			t.Errorf("CurrentEnv left out %s", name)
		}
	}
}

// TestContextKeyRefusesWhatTheKeyRefuses is the truncation's own failure path.
func TestContextKeyRefusesWhatTheKeyRefuses(t *testing.T) {
	t.Parallel()

	incomplete := validContext()
	incomplete.TestCommand = nil
	got, err := incomplete.ContextKey()
	assertCode(t, err, CodeInvalidContext)
	if got != "" {
		t.Errorf("ContextKey = %q beside an error, want nothing", got)
	}

	// And the shape of the answer when there is one: the context key is the
	// key's own prefix, which is what files one run's entries beside each
	// other.
	full, err := validContext().Key()
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	short, err := validContext().ContextKey()
	if err != nil {
		t.Fatalf("ContextKey: %v", err)
	}
	if short != full[:ContextKeyLength] {
		t.Errorf("ContextKey = %q, want the first %d characters of %q", short, ContextKeyLength, full)
	}
}

// TestLookupSaysWhichWayAnEntryWasUnusable separates the two corruptions, which
// send a reader to two different places.
func TestLookupSaysWhichWayAnEntryWasUnusable(t *testing.T) {
	t.Parallel()

	id := strings.Repeat("a1", 32)
	openOne := func(t *testing.T) *Cache {
		t.Helper()
		store, err := Open(Options{Root: t.TempDir(), Context: validContext(), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return store
	}

	t.Run("a file that cannot be read", func(t *testing.T) {
		t.Parallel()

		store := openOne(t)
		path := filepath.Join(store.Dir(), id+entrySuffix)
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatalf("staging: %v", err)
		}
		unreadableFile(t, path)
		_, _, err := store.Lookup(id)
		if !strings.Contains(err.Error(), "could not be read") {
			t.Errorf("Lookup = %v, want it to say the file could not be read", err)
		}
	})

	t.Run("a file that is not a cache entry", func(t *testing.T) {
		t.Parallel()

		store := openOne(t)
		path := filepath.Join(store.Dir(), id+entrySuffix)
		if err := os.WriteFile(path, []byte("this is not JSON"), 0o600); err != nil {
			t.Fatalf("staging: %v", err)
		}
		_, _, err := store.Lookup(id)
		if !strings.Contains(err.Error(), "is not a cache entry") {
			t.Errorf("Lookup = %v, want it to say the file is not an entry", err)
		}
	})

	t.Run("an entry written for something else", func(t *testing.T) {
		t.Parallel()

		store := openOne(t)
		path := filepath.Join(store.Dir(), id+entrySuffix)
		data, err := json.Marshal(Entry{
			Version: EntryVersion, Key: strings.Repeat("0", 64), Context: store.Dir(),
			ID: id, Outcome: mutation.OutcomeKilled, Attempts: 1, TimeoutMS: 1000,
		})
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("staging: %v", err)
		}
		_, _, err = store.Lookup(id)
		if !strings.Contains(err.Error(), "will not be reused") {
			t.Errorf("Lookup = %v, want it to say the entry will not be reused", err)
		}
	})
}

// TestASweepThatFailedTouchedNothingItDidNotTouch pins the flag that decides
// how many workspaces a sweep reports.
//
// A workspace a sweep could not read is not one it swept. Counting it would
// tell somebody their cache had been collected when it had not, which is the
// one number a `gc` that exits non-zero still prints.
func TestASweepThatFailedTouchedNothingItDidNotTouch(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		blind func(t *testing.T, root, workspace, context string)
	}{{
		name: "the outcomes directory it cannot list",
		blind: func(t *testing.T, _, workspace, _ string) {
			unreadableDir(t, filepath.Join(workspace, OutcomesDirName))
		},
	}, {
		name: "a context directory it cannot list",
		blind: func(t *testing.T, _, _, context string) {
			unreadableDir(t, context)
		},
	}, {
		name: "a context directory whose entries it cannot stat",
		blind: func(t *testing.T, _, _, context string) {
			unsearchableDir(t, context)
		},
	}, {
		name: "an entry it cannot remove",
		blind: func(t *testing.T, _, _, context string) {
			unwritableDir(t, context)
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root, workspace, context := staged(t)
			test.blind(t, root, workspace, context)

			sweep, err := GC(root, time.Now().Add(time.Hour))
			if err == nil {
				t.Fatalf("GC over a directory it cannot work in succeeded: %+v", sweep)
			}
			if sweep.Workspaces != 0 {
				t.Errorf("Workspaces = %d, want none: nothing was removed", sweep.Workspaces)
			}
			if sweep.Entries != 0 {
				t.Errorf("Entries = %d, want none", sweep.Entries)
			}
		})
	}
}

// TestASweepThatCannotPruneAnEmptyContextSaysSo is the last thing collect does
// with a context directory, and the one failure that happens after everything
// in it is gone.
func TestASweepThatCannotPruneAnEmptyContextSaysSo(t *testing.T) {
	t.Parallel()

	root, workspace, _ := staged(t)
	outcomes := filepath.Join(workspace, OutcomesDirName)
	// A context with nothing in it, named so that it sorts first and is
	// therefore the one reached before anything has been removed.
	empty := filepath.Join(outcomes, "0000000000000000")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatalf("staging an empty context: %v", err)
	}
	unwritableDir(t, outcomes)

	sweep, err := GC(root, time.Now().Add(time.Hour))
	assertCode(t, err, CodeNotRemoved)
	if sweep.Workspaces != 0 || sweep.Contexts != 0 {
		t.Errorf("Sweep = %+v, want nothing counted for a prune that did not happen", sweep)
	}
}

// TestASweepSurvivesADirectoryThatStopsBeingReadable is the race the emptiness
// check exists inside.
//
// The listing that decides whether a context is prunable is a second listing of
// a directory this function has already read, so the only way it fails is that
// another process changed the directory between the two -- and what this does
// then is stop and say so rather than prune a directory it can no longer see
// into.
func TestASweepSurvivesADirectoryThatStopsBeingReadable(t *testing.T) {
	root, _, context := staged(t)

	// The seam stands in for the emptiness check alone, which is the second
	// listing of a directory entryFiles has already read with os.ReadDir.
	swapSeam(t, &readDir, func(dir string) ([]os.DirEntry, error) {
		if dir == context {
			return nil, errStaged
		}
		return os.ReadDir(dir)
	})

	sweep, err := GC(root, time.Now().Add(time.Hour))
	assertCode(t, err, CodeScanFailed)
	if !errors.Is(err, errStaged) {
		t.Errorf("the failure does not carry the one that was staged: %v", err)
	}
	// The entries were removed before the listing failed, so that much is
	// reported: a sweep says what it did before it stopped.
	if sweep.Entries != 1 {
		t.Errorf("Entries = %d, want the one it removed before it stopped", sweep.Entries)
	}
	if sweep.Contexts != 0 {
		t.Errorf("Contexts = %d, want none: the prune never happened", sweep.Contexts)
	}
	// The workspace is counted because something in it was removed. A sweep
	// that reported none would say it had touched nothing while an entry it
	// had deleted was already gone.
	if sweep.Workspaces != 1 {
		t.Errorf("Workspaces = %d, want the one it had started on", sweep.Workspaces)
	}
}

// TestIsEmptyIsAboutEverythingAndNotOnlyAboutEntries is the reason a context is
// pruned on a second listing rather than on the count of entries it removed.
//
// A temporary file another run is in the middle of writing is not an entry and
// is not nothing either, and a directory pruned out from under it would take
// that run's outcome with it.
func TestIsEmptyIsAboutEverythingAndNotOnlyAboutEntries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	empty, err := isEmpty(dir)
	if err != nil || !empty {
		t.Fatalf("isEmpty of an empty directory = %v, %v", empty, err)
	}

	if err = os.WriteFile(filepath.Join(dir, "go-mutants-cache-1.tmp"), []byte("half"), 0o600); err != nil {
		t.Fatalf("staging a temporary file: %v", err)
	}
	if empty, err = isEmpty(dir); err != nil || empty {
		t.Errorf("isEmpty of a directory holding a temporary file = %v, %v, want false", empty, err)
	}

	// A directory that is not there is not an empty one: there is nothing to
	// prune, and reporting it as prunable would ask for a removal of a name
	// that no longer exists.
	if empty, err = isEmpty(filepath.Join(dir, "gone")); err != nil || empty {
		t.Errorf("isEmpty of a missing directory = %v, %v, want false and no failure", empty, err)
	}

	unreadableDir(t, dir)
	empty, err = isEmpty(dir)
	if err == nil {
		t.Error("isEmpty answered for a directory it cannot list")
	}
	assertCode(t, err, CodeScanFailed)
	// And the answer beside the failure is "not empty", which is the one that
	// cannot end in a prune.
	if empty {
		t.Error("a directory isEmpty could not list was reported as empty")
	}
}

// TestWithinRefusesAPathItCannotResolve is the answer that must not end in a
// deletion.
//
// Not knowing where a deletion would land is not the same as knowing it is
// outside, and it is certainly not the same as knowing it is inside -- so both
// resolutions refuse rather than guess, and the refusal keeps this package's
// own code.
func TestWithinRefusesAPathItCannotResolve(t *testing.T) {
	// Not parallel, and neither are the cases below it: one of them replaces a
	// seam, which is a package-level variable every test in this binary shares.
	t.Run("the path", func(t *testing.T) {
		root := t.TempDir()
		blocked := filepath.Join(root, "blocked")
		if err := os.MkdirAll(filepath.Join(blocked, "inner"), 0o700); err != nil {
			t.Fatalf("staging: %v", err)
		}
		unsearchableDir(t, blocked)

		inside, err := within(filepath.Join(blocked, "inner", "x"), root)
		assertCode(t, err, CodeNotRemoved)
		if !strings.Contains(err.Error(), "could not be resolved") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
		if inside {
			t.Error("a path nobody could resolve was reported as inside the cache")
		}

		// And remove carries it up rather than deleting on the strength of a
		// path it could not resolve. The two refusals share a code and say
		// different things: one is "this is outside the cache" and the other
		// is "nobody knows where this is".
		err = remove(filepath.Join(blocked, "inner", "x"), root)
		assertCode(t, err, CodeNotRemoved)
		if !strings.Contains(err.Error(), "could not be resolved") {
			t.Errorf("remove = %v, want the resolution's own refusal", err)
		}
	})

	t.Run("the root", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "cache")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("staging: %v", err)
		}
		// A path well outside the cache, so that it resolves; the root does
		// not, because reaching it means searching a directory that refuses.
		path := filepath.Join(t.TempDir(), "x")
		unsearchableDir(t, base)

		inside, err := within(path, root)
		assertCode(t, err, CodeNotRemoved)
		if !strings.Contains(err.Error(), "nothing under it was deleted") {
			t.Errorf("the failure names the wrong step: %v", err)
		}
		if inside {
			t.Error("a root nobody could resolve was reported as holding the path")
		}
	})

	t.Run("a path with no working directory to resolve against", func(t *testing.T) {
		swapSeam(t, &absPath, func(string) (string, error) { return "", errStaged })
		_, err := within("relative/path", t.TempDir())
		assertCode(t, err, CodeNotRemoved)
		if !errors.Is(err, errStaged) {
			t.Errorf("the failure does not carry the one that was staged: %v", err)
		}
	})
}

// TestRemoveReportsADeletionTheFilesystemRefused is the last step, and the one
// that makes `cache gc` exit non-zero rather than claim it had finished.
func TestRemoveReportsADeletionTheFilesystemRefused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	holder := filepath.Join(root, "holder")
	victim := filepath.Join(holder, "victim.json")
	if err := os.Mkdir(holder, 0o700); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if err := os.WriteFile(victim, []byte("{}"), 0o600); err != nil {
		t.Fatalf("staging: %v", err)
	}
	unwritableDir(t, holder)

	err := remove(victim, root)
	assertCode(t, err, CodeNotRemoved)
	if !strings.Contains(err.Error(), "could not be deleted") {
		t.Errorf("the failure names the wrong step: %v", err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Errorf("the file is gone after a removal that reported failing: %v", statErr)
	}
}

// TestCleanCarriesUpADeletionItCouldNotMake is the same rule for the command
// that removes a whole outcomes directory rather than one entry.
func TestCleanCarriesUpADeletionItCouldNotMake(t *testing.T) {
	t.Parallel()

	root, workspace, _ := staged(t)
	unwritableDir(t, workspace)

	sweep, err := Clean(root)
	assertCode(t, err, CodeNotRemoved)
	if sweep.Workspaces != 0 || sweep.Entries != 0 {
		t.Errorf("Sweep = %+v, want nothing counted for a removal that did not happen", sweep)
	}
}

// TestAWorkspaceWhoseMarkerCannotBeReadIsSkippedWithTheReason is the third way
// a directory is passed over, beside no marker at all and a marker naming
// somebody else.
func TestAWorkspaceWhoseMarkerCannotBeReadIsSkippedWithTheReason(t *testing.T) {
	t.Parallel()

	root, workspace, _ := staged(t)
	marker := filepath.Join(workspace, report.MarkerFileName)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the staged workspace has no marker: %v", err)
	}
	if err := os.WriteFile(marker, []byte("this is not a marker"), 0o600); err != nil {
		t.Fatalf("staging: %v", err)
	}

	survey, err := Status(root)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(survey.Workspaces) != 0 {
		t.Errorf("Status owned %d workspaces, want none", len(survey.Workspaces))
	}
	if len(survey.Skipped) != 1 {
		t.Fatalf("Status skipped %d directories, want one", len(survey.Skipped))
	}
	// The reason a user reads, without the diagnostic code that prefixed it:
	// the row is already a list of things not touched.
	reason := survey.Skipped[0].Reason
	if reason == "" || strings.HasPrefix(reason, "GOM") {
		t.Errorf("the skipped row reads %q, want a sentence without its code", reason)
	}
	// And it says the marker is not one this build wrote, rather than that it
	// names another workspace: a directory whose marker cannot be read is not
	// a copy of somebody else's, and telling a user it is would send them
	// looking for an original that does not exist.
	if !strings.Contains(reason, "is not one this build") {
		t.Errorf("the skipped row reads %q, want the refusal the marker produced", reason)
	}
}

// twoContexts stages one owned workspace holding two context directories, each
// with one stored outcome in it, and returns the root, the outcomes directory
// and the two contexts in the order a sweep reaches them.
func twoContexts(t *testing.T) (root, outcomes, first, second string) {
	t.Helper()

	root = t.TempDir()
	ctx := validContext()
	if _, err := (report.History{Root: root}).Claim(ctx.WorkspaceDigest); err != nil {
		t.Fatalf("claiming the workspace: %v", err)
	}
	workspace := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(ctx.WorkspaceDigest))
	outcomes = filepath.Join(workspace, OutcomesDirName)
	first = filepath.Join(outcomes, "0000000000000000")
	second = filepath.Join(outcomes, "ffffffffffffffff")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("staging %s: %v", dir, err)
		}
		entry := filepath.Join(dir, strings.Repeat("a1", 32)+entrySuffix)
		if err := os.WriteFile(entry, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("staging %s: %v", entry, err)
		}
	}
	return root, outcomes, first, second
}

// TestASweepReportsTheWorkspaceItHadAlreadyStartedOn is the other half of the
// touched flag: a sweep that removed something and then failed has still swept
// that workspace.
//
// Reporting none would say the cache was untouched while an entry it deleted
// was already gone, which is the one thing a `gc` that exits non-zero must not
// claim. The failures below are each staged in the *second* context directory,
// so that the first has been collected before any of them happens.
func TestASweepReportsTheWorkspaceItHadAlreadyStartedOn(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		blind func(t *testing.T, outcomes, second string)
		code  Code
	}{{
		name:  "a context directory it cannot list",
		blind: func(t *testing.T, _, second string) { unreadableDir(t, second) },
		code:  CodeScanFailed,
	}, {
		name:  "an entry it cannot stat",
		blind: func(t *testing.T, _, second string) { unsearchableDir(t, second) },
		code:  CodeScanFailed,
	}, {
		name:  "an entry it cannot remove",
		blind: func(t *testing.T, _, second string) { unwritableDir(t, second) },
		code:  CodeNotRemoved,
	}, {
		// The outcomes directory refuses the prune of the context it has just
		// emptied, which is the last thing collect does with one.
		name:  "an emptied context it cannot prune",
		blind: func(t *testing.T, outcomes, _ string) { unwritableDir(t, outcomes) },
		code:  CodeNotRemoved,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root, outcomes, _, second := twoContexts(t)
			test.blind(t, outcomes, second)

			sweep, err := GC(root, time.Now().Add(time.Hour))
			assertCode(t, err, test.code)
			if sweep.Entries < 1 {
				t.Fatalf("Entries = %d, want the first context's, removed before the failure", sweep.Entries)
			}
			if sweep.Workspaces != 1 {
				t.Errorf("Workspaces = %d, want the one it had already started on", sweep.Workspaces)
			}
		})
	}
}

// TestResolvePathWalksUpUntilSomethingResolves states the two ends of the walk,
// both of which need a resolver that answers what a filesystem will not.
//
// The recursion stops at the volume root, whose own name is the answer because
// there is nothing above it to resolve against -- a state no POSIX filesystem
// reaches, since its one volume root is always there. And a step of the walk
// that fails for a reason other than "not there" stops the whole resolution,
// because a path nobody can resolve is one nobody knows the location of, and
// not knowing where a deletion would land is the one answer that must not end
// in a deletion.
func TestResolvePathWalksUpUntilSomethingResolves(t *testing.T) {
	t.Run("a volume root that is not there", func(t *testing.T) {
		missing := &os.PathError{Op: "lstat", Path: "x", Err: os.ErrNotExist}
		swapSeam(t, &evalSymlinks, func(string) (string, error) { return "", missing })

		root := string(filepath.Separator)
		got, err := resolvePath(root)
		if err != nil {
			t.Fatalf("resolvePath(%q) = %v, want the root itself", root, err)
		}
		if got != root {
			t.Errorf("resolvePath(%q) = %q, want the volume root's own name", root, got)
		}
	})

	t.Run("a step of the walk that refuses", func(t *testing.T) {
		// The leaf is not there, and walking up to its parent is refused. The
		// first answer would have the resolution keep climbing and the second
		// stops it, which is the difference between a location and a guess.
		refused := errors.New("the directory may not be searched")
		missing := &os.PathError{Op: "lstat", Path: "x", Err: os.ErrNotExist}
		leaf := filepath.Join(string(filepath.Separator), "blocked", "gone")
		swapSeam(t, &evalSymlinks, func(path string) (string, error) {
			if path == leaf {
				return "", missing
			}
			return "", refused
		})

		got, err := resolvePath(leaf)
		if !errors.Is(err, refused) {
			t.Fatalf("resolvePath = %q, %v, want the refusal from the step above it", got, err)
		}
		if got != "" {
			t.Errorf("resolvePath answered %q beside a refusal, want nothing", got)
		}
	})
}

// TestASweepThatCannotSeeIntoAnEmptyContextTouchedNothing is the touched flag
// on the one path that reaches the emptiness check without removing anything.
//
// A context directory with nothing in it is a directory a sweep pruned or a run
// made and never filled, and the sweep looks at it again before pruning it. If
// that second look fails, nothing has been removed and no workspace has been
// touched -- and saying otherwise would report a sweep of a cache that is
// exactly as it was.
func TestASweepThatCannotSeeIntoAnEmptyContextTouchedNothing(t *testing.T) {
	root := t.TempDir()
	ctx := validContext()
	if _, err := (report.History{Root: root}).Claim(ctx.WorkspaceDigest); err != nil {
		t.Fatalf("claiming the workspace: %v", err)
	}
	workspace := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(ctx.WorkspaceDigest))
	empty := filepath.Join(workspace, OutcomesDirName, "0000000000000000")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatalf("staging an empty context: %v", err)
	}

	swapSeam(t, &readDir, func(dir string) ([]os.DirEntry, error) {
		if dir == empty {
			return nil, errStaged
		}
		return os.ReadDir(dir)
	})

	sweep, err := GC(root, time.Now().Add(time.Hour))
	assertCode(t, err, CodeScanFailed)
	if sweep.Workspaces != 0 {
		t.Errorf("Workspaces = %d, want none: nothing was removed", sweep.Workspaces)
	}
	if sweep.Entries != 0 || sweep.Contexts != 0 {
		t.Errorf("Sweep = %+v, want nothing counted", sweep)
	}
}
