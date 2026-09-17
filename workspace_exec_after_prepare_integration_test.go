// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const execWaitProbe = 100 * time.Millisecond

const writeAfterPrepareEnv = "WRITE_AFTER_PREPARE"

const writeAfterPrepareArtifact = "command-artifact.txt"

const writeAfterPrepareTest = `package simple

import (
	"os"
	"testing"
)

func TestWriteAfterPrepare(t *testing.T) {
	if os.Getenv("` + writeAfterPrepareEnv + `") != "yes" {
		return
	}
	if err := os.WriteFile("` + writeAfterPrepareArtifact + `", []byte("written after Prepare"), 0o600); err != nil {
		t.Fatal(err)
	}
}
`

func TestWorkspaceExecAfterPrepareRunsThePristineProgram(t *testing.T) {
	prepared := controlled(t)
	clamp := mutantkit.APIMutantAt(t, prepared.catalog, "clamp.go", "lt-to-le")

	baseline, err := prepared.workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "test", "./..."},
		Env:  []string{"GOWORK=off"},
	})
	if err != nil {
		t.Fatalf("a command after a successful Prepare: %v", err)
	}
	if baseline.TimedOut || baseline.ExitCode != 0 {
		t.Fatalf("go test ./... after Prepare = exit %d timeout=%v, want the pristine program to pass:\n%s",
			baseline.ExitCode, baseline.TimedOut, baseline.Output)
	}

	changes, err := prepared.session.Changes()
	if err != nil {
		t.Fatalf("checking the prepared snapshot after the command: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("the command changed the prepared snapshot: %+v", changes)
	}

	assertTreesAreTheSnapshot(t, prepared)

	killed, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.ID,
		Package: killableModule,
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("executing %s after the command: %v", clamp.DisplayID, err)
	}
	if killed.Outcome != gomutants.OutcomeKilled {
		t.Errorf("%s = %s after a workspace command, want the kill it gives without one:\n%s",
			clamp.DisplayID, killed.Outcome, killed.OutputTail)
	}
}

func TestAVerifiedAndProbedSessionAlsoLeavesTheSnapshotBehind(t *testing.T) {
	prepared := probeable(t)

	built, err := prepared.workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "build", "./..."},
		Env:  []string{"GOWORK=off"},
	})
	if err != nil {
		t.Fatalf("a command after a verified, probed Prepare: %v", err)
	}
	if built.TimedOut || built.ExitCode != 0 {
		t.Fatalf("go build ./... after Prepare = exit %d timeout=%v:\n%s",
			built.ExitCode, built.TimedOut, built.Output)
	}
	if changes, changesErr := prepared.session.Changes(); changesErr != nil {
		t.Fatalf("checking the prepared snapshot after the command: %v", changesErr)
	} else if len(changes) != 0 {
		t.Errorf("the command changed the prepared snapshot: %+v", changes)
	}
	assertTreesAreTheSnapshot(t, prepared)
}

func assertTreesAreTheSnapshot(t *testing.T, prepared *preparedFixture) {
	t.Helper()
	snapshots := snapshotDirectories(t, prepared.parent)
	if len(snapshots) == 0 {
		t.Fatalf("no snapshot directories under %s, so there is no tree to digest", prepared.parent)
	}
	for _, name := range snapshots {
		root := filepath.Join(prepared.parent, name, snapshot.TreeName)
		again, err := snapshot.Create(root, snapshot.Options{DestParent: t.TempDir()})
		if err != nil {
			t.Fatalf("re-freezing %s: %v", root, err)
		}
		t.Cleanup(func() { _ = again.Cleanup() })
		if got, want := again.WorkspaceDigest, prepared.catalog.WorkspaceDigest; got != want {
			t.Errorf("%s digests as %s, want the frozen %s: a command may run after Prepare"+
				" only because the tree it runs against is the snapshot", name, got, want)
		}
	}
}

func TestWorkspaceExecRunsAfterTheSessionIsClosed(t *testing.T) {
	workspace, err := gomutants.Open(t.Context(), copyFixture(t, "simple"),
		gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{SkipVerify: true})
	if err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if closeErr := session.Close(); closeErr != nil {
		t.Fatalf("closing the session: %v", closeErr)
	}

	listed, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "list", "./..."},
		Env:  []string{"GOWORK=off"},
	})
	if err != nil {
		t.Fatalf("a command after the session was closed: %v", err)
	}
	if listed.TimedOut || listed.ExitCode != 0 {
		t.Fatalf("go list ./... after Session.Close = exit %d timeout=%v:\n%s",
			listed.ExitCode, listed.TimedOut, listed.Output)
	}

	if closeErr := workspace.Close(); closeErr != nil {
		t.Fatalf("closing the workspace: %v", closeErr)
	}
	if _, execErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "version"},
	}); !errors.Is(execErr, gomutants.ErrWorkspaceClosed) {
		t.Errorf("Exec after Workspace.Close = %v, want ErrWorkspaceClosed", execErr)
	}
}

func TestWorkspaceExecAfterFailedPrepareIsRefused(t *testing.T) {
	root := copyFixture(t, "simple")
	if err := os.WriteFile(filepath.Join(root, "write_test.go"), []byte(writeAfterPrepareTest), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	drift, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "test", "-run=^TestWriteAfterPrepare$", "."},
		Env:  []string{"GOWORK=off", writeAfterPrepareEnv + "=yes"},
	})
	if err != nil || drift.TimedOut || drift.ExitCode != 0 {
		t.Fatalf("the drifting command = (%+v, %v)", drift, err)
	}
	_, prepareErr := workspace.Prepare(t.Context(), gomutants.PrepareOptions{SkipVerify: true})
	var refused *gomutants.DriftError
	if !errors.As(prepareErr, &refused) {
		t.Fatalf("Prepare after a drifting command = %v, want the integrity gate's *DriftError",
			prepareErr)
	}

	_, execErr := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "version"}})
	if !errors.Is(execErr, gomutants.ErrPrepareFailed) {
		t.Errorf("Exec after a failed Prepare = %v, want ErrPrepareFailed", execErr)
	}
	if errors.Is(execErr, gomutants.ErrWorkspaceClosed) {
		t.Errorf("Exec after a failed Prepare = %v, which also reads as a closed workspace", execErr)
	}
}

func TestWorkspaceExecRunsBesideAPreparationOutsideTheWindow(t *testing.T) {
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	held := holdPreparation(t, context.Background(), workspace,
		gomutants.PrepareOptions{SkipVerify: true},
		insidePhase(gomutants.PreparePhaseDiscovery, gomutants.PrepareEventStarted), nil)
	held.awaitHeld(t)

	execDone := make(chan error, 1)
	go func() {
		result, execErr := workspace.Exec(context.Background(), gomutants.Command{
			Argv: []string{"go", "list", "./..."},
			Env:  []string{"GOWORK=off"},
		})
		if execErr == nil && (result.TimedOut || result.ExitCode != 0) {
			execErr = errors.New("the command did not pass: " + string(result.Output))
		}
		execDone <- execErr
	}()
	select {
	case execErr := <-execDone:
		if execErr != nil {
			t.Fatalf("a command beside a held preparation: %v", execErr)
		}
	case <-time.After(commandBound):
		t.Fatalf("a command issued while Prepare was held in its discovery phase had not returned"+
			" after %s: a preparation blocks commands only inside the instrumentation window",
			commandBound)
	}

	held.release()
	prepared := held.await(t)
	if prepared.err != nil {
		t.Fatalf("Prepare: %v", prepared.err)
	}
	if closeErr := prepared.session.Close(); closeErr != nil {
		t.Errorf("closing the session: %v", closeErr)
	}
}

func TestACommandThatWritesAfterPrepareShowsUpInChanges(t *testing.T) {
	root := copyFixture(t, "simple")
	if err := os.WriteFile(filepath.Join(root, "write_test.go"), []byte(writeAfterPrepareTest), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		SkipVerify:    true,
		MutantTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking the freshly prepared snapshot: %v", changesErr)
	} else if len(changes) != 0 {
		t.Fatalf("freshly prepared snapshot already changed: %+v", changes)
	}

	written, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "test", "-run=^TestWriteAfterPrepare$", "."},
		Env:  []string{"GOWORK=off", writeAfterPrepareEnv + "=yes"},
	})
	if err != nil {
		t.Fatalf("the writing command: %v", err)
	}
	if written.TimedOut || written.ExitCode != 0 {
		t.Fatalf("the writing command = exit %d timeout=%v:\n%s",
			written.ExitCode, written.TimedOut, written.Output)
	}

	changes, err := session.Changes()
	if err != nil {
		t.Fatalf("checking the snapshot after the writing command: %v", err)
	}
	if !slices.ContainsFunc(changes, func(change gomutants.Change) bool {
		return change.Kind == gomutants.ChangeAdded && change.Path == writeAfterPrepareArtifact
	}) {
		t.Errorf("changes = %+v, want the %s a workspace command wrote", changes, writeAfterPrepareArtifact)
	}
	if _, statErr := os.Stat(filepath.Join(root, writeAfterPrepareArtifact)); !os.IsNotExist(statErr) {
		t.Errorf("the command wrote into the user's own tree: %v", statErr)
	}

	clamp := mutantkit.APIMutantAt(t, session.Catalog(), "simple.go", "lt-to-le")
	result, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("executing %s after a command wrote into the tree: %v", clamp.DisplayID, err)
	}
	if result.Outcome == "" {
		t.Errorf("%s = %+v, want an outcome: a write into the tree is not a broken session",
			clamp.DisplayID, result)
	}
}
