// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of the engine's tests. It runs a real `go build`
// and a real `go test` against the fixture corpus, which is the only way to
// prove that the baseline pipeline works: everything interesting about it —
// process supervision, the snapshot, the environment the children get, the
// timeout derived from what was actually measured — is exactly what a mock
// would have to invent.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
)

// fixture returns the absolute path of a corpus module.
func fixture(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "fixtures", name))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
		t.Fatalf("fixture %s is not a module: %v", name, err)
	}
	return path
}

// collect runs the engine with a drained event channel and returns everything
// that was published.
//
// The collector starts before the engine does and is joined after it returns.
// Both halves matter: the engine's sends block, so a consumer that is not
// already running deadlocks the run, and a consumer that is not waited for can
// miss the terminal event.
func collect(t *testing.T, ctx context.Context, opts Options) (RunOutcome, []Event, error) {
	t.Helper()
	events := make(chan Event, 64)
	done := make(chan []Event, 1)
	go func() {
		var seen []Event
		for e := range events {
			seen = append(seen, e)
		}
		done <- seen
	}()
	opts.Events = events
	outcome, err := Run(ctx, opts)
	return outcome, <-done, err
}

// kinds names each event by its type, so a sequence can be compared as data.
func kinds(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, fmt.Sprintf("%T", e))
	}
	return names
}

// privateTempDir points this process's temporary directory at a fresh one for
// the length of the test, and returns it.
//
// The engine creates its snapshot and its scratch directory under
// os.TempDir(), so redirecting that is what makes "nothing was left behind" an
// assertion rather than a guess: the shared temporary directory of a machine
// running `go test ./...` has several packages writing into it at once, and a
// leak of one is indistinguishable from a leak of another.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// os.TempDir reads TMPDIR on POSIX and TMP then TEMP on Windows, so all
	// three are set rather than guessing which platform is reading.
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

// entries lists the names directly under a directory, sorted.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(found))
	for _, e := range found {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func TestRunMeasuresTheBaselineAndDerivesTheTimeout(t *testing.T) {
	root := fixture(t, "simple")
	tempRoot := privateTempDir(t)

	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 2

	outcome, events, err := collect(t, t.Context(), Options{Config: cfg, WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if outcome.Status != StatusOK {
		t.Errorf("status = %s, want %s", outcome.Status, StatusOK)
	}
	if len(outcome.BaselineRuns) != cfg.Test.BaselineRuns {
		t.Fatalf("measured %d baseline runs, want %d", len(outcome.BaselineRuns), cfg.Test.BaselineRuns)
	}
	for i, d := range outcome.BaselineRuns {
		if d <= 0 {
			t.Errorf("baseline run %d took %s, want a positive duration", i+1, d)
		}
	}

	// The expectation is derived from what the run itself measured, never from
	// a second measurement: runner.Result.Duration is the outer, supervised
	// time, and anything this test timed independently would be a different
	// number.
	slowest := slices.Max(outcome.BaselineRuns)
	if outcome.SlowestBaseline != slowest {
		t.Errorf("SlowestBaseline = %s, want %s", outcome.SlowestBaseline, slowest)
	}
	want := max(MinDerivedTimeout, TimeoutFactor*slowest)
	if outcome.Timeout != want {
		t.Errorf("timeout = %s, want max(%s, 5 x %s) = %s", outcome.Timeout, MinDerivedTimeout, slowest, want)
	}
	if outcome.TimeoutSource != TimeoutDerived {
		t.Errorf("timeout source = %s, want %s", outcome.TimeoutSource, TimeoutDerived)
	}

	if !slices.Equal(outcome.TestCommand, config.DefaultTestCommand()) {
		t.Errorf("test command = %q, want the configured default", outcome.TestCommand)
	}
	if len(outcome.WorkspaceDigest) != 64 {
		t.Errorf("workspace digest = %q, want 64 hex characters", outcome.WorkspaceDigest)
	}
	if outcome.SnapshotFiles != 3 {
		t.Errorf("snapshotted %d files, want 3 (go.mod, simple.go, simple_test.go)", outcome.SnapshotFiles)
	}
	if outcome.Toolchain.GoBin == "" || outcome.Toolchain.Version.Raw == "" {
		t.Errorf("toolchain = %+v, want a located one", outcome.Toolchain)
	}

	wantKinds := []string{
		"engine.RunPlanned",
		"engine.PhaseChanged",
		"engine.PhaseChanged",
		"engine.BaselineProgress",
		"engine.BaselineProgress",
		"engine.BaselineCompleted",
		"engine.Warning",
		"engine.RunCompleted",
	}
	if got := kinds(events); !slices.Equal(got, wantKinds) {
		t.Fatalf("event sequence = %v, want %v", got, wantKinds)
	}

	if planned := events[0].(RunPlanned); planned.RunID != outcome.RunID || planned.Workers != cfg.Execution.Jobs {
		t.Errorf("RunPlanned = %+v, want run %s and %d workers", planned, outcome.RunID, cfg.Execution.Jobs)
	}
	if phase := events[1].(PhaseChanged); phase.Phase != PhaseDiscover || phase.Detail == "" {
		t.Errorf("first phase = %+v, want a described %s", phase, PhaseDiscover)
	}
	if phase := events[2].(PhaseChanged); phase.Phase != PhaseBaseline || phase.Detail == "" {
		t.Errorf("second phase = %+v, want a described %s", phase, PhaseBaseline)
	}
	for i, index := range []int{3, 4} {
		progress := events[index].(BaselineProgress)
		if progress.Run != i+1 || progress.Of != cfg.Test.BaselineRuns {
			t.Errorf("progress %d = %+v, want run %d of %d", index, progress, i+1, cfg.Test.BaselineRuns)
		}
		if progress.Duration != outcome.BaselineRuns[i] {
			t.Errorf("progress %d reported %s, outcome recorded %s", index, progress.Duration, outcome.BaselineRuns[i])
		}
	}
	completed := events[5].(BaselineCompleted)
	if completed.Timeout != outcome.Timeout || completed.TimeoutSource != outcome.TimeoutSource {
		t.Errorf("BaselineCompleted = %+v, want the outcome's timeout %s (%s)", completed, outcome.Timeout, outcome.TimeoutSource)
	}
	if !slices.Equal(completed.Runs, outcome.BaselineRuns) {
		t.Errorf("BaselineCompleted.Runs = %v, want %v", completed.Runs, outcome.BaselineRuns)
	}
	warning := events[6].(Warning)
	if warning.Code != CodeMutationPhasesPending {
		t.Errorf("warning code = %s, want %s", warning.Code, CodeMutationPhasesPending)
	}
	if !strings.Contains(warning.Message, "not yet implemented") {
		t.Errorf("warning message = %q, want the pre-release notice", warning.Message)
	}
	if final := events[7].(RunCompleted); final.Status != StatusOK || final.Summary == "" {
		t.Errorf("RunCompleted = %+v, want a described ok", final)
	}
	if !slices.Equal(outcome.Warnings, []Warning{warning}) {
		t.Errorf("outcome warnings = %v, want the published one", outcome.Warnings)
	}

	// The snapshot is gone, and so is the scratch directory beside it.
	if filepath.Dir(outcome.SnapshotRoot) != tempRoot {
		t.Errorf("the snapshot was created in %s, want it under the redirected temporary directory %s",
			filepath.Dir(outcome.SnapshotRoot), tempRoot)
	}
	if _, err := os.Stat(outcome.SnapshotRoot); !os.IsNotExist(err) {
		t.Errorf("the snapshot at %s survived the run (stat error %v)", outcome.SnapshotRoot, err)
	}
	if left := entries(t, tempRoot); len(left) != 0 {
		t.Errorf("the run left %v behind in the temporary directory", left)
	}
}

func TestRunStopsOnAFailingBaseline(t *testing.T) {
	root := fixture(t, "failing-baseline")

	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1

	outcome, events, err := collect(t, t.Context(), Options{Config: cfg, WorkspaceRoot: root})
	if err == nil {
		t.Fatal("Run succeeded against a workspace whose tests fail")
	}
	if got := CodeOf(err); got != CodeBaselineTestFailed {
		t.Fatalf("error code = %s, want %s (error: %v)", got, CodeBaselineTestFailed, err)
	}
	if outcome.Status != StatusFailed {
		t.Errorf("status = %s, want %s", outcome.Status, StatusFailed)
	}

	// The failure has to quote the test output, or the user is left with an
	// exit status and nothing to act on.
	output := OutputOf(err)
	if output == "" {
		t.Fatal("the baseline error carries no output tail")
	}
	for _, needle := range []string{"FAIL", "this fixture fails on purpose"} {
		if !strings.Contains(output, needle) {
			t.Errorf("the output tail does not contain %q:\n%s", needle, output)
		}
	}
	if strings.Contains(output, "\r") {
		t.Error("the output tail still carries carriage returns")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the error message is not one line: %q", err.Error())
	}

	// The stream still terminates, and the snapshot is still removed: a failed
	// run has to leave the machine as it found it.
	names := kinds(events)
	if len(names) == 0 || names[len(names)-1] != "engine.RunCompleted" {
		t.Fatalf("event sequence = %v, want it to end with RunCompleted", names)
	}
	if final := events[len(events)-1].(RunCompleted); final.Status != StatusFailed {
		t.Errorf("RunCompleted status = %s, want %s", final.Status, StatusFailed)
	}
	if _, err := os.Stat(outcome.SnapshotRoot); !os.IsNotExist(err) {
		t.Errorf("the snapshot at %s survived a failed run (stat error %v)", outcome.SnapshotRoot, err)
	}
}

// TestMutationExcludeChangesNeitherTheSnapshotNorItsDigest pins the boundary
// between selecting what to mutate and copying what to build.
//
// `mutation.exclude` is candidate selection. If it reaches the snapshot walk,
// two things break: the excluded files are not built or tested — so
// `exclude = ["**/*_test.go"]` deletes the suite and the baseline passes having
// run nothing — and the workspace digest moves when a setting that touches no
// byte of source changes, which is what the outcome cache and the shard
// congruence check are built on.
//
// Both halves are asserted, but they are not two independent probes: the digest
// is taken over the manifest, so equal file sets already imply equal digests.
// The digest line is the stronger spelling — it also catches a walk that copied
// the same number of different files — and it is here to name the invariant
// phases 9 and 10 will depend on, where the file count alone would not say why.
func TestMutationExcludeChangesNeitherTheSnapshotNorItsDigest(t *testing.T) {
	root := fixture(t, "simple")
	privateTempDir(t)

	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1

	plain, _, err := collect(t, t.Context(), Options{Config: cfg, WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Run with no excludes: %v", err)
	}

	// Every file in the fixture is named by one of these patterns, so a walk
	// that honoured them would copy nothing but go.mod.
	excluded := cfg
	excluded.Mutation.Exclude = []string{"**/*_test.go", "**/simple.go", "**/testdata/**"}

	selective, _, err := collect(t, t.Context(), Options{Config: excluded, WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Run with mutation.exclude set: %v", err)
	}

	if selective.SnapshotFiles != plain.SnapshotFiles {
		t.Errorf("mutation.exclude changed the snapshot from %d files to %d: a selection setting must not shrink the tree that gets built",
			plain.SnapshotFiles, selective.SnapshotFiles)
	}
	if selective.WorkspaceDigest != plain.WorkspaceDigest {
		t.Errorf("mutation.exclude changed the workspace digest from %s to %s: the digest describes the code, not the selection",
			plain.WorkspaceDigest, selective.WorkspaceDigest)
	}
}

// TestMutationExcludeCannotHideAFailingBaseline is the same contract stated as
// the consequence a user meets: a red suite stays red however the mutation
// candidates are selected.
func TestMutationExcludeCannotHideAFailingBaseline(t *testing.T) {
	root := fixture(t, "failing-baseline")

	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1
	cfg.Mutation.Exclude = []string{"**/*_test.go"}

	outcome, _, err := collect(t, t.Context(), Options{Config: cfg, WorkspaceRoot: root})
	if err == nil {
		t.Fatal("Run succeeded against a red suite that mutation.exclude named: the baseline gate ran no tests")
	}
	if got := CodeOf(err); got != CodeBaselineTestFailed {
		t.Fatalf("error code = %s, want %s (error: %v)", got, CodeBaselineTestFailed, err)
	}
	// The code alone could arrive for another reason; the needle proves the
	// deliberately-red test was copied, compiled, and executed.
	if output := OutputOf(err); !strings.Contains(output, "this fixture fails on purpose") {
		t.Errorf("the output tail does not show the excluded test running:\n%s", output)
	}
	if outcome.Status != StatusFailed {
		t.Errorf("status = %s, want %s", outcome.Status, StatusFailed)
	}
}

func TestExplicitTimeoutBelowTheBaselineIsRefused(t *testing.T) {
	root := fixture(t, "simple")

	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1
	// One nanosecond is below any real measurement, so the rejection cannot
	// depend on how fast this machine is.
	cfg.Test.Timeout = time.Nanosecond

	// The `--` passthrough travels here, and this is the run that proves it
	// reaches the child rather than being quietly ignored.
	argv := []string{"go", "test", "-count=1", "./..."}

	outcome, _, err := collect(t, t.Context(), Options{Config: cfg, WorkspaceRoot: root, TestArgv: argv})
	if got := CodeOf(err); got != CodeTimeoutTooSmall {
		t.Fatalf("error code = %s, want %s (error: %v)", got, CodeTimeoutTooSmall, err)
	}
	if !slices.Equal(outcome.TestCommand, argv) {
		t.Errorf("test command = %q, want the passthrough %q", outcome.TestCommand, argv)
	}
	if len(outcome.BaselineRuns) != 1 {
		t.Errorf("measured %d baseline runs, want 1 before the rejection", len(outcome.BaselineRuns))
	}
}

func TestCancellationEndsTheRunAsAnInterrupt(t *testing.T) {
	root := fixture(t, "simple")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	outcome, events, err := collect(t, ctx, Options{Config: config.Defaults(), WorkspaceRoot: root})
	if err == nil {
		t.Fatal("Run succeeded with an already cancelled context")
	}
	// A cancellation is reported as one wherever it lands. Depending on how far
	// the run got it carries either this package's interrupt code or the code
	// of whatever was cancelled, and in both cases context.Canceled is in the
	// chain — which is what the command line maps to exit 130.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled in the chain", err)
	}
	if outcome.Status != StatusInterrupted {
		t.Errorf("status = %s, want %s", outcome.Status, StatusInterrupted)
	}
	names := kinds(events)
	if len(names) == 0 || names[len(names)-1] != "engine.RunCompleted" {
		t.Fatalf("event sequence = %v, want it to end with RunCompleted", names)
	}
}

// TestCommandLineEndToEnd compiles cmd/go-mutants and runs it, which is the
// only test that covers the wiring between the command tree, the renderer, and
// the engine as a user meets it.
func TestCommandLineEndToEnd(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go executable on PATH: %v", err)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "go-mutants")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), goBin, "build", "-o", binary, "./cmd/go-mutants")
	build.Dir = repo
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("building cmd/go-mutants: %v\n%s", buildErr, out)
	}

	// The binary is the authority on its own version, which keeps this test out
	// of internal/cli — an import that would make the dependency run backwards,
	// from the engine to the command line that drives it.
	versionOut, err := exec.CommandContext(t.Context(), binary, "--version").Output()
	if err != nil {
		t.Fatalf("go-mutants --version: %v", err)
	}
	version := strings.TrimSpace(string(versionOut))
	if !strings.HasPrefix(version, "go-mutants ") {
		t.Fatalf("--version printed %q, want a `go-mutants <version>` line", version)
	}

	run := exec.CommandContext(t.Context(), binary, "run")
	run.Dir = fixture(t, "simple")
	// Hermetic here means "nothing of go-mutants' own leaks in", not "an empty
	// environment": the child has to find the same `go`, the same module cache,
	// and on Windows the same SystemRoot this process did, or it fails for
	// reasons that have nothing to do with what is being tested.
	run.Env = append(childEnv(t.TempDir()), "NO_COLOR=1")

	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	if err := run.Run(); err != nil {
		t.Fatalf("go-mutants run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if code := run.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}

	out := stdout.String()
	for _, needle := range []string{
		version + " (run ",
		"phase discover:",
		"phase baseline:",
		"baseline ok: avg ",
		"timeout ",
		"(derived)",
		"warning " + CodeMutationPhasesPending + ":",
		"run ok:",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("stdout does not contain %q:\n%s", needle, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Error("stdout carries escape sequences with NO_COLOR set")
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing on a successful run", stderr.String())
	}
}
