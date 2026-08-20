// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
)

// TestBaseEnvScrubsActivationAndKeepsTheRest pins both halves of the child
// environment, and the second half matters as much as the first.
//
// Stripping GO_MUTANTS_ is what stops a developer who exported an activation in
// their shell from having every "baseline" run a mutant. Keeping everything
// else is what makes the measurement mean anything: GOFLAGS, GOPROXY, the
// module cache and the PATH a project's tests need are part of what "the tests
// pass here" says, and a run that stripped them would be measuring a different
// project.
func TestBaseEnvScrubsActivationAndKeepsTheRest(t *testing.T) {
	t.Setenv("GO_MUTANTS_ACTIVE", "from-the-users-shell")
	t.Setenv("GO_MUTANTS_ANYTHING", "also-scrubbed")
	t.Setenv("GOFLAGS", "-mod=readonly")
	t.Setenv("A_HARMLESS_VARIABLE", "kept")

	env := execute.BaseEnv("")

	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GO_MUTANTS_") {
			t.Errorf("the child inherited %q", entry)
		}
	}
	if got := envValue(env, "GOFLAGS"); got != "-mod=readonly" {
		t.Errorf("GOFLAGS = %q, want it inherited", got)
	}
	if got := envValue(env, "A_HARMLESS_VARIABLE"); got != "kept" {
		t.Errorf("A_HARMLESS_VARIABLE = %q, want it inherited", got)
	}
}

// TestBaseEnvRedirectsEveryTemporaryDirectoryName covers all three spellings.
// TMPDIR is the POSIX one and TMP and TEMP the Windows ones, and a test helper
// may read any of them — so leaving one pointing at the user's own temporary
// directory would quietly undo the isolation the other two provide.
func TestBaseEnvRedirectsEveryTemporaryDirectoryName(t *testing.T) {
	t.Setenv("TMP", "/users/tmp")
	t.Setenv("TEMP", "/users/tmp")
	t.Setenv("TMPDIR", "/users/tmp")

	env := execute.BaseEnv("/run/scratch/w2")

	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		if got := envValue(env, key); got != "/run/scratch/w2" {
			t.Errorf("%s = %q, want the worker's own directory", key, got)
		}
		if got := countKey(env, key); got != 1 {
			t.Errorf("%s appears %d times; a duplicate leaves the value depending on os/exec's last-wins rule", key, got)
		}
	}
}

// TestMutantEnvActivatesExactlyOneMutant pins the dispatch mechanism's entire
// input: one variable, one identity, and nothing of the user's own activation
// left behind it.
func TestMutantEnvActivatesExactlyOneMutant(t *testing.T) {
	t.Setenv(instrument.ActiveEnv, "the-wrong-mutant")

	env := execute.MutantEnv("the-right-mutant", "")

	if got := envValue(env, instrument.ActiveEnv); got != "the-right-mutant" {
		t.Errorf("%s = %q, want the scheduled mutant", instrument.ActiveEnv, got)
	}
	if got := countKey(env, instrument.ActiveEnv); got != 1 {
		t.Errorf("%s appears %d times, want exactly once", instrument.ActiveEnv, got)
	}
}

// countKey counts how many entries of an environment name a variable.
func countKey(env []string, name string) int {
	n := 0
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, name) {
			n++
		}
	}
	return n
}
