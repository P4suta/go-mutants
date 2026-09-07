// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// StepTimeout bounds every child a test starts through this package.
//
// Each step is a build or a run of a fixture module of a few dozen lines, so a
// minute is a very long time — and far shorter than the per-package alarm `go
// test` fires, which is the point: a step that hangs should fail as a named step
// with its output quoted, not as a ten-minute panic with every goroutine in the
// binary dumped after it.
//
// It is the same number [testkit.DefaultTimeout] uses, for the same reason, and
// it is stated separately because the children here go through internal/runner
// rather than through the harness's own [testkit.Exec]: these are the commands a
// real run issues, supervised by the package that supervises them in a real run.
const StepTimeout = 60 * time.Second

// Toolchain locates the Go toolchain a test's children will run.
//
// The skip-or-fail policy is [testkit.GoBinary]'s and is applied first, so a
// developer without `go` on PATH sees the toolchain tests skip and a CI job
// without one fails rather than quietly running a smaller suite. The located
// binary is then passed to gocmd explicitly: resolving it twice is two chances
// to find two different toolchains, and the second resolution would happen under
// an environment the first one never saw.
//
// The probe runs under a composed environment rather than this process's, for
// the reason every child here does: a developer with GO_MUTANTS_ACTIVE exported
// in their shell would otherwise have the instrumented baseline running a
// mutant.
//
// It logs nothing of its own: [testkit.GoBinary] already writes the constructor
// line naming the binary, and a second line here would say the same path twice.
// The version is not lost — every child this package runs logs its argv, which
// starts with that binary, and a toolchain that could not be located is quoted
// in the failure below.
func Toolchain(t testing.TB) gocmd.Toolchain {
	t.Helper()
	goBin := testkit.GoBinary(t)
	toolchain, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: goBin,
		// t.TempDir rather than [testkit.Scratch], and this is the one place in
		// the harness where that is the right way round. The probe runs `go
		// version` and `go env` and writes nothing: what the directory holds
		// afterwards is an empty private home. Kept, it would be the *first*
		// scratch of almost every integration test in this repository — which is
		// the directory the dumps and the recording are filed in — so a reader of
		// a failed test would open the evidence directory and find a `home/` with
		// nothing in it, while the snapshot sat in a sibling.
		Env:     testkit.Compose(t, t.TempDir()),
		Timeout: StepTimeout,
	})
	if err != nil {
		t.Fatalf("locating a Go toolchain at %s: %v", goBin, err)
		return gocmd.Toolchain{}
	}
	return toolchain
}

// RunGo runs one `go` command in dir, supervised by the same package that
// supervises them in a real run.
//
// internal/runner rather than os/exec is deliberate: what these tests assert on
// is a child's exit status, its output and whether it ran out of time, and those
// three are exactly what the runner already knows how to separate — a non-zero
// status is a fact about the child rather than an error, and a timeout is not a
// status at all.
func RunGo(t testing.TB, tc gocmd.Toolchain, dir string, env []string, args ...string) runner.Result {
	t.Helper()
	spec := tc.Command(args...)
	spec.Dir = dir
	spec.Env = env
	spec.Timeout = StepTimeout
	// Logged before the child runs, and once. A [RequireExit] failure quotes the
	// output but not the command, and in CI the log is all there is — "the suite
	// exited 1" is a different report from "`go test -count=1 -v ./...` in this
	// snapshot exited 1". It is also what shows an activation reaching, or not
	// reaching, the process it was meant for.
	logInputs(t, "dir="+dir, "argv="+strings.Join(spec.Argv, " "))
	return runner.Run(t.Context(), spec)
}

// RunSuite runs a fixture's whole test suite in a snapshot.
//
// -count=1 defeats the go test result cache. That cache keys on the environment
// a test binary reads and so would very probably do the right thing under an
// activated mutant; "very probably" is not a foundation for the step that tells
// a survivor from a cached green. -v is what lets a failure name the subtest
// that passed or failed rather than only the package.
func RunSuite(t testing.TB, tc gocmd.Toolchain, root string, env []string) runner.Result {
	t.Helper()
	return RunGo(t, tc, root, env, "test", "-count=1", "-v", "./...")
}

// RequireExit ends the step unless the child ran to completion with the status
// the step expects, quoting the child's output whenever it did not.
//
// The quoting is the whole reason it exists. A `go test -c` that failed, a suite
// that went red under a mutant, a build that could not resolve an import —
// every one of them explains itself on its output, and a helper that reported
// only the status turns a two-second diagnosis into a re-run with the command
// copied out by hand. In CI there is no re-run: the log is all there is.
func RequireExit(t testing.TB, result runner.Result, want int, what string) {
	t.Helper()
	switch {
	case result.Err != nil:
		t.Fatalf("%s could not be run: %v\n%s", what, result.Err, result.Output)
	case result.TimedOut:
		t.Fatalf("%s did not finish within %s:\n%s", what, StepTimeout, result.Output)
	case result.ExitCode != want:
		t.Fatalf("%s exited %d, want %d:\n%s", what, result.ExitCode, want, result.Output)
	}
}

// RequireOutput fails the step once for each needle the child did not print,
// quoting the whole output every time.
//
// Errorf rather than Fatalf: a step that expected four lines and got two should
// say which two are missing in one run, not make the reader discover them one
// re-run at a time.
func RequireOutput(t testing.TB, result runner.Result, what string, needles ...string) {
	t.Helper()
	out := string(result.Output)
	for _, needle := range needles {
		if !strings.Contains(out, needle) {
			t.Errorf("%s did not print %q:\n%s", what, needle, out)
		}
	}
}

// Activate returns env with one mutant switched on, or with the activation
// removed when id is empty.
//
// Removing it rather than setting it to the empty string is what makes "the
// baseline" an unambiguous statement: the generated runtime reads the variable,
// and a test that composed a baseline environment by setting it blank would be
// relying on the runtime's reading of a value it was never meant to receive.
func Activate(env []string, id string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if name, _, ok := strings.Cut(entry, "="); ok && name == instrument.ActiveEnv {
			continue
		}
		out = append(out, entry)
	}
	if id == "" {
		return slices.Clip(out)
	}
	return append(out, instrument.ActiveEnv+"="+id)
}

// logInputs records what a constructor resolved, one line per constructor.
//
// It is the harness's own convention, repeated here rather than exported from
// it: a test that fails in CI on a machine nobody can reach is diagnosed from
// its log, and the questions asked first are always which fixture, which
// toolchain, which snapshot.
func logInputs(t testing.TB, fields ...string) {
	t.Helper()
	if len(fields) == 0 {
		return
	}
	t.Logf("mutantkit: %s", strings.Join(fields, " "))
}
