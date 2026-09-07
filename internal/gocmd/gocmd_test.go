// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The unit tier of the toolchain wrapper: every way `go` can misbehave, driven
// by a scripted stand-in rather than by a real toolchain.
//
// A probe that hangs, one that answers garbage, one that exits non-zero, a
// listing that fails — those are the failures this package exists to report, and
// none of them can be installed. They used to be tested by compiling a small
// program per case with a real `go build`, which cost a toolchain and about a
// second and a half of the unit tier; the scripted stand-in
// internal/testkit/mutantkit hands out answers them from a table instead.
//
// What is left needing a real toolchain — that the probe agrees with the `go`
// this machine actually has — is in toolchain_integration_test.go.

package gocmd_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// TestMain turns this binary into the scripted `go` when it is started as one.
// Every fake-driven test below runs this very binary as its toolchain, so
// without the dispatch each of them would run the whole package again.
func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

// probeTimeout bounds every scripted probe here.
//
// Two seconds rather than [gocmd.DefaultProbeTimeout]'s thirty, and the
// difference is not impatience: the hanging probe below asserts that it came
// back long before its own budget expired, and a budget equal to the default
// would make that assertion true of a probe that had waited out the whole
// thirty. The fake answers from a table, so anything approaching two seconds is
// a fake that did not start rather than one that was slow.
const probeTimeout = 2 * time.Second

// hangTimeout is what the hanging probe is given.
//
// It is a deadline this test pays in full every run, so it is as short as the
// claim allows. Two hundred milliseconds is far longer than the fake takes to
// start and far shorter than [gocmd.DefaultProbeTimeout]; a machine so loaded
// that the child has not started yet still produces the same verdict, because a
// probe whose context expired before its process began is a probe that did not
// answer either.
const hangTimeout = 200 * time.Millisecond

// hangSleep is how long the scripted probe sleeps for the hanging case, and it
// is bounded at both ends rather than simply being large.
//
// The lower bound is the assertion the hanging test makes: a probe still
// running at five times its deadline has already failed that test, so anything
// past that adds nothing to the claim. The upper bound is
// [gocmd.DefaultProbeTimeout], and that one was learned from the mutation gate.
// A sleep longer than the default turns the mutant that widens the deadline —
// negating `timeout <= 0`, so that a configured 200ms becomes the 30-second
// default — into a mutant nothing can catch but the per-mutant timeout, waited
// out twice, where a sleep shorter than the default makes it a test failure in
// three seconds: the probe comes back with a parse error instead of the
// deadline this test is about.
const hangSleep = 3 * time.Second

// fakeEnv is the environment a scripted toolchain runs with: the harness's
// hermetic policy plus the two variables that make the child answer as `go`.
func fakeEnv(t *testing.T, f *mutantkit.Fake) []string {
	t.Helper()
	return f.Env(testkit.Compose(t, testkit.Scratch(t)))
}

// TestLocateReportsAProbeThatExitsNonZero is the error a fresh machine hits
// second: something is at the configured path and it is not a Go toolchain.
//
// What it printed is the whole diagnosis, so the error keeps it — and keeps the
// command, because "`/opt/go/bin/go version` exited 3" is only actionable if the
// reader can run it themselves. The recording keeps both as well, and the error
// points at the event, which is what turns a one-line failure into the whole
// command's preserved output.
func TestLocateReportsAProbeThatExitsNonZero(t *testing.T) {
	t.Parallel()

	const garbage = "this executable is not a go toolchain"
	f := mutantkit.FakeGo(t)
	f.On("version").Stderr(garbage + "\n").Exit(3)

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{Kind: trace.StartKindRun, ToolVersion: "test"})

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: f.Bin(),
		Env:      fakeEnv(t, f),
		Timeout:  probeTimeout,
		Trace:    recorder,
	})
	if err == nil {
		t.Fatalf("LocateContext(%q) = %+v, want an error", f.Bin(), tc)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeVersionProbeFailed {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionProbeFailed)
	}

	var failure *gocmd.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a *gocmd.Error", err)
	}
	invocation := failure.Command()
	if invocation == nil {
		t.Fatal("Command() = nil, want the probe that failed")
	}
	if argv := []string{f.Bin(), "version"}; !slices.Equal(invocation.Argv, argv) {
		t.Errorf("Argv = %q, want %q", invocation.Argv, argv)
	}
	if invocation.Kind != trace.ExecKindGoVersion {
		t.Errorf("Kind = %q, want %q", invocation.Kind, trace.ExecKindGoVersion)
	}
	events := execEvents(sink)
	if len(events) != 1 {
		t.Fatalf("the recording holds %d exec events, want exactly one for the version probe", len(events))
	}
	if invocation.TraceSeq != events[0].Seq {
		t.Errorf("TraceSeq = %d, want the recorded sequence %d", invocation.TraceSeq, events[0].Seq)
	}
	if rec := events[0].Exec; rec.ExitCode != 3 {
		t.Errorf("exit_code = %d, want 3", rec.ExitCode)
	}

	if !strings.Contains(failure.RetainedOutput(), garbage) {
		t.Errorf("RetainedOutput() = %q, want it to hold what the stand-in printed, %q",
			failure.RetainedOutput(), garbage)
	}
	// The message a user reads has not changed.
	if want := "exited with status 3"; !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to contain %q", err, want)
	}
}

// TestLocateReportsAProbeThatHangs is the failure that has no other way to be
// written: a `go` that never answers.
//
// It is the worst kind of hang to debug — a run that stops before it has started
// — and the reason [gocmd.DefaultProbeTimeout] exists at all. There is no way to
// install a toolchain that behaves this way, so before the scripted stand-in
// this branch of [gocmd.LocateContext] had no test of any kind.
//
// The assertion is on the verdict rather than on the clock: the fake sleeps far
// longer than the deadline it is given — fifteen times it, and three times the
// bound asserted below — so a call that returned inside that bound can only
// have killed it. See [hangSleep] for why "far longer" stops well short of
// forever.
func TestLocateReportsAProbeThatHangs(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("version").Sleep(hangSleep)

	started := time.Now()
	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: f.Bin(),
		Env:      fakeEnv(t, f),
		Timeout:  hangTimeout,
	})
	if err == nil {
		t.Fatalf("LocateContext against a toolchain that never answers = %+v, want an error", tc)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeVersionProbeFailed {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionProbeFailed)
	}
	if want := "did not answer within " + hangTimeout.String(); !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to say %q: a hang is not an exit status and must not read as one",
			err, want)
	}
	// Bounded by a small multiple of the deadline the probe was given rather
	// than by the budget it did not use: the claim is that *this* deadline
	// ended it, and a bound of two seconds would be satisfied by a probe that
	// had ignored a two-hundred-millisecond one.
	if elapsed, bound := time.Since(started), 5*hangTimeout; elapsed > bound {
		t.Errorf("the probe took %s, want it ended by its own %s deadline (allowing %s for a "+
			"loaded machine) rather than by the sleep", elapsed, hangTimeout, bound)
	}
	// The command is attached to a timeout as much as to a failure: it is the
	// one a reader has to run by hand to see the hang for themselves.
	var failure *gocmd.Error
	if !errors.As(err, &failure) || failure.Command() == nil {
		t.Fatalf("err = %v, want a *gocmd.Error naming the probe that hung", err)
	}
	if argv := []string{f.Bin(), "version"}; !slices.Equal(failure.Command().Argv, argv) {
		t.Errorf("Argv = %q, want %q", failure.Command().Argv, argv)
	}
}

// TestLocateReportsAProbeThatCannotBeStarted is the third way the probe fails,
// and the one that is not about the toolchain answering badly: it never became
// a process at all.
//
// The stand-in here is not the scripted `go` but a file that is executable and
// is not a program, which is a shape a PATH really does hold — a text file
// somebody chmod'd, an archive extracted for the wrong platform. [exec.LookPath]
// accepts it, because the executable bit is all it can check, and the failure
// arrives from the operating system at Start.
//
// What the error has to carry is therefore the same as for the other two: the
// code, the command a reader can run by hand, and the operating system's own
// cause underneath, because "could not run `/opt/go/bin/go version`" without
// "exec format error" under it names the symptom and hides the diagnosis.
func TestLocateReportsAProbeThatCannotBeStarted(t *testing.T) {
	t.Parallel()

	name := "not-a-program"
	if runtime.GOOS == "windows" {
		// LookPath resolves by extension there, so the stand-in needs one it
		// recognises before the operating system can refuse to start it.
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("this file is executable and is not a program\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in: %v", err)
	}

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: path,
		Timeout:  probeTimeout,
	})
	if err == nil {
		t.Fatalf("LocateContext(%q) = %+v, want an error: that file is not a program", path, tc)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeVersionProbeFailed {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q: the path exists, so this is a probe that "+
			"failed rather than a toolchain that was not found", code, err, gocmd.CodeVersionProbeFailed)
	}
	if want := "could not run `" + path + " version`"; !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to say %q", err, want)
	}
	// The process layer's own verdict has to survive being wrapped: a start
	// failure is go-mutants failing to do its job, and a caller that has to
	// tell that apart from a toolchain answering badly reads it from here.
	if code := runner.CodeOf(err); code != runner.CodeProcessStartFailed {
		t.Errorf("runner.CodeOf(err) = %q (err %v), want %q", code, err, runner.CodeProcessStartFailed)
	}
	var failure *gocmd.Error
	if !errors.As(err, &failure) || failure.Command() == nil {
		t.Fatalf("err = %v, want a *gocmd.Error naming the probe that could not start", err)
	}
	if argv := []string{path, "version"}; !slices.Equal(failure.Command().Argv, argv) {
		t.Errorf("Argv = %q, want %q", failure.Command().Argv, argv)
	}
}

// TestLocateBoundsTheProbeWithTheDeadlineItWasGiven is the other half of
// [gocmd.DefaultProbeTimeout]: not that a hang is caught, but that the number
// caught it with is the one the caller asked for.
//
// The two rows are the whole of [gocmd.Options.Timeout]'s contract, and the
// zero row is the one that cannot be seen from the outside. A probe given no
// deadline and a probe given the default behave identically against a toolchain
// that answers — the difference only shows against one that does not, which is
// half a minute of waiting to assert. The recording is where it shows for
// nothing: internal/runner writes the deadline it was handed into the exec
// event, so what the option resolved to is a fact the trace already holds.
func TestLocateBoundsTheProbeWithTheDeadlineItWasGiven(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"zero asks for the default", 0, gocmd.DefaultProbeTimeout},
		{"a configured deadline is used as it stands", probeTimeout, probeTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			f := mutantkit.FakeGo(t)
			f.Version("1.99.0")

			sink := trace.NewMemorySink(0)
			recorder := trace.New(sink, time.Now, trace.StartRecord{Kind: trace.StartKindRun, ToolVersion: "test"})

			if _, err := gocmd.LocateContext(t.Context(), gocmd.Options{
				Explicit: f.Bin(),
				Env:      fakeEnv(t, f),
				Timeout:  c.timeout,
				Trace:    recorder,
			}); err != nil {
				t.Fatalf("LocateContext = %v, want the scripted toolchain", err)
			}

			events := execEvents(sink)
			if len(events) != 1 {
				t.Fatalf("the recording holds %d exec events, want exactly one for the version probe", len(events))
			}
			if got := events[0].Exec.TimeoutMS; got != c.want.Milliseconds() {
				t.Errorf("timeout_ms = %d, want %d: Options.Timeout = %s must resolve to %s",
					got, c.want.Milliseconds(), c.timeout, c.want)
			}
		})
	}
}

// TestLocateQuotesTheConfiguredPathWithoutEscapingIt is why this package has
// two renderers for a string instead of one.
//
// What another program printed is escaped, because the point there is to show
// exactly which bytes came back. A path is not: the reader's next move is to
// paste it back into the configuration file it came from or into a shell, and a
// Windows path rendered with doubled backslashes is wrong for both. The
// distinction is invisible on a message whose path holds nothing to escape, so
// this one holds backslashes on every platform — separators on Windows, and an
// ordinary, legal character in a POSIX filename everywhere else.
func TestLocateQuotesTheConfiguredPathWithoutEscapingIt(t *testing.T) {
	t.Parallel()

	name := "not-a-go-toolchain"
	if runtime.GOOS != "windows" {
		name = `not\a\go\toolchain`
	}
	missing := filepath.Join(t.TempDir(), name)

	tc, err := gocmd.Locate(gocmd.Options{Explicit: missing})
	if err == nil {
		t.Fatalf("Locate(%q) = %+v, want an error", missing, tc)
	}
	quoted := `"` + missing + `"`
	if strconv.Quote(missing) == quoted {
		t.Fatalf("the path %q holds nothing to escape, so this test cannot tell the two renderings "+
			"apart and would pass on either", missing)
	}
	// The lookup failure underneath renders the very same path with %q, and the
	// rendered error carries both — so this assertion is on which of the two
	// this package chose rather than on the path merely appearing somewhere.
	if !strings.Contains(err.Error(), quoted) {
		t.Errorf("Error() = %q, want it to name the configured path as %s, unescaped", err, quoted)
	}
}

// TestAbsoluteReportsAWorkingDirectoryThatIsGone covers the failure
// [gocmd.Toolchain.GoBin]'s absolutising has left, and the reason it is
// reported rather than papered over: handing the relative path back would
// return exactly the GoBin that absolutising exists to rule out, and it would
// be looked for inside the snapshot every later phase runs in.
//
// It is driven through the unexported function because [gocmd.Locate] cannot be
// steered here from the outside. filepath.Abs consults the working directory
// only for a relative path, and a relative explicit path reaches this code only
// after exec.LookPath has resolved it — which needs the very directory that
// would have to be gone.
func TestAbsoluteReportsAWorkingDirectoryThatIsGone(t *testing.T) {
	// No t.Parallel: t.Chdir is process-wide, and this test takes the working
	// directory away for the length of it.
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove a directory that is a process's working directory")
	}

	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o750); err != nil {
		t.Fatalf("creating the directory to stand in: %v", err)
	}
	t.Chdir(gone)
	// Unlinked, so getcwd(2) has no name to answer with. t.Chdir restores the
	// old directory through the descriptor it kept, which needs no name.
	if err := os.Remove(gone); err != nil {
		t.Fatalf("removing the working directory: %v", err)
	}

	path, err := gocmd.Absolute("go")
	if err == nil {
		t.Fatalf("Absolute(\"go\") = %q, want an error: there is no directory to resolve it against", path)
	}
	if path != "" {
		t.Errorf("Absolute returned %q beside its error, want the empty string: a relative path here "+
			"is the GoBin this function exists to rule out", path)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeToolchainNotFound {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeToolchainNotFound)
	}
	if want := `"go"`; !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to name the path it could not resolve, %s", err, want)
	}
}

// TestToolchainStringNamesThePathAndTheVersion pins the one rendering every log
// line and diagnostic in this repository quotes a toolchain with. Both halves
// have to be in it: the version alone does not say which of two installations
// answered, and the path alone does not say what it is.
func TestToolchainStringNamesThePathAndTheVersion(t *testing.T) {
	t.Parallel()

	tc := gocmd.Toolchain{
		GoBin: "/opt/go/bin/go",
		Version: gocmd.Version{
			Raw:     "go version go1.99.0 linux/amd64",
			Release: "go1.99.0",
			GOOS:    "linux",
			GOARCH:  "amd64",
		},
	}
	if got, want := tc.String(), "/opt/go/bin/go (go version go1.99.0 linux/amd64)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestLocateRejectsGarbageVersionOutput is why locating probes rather than
// stats. Something on PATH called `go` that exits zero and answers with anything
// else must be rejected here, not halfway through building test binaries.
//
// It is kept separate from a failed probe because the remedy differs: this one
// is either not the Go toolchain at all or a format change worth a bug report,
// and the message therefore quotes what was printed.
func TestLocateRejectsGarbageVersionOutput(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		printed string
		says    string
	}{
		{
			name:    "another program's banner",
			printed: "gnu make 4.4.1\n",
			says:    "does not begin with a release",
		},
		{
			name:    "a version line with no target",
			printed: "go version go1.99.0 something\n",
			says:    `does not end in a "os/arch" target`,
		},
		{
			name:    "nothing at all",
			printed: "",
			says:    "printed nothing",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			f := mutantkit.FakeGo(t)
			f.On("version").Stdout(test.printed)

			tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
				Explicit: f.Bin(),
				Env:      fakeEnv(t, f),
				Timeout:  probeTimeout,
			})
			if err == nil {
				t.Fatalf("LocateContext against a toolchain that printed %q = %+v, want an error",
					test.printed, tc)
			}
			if code := gocmd.CodeOf(err); code != gocmd.CodeVersionUnparsable {
				t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionUnparsable)
			}
			if !strings.Contains(err.Error(), test.says) {
				t.Errorf("Error() = %q, want it to say %q", err, test.says)
			}
			// An unreadable version line is a probe that failed too, however
			// successfully the process exited: the reader needs the same
			// command and the same bytes to see why.
			var failure *gocmd.Error
			if !errors.As(err, &failure) || failure.Command() == nil {
				t.Fatalf("err = %v, want a *gocmd.Error naming the probe", err)
			}
			if got := failure.RetainedOutput(); got != test.printed {
				t.Errorf("RetainedOutput() = %q, want exactly what the stand-in printed, %q",
					got, test.printed)
			}
		})
	}
}

// TestListFailureCarriesTheToolchainOutput is the other half of what this
// package hands out: a [gocmd.Toolchain.Command] fragment that a phase fills in
// and runs, and whose failure has to arrive with the toolchain's own words.
//
// The version probe is the one command this package issues itself, so its own
// error keeps the invocation and the output. Every other `go` command — the
// listing here, a `go test -c`, a `go build` — is issued by the phase that needs
// it, and what makes those diagnosable is that the pairing survives the process
// boundary: the exit status, the bytes the go command wrote on *stderr*, and the
// argv the phase composed, all reachable from one result.
func TestListFailureCarriesTheToolchainOutput(t *testing.T) {
	t.Parallel()

	const refusal = "go: cannot find module providing package ./nope: directory not found"
	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")
	f.On("list").Stderr(refusal + "\n").Exit(1)

	env := fakeEnv(t, f)
	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: f.Bin(),
		Env:      env,
		Timeout:  probeTimeout,
	})
	if err != nil {
		t.Fatalf("LocateContext = %v, want the scripted toolchain", err)
	}

	dir := t.TempDir()
	spec := tc.Command("list", "-e", "-f", "{{.Dir}}", "./nope/...")
	spec.Dir = dir
	spec.Env = env
	spec.Timeout = probeTimeout
	spec.Kind = trace.ExecKindGoList

	result := runner.Run(t.Context(), spec)
	if result.Err != nil {
		t.Fatalf("running the scripted listing: %v", result.Err)
	}
	if result.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1:\n%s", result.ExitCode, result.Output)
	}
	if !strings.Contains(string(result.Output), refusal) {
		t.Errorf("Output = %q, want the toolchain's own refusal %q", result.Output, refusal)
	}
	invocation := runner.CommandOf(spec, result)
	want := []string{tc.GoBin, "list", "-e", "-f", "{{.Dir}}", "./nope/..."}
	if !slices.Equal(invocation.Argv, want) {
		t.Errorf("Argv = %q, want %q", invocation.Argv, want)
	}
	if invocation.Dir != dir {
		t.Errorf("Dir = %q, want the directory the phase chose, %q", invocation.Dir, dir)
	}

	// And the same command as the toolchain saw it, which is the assertion no
	// injected runner can make: these are the arguments a real process received.
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("the toolchain was called %d times, want the probe and the listing: %+v", len(calls), calls)
	}
	if listed := []string{"list", "-e", "-f", "{{.Dir}}", "./nope/..."}; !slices.Equal(calls[1].Argv, listed) {
		t.Errorf("the toolchain received %q, want %q", calls[1].Argv, listed)
	}
	if !testkit.SamePath(calls[1].Dir, dir) {
		t.Errorf("the listing ran in %q, want %q", calls[1].Dir, dir)
	}
}

// TestLocateAbsolutisesARelativeExplicitPath is why [gocmd.Toolchain.GoBin] is
// a resolved path rather than the configured one.
//
// exec.LookPath hands a relative input straight back — `tools/go` resolves to
// `tools/go` — and os/exec resolves a relative argv[0] against Cmd.Dir. Every
// phase after locating sets Dir to a directory inside the snapshot, so a
// relative GoBin would be looked for inside the tree under test: absent there,
// or silently some other binary.
//
// The assertion is therefore not only that the path looks absolute but that the
// located toolchain still runs when the command is issued from somewhere else
// entirely, which is exactly what used to fail.
func TestLocateAbsolutisesARelativeExplicitPath(t *testing.T) {
	// No t.Parallel: t.Chdir is what gives a relative path a meaning, and the
	// two are mutually exclusive.
	//
	// The workspace is the harness's scratch rather than t.TempDir, because the
	// stand-in installed below is a copy of the running test binary on Windows
	// and this test starts it twice. The operating system can hold a copy for a
	// moment after its process exits, and the harness's scratch — under the
	// keep policy CI runs with — is removed with retries; Go's own t.TempDir
	// cleanup has none.
	workspace := testkit.Scratch(t)
	f := mutantkit.FakeGo(t)
	// A version line no released toolchain will ever print, so that a result
	// accidentally produced by the real `go` could not be mistaken for this one.
	const release = "1.99.0"
	want := "go version go" + release + " " + runtime.GOOS + "/" + runtime.GOARCH
	f.Version(release)
	f.Install(filepath.Join(workspace, "tools"))

	env := fakeEnv(t, f)
	t.Chdir(workspace)

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: filepath.Join("tools", "go"),
		Env:      env,
		Timeout:  probeTimeout,
	})
	if err != nil {
		t.Fatalf("Locate with a relative explicit path = %v, want a toolchain", err)
	}
	if !filepath.IsAbs(tc.GoBin) {
		t.Errorf("GoBin = %q, want an absolute path: a relative one is re-resolved against every Spec.Dir", tc.GoBin)
	}
	if tc.Version.Raw != want {
		t.Fatalf("Version.Raw = %q, want %q: something other than the stand-in answered", tc.Version.Raw, want)
	}

	// The failure this guards against: the same toolchain, invoked from
	// anywhere but the directory it was located in.
	elsewhere := t.TempDir()
	spec := tc.Command("version")
	spec.Dir = elsewhere
	spec.Env = env
	spec.Timeout = probeTimeout
	result := runner.Run(t.Context(), spec)
	if result.Err != nil {
		t.Fatalf("running the located toolchain with Dir = %q: %v", elsewhere, result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; output: %s", result.ExitCode, result.Output)
	}
	if got := strings.TrimSpace(string(result.Output)); got != want {
		t.Errorf("the toolchain run with Dir = %q printed %q, want %q", elsewhere, got, want)
	}
}

// TestLocateWithoutAToolchainOnPath is the error every fresh machine hits
// first. It has to name a code, and it has to say what to do — an exec failure
// repeated once per package is what this replaces.
func TestLocateWithoutAToolchainOnPath(t *testing.T) {
	// No t.Parallel: t.Setenv is how PATH is emptied.
	t.Setenv("PATH", "")

	tc, err := gocmd.Locate(gocmd.Options{})
	if err == nil {
		t.Fatalf("Locate with an empty PATH = %+v, want an error", tc)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeToolchainNotFound {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeToolchainNotFound)
	}

	message := err.Error()
	for _, want := range []string{gocmd.CodeToolchainNotFound, "PATH", "mise"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not mention %q", message, want)
		}
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want the lookup failure to survive unwrapping", err)
	}
}

// TestLocateWithAMissingExplicitPath covers the other way to fail to find a
// toolchain: one was configured and it is not there.
func TestLocateWithAMissingExplicitPath(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "not-a-go-toolchain")
	tc, err := gocmd.Locate(gocmd.Options{Explicit: missing})
	if err == nil {
		t.Fatalf("Locate(%q) = %+v, want an error", missing, tc)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeToolchainNotFound {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeToolchainNotFound)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the configured path %q", err, missing)
	}
}

// TestLocateIsCancellable pins that a probe respects the caller's context, so
// a Ctrl-C during start-up does not have to wait out the probe timeout.
//
// The stand-in is named explicitly rather than looked up on PATH, because
// [gocmd.LocateContext] resolves the executable before it consults the context:
// on a machine with no `go` the failure would otherwise be the lookup's rather
// than the cancellation's, and the test would pass for the wrong reason.
func TestLocateIsCancellable(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tc, err := gocmd.LocateContext(ctx, gocmd.Options{
		Explicit: f.Bin(),
		Env:      fakeEnv(t, f),
		Timeout:  probeTimeout,
	})
	if err == nil {
		t.Fatalf("LocateContext with a cancelled context = %+v, want an error", tc)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeVersionProbeFailed {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionProbeFailed)
	}
	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("the cancelled probe still ran %+v, want nothing started", calls)
	}
}

// TestCommandIsAFragment pins what Command does and, just as importantly, what
// it does not: it names the toolchain and passes the arguments through, and it
// leaves every execution decision to the caller.
func TestCommandIsAFragment(t *testing.T) {
	t.Parallel()

	tc := gocmd.Toolchain{GoBin: filepath.Join("opt", "go", "bin", "go")}
	args := []string{"test", "-c", "-o", "out.test", "./..."}
	spec := tc.Command(args...)

	want := append([]string{tc.GoBin}, args...)
	if len(spec.Argv) != len(want) {
		t.Fatalf("Argv = %q, want %q", spec.Argv, want)
	}
	for i := range want {
		if spec.Argv[i] != want[i] {
			t.Fatalf("Argv = %q, want %q", spec.Argv, want)
		}
	}

	if spec.Dir != "" || spec.Env != nil || spec.Timeout != 0 || spec.OutputLimit != 0 {
		t.Errorf("Command filled in %+v; everything but Argv belongs to the caller", spec)
	}
	// The recording fields are the caller's too, and for the same reason: this
	// package cannot know whether the command it is describing will be a
	// version probe, a compile, or a coverage pass.
	if spec.Trace != nil || spec.Kind != "" || spec.Subject != "" {
		t.Errorf("Command labelled the spec %+v; the label belongs to the phase that issues the command", spec)
	}

	// The caller's slice must not be reachable through the spec: a phase that
	// reuses an argument buffer would otherwise rewrite a command it already
	// handed over.
	args[0] = "mutated"
	if spec.Argv[1] != "test" {
		t.Errorf("Argv[1] = %q, want %q: Command aliased the caller's slice", spec.Argv[1], "test")
	}

	if spec := tc.Command(); len(spec.Argv) != 1 || spec.Argv[0] != tc.GoBin {
		t.Errorf("Command() = %q, want just the toolchain path", spec.Argv)
	}
}

// TestErrorCodesAreDistinct guards against two failures sharing a code, and
// against this package straying out of its allocated block.
func TestErrorCodesAreDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	for name, code := range map[string]string{
		"CodeToolchainNotFound":  gocmd.CodeToolchainNotFound,
		"CodeVersionProbeFailed": gocmd.CodeVersionProbeFailed,
		"CodeVersionUnparsable":  gocmd.CodeVersionUnparsable,
	} {
		if !strings.HasPrefix(code, "GOM72") {
			t.Errorf("%s = %q, want a code in this package's GOM72xx range", name, code)
		}
		if other, ok := seen[code]; ok {
			t.Errorf("%s and %s share the code %q", name, other, code)
		}
		seen[code] = name
	}

	// The block is shared with internal/runner, so the two packages must not
	// have chosen the same numbers.
	for _, code := range []string{
		runner.CodeSupervisionUnavailable,
		runner.CodeProcessStartFailed,
		runner.CodeSpecInvalid,
		runner.CodeProcessWaitFailed,
	} {
		if name, ok := seen[code]; ok {
			t.Errorf("gocmd.%s collides with a runner code: %q", name, code)
		}
	}
}

// TestErrorRendering pins the user-facing shape: code first, cause reachable.
func TestErrorRendering(t *testing.T) {
	t.Parallel()

	cause := errors.New("underlying failure")
	err := &gocmd.Error{Code: gocmd.CodeToolchainNotFound, Message: "no go", Err: cause}
	if got, want := err.Error(), "GOM7210: no go: underlying failure"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is does not reach the cause")
	}

	bare := &gocmd.Error{Code: gocmd.CodeVersionUnparsable, Message: "unreadable"}
	if got, want := bare.Error(), "GOM7212: unreadable"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := gocmd.CodeOf(errors.New("not ours")); got != "" {
		t.Errorf("CodeOf(foreign error) = %q, want an empty string", got)
	}
}

// execEvents is every exec event a recording kept, in order.
func execEvents(sink *trace.MemorySink) []trace.Event {
	var found []trace.Event
	for _, event := range sink.Events() {
		if event.Type == trace.TypeExec && event.Exec != nil {
			found = append(found, event)
		}
	}
	return found
}
