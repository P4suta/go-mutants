// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// keptRootFor points the keep policy at a directory of this test's own and
// returns it.
//
// Every test in this file calls it, and none of them may skip it: the real kept
// root is `<os.UserCacheDir()>/go-mutants-test/kept`, a directory the developer
// running the suite owns and a directory CI names for the whole job. A test that
// wrote there would leave evidence of a test that passed, and a test that
// *removed* from there would delete the evidence of the failure somebody was
// reading.
func keptRootFor(t *testing.T, policy string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "kept")
	t.Setenv(KeepEnv, policy)
	t.Setenv(KeepDirEnv, root)
	t.Setenv(VerboseEnv, "")
	return root
}

// requireDefaultKeptRootUntouched fails a test that filed anything in the
// developer's own kept root.
//
// It is the package guard in [TestMain] narrowed to one test, and it exists
// because that guard has to stand down in exactly the configuration this test
// creates. `GO_MUTANTS_TEST_KEEP=1` with no `GO_MUTANTS_TEST_KEEP_DIR` is the
// documented way to keep things locally, so the package guard cannot read "the
// default root appeared" as a defect — every test in the package files there by
// design under that setting. The tests that go out of their way to *unset* the
// override are the exception: what they are asking is where the default is, and
// asking must not create it. So they say so here, per test.
//
// A root that was already there is left alone and not checked: it is the
// developer's, it may hold evidence they are reading, and nothing here can tell
// what this test added to it.
func requireDefaultKeptRootUntouched(t *testing.T) {
	t.Helper()
	if pinned.userCache == "" {
		return
	}
	root := filepath.Join(pinned.userCache, harnessDirName, keptDirName)
	if _, err := os.Lstat(root); err == nil {
		return
	}
	t.Cleanup(func() {
		if _, err := os.Lstat(root); err != nil {
			return
		}
		t.Errorf("this test created %s, which is the developer's own kept root and the one CI does "+
			"not upload. A test that clears %s to ask what the default is has to clear %s as well, or "+
			"point it at a directory of its own: resolving a path must not file anything under it.",
			root, KeepDirEnv, KeepEnv)
	})
}

// keepTB is a [testing.TB] that records what the keep policy did to it and lets
// a test decide whether it failed.
//
// It is the recorder pattern this package already uses for [expectFatal], with
// the three methods the policy needs on top: Name, because a kept directory is
// named after the test; Cleanup, because keeping and removing both happen there
// and a test of them has to be able to run them; and Failed, because
// "on failure" is a question asked of the test rather than of the harness.
//
// The embedded TB is the parent, as [recorder]'s is, because [Scratch] falls
// back to t.TempDir under the policy that keeps nothing.
type keepTB struct {
	testing.TB
	name     string
	failed   bool
	cleanups []func()
	logs     []string
	errors   []string
	fatals   []string
}

func newKeepTB(t *testing.T, name string) *keepTB {
	return &keepTB{TB: t, name: name}
}

func (k *keepTB) Helper()      {}
func (k *keepTB) Name() string { return k.name }
func (k *keepTB) Failed() bool { return k.failed }
func (k *keepTB) Cleanup(f func()) {
	k.cleanups = append(k.cleanups, f)
}

func (k *keepTB) Logf(format string, args ...any) {
	k.logs = append(k.logs, fmt.Sprintf(format, args...))
}

func (k *keepTB) Errorf(format string, args ...any) {
	k.failed = true
	k.errors = append(k.errors, fmt.Sprintf(format, args...))
}

func (k *keepTB) Fatalf(format string, args ...any) {
	k.failed = true
	k.fatals = append(k.fatals, fmt.Sprintf(format, args...))
	runtime.Goexit()
}

// finish runs the cleanups the way the testing package does: last registered
// first, and after the test's own body has decided whether it failed.
func (k *keepTB) finish() {
	for i := len(k.cleanups) - 1; i >= 0; i-- {
		k.cleanups[i]()
	}
	k.cleanups = nil
}

// log is everything the fake was told, as one document to assert on.
func (k *keepTB) log() string { return strings.Join(k.logs, "\n") }

// run calls body on a goroutine of its own, so that a Fatalf inside it can end
// it with runtime.Goexit rather than the parent test.
func (k *keepTB) run(body func(testing.TB)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		body(k)
	}()
	<-done
}

// TestScratchIsRemovedByDefault is the promise that turning nothing on changes
// nothing: without the policy, a scratch directory is t.TempDir and the testing
// package removes it.
//
// Both halves are asserted, and the second is the one that matters on a
// developer's machine: nothing at all appears under the kept root. Keeping
// unconditionally filled a disk twice, which is why the default is off.
func TestScratchIsRemovedByDefault(t *testing.T) {
	root := keptRootFor(t, "")

	var dir string
	t.Run("inner", func(t *testing.T) {
		dir = Scratch(t)
		WriteFile(t, filepath.Join(dir, "evidence.txt"), []byte("here"))
	})

	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch directory %s is still there after the test that owned it: %v", dir, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists, and nothing should have been filed under the kept root with the "+
			"policy off: %v", root, err)
	}
}

// TestScratchIsKeptWhenTheTestFailsUnderThePolicy is the whole feature in one
// test: a failed test's directory is still there afterwards, and it says what
// the test was.
func TestScratchIsKeptWhenTheTestFailsUnderThePolicy(t *testing.T) {
	keptRootFor(t, "1")

	tb := newKeepTB(t, "TestSomething/a_subtest")
	var fixture, dir string
	tb.run(func(inner testing.TB) {
		dir = Copy(inner, "simple")
		fixture = filepath.Join(Root(t), FixturesDir, "simple")
	})
	tb.failed = true
	tb.finish()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the fixture copy %s was removed although the test failed: %v", dir, err)
	}
	kept := filepath.Dir(dir)
	report := string(ReadFile(t, filepath.Join(kept, KeptFileName)))
	for _, want := range []string{"TestSomething/a_subtest", fixture, kept} {
		if !strings.Contains(report, want) {
			t.Errorf("%s does not name %q:\n%s", KeptFileName, want, report)
		}
	}
	if log := tb.log(); !strings.Contains(log, "kept: ") || !strings.Contains(log, kept) {
		t.Errorf("the test was not told where its directory was kept:\n%s", log)
	}
}

// TestScratchIsRemovedWhenTheTestPassesUnderKeepOnFailure is the other half of
// the same policy, and the half a disk depends on: CI turns keeping on for every
// job, and a green job must leave nothing behind.
func TestScratchIsRemovedWhenTheTestPassesUnderKeepOnFailure(t *testing.T) {
	root := keptRootFor(t, "on-failure")

	tb := newKeepTB(t, "TestSomethingThatPasses")
	var dir string
	tb.run(func(inner testing.TB) { dir = Scratch(inner) })
	tb.finish()

	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch directory %s outlived a test that passed: %v", dir, err)
	}
	// The package directory above it goes too. Under keep-on-failure every test
	// in a green suite makes and removes one of these, and a run that left the
	// empty parents behind would fill the kept root with a directory per package
	// saying nothing — which CI would then upload.
	if _, err := os.Stat(filepath.Join(root, packageShortName())); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the package directory outlived the only test in it: %v", err)
	}
}

// TestScratchIsKeptAlwaysUnderAlways is the setting for the run where the
// question is what a *passing* test produced.
func TestScratchIsKeptAlwaysUnderAlways(t *testing.T) {
	keptRootFor(t, "always")

	tb := newKeepTB(t, "TestSomethingElse")
	var dir string
	tb.run(func(inner testing.TB) { dir = Scratch(inner) })
	tb.finish()

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the scratch directory %s was removed although the policy is always: %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeptFileName)); err != nil {
		t.Errorf("a kept directory with no %s: %v", KeptFileName, err)
	}
}

// TestKeptNamesStayShort is the bound that keeps a kept directory nameable on
// every filesystem, and the uniqueness that keeps two of them apart.
//
// A Go test name is a path — `TestX/a_case/deeper` — and a directory named after
// one would be a tree three levels deep under a name nothing could list. A
// subtest's separators are folded, the name is cut, and the six hex digits are
// what make two directories of one test two directories rather than one.
func TestKeptNamesStayShort(t *testing.T) {
	root := keptRootFor(t, "always")

	long := "TestSomethingWithAVeryLongNameIndeedThatKeepsGoing/and_a_subtest/deeper_still"
	tb := newKeepTB(t, long)
	var first, second string
	tb.run(func(inner testing.TB) {
		first = Scratch(inner)
		second = Scratch(inner)
	})
	tb.finish()

	if first == second {
		t.Fatalf("two calls to Scratch produced one directory: %s", first)
	}
	for _, dir := range []string{first, second} {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("%s is not under the kept root %s: %v", dir, root, err)
		}
		for _, element := range strings.Split(filepath.ToSlash(rel), "/") {
			if len(element) > KeptNameLimit {
				t.Errorf("the name element %q is %d bytes, over the %d-byte bound",
					element, len(element), KeptNameLimit)
			}
			if strings.ContainsAny(element, `/\:*?"<>|`) {
				t.Errorf("the name element %q holds a character a filesystem refuses", element)
			}
		}
	}
}

// TestKeepPolicyRejectsAnUnknownSpelling is why a typo cannot switch keeping
// off.
//
// The variable is set for a whole CI job, in a file nobody reads again. A policy
// that read `GO_MUTANTS_TEST_KEEP=sometimes` as "never" would leave every job
// with nothing to upload and nothing in the log to say why, which is exactly the
// failure the feature exists to prevent.
func TestKeepPolicyRejectsAnUnknownSpelling(t *testing.T) {
	t.Setenv(KeepEnv, "sometimes")

	defer func() {
		message, ok := recover().(string)
		if !ok {
			t.Fatalf("an unknown spelling was accepted rather than refused")
		}
		for _, want := range []string{KeepEnv, "sometimes", "always"} {
			if !strings.Contains(message, want) {
				t.Errorf("the refusal does not name %q: %s", want, message)
			}
		}
	}()
	t.Errorf("KeepPolicy returned %s rather than refusing an unknown spelling", KeepPolicy())
}

// TestDumpFilesPrintsOnlyOnFailureAndBoundsTheOutput is the rule that makes a
// dump readable and affordable.
//
// A passing test prints nothing, because a suite that printed every
// instrumented tree it built would bury the one that matters. A failing one
// prints them, capped: a CI log has a size limit of its own, and a dump that
// blows through it takes the failure with it.
func TestDumpFilesPrintsOnlyOnFailureAndBoundsTheOutput(t *testing.T) {
	keptRootFor(t, "")

	root := t.TempDir()
	WriteFile(t, filepath.Join(root, "small.go"), []byte("package p\n"))
	WriteFile(t, filepath.Join(root, "inner", "big.go"), []byte(strings.Repeat("x", 100<<10)))
	WriteFile(t, filepath.Join(root, "notes.txt"), []byte("not a go file"))

	passed := newKeepTB(t, "TestThatPassed")
	passed.run(func(inner testing.TB) { DumpFiles(inner, root, "**/*.go") })
	passed.finish()
	if log := passed.log(); strings.Contains(log, "small.go") {
		t.Errorf("a passing test printed its dump:\n%s", log)
	}

	failed := newKeepTB(t, "TestThatFailed")
	failed.run(func(inner testing.TB) { DumpFiles(inner, root, "**/*.go") })
	failed.failed = true
	failed.finish()

	log := failed.log()
	for _, want := range []string{
		"--- " + filepath.Join(root, "small.go") + " (10 bytes)",
		"--- " + filepath.Join(root, "inner", "big.go"),
		"package p",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the dump does not hold %q:\n%s", want, truncateForReport(log))
		}
	}
	if strings.Contains(log, "notes.txt") {
		t.Errorf("the dump printed a file no glob matched:\n%s", truncateForReport(log))
	}
	if len(log) > DumpTotalLimit+(64<<10) {
		t.Errorf("the dump is %d bytes, which is past the %d-byte cap and its headers",
			len(log), DumpTotalLimit)
	}
	if !strings.Contains(log, "elided") {
		t.Errorf("the dump cut a file off without saying so:\n%s", truncateForReport(log))
	}
}

// truncateForReport keeps a failure message from being the very wall of text the
// test is about.
func truncateForReport(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return s[:2000] + "\n…"
}

// TestDumpFilesCopiesIntoTheKeptDirectory is what makes a dump more than a log
// line: the files are beside the directory that was kept, whole rather than cut
// at the log's cap.
func TestDumpFilesCopiesIntoTheKeptDirectory(t *testing.T) {
	keptRootFor(t, "always")

	root := t.TempDir()
	body := []byte("package p\n\nfunc F() {}\n")
	WriteFile(t, filepath.Join(root, "inner", "f.go"), body)

	tb := newKeepTB(t, "TestWithADump")
	var kept string
	tb.run(func(inner testing.TB) {
		kept = Scratch(inner)
		DumpFiles(inner, root, "**/*.go")
	})
	tb.finish()

	// One numbered directory per call, named after the tree it came from: the
	// first call of this test over a directory whose base name is the temporary
	// one the parent made.
	copied := filepath.Join(kept, DumpDirName, "1-"+sanitizedName(filepath.Base(root), keptNameBudget),
		"inner", "f.go")
	if got := ReadFile(t, copied); string(got) != string(body) {
		t.Errorf("%s holds %q, want the whole file %q", copied, got, body)
	}
}

// TestPackageScratchReleaseRemovesUnlessKept is the same policy for a directory
// a TestMain owns, where there is no [testing.TB] to ask whether anything
// failed — so the caller says.
func TestPackageScratchReleaseRemovesUnlessKept(t *testing.T) {
	for _, test := range []struct {
		policy string
		failed bool
		kept   bool
	}{
		{policy: "", failed: false},
		{policy: "", failed: true},
		{policy: "1", failed: false},
		{policy: "1", failed: true, kept: true},
		{policy: "always", failed: false, kept: true},
	} {
		name := "policy=" + test.policy + "/failed=" + fmt.Sprint(test.failed)
		t.Run(name, func(t *testing.T) {
			keptRootFor(t, test.policy)

			dir, release := PackageScratch("probeable")
			WriteFile(t, filepath.Join(dir, "evidence.txt"), []byte("here"))
			release(test.failed)

			_, err := os.Stat(dir)
			switch {
			case test.kept && err != nil:
				t.Errorf("%s was removed although it should have been kept: %v", dir, err)
			case !test.kept && !errors.Is(err, fs.ErrNotExist):
				t.Errorf("%s outlived its release: %v", dir, err)
			}
		})
	}
}

// TestForceFailHookFiresOnlyForTheNamedTest is the hook that makes the feature
// demonstrable: a developer who wants to see what a failure leaves behind names
// a test rather than editing one.
func TestForceFailHookFiresOnlyForTheNamedTest(t *testing.T) {
	keptRootFor(t, "")
	t.Setenv(ForceFailEnv, "TestTheNamedOne")

	named := newKeepTB(t, "TestTheNamedOne")
	named.run(func(inner testing.TB) { ForceFail(inner) })
	if len(named.errors) != 1 {
		t.Errorf("the named test was reported %d time(s), want 1: %q", len(named.errors), named.errors)
	} else if !strings.Contains(named.errors[0], ForceFailEnv) {
		t.Errorf("the forced failure does not name %s: %s", ForceFailEnv, named.errors[0])
	}

	other := newKeepTB(t, "TestAnotherOne")
	other.run(func(inner testing.TB) { ForceFail(inner) })
	if len(other.errors) != 0 {
		t.Errorf("a test nobody named was failed: %q", other.errors)
	}
}

// TestExecLedgerRecordsEveryChildArgv is what turns a kept directory into a
// reproduction: the commands the test ran, in order, quoted the way a shell
// would take them back.
//
// The child is this very test binary with a pattern that selects nothing, which
// is the one child a unit-tier test can start without a toolchain.
func TestExecLedgerRecordsEveryChildArgv(t *testing.T) {
	keptRootFor(t, "always")

	tb := newKeepTB(t, "TestThatRanAChild")
	var kept string
	tb.run(func(inner testing.TB) {
		kept = Scratch(inner)
		for _, pattern := range []string{"^$", "^ThereIsNoSuchTest$"} {
			result := Exec(inner, kept, os.Environ(), TestBinary(), "-test.run="+pattern)
			RequireExit(inner, result, 0, "a child that runs no test")
		}
	})
	tb.finish()

	report := string(ReadFile(t, filepath.Join(kept, KeptFileName)))
	for _, want := range []string{"-test.run=^$", "-test.run=^ThereIsNoSuchTest$"} {
		if !strings.Contains(report, want) {
			t.Errorf("%s does not record the child %q:\n%s", KeptFileName, want, report)
		}
	}
}

// TestKeepPolicyReadsTheSpellingsAPersonTypes is the table of what the variable
// accepts, and it is a table because the variable is typed by a person into a
// workflow file or a shell.
//
// `yes`, `on` and `enabled` are here because [RequireTools] already accepts
// them for the harness's other switch: two variables in one namespace that
// disagree about what "on" is spelled would be a trap with no symptom, since one
// of them would silently be off. `no`, `off`, `0` and `false` are the same
// argument in the other direction — the one spelling of "off" a person is most
// likely to type is the one that used to panic.
func TestKeepPolicyReadsTheSpellingsAPersonTypes(t *testing.T) {
	for _, test := range []struct {
		value string
		want  Keep
	}{
		{value: "", want: KeepNever},
		{value: "0", want: KeepNever},
		{value: "false", want: KeepNever},
		{value: "no", want: KeepNever},
		{value: "off", want: KeepNever},
		{value: "1", want: KeepOnFailure},
		{value: "true", want: KeepOnFailure},
		{value: "yes", want: KeepOnFailure},
		{value: "y", want: KeepOnFailure},
		{value: "on", want: KeepOnFailure},
		{value: "enabled", want: KeepOnFailure},
		{value: "failed", want: KeepOnFailure},
		{value: "on-failure", want: KeepOnFailure},
		{value: "always", want: KeepAlways},
		// Case and surrounding space are a shell's doing rather than a
		// decision, so they are read through.
		{value: " ALWAYS ", want: KeepAlways},
		{value: "On-Failure", want: KeepOnFailure},
	} {
		t.Run("value="+test.value, func(t *testing.T) {
			keptRootFor(t, test.value)
			if got := KeepPolicy(); got != test.want {
				t.Errorf("KeepPolicy with %s=%q = %s, want %s", KeepEnv, test.value, got, test.want)
			}
		})
	}
}

// TestTwoDumpsInOneTestDoNotOverwriteEachOther is the failure a test with two
// snapshots had: TestValidateIsDeterministic instruments the same fixture twice
// and compares the two trees, and both dumps landed in one `dump/` directory
// under the same relative names — so what a reader found was one tree, half of
// it from each run, with nothing saying so.
//
// The printed headers have the same problem in the log: `--- limits.go` appears
// twice and names neither root.
func TestTwoDumpsInOneTestDoNotOverwriteEachOther(t *testing.T) {
	keptRootFor(t, "always")

	first, second := t.TempDir(), t.TempDir()
	WriteFile(t, filepath.Join(first, "limits.go"), []byte("package p // the first tree\n"))
	WriteFile(t, filepath.Join(second, "limits.go"), []byte("package p // the second tree\n"))

	tb := newKeepTB(t, "TestWithTwoTrees")
	var kept string
	tb.run(func(inner testing.TB) {
		kept = Scratch(inner)
		DumpFiles(inner, first, "**/*.go")
		DumpFiles(inner, second, "**/*.go")
	})
	tb.failed = true
	tb.finish()

	var copies []string
	for _, body := range []string{"the first tree", "the second tree"} {
		found := false
		err := filepath.WalkDir(filepath.Join(kept, DumpDirName), func(p string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if strings.Contains(string(ReadFile(t, p)), body) {
				found, copies = true, append(copies, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking the dump directory: %v", err)
		}
		if !found {
			t.Errorf("no copy of %q survived the second dump: %v", body, copies)
		}
	}

	log := tb.log()
	for _, root := range []string{first, second} {
		if !strings.Contains(log, "--- "+filepath.Join(root, "limits.go")) {
			t.Errorf("the printed header does not name the tree %s it came from:\n%s",
				root, truncateForReport(log))
		}
	}
}

// TestEveryKeptDirectoryNamesTheOthers is what makes several kept directories
// one piece of evidence rather than three.
//
// A real integration test takes a scratch for its environment, one for each
// snapshot and one for a fixture copy, and they are siblings named after the
// same test with different random suffixes. A reader who opens one has no way to
// know the others exist, let alone which is which — so each account lists them.
func TestEveryKeptDirectoryNamesTheOthers(t *testing.T) {
	keptRootFor(t, "always")

	tb := newKeepTB(t, "TestWithSeveralDirectories")
	var first, second string
	tb.run(func(inner testing.TB) {
		first = Scratch(inner)
		second = Scratch(inner)
		KeepPath(inner, "/somewhere/else", "the session's temporary parent")
	})
	tb.finish()

	for _, pair := range [][2]string{{first, second}, {second, first}} {
		report := string(ReadFile(t, filepath.Join(pair[0], KeptFileName)))
		if !strings.Contains(report, "Also kept:") {
			t.Errorf("%s has no `Also kept:` block:\n%s", pair[0], report)
		}
		if !strings.Contains(report, pair[1]) {
			t.Errorf("%s does not name its sibling %s:\n%s", pair[0], pair[1], report)
		}
		if !strings.Contains(report, "/somewhere/else") {
			t.Errorf("%s does not carry the marked path:\n%s", pair[0], report)
		}
	}
}

// TestForceFailFiresForATestThatTakesNoScratch is the hole the hook had: it was
// called from [Scratch], so a test that resolves a fixture, locates a toolchain
// or reads a shared session — the whole root package — could name itself in
// GO_MUTANTS_TEST_FORCE_FAIL and see nothing happen at all.
//
// It now fires from the one line every constructor here already writes.
func TestForceFailFiresForATestThatTakesNoScratch(t *testing.T) {
	keptRootFor(t, "")
	t.Setenv(ForceFailEnv, "TestThatOnlyResolvesAFixture")

	tb := newKeepTB(t, "TestThatOnlyResolvesAFixture")
	tb.run(func(inner testing.TB) { Fixture(inner, "simple") })

	if len(tb.errors) != 1 {
		t.Errorf("a test that took no scratch was reported %d time(s), want 1: %q",
			len(tb.errors), tb.errors)
	}
}

// TestConcurrentScratchesSurviveSiblingPruning is the CI failure that turned
// PR #48 red on ubuntu with the keep policy on:
//
//	creating a kept scratch directory under /home/runner/work/_temp/go-mutants-kept:
//	mkdir …/go-mutants-kept/testkit/TestImportGateNamesAProductionImportOfTh-05eeab:
//	no such file or directory
//
// Nothing was wrong with the name and nothing was wrong with the root. Two
// parallel tests of one test binary file their directories under the same
// package directory, and the cleanup of the one that finished first removes its
// own directory and then prunes that package directory, which is empty at that
// instant — while the other is between the [os.MkdirAll] of the parent and the
// [os.Mkdir] of its own directory. The second syscall then lands in a directory
// that no longer exists, and a test that had nothing to do with keeping fails on
// the harness's own bookkeeping.
//
// So the two are serialised, and this is the test that says so: many workers
// creating and pruning under one package directory, every creation asserted to
// succeed. The ledger's lock is per test and cannot help here — the tests racing
// are different tests.
func TestConcurrentScratchesSurviveSiblingPruning(t *testing.T) {
	keptRootFor(t, "1")

	const (
		workers = 32
		rounds  = 40
	)
	var (
		mu       sync.Mutex
		failures []string
		wg       sync.WaitGroup
	)
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := range rounds {
				// A name of its own per directory, because the ledger is keyed
				// by the test's name and these stand in for different tests.
				tb := newKeepTB(t, fmt.Sprintf("TestParallelWorker%02d/round=%02d", worker, round))
				tb.run(func(inner testing.TB) { Scratch(inner) })
				// The policy is on-failure and the fake passed, so this removes
				// the directory and prunes the package directory above it —
				// which is the other half of the race.
				tb.finish()
				if len(tb.fatals) == 0 {
					continue
				}
				mu.Lock()
				failures = append(failures, tb.fatals...)
				mu.Unlock()
				return
			}
		}()
	}
	wg.Wait()

	if len(failures) != 0 {
		t.Fatalf("%d of %d kept directories could not be created while a sibling was being pruned; "+
			"the first few:\n  %s", len(failures), workers*rounds,
			strings.Join(failures[:min(len(failures), 5)], "\n  "))
	}
}
