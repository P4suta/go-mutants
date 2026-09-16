// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The small deciders of this package, tested where they are rather than through
// the three commands above them.
//
// Each of them is one sentence of the cache's contract -- how a duration is
// rendered, whether an entry is evidence about this run, where a deletion is
// allowed to land -- and each is a pure function of its arguments. Driving them
// through `cache status` would be asserting on the arithmetic of a survey.

// TestPresenceAndTimeoutSourceAreHashedAsWordsRatherThanAsValues pins the two
// renderings the key is built out of.
//
// They are words rather than the values themselves because the key hashes a
// *statement*: a variable nobody set and one set to the empty string are
// different facts about a run, and a timeout the user promised and one a
// baseline measured are different facts about a bound even when the number is
// the same.
func TestPresenceAndTimeoutSourceAreHashedAsWordsRatherThanAsValues(t *testing.T) {
	t.Parallel()

	if got := presence(true); got != "set" {
		t.Errorf("presence(true) = %q, want %q", got, "set")
	}
	if got := presence(false); got != "unset" {
		t.Errorf("presence(false) = %q, want %q", got, "unset")
	}
	if presence(true) == presence(false) {
		t.Error("a variable that was set and one that was not hash the same")
	}

	for _, test := range []struct {
		configured time.Duration
		want       string
	}{
		{configured: time.Second, want: "explicit"},
		{configured: time.Nanosecond, want: "explicit"},
		{configured: 0, want: "derived"},
		{configured: -time.Second, want: "derived"},
	} {
		if got := timeoutSource(test.configured); got != test.want {
			t.Errorf("timeoutSource(%v) = %q, want %q", test.configured, got, test.want)
		}
	}
}

// TestMillisecondsTruncatesAndNeverGoesNegative pins the one arithmetic the key
// and every entry share.
//
// A negative bound is not a shorter one: it is a caller that has not set one,
// and rendering it as a negative number would put a duration in a key that no
// run could ever have been measured under.
func TestMillisecondsTruncatesAndNeverGoesNegative(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		in   time.Duration
		want int64
	}{
		{in: 0, want: 0},
		{in: time.Millisecond, want: 1},
		{in: 1500 * time.Microsecond, want: 1},
		{in: 2 * time.Second, want: 2000},
		{in: -time.Nanosecond, want: 0},
		{in: -time.Hour, want: 0},
	} {
		if got := milliseconds(test.in); got != test.want {
			t.Errorf("milliseconds(%v) = %d, want %d", test.in, got, test.want)
		}
	}
}

// TestAnEntryRendersTheMeasurementItStored is the round trip the two duration
// accessors exist for.
func TestAnEntryRendersTheMeasurementItStored(t *testing.T) {
	t.Parallel()

	entry := Entry{DurationMS: 1234, TimeoutMS: 10000}
	if got := entry.Duration(); got != 1234*time.Millisecond {
		t.Errorf("Duration() = %v, want 1.234s", got)
	}
	if got := entry.Timeout(); got != 10*time.Second {
		t.Errorf("Timeout() = %v, want 10s", got)
	}
	// A zero is a zero and not an absence: an entry a build before these
	// fields existed wrote renders as no measurement rather than as one.
	if got := (Entry{}).Duration(); got != 0 {
		t.Errorf("Duration() of an empty entry = %v, want 0", got)
	}
}

// TestDisplayShortensAnIDAndLeavesShortOnesAlone is the boundary every message
// in this package is built against.
func TestDisplayShortensAnIDAndLeavesShortOnesAlone(t *testing.T) {
	t.Parallel()

	const long = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	exact := long[:mutation.DisplayIDLength]
	for _, test := range []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{exact, exact},
		{exact + "0", exact},
		{long, exact},
	} {
		if got := display(test.in); got != test.want {
			t.Errorf("display(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

// TestTruncateTailKeepsTheEndAndSaysThatItDid is the other boundary: a tail of
// exactly the limit is kept whole, and one byte more is cut and marked.
//
// The end rather than the beginning, because the useful part of a failed test
// run is the assertion that failed and it is at the bottom. The marker is not
// decoration either: without it a reader is left wondering whether the output
// really did start mid-word.
func TestTruncateTailKeepsTheEndAndSaysThatItDid(t *testing.T) {
	t.Parallel()

	exact := strings.Repeat("x", MaxOutputTail)
	if got := truncateTail(exact); got != exact {
		t.Errorf("a tail of exactly the limit was changed: %d bytes became %d", len(exact), len(got))
	}
	if got := truncateTail("short"); got != "short" {
		t.Errorf("truncateTail(%q) = %q, want it left alone", "short", got)
	}

	over := strings.Repeat("a", MaxOutputTail) + "THE-END"
	got := truncateTail(over)
	if !strings.HasSuffix(got, "THE-END") {
		t.Error("truncateTail kept the beginning, want the end")
	}
	if !strings.Contains(got, "truncated by the outcome cache") {
		t.Error("a truncated tail does not say that it was truncated")
	}
	if len(got) <= MaxOutputTail {
		t.Errorf("the marked tail is %d bytes, want the marker on top of the limit", len(got))
	}
}

// TestUsableUnderIsTheWholeArgumentForKeepingTheTimeoutOutOfTheKey states the
// two rules in opposite directions, and the boundary of each.
//
// A killed or survived mutant finished in the recorded duration, so any bound
// at least that long reaches the same verdict. A confirmed timeout did not
// finish within its recorded bound, so any bound no larger does not finish
// either. A divergence was never measured against a bound at all, so every
// bound reaches it. A run that states no bound cannot say whether a measurement
// fits inside one, and adopts nothing.
func TestUsableUnderIsTheWholeArgumentForKeepingTheTimeoutOutOfTheKey(t *testing.T) {
	t.Parallel()

	finished := Entry{Outcome: mutation.OutcomeKilled, DurationMS: 500, TimeoutMS: 1000}
	timedOut := Entry{Outcome: mutation.OutcomeTimedOut, DurationMS: 1000, TimeoutMS: 1000}
	divergent := Entry{Outcome: mutation.OutcomeTimedOut, DurationMS: 20, TimeoutMS: 1000, Diverged: true}

	for _, test := range []struct {
		name    string
		entry   Entry
		timeout time.Duration
		want    bool
	}{
		{name: "a finished mutant under a longer bound", entry: finished, timeout: 2 * time.Second, want: true},
		{name: "a finished mutant under exactly its duration", entry: finished, timeout: 500 * time.Millisecond, want: true},
		{name: "a finished mutant under a shorter bound", entry: finished, timeout: 499 * time.Millisecond, want: false},
		{name: "a timeout under exactly its bound", entry: timedOut, timeout: time.Second, want: true},
		{name: "a timeout under a shorter bound", entry: timedOut, timeout: 999 * time.Millisecond, want: true},
		{name: "a timeout under a longer bound", entry: timedOut, timeout: 1001 * time.Millisecond, want: false},
		{name: "a finished mutant under no bound at all", entry: finished, timeout: 0, want: false},
		{name: "a timeout under no bound at all", entry: timedOut, timeout: 0, want: false},
		{name: "a finished mutant under a negative bound", entry: finished, timeout: -time.Second, want: false},
		{name: "a timeout under a negative bound", entry: timedOut, timeout: -time.Second, want: false},
		// The third rule, and the only one that does not read the bound. A
		// divergence is two counts taken in one tree -- the loop went further
		// than the original program ever goes under this suite -- so it is
		// evidence about every run of this tree, including one whose clock is
		// looser than the clock it was measured beside. Only a run that states
		// no bound at all adopts nothing, because that is a run this cache has
		// nothing to say to.
		{name: "a divergence under exactly its bound", entry: divergent, timeout: time.Second, want: true},
		{name: "a divergence under a shorter bound", entry: divergent, timeout: time.Millisecond, want: true},
		{name: "a divergence under a bound a thousand times longer", entry: divergent, timeout: 1000 * time.Second, want: true},
		{name: "a divergence under no bound at all", entry: divergent, timeout: 0, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.entry.UsableUnder(test.timeout); got != test.want {
				t.Errorf("UsableUnder(%v) = %v, want %v", test.timeout, got, test.want)
			}
		})
	}
}

// TestUsableWithinIsTheSameArgumentForTheMemoryBound covers the four cases the
// memory rule splits into, and the boundary of each.
func TestUsableWithinIsTheSameArgumentForTheMemoryBound(t *testing.T) {
	t.Parallel()

	const bound = 1 << 20
	stopped := Entry{Outcome: mutation.OutcomeKilled, MemoryExceeded: true, MemoryBytes: bound}
	killed := Entry{Outcome: mutation.OutcomeKilled, MemoryBytes: bound}
	survived := Entry{Outcome: mutation.OutcomeSurvived, MemoryBytes: bound}
	unbounded := Entry{Outcome: mutation.OutcomeSurvived}

	for _, test := range []struct {
		name  string
		entry Entry
		limit int64
		want  bool
	}{
		{name: "a bound stopped it, and this run's bound is the same", entry: stopped, limit: bound, want: true},
		{name: "a bound stopped it, and this run's is smaller", entry: stopped, limit: bound - 1, want: true},
		{name: "a bound stopped it, and this run's is larger", entry: stopped, limit: bound + 1, want: false},
		{name: "a bound stopped it, and this run has none", entry: stopped, limit: 0, want: false},
		{name: "a bound stopped it, and this run's is negative", entry: stopped, limit: -1, want: false},
		{name: "measured unbounded", entry: unbounded, limit: bound, want: true},
		{name: "measured unbounded and this run is too", entry: unbounded, limit: 0, want: true},
		{name: "a kill under any bound at all", entry: killed, limit: 1, want: true},
		{name: "a survivor under exactly the same bound", entry: survived, limit: bound, want: true},
		{name: "a survivor under a larger bound", entry: survived, limit: bound + 1, want: true},
		{name: "a survivor under a smaller bound", entry: survived, limit: bound - 1, want: false},
		{name: "a survivor under no bound at all", entry: survived, limit: 0, want: true},
		// A recorded bound of zero is "measured unbounded", which is what a
		// build before the field existed wrote, and it is judged by the clock
		// alone.
		{name: "a recorded bound of zero", entry: Entry{Outcome: mutation.OutcomeSurvived, MemoryBytes: 0}, limit: 1, want: true},
		{name: "a recorded bound below zero", entry: Entry{Outcome: mutation.OutcomeSurvived, MemoryBytes: -1}, limit: 1, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.entry.UsableWithin(test.limit); got != test.want {
				t.Errorf("UsableWithin(%d) = %v, want %v", test.limit, got, test.want)
			}
		})
	}
}

// TestAnEntryIsCheckedAgainstTheQuestionItWasAskedFor covers every shape check
// a document read off disk goes through.
//
// The key and the id are compared rather than assumed from the path, because
// the path is a truncated hash of one and a full hash of the other: a collision
// in the truncation would otherwise be adopted as an answer, and adopting the
// wrong run's outcome is the worst thing this package could do.
func TestAnEntryIsCheckedAgainstTheQuestionItWasAskedFor(t *testing.T) {
	t.Parallel()

	// Spelled with Repeat rather than written out: a run of sixty-four hex
	// characters in a test is indistinguishable from a leaked credential to the
	// scanner this repository runs over every file, and a digest that is
	// obviously a pattern says what it is to a reader as well.
	var (
		key     = strings.Repeat("ab", 32)
		context = strings.Repeat("ab", 8)
		id      = strings.Repeat("cd", 32)
	)
	good := func() Entry {
		return Entry{
			Version: EntryVersion, Key: key, Context: context, ID: id,
			Outcome: mutation.OutcomeKilled, DurationMS: 0, Attempts: 1, TimeoutMS: 1000,
		}
	}
	if err := good().check(key, context, id); err != nil {
		t.Fatalf("a well-formed entry was refused: %v", err)
	}

	for _, test := range []struct {
		name string
		edit func(*Entry)
		says string
	}{
		{name: "another schema version", edit: func(e *Entry) { e.Version = EntryVersion + 1 }, says: "version"},
		{name: "another cache key", edit: func(e *Entry) { e.Key = strings.Repeat("0", 64) }, says: "another cache key"},
		{name: "another cache context", edit: func(e *Entry) { e.Context = "ffffffffffffffff" }, says: "another cache context"},
		{name: "another mutant", edit: func(e *Entry) { e.ID = strings.Repeat("1", 64) }, says: "another mutant"},
		{name: "an outcome nobody may reuse", edit: func(e *Entry) { e.Outcome = mutation.OutcomeNotRun }, says: "not a reusable outcome"},
		{name: "a negative duration", edit: func(e *Entry) { e.DurationMS = -1 }, says: "could have happened"},
		{name: "no attempt at all", edit: func(e *Entry) { e.Attempts = 0 }, says: "could have happened"},
		{name: "no bound at all", edit: func(e *Entry) { e.TimeoutMS = 0 }, says: "could have happened"},
		{name: "a negative bound", edit: func(e *Entry) { e.TimeoutMS = -1 }, says: "could have happened"},
		{name: "a negative memory bound", edit: func(e *Entry) { e.MemoryBytes = -1 }, says: "could have happened"},
		{name: "a negative peak", edit: func(e *Entry) { e.PeakMemory = -1 }, says: "could have happened"},
		{
			name: "a memory kill with no bound to have exceeded",
			edit: func(e *Entry) { e.MemoryExceeded = true },
			says: "memory bound settled it",
		},
		{
			name: "a memory kill that is not a kill",
			edit: func(e *Entry) { e.MemoryExceeded, e.MemoryBytes, e.Outcome = true, 1<<20, mutation.OutcomeSurvived },
			says: "an outcome a bound cannot produce",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			entry := good()
			test.edit(&entry)
			err := entry.check(key, context, id)
			if err == nil {
				t.Fatalf("check accepted %+v", entry)
			}
			if !strings.Contains(err.Error(), test.says) {
				t.Errorf("check = %v, want it to say %q", err, test.says)
			}
		})
	}

	// And the shapes that are *not* refused, because each of them is an
	// ordinary entry somewhere: a zero duration is a very fast mutant, a zero
	// peak is one nothing measured, and a memory kill with a bound is the
	// case the field exists for.
	for _, test := range []struct {
		name string
		edit func(*Entry)
	}{
		{name: "a zero duration", edit: func(e *Entry) { e.DurationMS = 0 }},
		{name: "a zero peak", edit: func(e *Entry) { e.PeakMemory = 0 }},
		{name: "a zero memory bound", edit: func(e *Entry) { e.MemoryBytes = 0 }},
		{name: "a memory kill with the bound it exceeded", edit: func(e *Entry) {
			e.MemoryExceeded, e.MemoryBytes, e.PeakMemory = true, 1<<20, 2<<20
		}},
	} {
		t.Run("and "+test.name, func(t *testing.T) {
			t.Parallel()

			entry := good()
			test.edit(&entry)
			if err := entry.check(key, context, id); err != nil {
				t.Errorf("check refused %+v: %v", entry, err)
			}
		})
	}
}

// TestReasonOfDropsTheCodeAndKeepsTheSentence pins what a Skipped row reads
// like.
//
// The row is already a list of things not touched, so repeating a diagnostic
// code on every line of it would be noise -- and an error from somewhere with no
// code at all is printed as it stands rather than cut at its first colon.
func TestReasonOfDropsTheCodeAndKeepsTheSentence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		want string
	}{{
		name: "a coded failure",
		err:  errors.New("GOM5133: the marker is not this build's"),
		want: "the marker is not this build's",
	}, {
		name: "a failure with no code",
		err:  errors.New("something went wrong: and then more"),
		want: "something went wrong: and then more",
	}, {
		name: "a coded failure with a colon in the sentence",
		err:  errors.New("GOM5133: it says: no"),
		want: "it says: no",
	}, {
		name: "a code with nothing after it",
		err:  errors.New("GOM5133"),
		want: "GOM5133",
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := reasonOf(test.err); got != test.want {
				t.Errorf("reasonOf(%v) = %q, want %q", test.err, got, test.want)
			}
		})
	}
}

// TestWithinIsAnsweredByTheFilesystemAndNotBySpelling is the containment rule
// every deletion in this package goes through.
//
// It is the one function in go-mutants that deletes files in a directory
// somebody else's tools also keep things in, and a check that makes an escape
// unrepresentable is worth more than an argument that it cannot happen.
func TestWithinIsAnsweredByTheFilesystemAndNotBySpelling(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	inside := filepath.Join(root, "workspaces", "abc")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatalf("staging: %v", err)
	}
	outside := t.TempDir()

	for _, test := range []struct {
		name string
		path string
		want bool
	}{
		{name: "a directory under the root", path: inside, want: true},
		{name: "a file that is not there yet", path: filepath.Join(inside, "x.json"), want: true},
		{name: "the root itself", path: root, want: false},
		{name: "the root's parent", path: filepath.Dir(root), want: false},
		{name: "somewhere else entirely", path: outside, want: false},
		{name: "a path spelled through the root", path: filepath.Join(root, "..", "elsewhere"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := within(test.path, root)
			if err != nil {
				t.Fatalf("within: %v", err)
			}
			if got != test.want {
				t.Errorf("within(%q, %q) = %v, want %v", test.path, root, got, test.want)
			}
		})
	}
}

// TestWithinFollowsALinkOutOfTheCache is the reason containment is asked of the
// filesystem rather than of the two strings.
//
// A context directory replaced by a link to somewhere else is lexically inside
// the cache and physically wherever it points, and os.RemoveAll asks the
// filesystem: deleting through it would take the target's contents with it.
func TestWithinFollowsALinkOutOfTheCache(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	elsewhere := t.TempDir()
	link := filepath.Join(root, "outcomes")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("this platform will not create a symbolic link: %v", err)
	}

	// The link itself is a leaf, and RemoveAll unlinks a leaf rather than
	// following it -- so removing the link removes the link.
	if got, err := within(link, root); err != nil || !got {
		t.Errorf("within(the link itself) = %v, %v, want true", got, err)
	}
	// Anything *through* it is not in the cache at all.
	if got, err := within(filepath.Join(link, "abc"), root); err != nil || got {
		t.Errorf("within(through the link) = %v, %v, want false", got, err)
	}
}

// TestRemoveRefusesWhatIsNotInsideTheCache is [within]'s answer acted on.
func TestRemoveRefusesWhatIsNotInsideTheCache(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	elsewhere := t.TempDir()
	keep := filepath.Join(elsewhere, "keep.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatalf("staging: %v", err)
	}

	err := remove(elsewhere, root)
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeNotRemoved {
		t.Fatalf("remove = %v, want %s", err, CodeNotRemoved)
	}
	if _, statErr := os.Stat(keep); statErr != nil {
		t.Errorf("a path outside the cache was deleted anyway: %v", statErr)
	}
}

// TestResolvePathAnswersForAPathThatIsNotAllThere is what lets a sweep race
// another process without failing.
//
// filepath.EvalSymlinks needs the whole path to exist, and the paths this is
// asked about need not: an entry another process's sweep removed a moment ago,
// a context directory pruned between the listing and the deletion. The answer
// is about where a deletion *would* land, which is what the caller is deciding.
func TestResolvePathAnswersForAPathThatIsNotAllThere(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := resolvePath(root)
	if err != nil {
		t.Fatalf("resolvePath of a directory that is there: %v", err)
	}

	missing := filepath.Join(root, "not", "there", "at", "all")
	got, err := resolvePath(missing)
	if err != nil {
		t.Fatalf("resolvePath of a path that is not all there: %v", err)
	}
	if want := filepath.Join(resolvedRoot, "not", "there", "at", "all"); got != want {
		t.Errorf("resolvePath(%q) = %q, want %q", missing, got, want)
	}

	// The volume root is where walking up stops, and its own name is the
	// answer: there is nothing above it to resolve against.
	volume := filepath.VolumeName(resolvedRoot) + string(filepath.Separator)
	if resolved, resolveErr := resolvePath(volume); resolveErr != nil {
		t.Errorf("resolvePath(%q) = %v, want the volume root itself", volume, resolveErr)
	} else if resolved == "" {
		t.Errorf("resolvePath(%q) answered nothing", volume)
	}
}

// TestTrimExtendedPrefixPutsTwoSpellingsIntoOne is the Windows rule, checked
// everywhere because it is a string function and a platform cannot make a
// string function correct.
//
// A resolved path and a resolved root have to be compared in one spelling
// whichever of the two the operating system chose to hand back. Nothing outside
// Windows is affected: a resolved path on any other platform begins with a
// separator that is not a backslash.
func TestTrimExtendedPrefixPutsTwoSpellingsIntoOne(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ in, want string }{
		{`\\?\C:\Users\x`, `C:\Users\x`},
		{`\\?\UNC\server\share\x`, `\\server\share\x`},
		{`C:\Users\x`, `C:\Users\x`},
		{"/home/x", "/home/x"},
		{"", ""},
	} {
		if got := trimExtendedPrefix(test.in); got != test.want {
			t.Errorf("trimExtendedPrefix(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

// TestEveryCodeIsSpelledTheWayItIsPrinted writes this package's codes out.
func TestEveryCodeIsSpelledTheWayItIsPrinted(t *testing.T) {
	t.Parallel()

	for _, code := range Codes() {
		if code.String() != string(code) {
			t.Errorf("%s renders as %q", string(code), code.String())
		}
		if !strings.HasPrefix(code.String(), "GOM79") {
			t.Errorf("%s is outside the block this package owns", code)
		}
	}
	if len(Codes()) == 0 {
		t.Fatal("this package reports no codes at all")
	}
}

// unreadableDir makes a directory refuse to be listed, and skips the test where
// it cannot.
func unreadableDir(t *testing.T, dir string) {
	t.Helper()
	chmodOrSkip(t, dir, 0o000, 0o700, func() error {
		_, err := os.ReadDir(dir)
		return err
	})
}

// unsearchableDir makes a directory list its names and refuse to stat any of
// them, which is read without execute.
func unsearchableDir(t *testing.T, dir string) {
	t.Helper()
	chmodOrSkip(t, dir, 0o600, 0o700, func() error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Skipf("this filesystem does not list an unsearchable directory: %v", err)
		}
		if len(entries) == 0 {
			t.Skip("an unsearchable directory needs something in it to refuse to stat")
		}
		_, err = entries[0].Info()
		return err
	})
}

// unwritableDir makes a directory refuse new entries.
func unwritableDir(t *testing.T, dir string) {
	t.Helper()
	chmodOrSkip(t, dir, 0o500, 0o700, func() error {
		probe := filepath.Join(dir, "probe")
		err := os.WriteFile(probe, []byte("x"), 0o600)
		if err == nil {
			_ = os.Remove(probe)
		}
		return err
	})
}

// unreadableFile makes a file refuse to be opened.
func unreadableFile(t *testing.T, path string) {
	t.Helper()
	chmodOrSkip(t, path, 0o200, 0o600, func() error {
		f, err := os.Open(path)
		if err == nil {
			_ = f.Close()
		}
		return err
	})
}

// chmodOrSkip sets a mode, proves the mode is enforced, and restores it
// afterwards -- or skips where a platform or a user is not stopped by it.
func chmodOrSkip(t *testing.T, path string, mode, restore fs.FileMode, probe func() error) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows file mode does not refuse this the way the test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make an operation fail")
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("setting the mode of %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, restore) })
	if probe() == nil {
		t.Skip("this filesystem does not enforce the mode this test needs")
	}
}
