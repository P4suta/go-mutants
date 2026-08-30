// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
)

func TestPublicSessionSelectsOnlyMutantsOnChangedLines(t *testing.T) {
	root := copyFixture(t, "killable")
	git(t, root, "init", "--quiet")
	git(t, root, "add", "--all")
	git(t, root, "commit", "--quiet", "--message", "base")

	path := filepath.Join(root, "clamp.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(source), "if v > lo {", "if v > lo { // changed", 1)
	if changed == string(source) {
		t.Fatal("fixture no longer contains the comparison this test edits")
	}
	if writeErr := os.WriteFile(path, []byte(changed), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
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
		Operators:  []string{"comparison"},
		Changed:    true,
		ChangedRef: "HEAD",
	})
	if err != nil {
		t.Fatalf("preparing changed session: %v", err)
	}

	catalog := session.Catalog()
	want := catalogMutant(t, catalog, "clamp.go", "gt-to-ge")
	if !want.Accepted {
		t.Fatalf("changed mutant was not selected: %+v", want)
	}
	for _, location := range []struct{ path, rule string }{
		{path: "clamp.go", rule: "lt-to-le"},
		{path: "untested.go", rule: "neq-to-eq"},
	} {
		mutant := catalogMutant(t, catalog, location.path, location.rule)
		if mutant.Accepted {
			t.Errorf("unchanged mutant %s/%s was selected", location.path, location.rule)
		}
		if _, execErr := session.Exec(t.Context(), gomutants.ExecRequest{Mutant: mutant.ID}); execErr == nil {
			t.Errorf("Session.Exec accepted unchanged mutant %s/%s", location.path, location.rule)
		} else if !strings.Contains(execErr.Error(), "not selected") {
			t.Errorf("unchanged mutant error = %q, want selection context", execErr)
		}
	}
}

func TestPublicSessionReusesOnePreparedSnapshot(t *testing.T) {
	root := copyFixture(t, "killable")
	extraTest := `package killable

import (
	"os"
	"testing"
	"time"
)

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
		if _, err := os.Stat("session-artifact.txt"); err == nil {
			t.Fatal("fuzz target inherited an artifact from an earlier target")
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
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
	time.Sleep(10 * time.Second)
}
`
	if err := os.WriteFile(filepath.Join(root, "session_test.go"), []byte(extraTest), 0o644); err != nil {
		t.Fatal(err)
	}

	parent := t.TempDir()
	env := append(os.Environ(),
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
		Env:  []string{"EXPECT_CLEAN=yes"},
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
	clamp := findMutant(t, catalog, "clamp.go", "lt-to-le")
	untested := findMutant(t, catalog, "untested.go", "neq-to-eq")
	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking the freshly prepared snapshot: %v", changesErr)
	} else if len(changes) != 0 {
		t.Fatalf("freshly prepared snapshot already changed: %+v", changes)
	}

	timeoutStarted := time.Now()
	timedOut, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("executing a bounded target: %v", err)
	}
	if timedOut.Outcome != gomutants.OutcomeTimedOut || timedOut.KilledBy != "fixture.example/killable" {
		t.Errorf("bounded target result = %+v, want a package-local timeout", timedOut)
	}
	if elapsed := time.Since(timeoutStarted); elapsed > 5*time.Second {
		t.Errorf("bounded target returned after %s, want prompt process-tree cleanup", elapsed)
	}

	cancelContext, cancel := context.WithCancel(t.Context())
	cancelTimer := time.AfterFunc(250*time.Millisecond, cancel)
	cancelStarted := time.Now()
	cancelled, cancelErr := session.Exec(cancelContext, gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 30 * time.Second,
	})
	cancelTimer.Stop()
	cancel()
	if !errors.Is(cancelErr, context.Canceled) {
		t.Errorf("cancel error = %v, want context.Canceled", cancelErr)
	}
	if cancelled.Outcome != gomutants.OutcomeNotRun {
		t.Errorf("cancelled target result = %+v, want not_run", cancelled)
	}
	if elapsed := time.Since(cancelStarted); elapsed > 5*time.Second {
		t.Errorf("cancelled target returned after %s, want prompt process-tree cleanup", elapsed)
	}

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
			"-test.fuzztime=10x",
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
		Env:     []string{"WRITE_SNAPSHOT=yes"},
	})
	if err != nil {
		t.Fatalf("executing write target: %v", err)
	}
	if survived.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("write target result = %+v, want survivor", survived)
	}
	isolatedFuzz, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.DisplayID,
		Package: "fixture.example/killable",
		Args: []string{
			"-test.run=^$",
			"-test.fuzz=^FuzzSessionIdentity$",
			"-test.fuzztime=10x",
		},
	})
	if err != nil {
		t.Fatalf("fuzzing after a target changed the snapshot: %v", err)
	}
	if isolatedFuzz.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("fuzz target after snapshot write = %+v, want an isolated survivor", isolatedFuzz)
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

func findMutant(t *testing.T, catalog gomutants.Catalog, path, rule string) gomutants.Mutant {
	t.Helper()
	mutant := catalogMutant(t, catalog, path, rule)
	if !mutant.Accepted {
		t.Fatalf("mutant %s/%s was rejected", path, rule)
	}
	return mutant
}

func catalogMutant(t *testing.T, catalog gomutants.Catalog, path, rule string) gomutants.Mutant {
	t.Helper()
	for _, mutant := range catalog.Mutants {
		if mutant.Path == path && mutant.Rule == rule {
			return mutant
		}
	}
	t.Fatalf("no %s mutant in %s: %+v", rule, path, catalog.Mutants)
	return gomutants.Mutant{}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	source := filepath.Join("fixtures", name)
	destination := filepath.Join(t.TempDir(), name)
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
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
	if err != nil {
		t.Fatalf("copying fixture: %v", err)
	}
	return destination
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	configuration := []string{
		"-c", "user.name=go-mutants test",
		"-c", "user.email=go-mutants@example.invalid",
		"-c", "commit.gpgsign=false",
		"-c", "core.autocrlf=false",
	}
	command := exec.CommandContext(t.Context(), "git", append(configuration, args...)...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
