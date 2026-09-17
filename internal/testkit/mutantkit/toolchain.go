// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
)

const StepTimeout = testkit.DefaultTimeout

func Toolchain(t testing.TB) gocmd.Toolchain {
	t.Helper()
	goBin := testkit.GoBinary(t)
	toolchain, err := gocmd.LocateContext(t.Context(), gocmd.Options{
		Explicit: goBin,
		Env:      testkit.Compose(t, t.TempDir()),
		Timeout:  StepTimeout,
	})
	if err != nil {
		t.Fatalf("locating a Go toolchain at %s: %v", goBin, err)
		return gocmd.Toolchain{}
	}
	return toolchain
}

func RunGo(t testing.TB, tc gocmd.Toolchain, dir string, env []string, args ...string) runner.Result {
	t.Helper()
	spec := tc.Command(args...)
	spec.Dir = dir
	spec.Env = env
	spec.Timeout = StepTimeout
	logInputs(t, "dir="+dir, "argv="+strings.Join(spec.Argv, " "))
	return runner.Run(t.Context(), spec)
}

func RunSuite(t testing.TB, tc gocmd.Toolchain, root string, env []string) runner.Result {
	t.Helper()
	return RunGo(t, tc, root, env, "test", "-count=1", "-v", "./...")
}

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

func RequireOutput(t testing.TB, result runner.Result, what string, needles ...string) {
	t.Helper()
	out := string(result.Output)
	for _, needle := range needles {
		if !strings.Contains(out, needle) {
			t.Errorf("%s did not print %q:\n%s", what, needle, out)
		}
	}
}

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

func logInputs(t testing.TB, fields ...string) {
	t.Helper()
	if len(fields) == 0 {
		return
	}
	t.Logf("mutantkit: %s", strings.Join(fields, " "))
}
