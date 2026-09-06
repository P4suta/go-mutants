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
	"github.com/P4suta/go-mutants/trace"
)

// TestLocateFindsTheToolchainOnPath is the happy path against the real
// toolchain. It is not skipped when `go` is missing: these tests are run by
// `go test`, so a machine without a Go toolchain cannot have got this far, and
// a skip here would quietly delete the only test that proves the probe agrees
// with reality.
func TestLocateFindsTheToolchainOnPath(t *testing.T) {
	t.Parallel()

	tc, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		t.Fatalf("Locate = %v, want the toolchain running this test", err)
	}
	if !filepath.IsAbs(tc.GoBin) {
		t.Errorf("GoBin = %q, want an absolute path so a later PATH change cannot re-resolve it", tc.GoBin)
	}
	if tc.Version.Raw == "" {
		t.Error("Version.Raw is empty")
	}
	if !strings.HasPrefix(tc.Version.Release, "go") && !tc.Version.IsDevel() {
		t.Errorf("Version.Release = %q, want a go release or a devel build", tc.Version.Release)
	}
	// The toolchain that built this test binary is the toolchain that runs it,
	// so the target it reports has to be the one this code is executing on.
	if tc.Version.GOOS != runtime.GOOS || tc.Version.GOARCH != runtime.GOARCH {
		t.Errorf("target = %s/%s, want %s/%s", tc.Version.GOOS, tc.Version.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if !strings.Contains(tc.String(), tc.GoBin) || !strings.Contains(tc.String(), tc.Version.Raw) {
		t.Errorf("String() = %q, want it to name both the path and the version", tc.String())
	}
}

// TestLocateHonoursAnExplicitPath checks that configuration wins over PATH,
// using the toolchain PATH would have found anyway so the assertion is about
// which mechanism was used rather than about which binary exists.
func TestLocateHonoursAnExplicitPath(t *testing.T) {
	t.Parallel()

	found, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("looking up the go that is running this test: %v", err)
	}

	tc, err := gocmd.Locate(gocmd.Options{Explicit: found})
	if err != nil {
		t.Fatalf("Locate with an explicit path = %v, want a toolchain", err)
	}
	if tc.GoBin != found {
		t.Errorf("GoBin = %q, want the explicitly configured %q", tc.GoBin, found)
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
	tools := filepath.Join(workspace, "tools")
	if err := os.MkdirAll(tools, 0o750); err != nil {
		t.Fatalf("creating %q: %v", tools, err)
	}
	// A version line no released toolchain will ever print, so that a result
	// accidentally produced by the real `go` could not be mistaken for this one.
	const release = "go1.99.0"
	want := "go version " + release + " " + runtime.GOOS + "/" + runtime.GOARCH
	buildFakeGo(t, tools, want)

	t.Chdir(workspace)

	tc, err := gocmd.Locate(gocmd.Options{Explicit: filepath.Join("tools", "go")})
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
	spec.Timeout = gocmd.DefaultProbeTimeout
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

// TestLocateRejectsAnExecutableThatIsNotGo is why locating probes rather than
// stats. Something on PATH called `go` that answers with anything else must be
// rejected here, not halfway through building test binaries.
func TestLocateRejectsAnExecutableThatIsNotGo(t *testing.T) {
	t.Parallel()

	impostor := buildImpostor(t)
	tc, err := gocmd.Locate(gocmd.Options{Explicit: impostor})
	if err == nil {
		t.Fatalf("Locate(%q) = %+v, want an error", impostor, tc)
	}
	if code := gocmd.CodeOf(err); code != gocmd.CodeVersionUnparsable {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionUnparsable)
	}
}

// TestLocateIsCancellable pins that a probe respects the caller's context, so
// a Ctrl-C during start-up does not have to wait out the probe timeout.
func TestLocateIsCancellable(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if tc, err := gocmd.LocateContext(ctx, gocmd.Options{}); err == nil {
		t.Fatalf("LocateContext with a cancelled context = %+v, want an error", tc)
	} else if code := gocmd.CodeOf(err); code != gocmd.CodeVersionProbeFailed {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionProbeFailed)
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

// TestCommandProducesARunnableSpec closes the loop between the two packages:
// the fragment Command returns really is something runner.Run accepts.
func TestCommandProducesARunnableSpec(t *testing.T) {
	t.Parallel()

	tc, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		t.Fatalf("Locate = %v", err)
	}

	spec := tc.Command("version")
	spec.Timeout = gocmd.DefaultProbeTimeout
	result := runner.Run(t.Context(), spec)
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; output: %s", result.ExitCode, result.Output)
	}
	if got := strings.TrimSpace(string(result.Output)); got != tc.Version.Raw {
		t.Errorf("`go version` printed %q, want the located %q", got, tc.Version.Raw)
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

// TestLocateRecordsTheVersionProbeAsGoVersion is the first labelled command in
// a run: the toolchain probe. It is here rather than in internal/runner because
// the label belongs to the call site, and this is the only call site that has
// one until the engine's own commands are labelled.
func TestLocateRecordsTheVersionProbeAsGoVersion(t *testing.T) {
	t.Parallel()

	found := testkit.GoBinary(t)
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindWorkspace,
		ToolVersion: "test",
		PID:         os.Getpid(),
	})

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{Trace: recorder})
	if err != nil {
		t.Fatalf("LocateContext = %v, want the toolchain running this test", err)
	}
	want, err := filepath.Abs(found)
	if err != nil {
		t.Fatalf("resolving %q: %v", found, err)
	}
	if tc.GoBin != want {
		t.Fatalf("GoBin = %q, want the toolchain on PATH, %q", tc.GoBin, want)
	}

	events := execEvents(sink)
	if len(events) != 1 {
		t.Fatalf("the recording holds %d exec events, want exactly one for the version probe", len(events))
	}
	rec := events[0].Exec
	if rec.Kind != trace.ExecKindGoVersion {
		t.Errorf("kind = %q, want %q", rec.Kind, trace.ExecKindGoVersion)
	}
	if argv := []string{tc.GoBin, "version"}; !slices.Equal(rec.Argv, argv) {
		t.Errorf("argv = %q, want %q", rec.Argv, argv)
	}
	if rec.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", rec.ExitCode)
	}
	if rec.TimeoutMS != gocmd.DefaultProbeTimeout.Milliseconds() {
		t.Errorf("timeout_ms = %d, want the probe timeout %d", rec.TimeoutMS, gocmd.DefaultProbeTimeout.Milliseconds())
	}
	if rec.OutputBytes == 0 || rec.OutputSHA256 == "" {
		t.Errorf("output_bytes = %d and output_sha256 = %q, want the digest of the version line",
			rec.OutputBytes, rec.OutputSHA256)
	}
}

// TestVersionProbeFailureCarriesTheInvocationAndOutput is the error a fresh
// machine hits second: something is at the configured path and it is not a Go
// toolchain. What it printed is the whole diagnosis, so the error keeps it — and
// keeps the command, because "`/opt/go/bin/go version` exited 3" is only
// actionable if the reader can run it themselves.
func TestVersionProbeFailureCarriesTheInvocationAndOutput(t *testing.T) {
	t.Parallel()

	const garbage = "this executable is not a go toolchain"
	standIn := buildGoStandIn(t, t.TempDir(), garbage, 3)

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{Kind: trace.StartKindRun, ToolVersion: "test"})

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{Explicit: standIn, Trace: recorder})
	if err == nil {
		t.Fatalf("LocateContext(%q) = %+v, want an error", standIn, tc)
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
	if argv := []string{standIn, "version"}; !slices.Equal(invocation.Argv, argv) {
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

// buildImpostor compiles a program that answers `version` with something the
// Go toolchain would never print, and returns its path.
func buildImpostor(t *testing.T) string {
	t.Helper()
	return buildFakeGo(t, t.TempDir(), "this is not the go toolchain")
}

// buildFakeGo compiles a stand-in toolchain that answers successfully.
func buildFakeGo(t *testing.T, dir, prints string) string {
	t.Helper()
	return buildGoStandIn(t, dir, prints, 0)
}

// buildGoStandIn compiles a program that answers any invocation by printing the
// given line and exiting with the given status, installs it into dir under the
// name a toolchain has, and returns its path.
//
// Three tests need a `go` that is not the real one, for three reasons: the
// impostor, which has to be rejected for what it printed; a stand-in that has
// to be reachable at a path of the test's choosing — copying the real
// twenty-megabyte toolchain into a fixture directory to prove a point about
// path resolution would be a much slower way to say the same thing; and one
// that fails outright, which is the only way to observe what a failed probe
// keeps.
func buildGoStandIn(t *testing.T, dir, prints string, status int) string {
	t.Helper()

	work := t.TempDir()
	source := filepath.Join(work, "main.go")
	program := "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tfmt.Println(" +
		strconv.Quote(prints) + ")\n\tos.Exit(" + strconv.Itoa(status) + ")\n}\n"
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatalf("writing the stand-in source: %v", err)
	}

	binary := filepath.Join(dir, "go")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	goBin := testkit.GoBinary(t)
	build := exec.Command(goBin, "build", "-o", binary, source)
	build.Dir = work
	// The harness's environment, so this build lands in the suites' shared
	// build cache rather than in the developer's own — three stand-ins per run
	// of this package, each keyed on a temporary path that exists for one test,
	// is exactly the kind of entry that filled that cache up.
	//
	// Two rows are then overridden: the build is module-less and has to be told
	// not to look for a module, and the harness's `-mod=readonly` means nothing
	// outside module mode.
	build.Env = append(testkit.Compose(t, t.TempDir()), "GO111MODULE=off", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in toolchain: %v\n%s", err, out)
	}
	return binary
}
