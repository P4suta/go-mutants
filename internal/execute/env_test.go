// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
)

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

func TestBaseEnvKeepsAParentsCoverageDirectoryOutOfTheChild(t *testing.T) {
	t.Setenv("GOCOVERDIR", "/the/parents/coverage")

	for _, c := range []struct {
		name string
		env  []string
	}{
		{"baseEnv", execute.BaseEnv("")},
		{"mutantEnv", execute.MutantEnv("a-mutant", "")},
		{"probeEnv", execute.ProbeEnv("", "/run/probe.log")},
		{"controlEnv", execute.ControlEnv("")},
	} {
		if got := envValue(c.env, "GOCOVERDIR"); got != "" {
			t.Errorf("%s passed GOCOVERDIR=%q to the child, which would write into the parent's profile",
				c.name, got)
		}
	}
}

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

func countKey(env []string, name string) int {
	n := 0
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, name) {
			n++
		}
	}
	return n
}
