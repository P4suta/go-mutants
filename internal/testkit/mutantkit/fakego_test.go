// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

func fakeEnv(t *testing.T, f *mutantkit.Fake) []string {
	t.Helper()
	return f.Env(testkit.Compose(t, testkit.Scratch(t)))
}

func runFake(t *testing.T, f *mutantkit.Fake, dir string, args ...string) runner.Result {
	t.Helper()
	spec := runner.Spec{
		Argv:    append([]string{f.Bin()}, args...),
		Dir:     dir,
		Env:     fakeEnv(t, f),
		Timeout: mutantkit.StepTimeout,
	}
	return runner.Run(t.Context(), spec)
}

func TestFakeGoAnswersTheVersionProbe(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: f.Bin(),
		Env:      fakeEnv(t, f),
		Timeout:  mutantkit.StepTimeout,
	})
	if err != nil {
		t.Fatalf("Locate against the fake = %v, want a toolchain", err)
	}
	if want := "go1.99.0"; tc.Version.Release != want {
		t.Errorf("Release = %q, want %q", tc.Version.Release, want)
	}
	if tc.Version.GOOS != runtime.GOOS || tc.Version.GOARCH != runtime.GOARCH {
		t.Errorf("target = %s/%s, want %s/%s",
			tc.Version.GOOS, tc.Version.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if tc.GoBin != f.Bin() {
		t.Errorf("GoBin = %q, want the fake at %q", tc.GoBin, f.Bin())
	}

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("the fake was called %d times, want exactly the version probe: %+v", len(calls), calls)
	}
	if want := []string{"version"}; !slices.Equal(calls[0].Argv, want) {
		t.Errorf("argv = %q, want %q", calls[0].Argv, want)
	}
}

func TestFakeGoRoutesTheLongestPrefix(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("list").Stdout("the general listing\n")
	f.On("list", "-e").Stdout("the scope listing\n")

	general := runFake(t, f, t.TempDir(), "list", "-json=ImportPath", "./...")
	if got := string(general.Output); !strings.Contains(got, "the general listing") {
		t.Errorf("`go list -json` printed %q, want the general rule's answer", got)
	}
	scope := runFake(t, f, t.TempDir(), "list", "-e", "-f", "{{.Dir}}", "./...")
	if got := string(scope.Output); !strings.Contains(got, "the scope listing") {
		t.Errorf("`go list -e` printed %q, want the longer prefix's answer", got)
	}
}

func TestFakeGoFailsClosedNamingTheArgv(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")

	result := runFake(t, f, t.TempDir(), "env", "GOMODCACHE")
	if result.ExitCode != mutantkit.FakeGoNoRule {
		t.Errorf("an unscripted call exited %d, want %d:\n%s",
			result.ExitCode, mutantkit.FakeGoNoRule, result.Output)
	}
	out := string(result.Output)
	for _, want := range []string{"no rule for", "env GOMODCACHE"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal %q does not name %q", out, want)
		}
	}
	calls := f.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Argv, []string{"env", "GOMODCACHE"}) {
		t.Errorf("the call log holds %+v, want the refused call", calls)
	}
}

func TestFakeGoCreateOutputWritesAnExecutableAtTheDashOPath(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("test", "-c").CreateOutput()
	f.On("-test.run=^TestOnly$").Stdout("PASS\n")
	env := fakeEnv(t, f)

	out := filepath.Join(testkit.Scratch(t), "pkg.test")
	result := runFake(t, f, t.TempDir(), "test", "-c", "-o", out, "example.com/m/pkg")
	if result.ExitCode != 0 {
		t.Fatalf("the scripted compile exited %d, want 0:\n%s", result.ExitCode, result.Output)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("the scripted compile left nothing at its -o path: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("the file at the -o path is empty, so nothing could run it")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode = %v, want the executable bit: a caller that runs the binary would get EACCES",
			info.Mode().Perm())
	}

	ran := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{out, "-test.run=^TestOnly$"},
		Dir:     t.TempDir(),
		Env:     env,
		Timeout: mutantkit.StepTimeout,
	})
	if ran.Err != nil || ran.ExitCode != 0 {
		t.Fatalf("running the created binary = exit %d, %v:\n%s", ran.ExitCode, ran.Err, ran.Output)
	}
	if got := strings.TrimSpace(string(ran.Output)); got != "PASS" {
		t.Errorf("the created binary printed %q, want the rule scripted for it", got)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("the fake answered %v, want the compile and the binary it produced", calls)
	}
	if want := []string{"-test.run=^TestOnly$"}; !slices.Equal(calls[1].Argv, want) {
		t.Errorf("the produced binary received %q, want %q", calls[1].Argv, want)
	}
}

func TestFakeGoRecordsDirAndEnvNamesButNotValues(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")

	const secret = "hunter2-never-write-this-down"
	dir := t.TempDir()
	spec := runner.Spec{
		Argv:    []string{f.Bin(), "version"},
		Dir:     dir,
		Env:     append(fakeEnv(t, f), "TESTKIT_FAKE_GO_SECRET="+secret, "GOFLAGS=-mod=readonly -vet=off"),
		Timeout: mutantkit.StepTimeout,
	}
	if result := runner.Run(t.Context(), spec); result.ExitCode != 0 {
		t.Fatalf("the version probe exited %d:\n%s", result.ExitCode, result.Output)
	}

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("the fake was called %d times, want one: %+v", len(calls), calls)
	}
	call := calls[0]
	if !testkit.SamePath(call.Dir, dir) {
		t.Errorf("Dir = %q, want the directory the caller chose, %q", call.Dir, dir)
	}
	if !slices.Contains(call.EnvNames, "TESTKIT_FAKE_GO_SECRET") {
		t.Errorf("EnvNames = %q, want it to name every variable the child could see", call.EnvNames)
	}
	if got, want := call.Env["GOFLAGS"], "-mod=readonly -vet=off"; got != want {
		t.Errorf("Env[GOFLAGS] = %q, want %q: the flags a phase composed are the claim to assert", got, want)
	}
	if got, ok := call.Env["TESTKIT_FAKE_GO_SECRET"]; ok {
		t.Errorf("Env holds the value of a variable nobody promised to keep: %q", got)
	}
	if log := string(testkit.ReadFile(t, f.CallsPath())); strings.Contains(log, secret) {
		t.Errorf("the call log holds a value that was never meant to be written down:\n%s", log)
	}
}

func TestFakeGoSleepIsCutOffByTheCallersTimeout(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("version").Sleep(2 * time.Minute)

	started := time.Now()
	result := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{f.Bin(), "version"},
		Env:     fakeEnv(t, f),
		Timeout: 300 * time.Millisecond,
	})
	if !result.TimedOut {
		t.Fatalf("TimedOut = false (exit %d, err %v), want the sleeping fake to be killed",
			result.ExitCode, result.Err)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Errorf("the caller waited %s, want the deadline rather than the sleep", elapsed)
	}
}

func TestFakeGoIsSafeUnderParallelTests(t *testing.T) {
	t.Parallel()

	t.Run("one fake per test", func(t *testing.T) {
		for _, name := range []string{"alpha", "beta", "gamma"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := mutantkit.FakeGo(t)
				f.On("list").Stdout(name + "\n")
				result := runFake(t, f, t.TempDir(), "list", name)
				if got := strings.TrimSpace(string(result.Output)); got != name {
					t.Errorf("the fake printed %q, want %q: two fakes shared a rule table", got, name)
				}
				calls := f.Calls()
				if len(calls) != 1 || !slices.Equal(calls[0].Argv, []string{"list", name}) {
					t.Errorf("the call log holds %+v, want only this test's own call", calls)
				}
			})
		}
	})

	t.Run("one fake many workers", func(t *testing.T) {
		t.Parallel()
		f := mutantkit.FakeGo(t)
		f.On("test", "-c").Stdout("compiled\n")

		const workers = 8
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				runFake(t, f, t.TempDir(), "test", "-c", "-o", strconv.Itoa(i)+".test", "./pkg")
			}()
		}
		wg.Wait()

		calls := f.Calls()
		if len(calls) != workers {
			t.Fatalf("the call log holds %d calls, want %d: concurrent appends lost a line\n%+v",
				len(calls), workers, calls)
		}
		for _, call := range calls {
			if len(call.Argv) < 2 || call.Argv[0] != "test" || call.Argv[1] != "-c" {
				t.Errorf("a recorded call is %q, want a whole compile: an append was interleaved", call.Argv)
			}
		}
	})
}

func TestFakeGoInstallsOneBinaryPerTestBinary(t *testing.T) {
	t.Parallel()

	first := mutantkit.FakeGo(t)
	installed := mutantkit.FakeGoInstalls()
	if installed < 1 {
		t.Fatalf("FakeGoInstalls() = %d after a fake was built, want the install to be counted", installed)
	}
	for range 3 {
		f := mutantkit.FakeGo(t)
		f.Version("1.99.0")
		if f.Bin() != first.Bin() {
			t.Errorf("a second fake is at %s, want the one install at %s", f.Bin(), first.Bin())
		}
		if _, err := os.Stat(f.Bin()); err != nil {
			t.Fatalf("the fake `go` is not there: %v", err)
		}
	}
	if got := mutantkit.FakeGoInstalls(); got != installed {
		t.Errorf("three more fakes cost %d more installs, want none: the binary is shared",
			got-installed)
	}

	if entries := testkit.Entries(t, filepath.Dir(first.Bin())); len(entries) != 1 {
		t.Errorf("the shared directory holds %q, want exactly one scripted `go`", entries)
	}

	if within(t, filepath.Dir(first.CallsPath()), first.Bin()) {
		t.Errorf("the fake `go` at %s is inside the test's own scratch %s, which is what the keep "+
			"policy uploads", first.Bin(), filepath.Dir(first.CallsPath()))
	}
}

func within(t *testing.T, dir, path string) bool {
	t.Helper()
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestFakeGoExportLeavesTheHarnessItsOwnToolchain(t *testing.T) {
	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")
	f.Export()

	_ = testkit.Compose(t, testkit.Scratch(t))

	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("composing an environment after Export ran %v against the fake, want nothing: "+
			"the harness's own probe has to reach the machine's `go`", calls)
	}
}

func TestFakeGoLastRuleOfEqualLengthWins(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")
	f.On("version").Stdout("").Stderr("no toolchain here\n").Exit(4)

	result := runFake(t, f, t.TempDir(), "version")
	if result.ExitCode != 4 {
		t.Errorf("the probe exited %d, want the later rule's 4:\n%s", result.ExitCode, result.Output)
	}
	if got := string(result.Output); !strings.Contains(got, "no toolchain here") {
		t.Errorf("the probe printed %q, want the later rule's answer", got)
	}
}

func TestFakeGoRefusesToRunTheSuiteInPlaceOfTheGoCommand(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")

	result := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{f.Bin(), "version"},
		Env:     testkit.Compose(t, testkit.Scratch(t)),
		Timeout: mutantkit.StepTimeout,
	})
	if result.ExitCode != mutantkit.FakeGoNoRule {
		t.Fatalf("the stray fake exited %d, want %d:\n%s",
			result.ExitCode, mutantkit.FakeGoNoRule, result.Output)
	}
	out := string(result.Output)
	for _, want := range []string{"in place of the go command", mutantkit.FakeGoRulesEnv} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal %q does not mention %q", out, want)
		}
	}
	if strings.Contains(out, "PASS") || strings.Contains(out, "--- FAIL") {
		t.Errorf("the stray fake ran tests:\n%s", out)
	}
}

func TestFakeGoCreateOutputRefusesAnArgvWithNoDashOPath(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("build").CreateOutput()

	result := runFake(t, f, t.TempDir(), "build", "./...")
	if result.ExitCode != mutantkit.FakeGoNoRule {
		t.Fatalf("the scripted compile exited %d, want %d:\n%s",
			result.ExitCode, mutantkit.FakeGoNoRule, result.Output)
	}
	if got := string(result.Output); !strings.Contains(got, "-o") {
		t.Errorf("the refusal %q does not say what was missing", got)
	}
}

func TestInstallLinksOrCopiesAccordingToTheLinkerItWasGiven(t *testing.T) {
	t.Parallel()

	const body = "the program\n"
	for _, test := range []struct {
		name   string
		linker func(from, to string) error
		linked bool
	}{
		{
			name:   "a platform whose hard links can be removed again",
			linker: mutantkit.PlatformLinker("linux"),
			linked: true,
		},
		{
			name:   "a platform whose hard links cannot",
			linker: mutantkit.PlatformLinker("windows"),
			linked: false,
		},
		{
			name:   "a link the filesystem refuses",
			linker: func(string, string) error { return errors.New("invalid cross-device link") },
			linked: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			restore := mutantkit.SetFakeGoLinker(test.linker)
			defer restore()

			dir := t.TempDir()
			from := filepath.Join(dir, "source")
			if err := os.WriteFile(from, []byte(body), 0o755); err != nil {
				t.Fatalf("writing the source: %v", err)
			}
			to := filepath.Join(dir, "installed")
			if err := mutantkit.LinkOrCopyExecutable(from, to); err != nil {
				t.Fatalf("installing: %v", err)
			}

			source, err := os.Stat(from)
			if err != nil {
				t.Fatalf("stat of the source: %v", err)
			}
			installed, err := os.Stat(to)
			if err != nil {
				t.Fatalf("stat of the install: %v", err)
			}
			if got := os.SameFile(source, installed); got != test.linked {
				t.Errorf("os.SameFile = %v, want %v: a link is one file under two names and a copy "+
					"is two files, and only one of them can be removed while the other is running",
					got, test.linked)
			}
			if got := testkit.ReadFile(t, to); string(got) != body {
				t.Errorf("the install holds %q, want the source's %q", got, body)
			}
			if perm := installed.Mode().Perm(); runtime.GOOS != "windows" && perm&0o111 == 0 {
				t.Errorf("mode = %v, want the executable bit", perm)
			}
		})
	}
}

func TestFakeGoFallsBackToCopyingWhenItCannotLink(t *testing.T) {
	restore := mutantkit.SetFakeGoLinker(mutantkit.PlatformLinker("windows"))
	defer restore()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")
	copied := f.Install(filepath.Join(testkit.Scratch(t), "copied"))

	source, err := os.Stat(f.Bin())
	if err != nil {
		t.Fatalf("stat of the shared fake: %v", err)
	}
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatalf("the copy is not there: %v", err)
	}
	if info.Size() != source.Size() {
		t.Errorf("the copy is %d bytes, want the %d the original has", info.Size(), source.Size())
	}

	result := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{copied, "version"},
		Env:     fakeEnv(t, f),
		Timeout: mutantkit.StepTimeout,
	})
	if result.ExitCode != 0 {
		t.Fatalf("the copied fake exited %d, want 0:\n%s", result.ExitCode, result.Output)
	}
	if got := string(result.Output); !strings.Contains(got, "go version go1.99.0") {
		t.Errorf("the copied fake printed %q, want the scripted version line", got)
	}
}

func TestFakeGoNeverHardLinksWhereALinkCannotBeRemoved(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		goos      string
		hardLinks bool
	}{
		{goos: "windows", hardLinks: false},
		{goos: "linux", hardLinks: true},
		{goos: "darwin", hardLinks: true},
		{goos: "freebsd", hardLinks: true},
	} {
		t.Run(test.goos, func(t *testing.T) {
			t.Parallel()
			if got := mutantkit.HardLinksAreRemovable(test.goos); got != test.hardLinks {
				t.Errorf("HardLinksAreRemovable(%q) = %v, want %v", test.goos, got, test.hardLinks)
			}
		})
	}
}

func TestFakeGoRemovalRetriesLongerWhereAnExitingChildHoldsItsImage(t *testing.T) {
	t.Parallel()

	windowsAttempts, windowsDelay := mutantkit.FakeGoRemovalPolicy("windows")
	posixAttempts, posixDelay := mutantkit.FakeGoRemovalPolicy("linux")
	if windowsAttempts <= posixAttempts || windowsDelay <= posixDelay {
		t.Errorf("windows gets %d attempts %s apart and linux %d attempts %s apart, want windows to "+
			"wait longer: it is the platform where a just-exited child still holds its image",
			windowsAttempts, windowsDelay, posixAttempts, posixDelay)
	}
	if got := time.Duration(windowsAttempts) * windowsDelay; got < time.Second {
		t.Errorf("windows gives up after %s, want at least a second for a mapping to be torn down", got)
	}
}

func TestInstallReplacesADestinationRatherThanWritingThroughIt(t *testing.T) {
	const body = "the program that must survive\n"

	for _, existing := range []struct {
		name string
		make func(t *testing.T, from, to string)
	}{
		{
			name: "a hard link to the source",
			make: func(t *testing.T, from, to string) {
				t.Helper()
				if err := os.Link(from, to); err != nil {
					t.Skipf("this filesystem cannot make the destination a link to the source: %v", err)
				}
			},
		},
		{
			name: "an ordinary file of its own",
			make: func(t *testing.T, from, to string) {
				t.Helper()
				if err := os.WriteFile(to, []byte("something older\n"), 0o755); err != nil {
					t.Fatalf("writing the destination that is already there: %v", err)
				}
			},
		},
		{
			name: "nothing at all",
			make: func(*testing.T, string, string) {},
		},
	} {
		for _, platform := range []struct {
			name   string
			linker func(from, to string) error
		}{
			{name: "a platform that links", linker: mutantkit.PlatformLinker("linux")},
			{name: "a platform that copies", linker: mutantkit.PlatformLinker("windows")},
		} {
			t.Run(existing.name+", "+platform.name, func(t *testing.T) {
				restore := mutantkit.SetFakeGoLinker(platform.linker)
				defer restore()

				dir := t.TempDir()
				from := filepath.Join(dir, "source")
				if err := os.WriteFile(from, []byte(body), 0o755); err != nil {
					t.Fatalf("writing the source: %v", err)
				}
				to := filepath.Join(dir, "installed")
				existing.make(t, from, to)

				if err := mutantkit.LinkOrCopyExecutable(from, to); err != nil {
					t.Fatalf("installing over a destination that was already there: %v", err)
				}

				if got := testkit.ReadFile(t, from); string(got) != body {
					t.Fatalf("the source now holds %q, want the %q it started with: the install "+
						"wrote through a name it shared", got, body)
				}
				if got := testkit.ReadFile(t, to); string(got) != body {
					t.Errorf("the install holds %q, want the source's %q", got, body)
				}
				info, err := os.Stat(to)
				if err != nil {
					t.Fatalf("stat of the install: %v", err)
				}
				if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm&0o111 == 0 {
					t.Errorf("mode = %v, want the executable bit", perm)
				}
				if entries := testkit.Entries(t, dir); len(entries) != 2 {
					t.Errorf("the directory holds %q, want just the source and the install", entries)
				}
			})
		}
	}
}

func TestCreateOutputCanBeAskedTwiceForTheSamePath(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("test", "-c").CreateOutput()
	f.On("-test.run=^TestOnly$").Stdout("PASS\n")
	env := fakeEnv(t, f)

	out := filepath.Join(testkit.Scratch(t), "pkg.test")
	for attempt := range 2 {
		result := runFake(t, f, t.TempDir(), "test", "-c", "-o", out, "example.com/m/pkg")
		if result.ExitCode != 0 {
			t.Fatalf("compile %d exited %d, want 0:\n%s", attempt+1, result.ExitCode, result.Output)
		}
	}

	ran := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{out, "-test.run=^TestOnly$"},
		Dir:     t.TempDir(),
		Env:     env,
		Timeout: mutantkit.StepTimeout,
	})
	if ran.Err != nil || ran.ExitCode != 0 {
		t.Fatalf("running the rebuilt binary = exit %d, %v:\n%s", ran.ExitCode, ran.Err, ran.Output)
	}
	if got := strings.TrimSpace(string(ran.Output)); got != "PASS" {
		t.Errorf("the rebuilt binary printed %q, want the rule scripted for it", got)
	}
}
