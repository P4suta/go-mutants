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

func keptRootFor(t *testing.T, policy string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "kept")
	t.Setenv(KeepEnv, policy)
	t.Setenv(KeepDirEnv, root)
	t.Setenv(VerboseEnv, "")
	return root
}

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

func (k *keepTB) finish() {
	for i := len(k.cleanups) - 1; i >= 0; i-- {
		k.cleanups[i]()
	}
	k.cleanups = nil
}

func (k *keepTB) log() string { return strings.Join(k.logs, "\n") }

func (k *keepTB) run(body func(testing.TB)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		body(k)
	}()
	<-done
}

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

func TestScratchIsRemovedWhenTheTestPassesUnderKeepOnFailure(t *testing.T) {
	root := keptRootFor(t, "on-failure")

	tb := newKeepTB(t, "TestSomethingThatPasses")
	var dir string
	tb.run(func(inner testing.TB) { dir = Scratch(inner) })
	tb.finish()

	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch directory %s outlived a test that passed: %v", dir, err)
	}
	pkg := filepath.Join(root, packageShortName())
	entries, err := os.ReadDir(pkg)
	if err != nil {
		t.Fatalf("the package directory %s was removed rather than left empty, which is the prune "+
			"that cannot be made safe across two test binaries: %v", pkg, err)
	}
	if len(entries) != 0 {
		t.Errorf("%s still holds %d entry/entries after the only test in it passed: %v",
			pkg, len(entries), entries)
	}
}

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

func truncateForReport(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return s[:2000] + "\n…"
}

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

	copied := filepath.Join(kept, DumpDirName, "1-"+sanitizedName(filepath.Base(root), keptNameBudget),
		"inner", "f.go")
	if got := ReadFile(t, copied); string(got) != string(body) {
		t.Errorf("%s holds %q, want the whole file %q", copied, got, body)
	}
}

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

func TestConcurrentScratchesSurviveConcurrentSiblings(t *testing.T) {
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
				tb := newKeepTB(t, fmt.Sprintf("TestParallelWorker%02d/round=%02d", worker, round))
				tb.run(func(inner testing.TB) { Scratch(inner) })
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
		t.Fatalf("%d of %d kept directories could not be created while a sibling was being removed; "+
			"the first few:\n  %s", len(failures), workers*rounds,
			strings.Join(failures[:min(len(failures), 5)], "\n  "))
	}
}
