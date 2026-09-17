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

func TestAnEntryRendersTheMeasurementItStored(t *testing.T) {
	t.Parallel()

	entry := Entry{DurationMS: 1234, TimeoutMS: 10000}
	if got := entry.Duration(); got != 1234*time.Millisecond {
		t.Errorf("Duration() = %v, want 1.234s", got)
	}
	if got := entry.Timeout(); got != 10*time.Second {
		t.Errorf("Timeout() = %v, want 10s", got)
	}
	if got := (Entry{}).Duration(); got != 0 {
		t.Errorf("Duration() of an empty entry = %v, want 0", got)
	}
}

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

func TestAnEntryIsCheckedAgainstTheQuestionItWasAskedFor(t *testing.T) {
	t.Parallel()

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

func TestWithinFollowsALinkOutOfTheCache(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	elsewhere := t.TempDir()
	link := filepath.Join(root, "outcomes")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("this platform will not create a symbolic link: %v", err)
	}

	if got, err := within(link, root); err != nil || !got {
		t.Errorf("within(the link itself) = %v, %v, want true", got, err)
	}
	if got, err := within(filepath.Join(link, "abc"), root); err != nil || got {
		t.Errorf("within(through the link) = %v, %v, want false", got, err)
	}
}

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

	volume := filepath.VolumeName(resolvedRoot) + string(filepath.Separator)
	if resolved, resolveErr := resolvePath(volume); resolveErr != nil {
		t.Errorf("resolvePath(%q) = %v, want the volume root itself", volume, resolveErr)
	} else if resolved == "" {
		t.Errorf("resolvePath(%q) answered nothing", volume)
	}
}

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

func unreadableDir(t *testing.T, dir string) {
	t.Helper()
	chmodOrSkip(t, dir, 0o000, 0o700, func() error {
		_, err := os.ReadDir(dir)
		return err
	})
}

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
