// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

const probeTimeout = 2 * time.Second

const hangTimeout = 200 * time.Millisecond

const hangSleep = 3 * time.Second

func fakeEnv(t *testing.T, f *mutantkit.Fake) []string {
	t.Helper()
	return f.Env(testkit.Compose(t, testkit.Scratch(t)))
}

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
	if want := "exited with status 3"; !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to contain %q", err, want)
	}
}

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
	if elapsed, bound := time.Since(started), hangSleep/2; elapsed > bound {
		t.Errorf("the probe took %s, want it ended by its own %s deadline rather than by the "+
			"%s sleep (bounded at %s, which is half the sleep)",
			elapsed, hangTimeout, hangSleep, bound)
	}
	var failure *gocmd.Error
	if !errors.As(err, &failure) || failure.Command() == nil {
		t.Fatalf("err = %v, want a *gocmd.Error naming the probe that hung", err)
	}
	if argv := []string{f.Bin(), "version"}; !slices.Equal(failure.Command().Argv, argv) {
		t.Errorf("Argv = %q, want %q", failure.Command().Argv, argv)
	}
}

func TestLocateReportsAProbeThatCannotBeStarted(t *testing.T) {
	t.Parallel()

	name := "not-a-program"
	if runtime.GOOS == "windows" {
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
	if !strings.Contains(err.Error(), quoted) {
		t.Errorf("Error() = %q, want it to name the configured path as %s, unescaped", err, quoted)
	}
}

func TestAbsoluteReportsAWorkingDirectoryThatIsGone(t *testing.T) {
	gone := errors.New("getwd: no such file or directory")
	gocmd.FailAbsolutePath(t, gone)

	path, err := gocmd.Absolute("go")
	if err == nil {
		t.Fatalf("Absolute(\"go\") = %q, want an error: there is no directory to resolve it against", path)
	}
	if path != "" {
		t.Errorf("Absolute(\"go\") = %q beside an error, want an empty path", path)
	}
	if got := gocmd.CodeOf(err); got != gocmd.CodeToolchainNotFound {
		t.Errorf("CodeOf(err) = %q, want %q", got, gocmd.CodeToolchainNotFound)
	}
	if !errors.Is(err, gone) {
		t.Errorf("err = %v does not wrap the resolution failure", err)
	}
	if !strings.Contains(err.Error(), `"go"`) {
		t.Errorf("err = %v does not quote the path it could not resolve", err)
	}
}

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

func TestLocateAbsolutisesARelativeExplicitPath(t *testing.T) {
	workspace := testkit.Scratch(t)
	f := mutantkit.FakeGo(t)
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

func TestLocateWithoutAToolchainOnPath(t *testing.T) {
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
	if !strings.Contains(err.Error(), "was cancelled") {
		t.Errorf("the failure does not say the probe was cancelled: %v", err)
	}
	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("the cancelled probe still ran %+v, want nothing started", calls)
	}
}

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
	if spec.Trace != nil || spec.Kind != "" || spec.Subject != "" {
		t.Errorf("Command labelled the spec %+v; the label belongs to the phase that issues the command", spec)
	}

	args[0] = "mutated"
	if spec.Argv[1] != "test" {
		t.Errorf("Argv[1] = %q, want %q: Command aliased the caller's slice", spec.Argv[1], "test")
	}

	if spec := tc.Command(); len(spec.Argv) != 1 || spec.Argv[0] != tc.GoBin {
		t.Errorf("Command() = %q, want just the toolchain path", spec.Argv)
	}
}

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

func execEvents(sink *trace.MemorySink) []trace.Event {
	var found []trace.Event
	for _, event := range sink.Events() {
		if event.Type == trace.TypeExec && event.Exec != nil {
			found = append(found, event)
		}
	}
	return found
}
