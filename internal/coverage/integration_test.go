// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package coverage_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const commandCap = 5 * time.Minute

var recordPattern = regexp.MustCompile(`^.+:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+ [0-9]+ [0-9]+$`)

const (
	fixtureModule = "cov.example/exp"

	dependencySource = `package dependency

func Clamp(v, lo, hi int) int {
	if v < hi {
		if v > lo {
			return v
		}
		return lo + 1
	}
	return hi - 1
}

func OnlyCaller(a, b int) bool {
	return a != b
}

func Orphan(a, b int) bool {
	return a == b
}
`

	dependencyTest = `package dependency

import "testing"

func TestClamp(t *testing.T) {
	if Clamp(5, 0, 10) != 5 {
		t.Fatal("Clamp(5, 0, 10) is not 5")
	}
}
`

	callerSource = `package caller

import "` + fixtureModule + `/dependency"

func Use(a, b int) bool { return dependency.OnlyCaller(a, b) }
`

	callerTest = `package caller

import "testing"

func TestUse(t *testing.T) {
	if !Use(1, 2) {
		t.Fatal("Use(1, 2) is false")
	}
}
`
)

func TestParsesWhatTheToolchainWrites(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := writeFixtureModule(t)

	dependencyProfile := collect(t, toolchain, root, env, "dependency")
	callerProfile := collect(t, toolchain, root, env, "caller")

	for name, profile := range map[string]coverage.Profile{
		"dependency": dependencyProfile,
		"caller":     callerProfile,
	} {
		if profile.Mode != "set" {
			t.Errorf("%s: mode = %q, want %q", name, profile.Mode, "set")
		}
		if len(profile.Blocks) == 0 {
			t.Errorf("%s: the profile holds no blocks", name)
		}
	}

	dependencyFile := fixtureModule + "/dependency/dependency.go"
	assertCovered(t, dependencyProfile, dependencyFile, clampLine, true, "Clamp under its own tests")
	assertCovered(t, dependencyProfile, dependencyFile, onlyCallerLine, false, "OnlyCaller under the dependency tests")
	assertCovered(t, callerProfile, dependencyFile, onlyCallerLine, true, "OnlyCaller under the caller tests")
	assertCovered(t, dependencyProfile, dependencyFile, orphanLine, false, "Orphan under any test")
	assertCovered(t, callerProfile, dependencyFile, orphanLine, false, "Orphan under any test")

	callerFile := fixtureModule + "/caller/caller.go"
	if named(dependencyProfile, callerFile) {
		t.Errorf("the dependency test binary's profile names %s, which it does not link", callerFile)
	}
	if !named(callerProfile, callerFile) {
		t.Errorf("the caller test binary's profile does not name its own file %s", callerFile)
	}
}

const (
	clampLine      = 8
	onlyCallerLine = 16
	orphanLine     = 20
)

func TestCommittedSampleStillDescribesTheFormat(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := writeFixtureModule(t)
	fresh := render(t, toolchain, root, env, "dependency")

	committed := testkit.ReadFile(t, samplePath)

	freshLines := documentLines(string(fresh))
	sampleLines := documentLines(string(committed))
	if len(freshLines) == 0 || len(sampleLines) == 0 {
		t.Fatal("one of the documents is empty")
	}
	if freshLines[0] != sampleLines[0] {
		t.Errorf("the toolchain now opens a profile with %q; the committed sample says %q",
			freshLines[0], sampleLines[0])
	}
	for _, lines := range [][]string{freshLines, sampleLines} {
		for i, line := range lines[1:] {
			if !recordPattern.MatchString(line) {
				t.Errorf("record %d is not in the documented grammar: %q", i, line)
			}
		}
	}
	profile, err := coverage.ParseTextfmt(strings.NewReader(string(fresh)))
	if err != nil {
		t.Fatalf("ParseTextfmt over fresh toolchain output: %v", err)
	}
	if len(profile.Blocks) != len(freshLines)-1 {
		t.Errorf("parsed %d blocks from %d record lines", len(profile.Blocks), len(freshLines)-1)
	}
}

func writeFixtureModule(t *testing.T) string {
	t.Helper()
	return testkit.NewModule(t).Module(fixtureModule).
		Source("dependency/dependency.go", dependencySource).
		Source("dependency/dependency_test.go", dependencyTest).
		Source("caller/caller.go", callerSource).
		Source("caller/caller_test.go", callerTest).
		Root()
}

func render(t *testing.T, toolchain gocmd.Toolchain, root string, env []string, pkg string) []byte {
	t.Helper()
	work := t.TempDir()
	binary := filepath.Join(work, pkg+".test")
	coverDir := filepath.Join(work, "cover")
	profilePath := filepath.Join(work, pkg+".txt")
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", coverDir, err)
	}

	build := toolchain.Command("test", "-c", "-cover", "-coverpkg="+fixtureModule+"/...",
		"-o", binary, "./"+pkg)
	build.Dir = root
	build.Env = env
	build.Timeout = commandCap
	mustRun(t, build, "building the "+pkg+" test binary")

	run := runner.Spec{
		Argv:    []string{binary, "-test.gocoverdir=" + coverDir},
		Dir:     filepath.Join(root, pkg),
		Env:     env,
		Timeout: commandCap,
	}
	mustRun(t, run, "running the "+pkg+" test binary")

	textfmt := toolchain.Command("tool", "covdata", "textfmt", "-i="+coverDir, "-o="+profilePath)
	textfmt.Dir = root
	textfmt.Env = env
	textfmt.Timeout = commandCap
	mustRun(t, textfmt, "rendering the "+pkg+" profile")

	return testkit.ReadFile(t, profilePath)
}

func collect(t *testing.T, toolchain gocmd.Toolchain, root string, env []string, pkg string) coverage.Profile {
	t.Helper()
	profile, err := coverage.ParseTextfmt(strings.NewReader(string(render(t, toolchain, root, env, pkg))))
	if err != nil {
		t.Fatalf("ParseTextfmt over the %s profile: %v", pkg, err)
	}
	return profile
}

func mustRun(t *testing.T, spec runner.Spec, what string) {
	t.Helper()
	result := runner.Run(context.WithoutCancel(t.Context()), spec)
	switch {
	case result.Err != nil:
		t.Fatalf("%s: %v\n%s", what, result.Err, result.Output)
	case result.TimedOut:
		t.Fatalf("%s: no answer within %s\n%s", what, spec.Timeout, result.Output)
	case result.ExitCode != 0:
		t.Fatalf("%s: exit %d\n%s", what, result.ExitCode, result.Output)
	}
}

func assertCovered(t *testing.T, profile coverage.Profile, file string, line int, want bool, what string) {
	t.Helper()
	got := false
	for _, b := range profile.Blocks {
		if b.File == file && b.Covered() && b.StartLine <= line && line <= b.EndLine {
			got = true
		}
	}
	if got != want {
		t.Errorf("%s: line %d of %s covered = %t, want %t", what, line, file, got, want)
	}
}

func named(profile coverage.Profile, file string) bool {
	for _, b := range profile.Blocks {
		if b.File == file {
			return true
		}
	}
	return false
}

func documentLines(document string) []string {
	var out []string
	for _, line := range strings.Split(document, "\n") {
		if trimmed := strings.TrimRight(line, "\r"); strings.TrimSpace(trimmed) != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
