// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of this package's tests. It builds a real
// two-package module with `go test -c -cover`, runs both binaries, renders the
// data with `go tool covdata textfmt`, and reads the result back — which is the
// only way to know that the parser reads what the toolchain writes rather than
// what this package believes it writes.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/coverage/...
package coverage_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
)

// commandCap bounds each toolchain command and each profiling run. Generous,
// because the point is to stop a hung toolchain rather than to measure one.
const commandCap = 5 * time.Minute

// recordPattern is the block-record grammar the committed sample and every
// freshly generated profile both have to satisfy.
//
// It is written out here rather than reused from the parser on purpose: a
// grammar that came from the code under test would agree with it by
// construction, and what this file is for is noticing the day the toolchain
// stops writing what the parser expects.
var recordPattern = regexp.MustCompile(`^.+:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+ [0-9]+ [0-9]+$`)

// The fixture module. Package `dependency` holds three functions with
// deliberately different fates, and `caller` reaches exactly one of them.
const (
	fixtureModule = "cov.example/exp"

	fixtureGoMod = "module " + fixtureModule + "\n\ngo 1.26\n"

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

// TestParsesWhatTheToolchainWrites is the round trip: build, run, render, read.
func TestParsesWhatTheToolchainWrites(t *testing.T) {
	toolchain := locate(t)
	root := writeFixtureModule(t)

	dependencyProfile := collect(t, toolchain, root, "dependency")
	callerProfile := collect(t, toolchain, root, "caller")

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

	// The three facts the mapping is built on, read off real toolchain output.
	//
	// Clamp is reached by the dependency package's own tests. OnlyCaller is not,
	// and is reached by the caller package's tests instead — which is what makes
	// per-binary profiles worth collecting at all. Orphan is reached by neither,
	// and its blocks are present with a zero count rather than absent, which is
	// what lets an uncovered mutant be told apart from an unlinked package.
	dependencyFile := fixtureModule + "/dependency/dependency.go"
	assertCovered(t, dependencyProfile, dependencyFile, clampLine, true, "Clamp under its own tests")
	assertCovered(t, dependencyProfile, dependencyFile, onlyCallerLine, false, "OnlyCaller under the dependency tests")
	assertCovered(t, callerProfile, dependencyFile, onlyCallerLine, true, "OnlyCaller under the caller tests")
	assertCovered(t, dependencyProfile, dependencyFile, orphanLine, false, "Orphan under any test")
	assertCovered(t, callerProfile, dependencyFile, orphanLine, false, "Orphan under any test")

	// And the absence the mapping reads as "this binary never linked that
	// package": the caller's file is nowhere in the dependency's profile.
	callerFile := fixtureModule + "/caller/caller.go"
	if named(dependencyProfile, callerFile) {
		t.Errorf("the dependency test binary's profile names %s, which it does not link", callerFile)
	}
	if !named(callerProfile, callerFile) {
		t.Errorf("the caller test binary's profile does not name its own file %s", callerFile)
	}
}

// The lines of dependency.go the assertions above are about, counted from the
// source constant. They are named rather than written as numbers at the call
// site so that editing the fixture moves one constant instead of five.
const (
	clampLine      = 5  // `if v > lo` inside Clamp, reached by TestClamp
	onlyCallerLine = 13 // `return a != b` in OnlyCaller
	orphanLine     = 17 // `return a == b` in Orphan
)

// TestCommittedSampleStillDescribesTheFormat holds the checked-in fixture
// against freshly generated output.
//
// The unit tests read the sample on every machine, toolchain or not; this is
// what stops the sample from quietly becoming a description of a format Go no
// longer writes. It compares grammar rather than content — the sample is from a
// different module and would never match line for line — because the grammar is
// the part the parser depends on.
func TestCommittedSampleStillDescribesTheFormat(t *testing.T) {
	toolchain := locate(t)
	root := writeFixtureModule(t)
	fresh := render(t, toolchain, root, "dependency")

	committed, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("reading the committed sample: %v", err)
	}

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
	// And the parser agrees with the grammar on the fresh bytes, which is the
	// assertion the unit tests can only make about the committed ones.
	profile, err := coverage.ParseTextfmt(strings.NewReader(string(fresh)))
	if err != nil {
		t.Fatalf("ParseTextfmt over fresh toolchain output: %v", err)
	}
	if len(profile.Blocks) != len(freshLines)-1 {
		t.Errorf("parsed %d blocks from %d record lines", len(profile.Blocks), len(freshLines)-1)
	}
}

// locate finds the Go toolchain, or ends the test saying so.
func locate(t *testing.T) gocmd.Toolchain {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("no go executable on PATH: %v", err)
	}
	toolchain, err := gocmd.LocateContext(t.Context(), gocmd.Options{})
	if err != nil {
		t.Fatalf("locating the Go toolchain: %v", err)
	}
	return toolchain
}

// writeFixtureModule writes the two-package module into a directory of the
// test's own and returns its root.
func writeFixtureModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                        fixtureGoMod,
		"dependency/dependency.go":      dependencySource,
		"dependency/dependency_test.go": dependencyTest,
		"caller/caller.go":              callerSource,
		"caller/caller_test.go":         callerTest,
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	return root
}

// render builds one package's test binary with coverage, runs it, and returns
// the textfmt document the toolchain wrote.
func render(t *testing.T, toolchain gocmd.Toolchain, root, pkg string) []byte {
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
	build.Timeout = commandCap
	mustRun(t, build, "building the "+pkg+" test binary")

	// `-test.gocoverdir` and never the GOCOVERDIR environment variable: a test
	// binary emits through testing's coverTearDown, which is handed only the
	// flag, and setting the variable produces a run that prints a coverage
	// percentage and writes nothing at all. See internal/execute's coverDirFlag.
	run := runner.Spec{
		Argv:    []string{binary, "-test.gocoverdir=" + coverDir},
		Dir:     filepath.Join(root, pkg),
		Timeout: commandCap,
	}
	mustRun(t, run, "running the "+pkg+" test binary")

	textfmt := toolchain.Command("tool", "covdata", "textfmt", "-i="+coverDir, "-o="+profilePath)
	textfmt.Dir = root
	textfmt.Timeout = commandCap
	mustRun(t, textfmt, "rendering the "+pkg+" profile")

	document, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("reading the rendered profile: %v", err)
	}
	return document
}

// collect is [render] followed by the parser.
func collect(t *testing.T, toolchain gocmd.Toolchain, root, pkg string) coverage.Profile {
	t.Helper()
	profile, err := coverage.ParseTextfmt(strings.NewReader(string(render(t, toolchain, root, pkg))))
	if err != nil {
		t.Fatalf("ParseTextfmt over the %s profile: %v", pkg, err)
	}
	return profile
}

// mustRun runs one command and ends the test if it did not succeed.
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

// assertCovered checks whether any block of the profile covering a line was
// reached, and says which fact was expected when it was not.
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

// named reports whether the profile mentions a file at all, covered or not.
func named(profile coverage.Profile, file string) bool {
	for _, b := range profile.Blocks {
		if b.File == file {
			return true
		}
	}
	return false
}

// documentLines splits a document into its non-empty lines, with carriage
// returns removed.
func documentLines(document string) []string {
	var out []string
	for _, line := range strings.Split(document, "\n") {
		if trimmed := strings.TrimRight(line, "\r"); strings.TrimSpace(trimmed) != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
