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

// TestMain turns this binary into the scripted `go` when it is started as one,
// and otherwise runs the suite. Every test below starts a child that is this
// very binary, so without it they would each run the whole package again.
func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

// fakeEnv is the environment a child of these tests runs with: the harness's
// own policy, plus the two variables that make the binary answer as `go`.
func fakeEnv(t *testing.T, f *mutantkit.Fake) []string {
	t.Helper()
	return f.Env(testkit.Compose(t, testkit.Scratch(t)))
}

// runFake runs the fake once and returns what it did, under a deadline short
// enough that a hang is a failure rather than a ten-minute panic.
func runFake(t *testing.T, f *mutantkit.Fake, dir string, args ...string) runner.Result {
	t.Helper()
	spec := runner.Spec{
		Argv:    append([]string{f.Bin()}, args...),
		Dir:     dir,
		Env:     fakeEnv(t, f),
		Timeout: 30 * time.Second,
	}
	return runner.Run(t.Context(), spec)
}

// TestFakeGoAnswersTheVersionProbe is the whole point of the fake in one test:
// internal/gocmd's probe — the first command every run issues — is satisfied by
// a scripted answer, so a test about what a probe does needs no toolchain.
//
// The release is one no toolchain will ever print, so a result accidentally
// produced by the real `go` could not be mistaken for this one.
func TestFakeGoAnswersTheVersionProbe(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: f.Bin(),
		Env:      fakeEnv(t, f),
		Timeout:  30 * time.Second,
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

// TestFakeGoRoutesTheLongestPrefix is the rule that lets one fake answer a whole
// phase: the general rule stays, and the one call that has to behave differently
// gets a longer prefix of its own.
//
// Without it a test that scripted `go list` could not also script the scope
// resolution's `go list -e`, which is the pair internal/engine issues.
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

// TestFakeGoFailsClosedNamingTheArgv is what makes a scripted toolchain
// trustworthy: a command nobody scripted is refused, loudly, with the argv in
// the message.
//
// The alternative — answering an unscripted call with a silent success — is the
// failure shape a fake must never have, because the test would then be passing
// on a command the author never thought about.
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
	// Refused, and still recorded: a test that wants to know which unscripted
	// command a phase issued reads it off the log rather than off stderr.
	calls := f.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Argv, []string{"env", "GOMODCACHE"}) {
		t.Errorf("the call log holds %+v, want the refused call", calls)
	}
}

// TestFakeGoCreateOutputWritesAnExecutableAtTheDashOPath is what lets a compile
// be faked at all, and then what lets the phase *below* the compile be faked
// too.
//
// internal/execute's caller checks that `go test -c -o <path>` left a binary
// behind, so a fake that only printed would fail that check for a reason that
// has nothing to do with the test. But the scheduler then *starts* that binary,
// so a file that merely existed would leave everything below the build needing
// a real toolchain. The produced binary is the fake itself, which is why it can
// be scripted in turn — here with the shape a test binary is really started
// with.
func TestFakeGoCreateOutputWritesAnExecutableAtTheDashOPath(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("test", "-c").CreateOutput()
	f.On("-test.run=^TestOnly$").Stdout("PASS\n")
	env := fakeEnv(t, f)

	// The produced binary is the fake itself, and this test then runs it. A
	// copy of a program whose process has just exited can still be held for a
	// moment on Windows while the operating system tears its mapping down, so
	// the file goes under the harness's scratch: with the keep policy CI runs
	// with, that directory is removed with retries. Go's own t.TempDir cleanup
	// has none, and would fail this test through its cleanup after every
	// assertion in it had passed.
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

	// Started the way internal/execute starts one: in a directory of its own,
	// with the environment the phase composed. That environment carries the
	// control variables, so the produced binary is the fake again and answers
	// the same table.
	ran := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{out, "-test.run=^TestOnly$"},
		Dir:     t.TempDir(),
		Env:     env,
		Timeout: 30 * time.Second,
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

// TestFakeGoRecordsDirAndEnvNamesButNotValues is the call log's contract.
//
// A recorded call has to be enough to assert what a phase asked for — the argv,
// where it ran, which variables it composed — and it must never become a place a
// developer's own secrets are written down. So the names of every variable are
// kept and the values of five are: the ones production is supposed to set, and
// which a test therefore has a claim to make about.
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
		Timeout: 30 * time.Second,
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

// TestFakeGoSleepIsCutOffByTheCallersTimeout is the hang, which is otherwise a
// toolchain nobody can install: a `go` that never answers.
//
// The assertion is on the caller's verdict rather than on the clock — the fake
// sleeps far longer than the deadline, so a run that came back in time can only
// have been killed.
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

// TestFakeGoIsSafeUnderParallelTests pins the two ways the fake is used at once:
// one per parallel test, and one called by several workers of a single phase.
//
// Both are the ordinary case rather than an exotic one — internal/execute
// compiles with as many workers as the run was given jobs, and every suite here
// runs parallel tests — so a log that lost or interleaved a line would show up
// as a flake in somebody else's test rather than as a failure here.
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

// TestFakeGoInstallsOneBinaryPerTestBinary is the resource story, and it is a
// resource story rather than a tidiness one.
//
// The fake is a link to this test binary, six megabytes of it. Installed per
// [mutantkit.Fake] there would be twenty-eight of them in one unit-tier run,
// each in a per-test scratch directory — which is a hundred and seventy
// megabytes of copying on a Windows runner, where the temporary directory and
// the build cache sit on different volumes and os.Link cannot answer, and a
// six-megabyte binary in the evidence CI uploads for every fake test that
// failed. Installed once per process it is one link, in a directory of the
// process's own that [mutantkit.Main] removes after the suite, and what lands
// in a test's scratch is two small text files.
//
// The count is of the whole process, so it does not matter which tests ran
// before this one: every [mutantkit.FakeGo] in this binary shares the one
// install, and an explicit [mutantkit.Fake.Install] — which the tests here do
// not use — would be the only thing that could add to it.
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

	// The count above is of this whole process, so it says nothing on its own
	// about how many binaries exist. This does: the shared directory holds the
	// one program and nothing else, whatever else the suite has been doing.
	if entries := testkit.Entries(t, filepath.Dir(first.Bin())); len(entries) != 1 {
		t.Errorf("the shared directory holds %q, want exactly one scripted `go`", entries)
	}

	// And what a test's own scratch holds is the protocol rather than the
	// program, because that scratch is what the keep policy uploads.
	if within(t, filepath.Dir(first.CallsPath()), first.Bin()) {
		t.Errorf("the fake `go` at %s is inside the test's own scratch %s, which is what the keep "+
			"policy uploads", first.Bin(), filepath.Dir(first.CallsPath()))
	}
}

// within reports whether path is under dir.
func within(t *testing.T, dir, path string) bool {
	t.Helper()
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// TestFakeGoExportLeavesTheHarnessItsOwnToolchain is the flake this design
// makes possible, pinned before it can happen.
//
// [mutantkit.Fake.Export] puts the fake in front of PATH, and PATH is a
// process-wide global: every `go` this process starts afterwards is the fake,
// the harness's own included. testkit's environment policy probes `go env GOENV
// GOPATH GOMODCACHE` once per process to pin the go command's directories
// against a moved HOME, and that probe is lazy — so whether a test that
// exported the fake and then composed an environment sends the harness's probe
// to the fake depends on which tests ran before it.
//
// Two things go wrong when it does, and neither looks like a bug. The fake
// records a call nobody asked for, which is aimed straight at the exact-count
// assertions in internal/engine and internal/cli; and the policy falls back to
// build.Default, so the child gets a GOMODCACHE that is not the machine's.
func TestFakeGoExportLeavesTheHarnessItsOwnToolchain(t *testing.T) {
	// No t.Parallel: Export publishes into this process with t.Setenv.
	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")
	f.Export()

	// The harness composing an environment, which is what a test does next.
	_ = testkit.Compose(t, testkit.Scratch(t))

	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("composing an environment after Export ran %v against the fake, want nothing: "+
			"the harness's own probe has to reach the machine's `go`", calls)
	}
}

// TestFakeGoLastRuleOfEqualLengthWins pins the tie-break, which is the half of
// the matching rule a longest-prefix test cannot see.
//
// Two rules of the same length is how a convenience is overridden — `Version`
// lays one down and a test that wants the probe to fail writes another — and a
// first-wins tie-break would make that silently do nothing.
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

// TestFakeGoRefusesToRunTheSuiteInPlaceOfTheGoCommand is the guard against the
// one mistake this design makes possible.
//
// The fake is a link to the test binary, so a call site that dropped the
// control variables from the environment it composed would start the whole
// suite as a child of itself, once per `go` command, and the only symptom would
// be a run that took a very long time.
func TestFakeGoRefusesToRunTheSuiteInPlaceOfTheGoCommand(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")

	// The harness's environment without the two variables that make the binary
	// answer as `go`, which is what a policy that stripped them would leave.
	result := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{f.Bin(), "version"},
		Env:     testkit.Compose(t, testkit.Scratch(t)),
		Timeout: 30 * time.Second,
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

// TestFakeGoCreateOutputRefusesAnArgvWithNoDashOPath is the other half of
// failing closed: a rule that promises a binary and an argv that names nowhere
// to put it is a script with a mistake in it, not a compile.
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

// TestInstallLinksOrCopiesAccordingToTheLinkerItWasGiven pins all three answers
// a linker can give, against a source this test owns.
//
// The source is a file in the destination's own directory rather than the test
// binary, so "a link was made" is a claim about the code and not about whether
// `go test` put this binary on the same volume as the temporary directory —
// which on a Windows runner it does not.
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
			// The reason this package copies on Windows: see
			// [mutantkit.HardLinksAreRemovable].
			name:   "a platform whose hard links cannot",
			linker: mutantkit.PlatformLinker("windows"),
			linked: false,
		},
		{
			// The reason the fallback existed before Windows needed it: a
			// GitHub runner puts RUNNER_TEMP and the build cache on different
			// volumes, and os.Link cannot answer across them.
			name:   "a link the filesystem refuses",
			linker: func(string, string) error { return errors.New("invalid cross-device link") },
			linked: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			// No t.Parallel: the linker is replaced for the whole process.
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

// TestFakeGoFallsBackToCopyingWhenItCannotLink is the whole install through the
// copy branch, which is what a Windows runner takes every time.
//
// The table above says the copy is a copy; this says it is still a program. A
// fallback that produced a file of the right length and no entry point would
// pass everything else here and fail on the first machine that used it.
func TestFakeGoFallsBackToCopyingWhenItCannotLink(t *testing.T) {
	// No t.Parallel: the linker is replaced for the whole process.
	restore := mutantkit.SetFakeGoLinker(mutantkit.PlatformLinker("windows"))
	defer restore()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")
	// Under the harness's scratch rather than t.TempDir: on Windows a copy a
	// child has just run can still be held for a moment, and Go's own TempDir
	// cleanup does not retry.
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

	// And it is a working executable rather than a file of the right length:
	// the copy answers the same table.
	result := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{copied, "version"},
		Env:     fakeEnv(t, f),
		Timeout: 30 * time.Second,
	})
	if result.ExitCode != 0 {
		t.Fatalf("the copied fake exited %d, want 0:\n%s", result.ExitCode, result.Output)
	}
	if got := string(result.Output); !strings.Contains(got, "go version go1.99.0") {
		t.Errorf("the copied fake printed %q, want the scripted version line", got)
	}
}

// TestFakeGoNeverHardLinksWhereALinkCannotBeRemoved is the Windows rule, and it
// is a rule about *removal* rather than about linking.
//
// A hard link to a running executable is a second name for an image the
// operating system has mapped, and Windows refuses to unlink a mapped image:
// `Access is denied`. Both of the things this package installs are links to the
// test binary that is running — the shared `go`, which [Main] removes while the
// process is still alive, and every `-o` output a scripted compile creates — so
// on Windows both were undeletable, and they failed the test that made them
// through t.TempDir's cleanup rather than through any assertion. A copy has no
// such problem: it is a different file, and nothing has it mapped once the child
// that ran it has exited.
//
// The choice is expressed as "is there a linker at all", so the fallback that
// already existed for a cross-volume install is the only code path either
// platform takes.
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

// TestFakeGoRemovalRetriesLongerWhereAnExitingChildHoldsItsImage is the other
// half of the same Windows fact.
//
// A copy is deletable, but not necessarily at the instant the child that ran it
// returned: the process has exited from the caller's point of view while the
// operating system is still tearing its mapping down, and an unlink in that
// window is refused. The removal therefore retries, and it waits longer on the
// platform where the window exists at all.
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

// TestInstallReplacesADestinationRatherThanWritingThroughIt is the one way this
// package could have destroyed the binary it is a copy of.
//
// Both installs reuse a destination: the shared `go` when a second process
// arrives at the same path, and every scripted compile whose `-o` names a file
// that is already there. A hard link is a second *name* for one file, so on a
// platform that links, opening the destination for writing opens the source —
// and the old code opened it `O_TRUNC`. Linking a fresh install over an
// existing one fails with EEXIST, the copy fallback ran, and it truncated the
// running test binary to nothing before reading it.
//
// So the destination is removed and then replaced, never written through: the
// link path unlinks a name and makes a new one, and the copy path stages a
// sibling and renames it over. The rows below are the two shapes a destination
// comes in, against both a linking platform and a copying one, and the
// assertion in each is that the *source* still holds what it held.
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
				// No t.Parallel: the linker is replaced for the whole process.
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

				// The whole point: whatever happened to the destination, the
				// source is untouched.
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
				// And nothing was left staged beside it.
				if entries := testkit.Entries(t, dir); len(entries) != 2 {
					t.Errorf("the directory holds %q, want just the source and the install", entries)
				}
			})
		}
	}
}

// TestCreateOutputCanBeAskedTwiceForTheSamePath is the reuse the helper above
// is about, through the caller that really does it.
//
// A scheduler compiles a package's test binary into a path derived from its
// import path, so two compiles in one run name the same file — and on a
// platform that links, the file already there is the fake itself under a second
// name. This is the shape that truncated the running test binary, and it is
// worth having end to end rather than only against a source the test wrote.
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

	// The fake is still a fake: nothing wrote through a name it shared with the
	// binary this test is running in.
	ran := runner.Run(t.Context(), runner.Spec{
		Argv:    []string{out, "-test.run=^TestOnly$"},
		Dir:     t.TempDir(),
		Env:     env,
		Timeout: 30 * time.Second,
	})
	if ran.Err != nil || ran.ExitCode != 0 {
		t.Fatalf("running the rebuilt binary = exit %d, %v:\n%s", ran.ExitCode, ran.Err, ran.Output)
	}
	if got := strings.TrimSpace(string(ran.Output)); got != "PASS" {
		t.Errorf("the rebuilt binary printed %q, want the rule scripted for it", got)
	}
}
