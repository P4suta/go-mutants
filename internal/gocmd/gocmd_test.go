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
// It is far shorter than [gocmd.DefaultProbeTimeout], because the fake answers
// from a table: anything approaching this is a fake that did not start, and
// waiting out thirty seconds to learn that is thirty seconds nobody has.
const probeTimeout = 30 * time.Second

// hangTimeout is what the hanging probe is given.
//
// It is a deadline this test pays in full every run, so it is as short as the
// claim allows. Two hundred milliseconds is far longer than the fake takes to
// start and far shorter than [gocmd.DefaultProbeTimeout]; a machine so loaded
// that the child has not started yet still produces the same verdict, because a
// probe whose context expired before its process began is a probe that did not
// answer either.
const hangTimeout = 200 * time.Millisecond

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
// longer than the deadline it is given, so a call that returned at all can only
// have killed it.
func TestLocateReportsAProbeThatHangs(t *testing.T) {
	t.Parallel()

	f := mutantkit.FakeGo(t)
	f.On("version").Sleep(2 * time.Minute)

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
	if elapsed := time.Since(started); elapsed > probeTimeout {
		t.Errorf("the probe took %s, want the deadline rather than the sleep", elapsed)
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
	workspace := t.TempDir()
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
