// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const workspaceBarrierEnv = "WORKSPACE_EXEC_BARRIER_HELPER"

func TestMain(m *testing.M) {
	code := mutantkit.Main(m)
	if mutantkit.IsFakeGo() {
		os.Exit(code)
	}
	releasePreparedFixtures(code != 0)
	os.Exit(code)
}

const sessionBlockEnv = "SESSION_BLOCK"

const sessionBlockPIDFileEnv = "SESSION_BLOCK_PIDFILE"

const (
	expectCleanEnv   = "EXPECT_CLEAN"
	writeSnapshotEnv = "WRITE_SNAPSHOT"
)

const targetDeathBound = 5 * time.Second

const targetDeathPoll = 50 * time.Millisecond

const cleanupBound = runner.TerminationGrace + runner.IODrainGrace + 20*time.Second

var fixtureGateEnv = []string{sessionBlockEnv, controlFailEnv, expectCleanEnv, writeSnapshotEnv}

func hostEnvWithoutFixtureGates() []string {
	return slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.ContainsFunc(fixtureGateEnv, func(gate string) bool {
			return strings.EqualFold(name, gate)
		})
	})
}

func requireTargetIsGone(t *testing.T, pidFile, what string) {
	t.Helper()

	recorded, err := os.ReadFile(pidFile)
	if err != nil {
		t.Errorf("the %s target recorded no pid (%v), so its cleanup is unproven: "+
			"it was cut off before it ran, or %s never reached it", what, err, sessionBlockPIDFileEnv)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(recorded)))
	if err != nil {
		t.Errorf("the %s target wrote %q into its pid file, which is not a pid: %v", what, recorded, err)
		return
	}

	started := time.Now()
	for {
		gone, probeErr := processIsGone(pid)
		if probeErr != nil {
			t.Errorf("asking whether the %s target (pid %d) is still running: %v", what, pid, probeErr)
			return
		}
		if gone {
			return
		}
		if elapsed := time.Since(started); elapsed > targetDeathBound {
			t.Errorf("the %s target (pid %d) was still running %s after Exec returned, "+
				"so the call was bounded but the process tree was not killed", what, pid, elapsed)
			return
		}
		time.Sleep(targetDeathPoll)
	}
}

const blockGateBound = 5 * time.Second

const killableExtraTests = `package killable

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func init() {
	if os.Getenv("SESSION_BLOCK") != "yes" {
		return
	}
	if path := os.Getenv("SESSION_BLOCK_PIDFILE"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
}

// TestPrintsALot is the chatty target the output-budget tests need: 2000 lines
// of 64 characters, which is over a hundred kilobytes and thirty times the
// smallest limit those tests set.
//
// Many short lines rather than one long one, so that the fifty-line rule the
// retained tail is trimmed by actually bites. One line would make OutputTail
// and the whole kept capture the same string, and a test comparing the two
// would then pass whatever that rule was.
//
// It prints to stdout directly rather than through t.Log because what is under
// test is what the engine captured from the process, and t.Log's buffer only
// reaches the stream when the test fails.
func TestPrintsALot(t *testing.T) {
	for range 2000 {
		fmt.Println(strings.Repeat("x", 64))
	}
}

func TestSessionEnvironment(t *testing.T) {
	if os.Getenv("EXPECT_CLEAN") == "yes" && os.Getenv("GO_MUTANTS_ACTIVE") != "" {
		t.Fatalf("baseline inherited GO_MUTANTS_ACTIVE")
	}
	if os.Getenv("EXPECT_CLEAN") == "yes" && os.Getenv("FROZEN_AT_OPEN") != "before" {
		t.Fatalf("FROZEN_AT_OPEN = %q, want the value captured by Open", os.Getenv("FROZEN_AT_OPEN"))
	}
	if os.Getenv("WRITE_SNAPSHOT") == "yes" {
		if err := os.WriteFile("session-artifact.txt", []byte("written by target"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzSessionIdentity(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, value string) {
		if string([]byte(value)) != value {
			t.Fatalf("string round trip changed %q", value)
		}
	})
}

func FuzzSessionClamp(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		value := 5
		if len(input) > 0 {
			value = 10
		}
		if got := Clamp(value, 0, 10); got != map[bool]int{true: 9, false: 5}[len(input) > 0] {
			t.Fatalf("Clamp(%d, 0, 10) = %d", value, got)
		}
	})
}

func TestSessionBlocks(t *testing.T) {
	if os.Getenv("SESSION_BLOCK") != "yes" {
		return
	}
	time.Sleep(60 * time.Second)
}

// TestControlFails is red on the *original* program when the environment asks
// it to be, which is the one thing a control run has to be able to report and
// the one thing this fixture otherwise has no target for.
//
// The module is called killable because everything in it passes; without this
// there is nothing here whose failure belongs to the repository rather than to
// a mutant, and "a control reports a failing suite" would be a claim nothing
// exercises. The gate is what the sleeper's is for — the baseline, the
// verification inside Prepare and every whole-package target would otherwise be
// red — and it reads a value rather than a presence, so an inherited empty
// variable cannot turn it on.
func TestControlFails(t *testing.T) {
	if os.Getenv("CONTROL_FAIL") != "yes" {
		return
	}
	t.Fatal("CONTROL_FAIL asked this test to fail on the original program")
}
`

func killableRoot(t *testing.T) string {
	t.Helper()
	root := copyFixture(t, "killable")
	if err := os.WriteFile(filepath.Join(root, "session_test.go"), []byte(killableExtraTests), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPublicSessionReusesOnePreparedSnapshot(t *testing.T) {
	root := killableRoot(t)

	parent := t.TempDir()
	env := append(hostEnvWithoutFixtureGates(),
		"GO_MUTANTS_ACTIVE=must-be-scrubbed",
		"FROZEN_AT_OPEN=before",
	)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Env:           env,
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Setenv("FROZEN_AT_OPEN", "after")
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})

	baseline, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "test", "./..."},
		Env:  []string{expectCleanEnv + "=yes"},
	})
	if err != nil {
		t.Fatalf("baseline infrastructure: %v", err)
	}
	if baseline.TimedOut || baseline.ExitCode != 0 {
		t.Fatalf("baseline = exit %d timeout=%v:\n%s", baseline.ExitCode, baseline.TimedOut, baseline.Output)
	}
	if _, reservedErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "version"},
		Env:  []string{"GO_MUTANTS_ACTIVE=stolen"},
	}); reservedErr == nil {
		t.Fatal("Workspace.Exec accepted a reserved activation variable")
	}

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		MutantTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("closing session: %v", closeErr)
		}
	})

	catalog := session.Catalog()
	if catalog.WorkspaceDigest == "" || catalog.Digest == "" {
		t.Fatalf("catalog has no frozen digests: %+v", catalog)
	}
	if catalog.ModulePath != "fixture.example/killable" {
		t.Errorf("module path = %q", catalog.ModulePath)
	}
	if !slices.Equal(catalog.TestPackages, []string{"fixture.example/killable"}) {
		t.Errorf("test packages = %v", catalog.TestPackages)
	}
	clamp := mutantkit.APIMutantAt(t, catalog, "clamp.go", "lt-to-le")
	untested := mutantkit.APIMutantAt(t, catalog, "untested.go", "neq-to-eq")
	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking the freshly prepared snapshot: %v", changesErr)
	} else if len(changes) != 0 {
		t.Fatalf("freshly prepared snapshot already changed: %+v", changes)
	}

	if _, warmErr := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^$"},
		Timeout: time.Minute,
	}); warmErr != nil {
		t.Fatalf("warming the prepared test binary: %v", warmErr)
	}

	const blockingBudget = 2 * time.Second
	timeoutPIDFile := filepath.Join(t.TempDir(), "timed-out.pid")
	cancelPIDFile := filepath.Join(t.TempDir(), "cancelled.pid")

	timeoutStarted := time.Now()
	timedOut, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: blockingBudget,
		Env: []string{
			sessionBlockEnv + "=yes",
			sessionBlockPIDFileEnv + "=" + timeoutPIDFile,
		},
	})
	if err != nil {
		t.Fatalf("executing a bounded target: %v", err)
	}
	if timedOut.Outcome != gomutants.OutcomeTimedOut || timedOut.KilledBy != "fixture.example/killable" {
		t.Errorf("bounded target result = %+v, want a package-local timeout", timedOut)
	}
	if elapsed := time.Since(timeoutStarted); elapsed > cleanupBound {
		t.Errorf("bounded target returned after %s, want prompt process-tree cleanup", elapsed)
	}
	requireTargetIsGone(t, timeoutPIDFile, "bounded")

	cancelContext, cancel := context.WithCancel(t.Context())
	cancelTimer := time.AfterFunc(blockingBudget, cancel)
	cancelStarted := time.Now()
	cancelled, cancelErr := session.Exec(cancelContext, gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 30 * time.Second,
		Env: []string{
			sessionBlockEnv + "=yes",
			sessionBlockPIDFileEnv + "=" + cancelPIDFile,
		},
	})
	cancelTimer.Stop()
	cancel()
	if !errors.Is(cancelErr, context.Canceled) {
		t.Errorf("cancel error = %v, want context.Canceled", cancelErr)
	}
	if cancelled.Outcome != gomutants.OutcomeNotRun {
		t.Errorf("cancelled target result = %+v, want not_run", cancelled)
	}
	if elapsed := time.Since(cancelStarted); elapsed > cleanupBound {
		t.Errorf("cancelled target returned after %s, want prompt process-tree cleanup", elapsed)
	}
	requireTargetIsGone(t, cancelPIDFile, "cancelled")

	killed, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.DisplayID,
		Package: "fixture.example/killable",
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("executing clamp mutant: %v", err)
	}
	if killed.Outcome != gomutants.OutcomeKilled || killed.KilledBy != "fixture.example/killable" {
		t.Errorf("clamp result = %+v, want a package-local kill", killed)
	}
	fuzzKilled, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.DisplayID,
		Package: "fixture.example/killable",
		Args: []string{
			"-test.run=^$",
			"-test.fuzz=^FuzzSessionClamp$",
			"-test.fuzztime=5s",
		},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("fuzzing the clamp mutant: %v", err)
	}
	if fuzzKilled.Outcome != gomutants.OutcomeKilled || len(fuzzKilled.Artifacts) == 0 {
		t.Fatalf("fuzz kill = %+v, want a kill with captured standard corpus artifacts", fuzzKilled)
	}
	if !slices.ContainsFunc(fuzzKilled.Artifacts, func(artifact gomutants.Artifact) bool {
		return strings.HasPrefix(string(artifact.Data), "go test fuzz v1\n") && artifact.SHA256 != "" && artifact.Path != ""
	}) {
		t.Errorf("artifacts = %+v, want standard Go fuzz encoding", fuzzKilled.Artifacts)
	}
	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking snapshot after fuzz: %v", changesErr)
	} else if len(changes) != 0 {
		t.Fatalf("fuzz target changed the prepared snapshot: %+v", changes)
	}
	fuzzed, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.DisplayID,
		Package: "fixture.example/killable",
		Args: []string{
			"-test.run=^$",
			"-test.fuzz=^FuzzSessionIdentity$",
			"-test.fuzztime=100ms",
		},
	})
	if err != nil {
		t.Fatalf("executing fuzz target: %v", err)
	}
	if fuzzed.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("fuzz target result = %+v, want survivor", fuzzed)
	}

	survived, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionEnvironment$"},
		Env:     []string{writeSnapshotEnv + "=yes"},
	})
	if err != nil {
		t.Fatalf("executing write target: %v", err)
	}
	if survived.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("write target result = %+v, want survivor", survived)
	}
	if _, reservedErr := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant: untested.ID,
		Env:    []string{"GO_MUTANTS_ACTIVE=stolen"},
	}); reservedErr == nil {
		t.Fatal("Session.Exec accepted a reserved activation variable")
	}

	changes, err := session.Changes()
	if err != nil {
		t.Fatalf("checking snapshot changes: %v", err)
	}
	if !slices.ContainsFunc(changes, func(change gomutants.Change) bool {
		return change.Kind == gomutants.ChangeAdded && change.Path == "session-artifact.txt"
	}) {
		t.Errorf("changes = %+v, want the target-created artifact", changes)
	}
	if _, statErr := os.Stat(filepath.Join(root, "session-artifact.txt")); !os.IsNotExist(statErr) {
		t.Errorf("the target wrote into the user's workspace: %v", statErr)
	}
	for i := 1; i < len(changes); i++ {
		if strings.Compare(changes[i-1].Path, changes[i].Path) >= 0 {
			t.Errorf("changes are not strictly path-sorted: %+v", changes)
		}
	}
	if closeErr := session.Close(); closeErr != nil {
		t.Fatalf("closing session explicitly: %v", closeErr)
	}
	if afterClose := session.Catalog(); afterClose.Digest != catalog.Digest {
		t.Errorf("closed session catalog digest = %q, want immutable %q", afterClose.Digest, catalog.Digest)
	}
	if closeErr := workspace.Close(); closeErr != nil {
		t.Fatalf("closing workspace explicitly: %v", closeErr)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading temporary parent after close: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary parent still holds %v after Close", entries)
	}
}

func TestWorkspaceExecReportsTruncation(t *testing.T) {
	root := killableRoot(t)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})

	const limit = 4096
	result, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv:        []string{"go", "test", "-run=^TestPrintsALot$", "-v", "."},
		OutputLimit: limit,
	})
	if err != nil {
		t.Fatalf("running the chatty target: %v", err)
	}
	if result.TimedOut || result.ExitCode != 0 {
		t.Fatalf("chatty target = exit %d timeout=%v:\n%s", result.ExitCode, result.TimedOut, result.Output)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false although the target prints 64 KiB into a %d-byte budget", limit)
	}
	if result.TotalBytes <= limit {
		t.Errorf("TotalBytes = %d, want more than the %d-byte budget", result.TotalBytes, limit)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most %d: the notice is paid for out of the budget",
			len(result.Output), limit)
	}
	if !bytes.HasPrefix(result.Output, []byte(gomutants.OutputTruncatedPrefix)) {
		t.Errorf("Output begins %q, want the exported prefix %q",
			string(result.Output[:min(len(result.Output), 120)]), gomutants.OutputTruncatedPrefix)
	}
}

func TestSessionExecHonoursOutputLimit(t *testing.T) {
	root := killableRoot(t)
	parent := t.TempDir()
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		SkipVerify:    true,
		MutantTimeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("closing session: %v", closeErr)
		}
	})

	const limit = 4096
	clamp := mutantkit.APIMutantAt(t, session.Catalog(), "clamp.go", "lt-to-le")
	result, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:      clamp.ID,
		Package:     "fixture.example/killable",
		Args:        []string{"-test.run=^TestPrintsALot$|^TestClamp$", "-test.failfast=false"},
		OutputLimit: limit,
	})
	if err != nil {
		t.Fatalf("executing the chatty target: %v", err)
	}
	if result.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.OutcomeKilled, result.OutputTail)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false although the target prints 64 KiB into a %d-byte budget", limit)
	}
	if result.TotalBytes <= limit {
		t.Errorf("TotalBytes = %d, want more than the %d-byte budget", result.TotalBytes, limit)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most %d", len(result.Output), limit)
	}
	if want := lastLines(result.Output, 50); result.OutputTail != want {
		t.Errorf("OutputTail = %q, want the last 50 lines of Output with the carriage returns stripped: %q",
			result.OutputTail, want)
	}
}

func lastLines(output []byte, n int) string {
	text := strings.TrimRight(string(output), "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

func TestSessionBlocksOnlyWhenAsked(t *testing.T) {
	root := killableRoot(t)
	t.Setenv(sessionBlockEnv, "yes")

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:  []string{"comparison"},
		SkipVerify: true,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	mutant := mutantkit.APIMutantAt(t, session.Catalog(), "untested.go", "neq-to-eq")

	if _, warmErr := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  mutant.ID,
		Package: ".",
		Args:    []string{"-test.run=^$"},
	}); warmErr != nil {
		t.Fatalf("warming the fixture's test binary: %v", warmErr)
	}

	started := time.Now()
	quiet, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  mutant.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
	})
	if err != nil {
		t.Fatalf("executing the ungated target: %v", err)
	}
	if quiet.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("ungated target = %+v, want a survivor: the injected TestSessionBlocks slept without being asked to", quiet)
	}
	if elapsed := time.Since(started); elapsed > blockGateBound {
		t.Errorf("ungated target returned after %s, want under %s: the sleep is no longer gated on %s",
			elapsed, blockGateBound, sessionBlockEnv)
	}

	blocked, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  mutant.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 250 * time.Millisecond,
		Env:     []string{sessionBlockEnv + "=yes"},
	})
	if err != nil {
		t.Fatalf("executing the gated target: %v", err)
	}
	if blocked.Outcome != gomutants.OutcomeTimedOut {
		t.Errorf("gated target = %+v, want a timeout: %s no longer reaches the target that reads it",
			blocked, sessionBlockEnv)
	}
}

func TestWorkspaceExecBarrierHelper(t *testing.T) {
	if !testkit.HelperEnabled(workspaceBarrierEnv) {
		return
	}
	temporary := strings.Join([]string{
		os.Getenv("TMP"), os.Getenv("TEMP"), os.Getenv("TMPDIR"),
	}, "\n")
	if err := os.WriteFile(os.Getenv("MARKER"), []byte(temporary), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("RELEASE")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("release was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkspaceExecRunsConcurrentlyWithPrivateTemporaryDirectories(t *testing.T) {
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	coordination := t.TempDir()
	release := filepath.Join(coordination, "release")
	markers := []string{filepath.Join(coordination, "first"), filepath.Join(coordination, "second")}
	results := make(chan error, len(markers))
	for _, marker := range markers {
		go func() {
			result, execErr := workspace.Exec(t.Context(), gomutants.Command{
				Argv: testkit.HelperArgv("TestWorkspaceExecBarrierHelper"),
				Env: []string{
					workspaceBarrierEnv + "=1", "MARKER=" + marker, "RELEASE=" + release,
				},
			})
			if execErr == nil && (result.TimedOut || result.ExitCode != 0) {
				execErr = errors.New("barrier command did not pass: " + string(result.Output))
			}
			results <- execErr
		}()
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		ready := 0
		for _, marker := range markers {
			if _, statErr := os.Stat(marker); statErr == nil {
				ready++
			}
		}
		if ready == len(markers) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d concurrent commands reached the barrier", ready, len(markers))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if writeErr := os.WriteFile(release, []byte("release"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	for range markers {
		if execErr := <-results; execErr != nil {
			t.Error(execErr)
		}
	}
	firstBytes, err := os.ReadFile(markers[0])
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(markers[1])
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(string(firstBytes), "\n")
	second := strings.Split(string(secondBytes), "\n")
	keys := []string{"TMP", "TEMP", "TMPDIR"}
	if len(first) != len(keys) || len(second) != len(keys) {
		t.Fatalf("temporary variable markers = %q and %q", firstBytes, secondBytes)
	}
	for index, key := range keys {
		if first[index] == "" || second[index] == "" || first[index] == second[index] {
			t.Errorf("concurrent %s values = %q and %q, want two private directories", key, first[index], second[index])
		}
		if first[index] != first[0] || second[index] != second[0] {
			t.Errorf("temporary variables do not agree within each command: first=%q second=%q", first, second)
		}
	}
}

func TestPrepareRefusesDriftFromAWorkspaceCommand(t *testing.T) {
	root := copyFixture(t, "simple")
	testSource := `package simple

import (
	"os"
	"testing"
)

func TestWriteSnapshot(t *testing.T) {
	if err := os.WriteFile("command-artifact.txt", []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
}
`
	if err := os.WriteFile(filepath.Join(root, "write_test.go"), []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	run, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test", "-run=^TestWriteSnapshot$", "."}})
	if err != nil || run.TimedOut || run.ExitCode != 0 {
		t.Fatalf("drifting command = (%+v, %v)", run, err)
	}
	_, err = workspace.Prepare(t.Context(), gomutants.PrepareOptions{})
	if err == nil || !strings.Contains(err.Error(), "commands changed the frozen snapshot:\nadded command-artifact.txt") {
		t.Fatalf("Prepare after drift = %v", err)
	}
	var drift *gomutants.DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("Prepare after drift = %v, want a *DriftError", err)
	}
	if drift.Stage != "commands" {
		t.Errorf("Stage = %q, want %q", drift.Stage, "commands")
	}
	if len(drift.Changes) != 1 {
		t.Fatalf("Changes = %+v, want the one file the command wrote", drift.Changes)
	}
	change := drift.Changes[0]
	if change.Kind != gomutants.ChangeAdded || change.Path != "command-artifact.txt" {
		t.Errorf("Changes[0] = %+v, want command-artifact.txt added", change)
	}
	if change.AfterSHA256 == "" {
		t.Errorf("Changes[0].AfterSHA256 is empty, so the added file cannot be identified: %+v", change)
	}
	if change.BeforeSHA256 != "" {
		t.Errorf("Changes[0].BeforeSHA256 = %q, want empty for a file that was not there", change.BeforeSHA256)
	}
}

func TestPrepareRefusesDriftFromTheBaselineItself(t *testing.T) {
	root := copyFixture(t, "selfwriting")
	workspace, err := gomutants.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	if _, err = workspace.Prepare(t.Context(), gomutants.PrepareOptions{}); err == nil {
		t.Fatal("Prepare accepted a module whose own tests write into the snapshot")
	}
	var drift *gomutants.DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("Prepare = %v, want a *DriftError", err)
	}
	if drift.Stage != "verification" {
		t.Errorf("Stage = %q, want %q: nobody ran a command here, the suite did it",
			drift.Stage, "verification")
	}
	if len(drift.Changes) != 1 {
		t.Fatalf("Changes = %+v, want the one file the suite wrote", drift.Changes)
	}
	change := drift.Changes[0]
	if change.Kind != gomutants.ChangeAdded || change.Path != "witness.txt" {
		t.Errorf("Changes[0] = %+v, want witness.txt added", change)
	}
	if !strings.Contains(err.Error(), "changed the snapshot outside instrumentation") {
		t.Errorf("the message does not say what kind of change it refused:\n%v", err)
	}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	return testkit.Copy(t, name)
}

func copyFixtureTree(name, destination string) error {
	source := filepath.Join("fixtures", name)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

const probeableModule = "fixture.example/probeable"

const (
	widthRule   = "return-zero-numeric"
	labelRule   = "return-empty-string"
	readyRule   = "true-to-false"
	doubledRule = "add-to-sub"
)

type preparedFixture struct {
	parent    string
	release   func(failed bool)
	workspace *gomutants.Workspace
	session   *gomutants.Session
	catalog   gomutants.Catalog
	events    []gomutants.PrepareEvent
	err       error

	keepTemp bool
}

func expectScratchAfterCall(t *testing.T, prepared *preparedFixture, before, after []string) {
	t.Helper()

	added := make([]string, 0, len(after))
	for _, path := range after {
		if !slices.Contains(before, path) {
			added = append(added, path)
		}
	}
	var removed []string
	for _, path := range before {
		if !slices.Contains(after, path) {
			removed = append(removed, path)
		}
	}

	if len(removed) != 0 {
		t.Errorf("the call removed %v, which was there before it: a per-call directory belongs to"+
			" the call that made it", removed)
	}
	if !prepared.keepTemp {
		if len(added) != 0 {
			t.Errorf("the call left %v behind; before it there were %v", added, before)
		}
		return
	}
	if len(added) != 1 {
		t.Errorf("a KeepTemp session kept %d per-call directories for one call, want exactly the"+
			" one it ran in: %v", len(added), added)
		return
	}
	if info, err := os.Stat(added[0]); err != nil || !info.IsDir() {
		t.Errorf("%s was kept and is not a directory on disk: %v", added[0], err)
	}
}

var (
	probedFixture   = sync.OnceValue(func() *preparedFixture { return prepareProbeable(true) })
	unprobedFixture = sync.OnceValue(func() *preparedFixture { return prepareProbeable(false) })

	rejectedFixture = sync.OnceValue(func() *preparedFixture {
		return prepareFixture("rejectable", gomutants.PrepareOptions{SkipVerify: true})
	})

	preparedMu       sync.Mutex
	preparedFixtures []*preparedFixture
)

func prepareProbeable(probe bool) *preparedFixture {
	return prepareFixture("probeable", gomutants.PrepareOptions{
		Probe:              probe,
		ProbeCoverPackages: []string{probeableModule + "/..."},
		MutantTimeout:      30 * time.Second,
		SkipVerify:         !probe,
	})
}

func prepareFixture(name string, options gomutants.PrepareOptions) *preparedFixture {
	return prepareFixtureWith(name, nil, gomutants.OpenOptions{}, options)
}

func prepareFixtureWith(
	name string, inject map[string]string, open gomutants.OpenOptions, options gomutants.PrepareOptions,
) *preparedFixture {
	prepared := &preparedFixture{}
	preparedMu.Lock()
	preparedFixtures = append(preparedFixtures, prepared)
	preparedMu.Unlock()

	parent, release := testkit.PackageScratch(name + "-fixture")
	prepared.parent = parent
	prepared.release = release

	root := filepath.Join(parent, name)
	if err := copyFixtureTree(name, root); err != nil {
		prepared.err = err
		return prepared
	}
	if err := writeInjected(root, inject); err != nil {
		prepared.err = err
		return prepared
	}
	open.TempDirectory = parent
	open.KeepTemp = testkit.KeepPolicy() != testkit.KeepNever
	prepared.keepTemp = open.KeepTemp
	workspace, err := gomutants.Open(context.Background(), root, open)
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.workspace = workspace

	options.Trace = func(event gomutants.PrepareEvent) {
		prepared.events = append(prepared.events, event)
	}
	session, err := workspace.Prepare(context.Background(), options)
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.session = session
	prepared.catalog = session.Catalog()
	return prepared
}

func writeInjected(root string, inject map[string]string) error {
	for path, source := range inject {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(source), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func TestInjectedSourceReachesADirectoryThatDoesNotExistYet(t *testing.T) {
	t.Parallel()

	root := testkit.Scratch(t)
	sources := map[string]string{
		"top_test.go":               "package top\n",
		"nested/dir/nested_test.go": "package dir\n",
	}
	if err := writeInjected(root, sources); err != nil {
		t.Fatalf("injecting %d source(s) into %s: %v", len(sources), root, err)
	}
	for path, want := range sources {
		got := testkit.ReadFile(t, filepath.Join(root, filepath.FromSlash(path)))
		if string(got) != want {
			t.Errorf("%s holds %q, want %q", path, got, want)
		}
	}
}

func TestPrepareTraceReportsEveryPhaseInOrder(t *testing.T) {
	serialPhases := []gomutants.PreparePhase{
		gomutants.PreparePhaseDiscovery,
		gomutants.PreparePhaseProbeSnapshot,
		gomutants.PreparePhaseMainValidation,
		gomutants.PreparePhaseMainRestoration,
		gomutants.PreparePhaseVerification,
	}
	probePhases := []gomutants.PreparePhase{
		gomutants.PreparePhaseProbeValidation,
		gomutants.PreparePhaseProbeCoverageBuild,
		gomutants.PreparePhaseProbeRestoration,
	}
	type eventKey struct {
		phase gomutants.PreparePhase
		state gomutants.PrepareEventState
	}
	var order []eventKey
	for _, phase := range serialPhases {
		order = append(order,
			eventKey{phase: phase, state: gomutants.PrepareEventStarted},
			eventKey{phase: phase, state: gomutants.PrepareEventFinished},
		)
	}
	order = append(order, eventKey{phase: gomutants.PreparePhaseBinaryBuild, state: gomutants.PrepareEventStarted})
	for _, phase := range probePhases {
		order = append(order,
			eventKey{phase: phase, state: gomutants.PrepareEventStarted},
			eventKey{phase: phase, state: gomutants.PrepareEventFinished},
		)
	}
	order = append(order, eventKey{phase: gomutants.PreparePhaseBinaryBuild, state: gomutants.PrepareEventFinished})
	for _, test := range []struct {
		name    string
		fixture *preparedFixture
		skipped map[gomutants.PreparePhase]bool
	}{
		{name: "probe", fixture: probeable(t)},
		{
			name:    "without probe",
			fixture: unprobeable(t),
			skipped: map[gomutants.PreparePhase]bool{
				gomutants.PreparePhaseProbeSnapshot:      true,
				gomutants.PreparePhaseVerification:       true,
				gomutants.PreparePhaseProbeValidation:    true,
				gomutants.PreparePhaseProbeCoverageBuild: true,
				gomutants.PreparePhaseProbeRestoration:   true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, want := len(test.fixture.events), len(order); got != want {
				t.Fatalf("event count = %d, want %d: %+v", got, want, test.fixture.events)
			}
			for index, want := range order {
				event := test.fixture.events[index]
				if event.Phase != want.phase || event.State != want.state {
					t.Errorf("event %d = %+v, want phase %s state %s", index, event, want.phase, want.state)
					continue
				}
				if want.state == gomutants.PrepareEventStarted {
					if event.Result != "" || event.Duration != 0 {
						t.Errorf("phase %s start = %+v", want.phase, event)
					}
					continue
				}
				wantResult := gomutants.PreparePhaseSucceeded
				if test.skipped[want.phase] {
					wantResult = gomutants.PreparePhaseSkipped
				}
				if event.Result != wantResult || event.Duration < 0 {
					t.Errorf("phase %s finish = %+v, want result %s", want.phase, event, wantResult)
				}
			}
		})
	}
}

func releasePreparedFixtures(failed bool) {
	preparedMu.Lock()
	defer preparedMu.Unlock()
	for _, prepared := range preparedFixtures {
		if prepared.workspace != nil {
			_ = prepared.workspace.Close()
		}
		if prepared.release != nil {
			prepared.release(failed)
		}
	}
	preparedFixtures = nil
}

func probeable(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := probedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the probeable fixture with a probe tree: %v", prepared.err)
	}
	return prepared
}

func unprobeable(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := unprobedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the probeable fixture without a probe tree: %v", prepared.err)
	}
	return prepared
}

func rejectable(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := rejectedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the rejectable fixture: %v", prepared.err)
	}
	return prepared
}

func snapshotDirectories(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading %s: %v", parent, err)
	}
	var snapshots []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "go-mutants-snap-") {
			snapshots = append(snapshots, entry.Name())
		}
	}
	return snapshots
}

func probeOf(t *testing.T, session *gomutants.Session, request gomutants.ProbeRequest) gomutants.ProbeResult {
	t.Helper()
	result, err := session.Probe(t.Context(), request)
	if err != nil {
		t.Fatalf("probing %v: %v", request.Args, err)
	}
	if result.Outcome != gomutants.ProbeMeasured {
		t.Fatalf("probing %v = %s, want %s:\n%s",
			request.Args, result.Outcome, gomutants.ProbeMeasured, result.Output)
	}
	if result.Infected == nil {
		t.Fatalf("probing %v measured, but Infected is nil rather than a set", request.Args)
	}
	return result
}

func TestPrepareWithProbeMarksProbedMutants(t *testing.T) {
	catalog := probeable(t).catalog
	if len(catalog.Mutants) != 4 {
		t.Fatalf("the fixture catalogues %d mutants, want 4: %+v", len(catalog.Mutants), catalog.Mutants)
	}
	probed := 0
	for _, mutant := range catalog.Mutants {
		want := mutant.Rule != doubledRule
		if mutant.Probed != want {
			t.Errorf("mutant %s (%s/%s) Probed = %v, want %v",
				mutant.DisplayID, mutant.Family, mutant.Rule, mutant.Probed, want)
		}
		if mutant.Probed {
			probed++
		}
	}
	if probed != 3 {
		t.Errorf("%d mutants are probed, want the fixture's 3 with a form", probed)
	}
}

func TestPrepareWithoutProbeBuildsNoProbeTree(t *testing.T) {
	prepared := unprobeable(t)
	if snapshots := snapshotDirectories(t, prepared.parent); len(snapshots) != 1 {
		t.Errorf("a session prepared without Probe left %d snapshots (%v), want only the mutant tree",
			len(snapshots), snapshots)
	}
	for _, mutant := range prepared.catalog.Mutants {
		if mutant.Probed {
			t.Errorf("mutant %s claims to be probed although no probe tree was built", mutant.DisplayID)
		}
	}
}

func TestProbeWithoutPreparationIsAnError(t *testing.T) {
	prepared := unprobeable(t)
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestWidth$"},
	})
	if !errors.Is(err, gomutants.ErrProbeNotPrepared) {
		t.Fatalf("error = %v, want one carrying ErrProbeNotPrepared", err)
	}
	if result.Infected != nil {
		t.Errorf("the refusal carried %v as infection facts, want none", result.Infected)
	}
	if result.Outcome != "" {
		t.Errorf("the refusal reported outcome %q, want none: an error carries no facts", result.Outcome)
	}
}

func TestProbeReportsTheMutantsATestInfected(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)
	label := mutantkit.APIByRule(t, prepared.catalog, labelRule)

	widthRun := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestWidth$"},
	})
	if !slices.Contains(widthRun.Infected, width.Index) {
		t.Errorf("probing TestWidth reported %v, want it to hold %d: Width() returns 3 and its mutant returns 0",
			widthRun.Infected, width.Index)
	}
	if slices.Contains(widthRun.Infected, label.Index) {
		t.Errorf("probing TestWidth reported %v, which holds %d although the test never calls Label",
			widthRun.Infected, label.Index)
	}
	if !slices.IsSorted(widthRun.Infected) {
		t.Errorf("infected = %v, want ascending catalogue indices", widthRun.Infected)
	}
	if len(slices.Compact(slices.Clone(widthRun.Infected))) != len(widthRun.Infected) {
		t.Errorf("infected = %v, want each index once", widthRun.Infected)
	}

	labelRun := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestLabel$"},
	})
	if !slices.Contains(labelRun.Infected, label.Index) {
		t.Errorf("probing TestLabel reported %v, want it to hold %d", labelRun.Infected, label.Index)
	}
	if slices.Contains(labelRun.Infected, width.Index) {
		t.Errorf("probing TestLabel reported %v, which holds %d although the test never calls Width",
			labelRun.Infected, width.Index)
	}
}

func TestProbeNeverReportsAnUnprobedMutant(t *testing.T) {
	prepared := probeable(t)
	unprobed := mutantkit.APIByRule(t, prepared.catalog, doubledRule)
	if unprobed.Probed {
		t.Fatalf("the fixture's effectful statement %s is probed; it is the specimen for the unprobed case",
			unprobed.DisplayID)
	}
	if ready := mutantkit.APIByRule(t, prepared.catalog, readyRule); !ready.Probed {
		t.Fatalf("the fixture's boolean literal %s is not probed either, so the contrast is gone",
			ready.DisplayID)
	}

	whole := probeOf(t, prepared.session, gomutants.ProbeRequest{Package: probeableModule})
	if slices.Contains(whole.Infected, unprobed.Index) {
		t.Errorf("a whole-package probe reported %v, which holds the unprobed mutant %d",
			whole.Infected, unprobed.Index)
	}
	byIndex := make(map[uint32]gomutants.Mutant, len(prepared.catalog.Mutants))
	for _, mutant := range prepared.catalog.Mutants {
		byIndex[mutant.Index] = mutant
	}
	for _, index := range whole.Infected {
		mutant, known := byIndex[index]
		if !known {
			t.Errorf("the probe reported index %d, which names no catalogued mutant", index)
			continue
		}
		if !mutant.Probed {
			t.Errorf("the probe reported %s, which is not probed", mutant.DisplayID)
		}
	}
}

func TestEveryKillIsPrecededByAnInfection(t *testing.T) {
	prepared := probeable(t)
	tests := []string{"TestWidth", "TestLabel", "TestReady", "TestDoubled", "TestFlagged"}

	probedKills := 0
	for _, name := range tests {
		selector := "-test.run=^" + name + "$"
		measured := probeOf(t, prepared.session, gomutants.ProbeRequest{
			Package: probeableModule,
			Args:    []string{selector},
		})
		for _, mutant := range prepared.catalog.Mutants {
			executed, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
				Mutant:  mutant.ID,
				Package: probeableModule,
				Args:    []string{selector},
			})
			if err != nil {
				t.Fatalf("executing %s against %s: %v", mutant.DisplayID, name, err)
			}
			if executed.Outcome != gomutants.OutcomeKilled {
				continue
			}
			if !mutant.Probed {
				continue
			}
			probedKills++
			if !slices.Contains(measured.Infected, mutant.Index) {
				t.Errorf("%s kills %s, but probing it reported %v, which does not hold %d:"+
					" a consumer would have skipped the execution that finds this kill",
					name, mutant.DisplayID, measured.Infected, mutant.Index)
			}
		}
	}
	if probedKills == 0 {
		t.Error("no probed mutant was killed by any test, so the soundness statement held vacuously")
	}
}

func TestProbeOfAFailingTestCarriesNoFacts(t *testing.T) {
	prepared := probeable(t)
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestFlagged$"},
		Env:     []string{"PROBEABLE_FAIL=yes"},
	})
	if err != nil {
		t.Fatalf("probing a failing target: %v", err)
	}
	if result.Outcome != gomutants.ProbeTestFailed {
		t.Errorf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeTestFailed, result.Output)
	}
	if result.Infected != nil {
		t.Errorf("infected = %v, want nil: a failing target proves nothing about infection", result.Infected)
	}
	if result.ExitCode == 0 {
		t.Errorf("exit code = 0 for a target reported as failed")
	}
	if !strings.Contains(string(result.Output), "TestFlagged") {
		t.Errorf("output = %q, want the failing target's own output", result.Output)
	}
}

func TestProbeReturnsSuccessfulOutputAndCoverage(t *testing.T) {
	prepared := probeable(t)
	profile := filepath.Join(t.TempDir(), "coverage.out")
	result := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args: []string{
			"-test.run=^TestWidth$",
			"-test.coverprofile=" + profile,
		},
	})
	if !bytes.Contains(result.Output, []byte("PASS")) {
		t.Fatalf("output = %q", result.Output)
	}
	if info, err := os.Stat(profile); err != nil || info.Size() == 0 {
		t.Fatalf("coverage profile = (%v, %v)", info, err)
	}
}

func TestProbeHonoursOutputLimit(t *testing.T) {
	prepared := probeable(t)
	const limit = 4096
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package:     probeableModule,
		Args:        []string{"-test.run=^TestPrintsALot$"},
		OutputLimit: limit,
	})
	if err != nil {
		t.Fatalf("probing the chatty target: %v", err)
	}
	if result.Outcome != gomutants.ProbeMeasured {
		t.Fatalf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeMeasured, result.Output)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false although the target prints 64 KiB into a %d-byte budget", limit)
	}
	if result.TotalBytes <= limit {
		t.Errorf("TotalBytes = %d, want more than the %d-byte budget", result.TotalBytes, limit)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most %d", len(result.Output), limit)
	}
	if !bytes.HasPrefix(result.Output, []byte(gomutants.OutputTruncatedPrefix)) {
		t.Errorf("Output begins %q, want the exported prefix %q",
			string(result.Output[:min(len(result.Output), 120)]), gomutants.OutputTruncatedPrefix)
	}
}

func TestPreparedExecutionsExposeOnlyPristineSource(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIMutantAt(t, prepared.catalog, "probeable.go", widthRule)
	probe := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestSourceTreeIsPristine$"},
	})
	if len(probe.Infected) != 0 {
		t.Fatalf("source audit infected mutants = %v", probe.Infected)
	}
	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  width.ID,
		Package: probeableModule,
		Args:    []string{"-test.run=^TestSourceTreeIsPristine$"},
	})
	if err != nil || result.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("source audit against mutant = (%+v, %v)", result, err)
	}
}

func TestPreparedExecutionsPropagateOverlayToChildGoTest(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIMutantAt(t, prepared.catalog, "probeable.go", widthRule)
	probe := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestChildGoTestUsesSessionOverlay$"},
		Env:     []string{"PROBEABLE_CHILD_GO_TEST=parent"},
	})
	if !slices.Contains(probe.Infected, width.Index) {
		t.Fatalf("child go test infected mutants = %v, want %d", probe.Infected, width.Index)
	}
	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  width.ID,
		Package: probeableModule,
		Args:    []string{"-test.run=^TestChildGoTestUsesSessionOverlay$"},
		Env:     []string{"PROBEABLE_CHILD_GO_TEST=parent"},
	})
	if err != nil || result.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("child go test against mutant = (%+v, %v)", result, err)
	}
}

func TestProbeOfATimedOutTestCarriesNoFacts(t *testing.T) {
	prepared := probeable(t)
	started := time.Now()
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestBlocks$"},
		Env:     []string{"PROBEABLE_BLOCK=yes"},
		Timeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("probing a blocking target: %v", err)
	}
	if result.Outcome != gomutants.ProbeTimedOut {
		t.Errorf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeTimedOut, result.Output)
	}
	if result.Infected != nil {
		t.Errorf("infected = %v, want nil: a target that was killed proves nothing", result.Infected)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("the probe returned after %s, want prompt process-tree cleanup", elapsed)
	}
}

func TestProbeOfABinaryWithoutTheRuntimeMeasuresNothing(t *testing.T) {
	prepared := probeable(t)
	result := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule + "/isolated",
	})
	if len(result.Infected) != 0 {
		t.Errorf("infected = %v, want the empty set: the package links no instrumented file", result.Infected)
	}
}

func TestProbeRefusesTheSameRequestsAsExec(t *testing.T) {
	prepared := probeable(t)
	cases := []struct {
		name    string
		request gomutants.ProbeRequest
	}{
		{
			name:    "a package with no prepared test binary",
			request: gomutants.ProbeRequest{Package: "fixture.example/probeable/absent"},
		},
		{
			name: "a target that overrides the harness timeout",
			request: gomutants.ProbeRequest{
				Package: probeableModule,
				Args:    []string{"-test.timeout=1s"},
			},
		},
		{
			name: "an activation variable the session reserves",
			request: gomutants.ProbeRequest{
				Package: probeableModule,
				Env:     []string{"GO_MUTANTS_ACTIVE=stolen"},
			},
		},
		{
			name:    "a negative timeout",
			request: gomutants.ProbeRequest{Package: probeableModule, Timeout: -time.Second},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := prepared.session.Probe(t.Context(), c.request)
			if err == nil {
				t.Fatalf("Probe accepted %+v and answered %+v", c.request, result)
			}
			if result.Infected != nil {
				t.Errorf("the refusal carried %v as infection facts, want none", result.Infected)
			}
		})
	}
}

func TestProbeIsSafeConcurrently(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)

	const workers = 8
	var wait sync.WaitGroup
	failures := make([]error, workers)
	infected := make([]bool, workers)
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if worker%2 == 0 {
				result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
					Package: probeableModule,
					Args:    []string{"-test.run=^TestWidth$"},
				})
				if err != nil {
					failures[worker] = err
					return
				}
				if result.Outcome != gomutants.ProbeMeasured {
					failures[worker] = errors.New("probe outcome " + string(result.Outcome))
					return
				}
				infected[worker] = slices.Contains(result.Infected, width.Index)
				return
			}
			result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
				Mutant:  width.ID,
				Package: probeableModule,
				Args:    []string{"-test.run=^TestWidth$"},
			})
			if err != nil {
				failures[worker] = err
				return
			}
			if result.Outcome != gomutants.OutcomeKilled {
				failures[worker] = errors.New("exec outcome " + string(result.Outcome))
			}
		}()
	}
	wait.Wait()

	for worker, err := range failures {
		if err != nil {
			t.Errorf("worker %d: %v", worker, err)
		}
	}
	for worker := 0; worker < workers; worker += 2 {
		if !infected[worker] {
			t.Errorf("worker %d probed TestWidth without reporting the mutant it infects", worker)
		}
	}
}

func TestCloseRemovesTheProbeTree(t *testing.T) {
	root := copyFixture(t, "probeable")
	parent := t.TempDir()
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{Probe: true})
	if err != nil {
		t.Fatalf("preparing a probe session: %v", err)
	}
	if snapshots := snapshotDirectories(t, parent); len(snapshots) != 2 {
		t.Fatalf("a probe session left %d snapshots (%v), want the mutant tree and the probe tree",
			len(snapshots), snapshots)
	}
	if err = session.Close(); err != nil {
		t.Fatalf("closing the session: %v", err)
	}
	if snapshots := snapshotDirectories(t, parent); len(snapshots) != 1 {
		t.Errorf("closing the session left %d snapshots (%v), want only the workspace's own",
			len(snapshots), snapshots)
	}
	if err = workspace.Close(); err != nil {
		t.Fatalf("closing the workspace: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading the temporary parent: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the temporary parent still holds %v after Close", entries)
	}
}

const keptSessionTarget = "TestCatalogInvariants/with_rejections"

const keptSessionTimeout = 5 * time.Minute

func TestAKeptPackageScratchHoldsTheSessionsTrees(t *testing.T) {
	t.Parallel()

	cache, err := testkit.BuildCache()
	if err != nil {
		t.Fatalf("resolving the test build cache for the child: %v", err)
	}
	kept := filepath.Join(testkit.Scratch(t), "kept")
	env := append(testkit.Compose(t, testkit.Scratch(t)),
		testkit.KeepEnv+"=always",
		testkit.KeepDirEnv+"="+kept,
		testkit.BuildCacheEnv+"="+cache,
	)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), keptSessionTimeout)
	defer cancel()
	result := testkit.ExecContext(ctx, t, testkit.Root(t), env, testkit.HelperArgv(keptSessionTarget)...)
	testkit.RequireExit(t, result, 0, "a child preparing one shared session under the keep policy")

	var snapshots []string
	err = filepath.WalkDir(kept, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "go-mutants-snap-") {
			snapshots = append(snapshots, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the kept root %s: %v", kept, err)
	}
	if len(snapshots) == 0 {
		t.Errorf("the kept package scratch holds no snapshot, so a reader gets the fixture copy and "+
			"nothing the session actually ran:\n%s", strings.Join(testkit.Entries(t, kept), "\n"))
	}
}
