// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// KeptNameLimit is how many bytes one element of a kept directory's relative
// name may take.
//
// A Go test name is a path — `TestX/a_case/deeper` — and one of them can be a
// sentence. Named as they come, a kept directory would be a tree several levels
// deep under a name no `ls` output can hold and no Windows path can reach. So
// the separators are folded, the name is cut to [keptNameBudget] and six hex
// digits are added: 40 + 1 + 6 fits inside this bound with a byte to spare, and
// the hex is what makes two directories of one test two directories.
const KeptNameLimit = 48

// keptNameBudget is the test name's share of [KeptNameLimit].
const keptNameBudget = 40

// keptNameAttempts is how many times a name collision is retried. Six hex digits
// collide once in sixteen million; the retry is there because "once in sixteen
// million" over a suite that keeps everything is a Tuesday.
const keptNameAttempts = 5

// keptRemoval bounds the removal of a directory that is not being kept.
//
// The retry is the Windows rule: a file can be held open by something the test
// started — an antivirus scanner, a `go` command that has not quite exited, a
// test binary the operating system has not finished unmapping — and a second
// attempt a moment later usually succeeds. Three attempts, then a log line: a
// collector that fails a green test because a directory it wanted to delete is
// still there has done more damage than the directory ever would.
const (
	keptRemovalAttempts = 3
	keptRemovalDelay    = 100 * time.Millisecond
)

// Scratch is the directory a test writes into.
//
// Under [KeepNever] it is t.TempDir and nothing about the test changes.
// Otherwise it is a directory of its own under [KeepRoot], removed by a cleanup
// unless the policy says to keep it — and when it is kept, it carries
// [KeptFileName], which says which test filed it, over which fixture, with
// which toolchain and what it ran, and the test is told the path.
//
// Every constructor here that used to call t.TempDir for a tree a test works in
// goes through this, which is the point: what gets kept has to be the fixture
// copy, the snapshot and the composed environment's scratch, not a directory
// beside them. Two exceptions are deliberate, and both are directories with
// nothing in them to read: the git environment's, which names two configuration
// files that must never exist, and internal/testkit/mutantkit's toolchain probe,
// whose scratch holds an empty private home. A directory per `git` command or
// per toolchain lookup would bury the ones that hold something.
//
// A second call returns a second directory. A test that copies two fixtures
// wants two trees, both are kept, and each one's account names the others.
func Scratch(t testing.TB) string {
	t.Helper()
	ForceFail(t)
	if KeepPolicy() == KeepNever {
		return t.TempDir()
	}
	l := ledgerFor(t)
	l.mu.Lock()
	first := !l.scratchTaken
	l.scratchTaken = true
	l.mu.Unlock()
	if first {
		return KeptDir(t)
	}
	return newKeptDir(t, l)
}

// KeptDir is where this test's evidence is filed: the directory [DumpFiles]
// copies into and mutantkit's trace recorder writes its recording into.
//
// It is the first [Scratch] of the test, so that everything one test kept is in
// one place, and it is empty for a run that keeps nothing — which is how a
// caller asks "is anything being kept?" without a second question. Under
// [KeepOnFailure] the directory is created before the test's verdict is known
// and removed by the cleanup if the test passes: a cleanup cannot create the
// directory it is deciding about, because the decision is taken in a cleanup of
// its own.
//
// It is created once under the ledger's lock. Two helpers asking for it at the
// same moment — a dump registering and a recorder attaching — would otherwise
// each make one, and the loser's directory would be a second kept directory
// holding nothing.
func KeptDir(t testing.TB) string {
	t.Helper()
	if KeepPolicy() == KeepNever {
		return ""
	}
	l := ledgerFor(t)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.kept == "" {
		l.kept = newKeptDirLocked(t, l)
	}
	return l.kept
}

// PackageScratch is [Scratch] for a directory a TestMain owns.
//
// A package that prepares one expensive fixture and shares it across its tests
// has no [testing.TB] to hang a cleanup on and no test to ask whether anything
// failed — so the caller says, with the status m.Run returned:
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		release(code != 0)
//		os.Exit(code)
//	}
//
// The release is the caller's obligation and nothing enforces it: a package that
// returns from TestMain without calling one leaves a directory behind, under the
// operating system's temporary directory where nothing will ever look at it
// again. That is a bug in the caller, and it is written down here because there
// is no cleanup to catch it.
//
// A kept root that cannot be resolved or created is reported and stepped around
// rather than fatal: the package still needs somewhere to work, so it falls back
// to os.MkdirTemp and says so. A suite that cannot run at all is a worse answer
// to a misconfigured variable than a suite that runs and keeps nothing.
func PackageScratch(name string) (dir string, release func(failed bool)) {
	policy := KeepPolicy()
	created, err := "", error(nil)
	if policy != KeepNever {
		created, err = keptPackageScratch(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "testkit: %v; keeping nothing for %s\n", err, name)
			policy = KeepNever
		}
	}
	if policy == KeepNever {
		created = temporaryPackageScratch(name)
	}

	return created, func(failed bool) {
		warnIfNothingWasForced()
		// KeepNever removes whatever happened, which is what makes the default
		// path the path there was before this existed: a package that failed
		// with the policy off leaves nothing behind.
		if policy == KeepNever || (policy == KeepOnFailure && !failed) {
			removeQuietly(created)
			return
		}
		writeReport(created, packageReport(created, name, policy, failed))
		fmt.Fprintf(os.Stderr, "testkit: kept: %s\n", created)
	}
}

// keptPackageScratch makes a package's directory under the kept root.
func keptPackageScratch(name string) (string, error) {
	root, err := KeepRoot()
	if err != nil {
		return "", fmt.Errorf("resolving the kept scratch root: %w", err)
	}
	if _, stampErr := stampHarnessDirectory(root, KeptMarker, keptMarkerBody); stampErr != nil {
		return "", fmt.Errorf("creating the kept scratch root %s: %w", root, stampErr)
	}
	created, err := makeKeptDir(filepath.Join(root, packageShortName()), name)
	if err != nil {
		return "", fmt.Errorf("creating the package scratch directory for %s under %s: %w", name, root, err)
	}
	return created, nil
}

// temporaryPackageScratch is the directory a package works in when nothing is
// being kept. A machine with no temporary directory at all has nothing to run,
// and a TestMain has no [testing.TB] to say so to.
func temporaryPackageScratch(name string) string {
	created, err := os.MkdirTemp("", "go-mutants-"+name+"-")
	if err != nil {
		panic(fmt.Sprintf("testkit: creating the package scratch directory for %s: %v", name, err))
	}
	return created
}

// newKeptDir creates one directory under the kept root and registers what
// happens to it.
func newKeptDir(t testing.TB, l *ledger) string {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	return newKeptDirLocked(t, l)
}

// newKeptDirLocked is [newKeptDir] for a caller that already holds the ledger.
//
// The cleanup it registers closes over the policy the directory was made under
// rather than reading the environment again, so a test that changes the variable
// cannot have a directory created under one rule and judged under another.
func newKeptDirLocked(t testing.TB, l *ledger) string {
	t.Helper()
	root, err := KeepRoot()
	if err != nil {
		t.Fatalf("resolving the kept scratch root: %v", err)
		return ""
	}
	stampKeptRoot(t, root)
	dir, err := makeKeptDir(filepath.Join(root, packageShortName()), t.Name())
	if err != nil {
		t.Fatalf("creating a kept scratch directory under %s: %v", root, err)
		return ""
	}
	l.dirs = append(l.dirs, dir)
	policy := l.policy
	t.Cleanup(func() {
		if policy != KeepAlways && !t.Failed() {
			removeKept(t, dir)
			return
		}
		writeReport(dir, testReport(t, dir, l, policy))
		t.Logf("kept: %s", dir)
	})
	return dir
}

// makeKeptDir creates `<parent>/<name cut short>-<six hex digits>`, retrying a
// collision — and retrying once more if the parent is gone when the leaf is
// made.
//
// # Why the parent could vanish, and why nothing here removes one now
//
// The parent is `<kept root>/<package>` and every test in a binary files under
// it. It used to be removed by the cleanup of whichever test emptied it, and
// under t.Parallel that removal landed between another test's MkdirAll of the
// same parent and the Mkdir of its own directory: the second syscall found no
// directory, and a test with nothing to do with keeping failed on the harness's
// own bookkeeping. It is what turned an ubuntu job on PR #48 red:
//
//	creating a kept scratch directory under /home/runner/work/_temp/go-mutants-kept:
//	mkdir …/go-mutants-kept/testkit/TestImportGateNamesAProductionImportOfTh-05eeab:
//	no such file or directory
//
// A mutex is not an answer to that, because the two racing sides need not be in
// one process: `go test ./...` runs the root package's binary and
// cmd/go-mutants' beside each other, [packageShortName] calls both `go-mutants`,
// both file under `<kept root>/go-mutants`, and one process's removal means
// nothing to the other's lock. So nothing removes a package directory at all,
// and an empty one is left where it is — which costs nothing anybody notices:
// actions/upload-artifact puts files in an artifact and skips empty directories,
// and `mise run test-clean` empties the whole kept root regardless.
//
// The one retry stays for the deleter this package does not control — somebody's
// `rm -rf`, a CI step tidying the runner's temporary directory, a sweeper with
// an opinion about stale files. The window is two syscalls wide, and the second
// attempt makes the parent again.
func makeKeptDir(parent, name string) (string, error) {
	dir, err := makeKeptDirOnce(parent, name)
	if !errors.Is(err, fs.ErrNotExist) {
		return dir, err
	}
	return makeKeptDirOnce(parent, name)
}

// makeKeptDirOnce is one attempt at [makeKeptDir]: the parent, then the leaf,
// under the collision retry [keptNameAttempts] bounds.
func makeKeptDirOnce(parent, name string) (string, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	var err error
	for range keptNameAttempts {
		dir := filepath.Join(parent, keptLeaf(name))
		// Mkdir rather than MkdirAll, because an existing directory is the
		// collision this loop is retrying rather than a directory to share.
		if err = os.Mkdir(dir, 0o755); err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", err
}

// keptLeaf turns a test name into one filesystem name.
//
// Three things happen and each is a name somebody could not have used: the
// separators of a subtest path are folded, so the name is one directory rather
// than a tree; everything a filesystem argues about becomes an underscore, which
// covers `:` and `*` on Windows as well as the spaces and quotation marks a
// table-driven case name carries; and the whole thing is cut to
// [keptNameBudget] before the six hex digits that make it unique.
func keptLeaf(name string) string {
	return sanitizedName(name, keptNameBudget) + "-" + randomSuffix()
}

// sanitizedName is one filesystem-safe name of at most budget bytes.
//
// Every rune it keeps is ASCII, so cutting at the budget cannot cut a rune in
// half — the trap in truncating a name that came from a subtest with a `→` in
// it.
func sanitizedName(name string, budget int) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= budget {
			break
		}
	}
	cut := b.String()
	if len(cut) > budget {
		cut = cut[:budget]
	}
	if cut == "" {
		return "unnamed"
	}
	return cut
}

// randomSuffix is six hex digits, or a timestamp when the machine has no
// randomness to give — which is not a reason to fail a test, only a reason for
// two directories of one test to be told apart less prettily.
func randomSuffix() string {
	var raw [3]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(raw[:])
}

// packageShortName is the package the running test binary was built from.
//
// It is read from the binary's own name rather than from the working directory,
// because `go test` names the binary `<package>.test` wherever it runs it, and
// a test that changed directory would otherwise file its evidence under a
// different heading from the test beside it.
var packageShortName = sync.OnceValue(func() string {
	base := filepath.Base(TestBinary())
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".test")
	return sanitizedName(base, keptNameBudget)
})

// removeKept removes a directory that is not being kept, retrying a file
// something still holds open.
//
// The package directory above it stays, empty. Removing it the moment it was
// empty is the race [makeKeptDir] describes, and no lock closes that one, since
// the two sides can be two test binaries. What a green run has to leave nothing
// of is evidence, and evidence is files — an empty directory is neither.
func removeKept(t testing.TB, dir string) {
	t.Helper()
	var err error
	for attempt := range keptRemovalAttempts {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		if attempt < keptRemovalAttempts-1 {
			time.Sleep(keptRemovalDelay)
		}
	}
	// Reported rather than failed: a file a virus scanner or an exiting child
	// still holds open is not a reason to turn a green test red.
	t.Logf("testkit: the scratch directory %s could not be removed and is left as it is "+
		"(a file in it is probably still open): %v", dir, err)
}

// removeQuietly is [removeKept] for a caller with no test to report to, and
// leaves the directory above it alone for the same reason.
func removeQuietly(dir string) {
	for attempt := range keptRemovalAttempts {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		if attempt < keptRemovalAttempts-1 {
			time.Sleep(keptRemovalDelay)
		}
	}
	fmt.Fprintf(os.Stderr, "testkit: %s could not be removed and is left as it is\n", dir)
}
