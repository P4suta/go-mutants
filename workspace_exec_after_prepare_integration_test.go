// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Commands beside a prepared session: what `Workspace.Exec` may do once
// `Workspace.Prepare` has been called.
//
// The rule used to be one line — every command is refused once a preparation
// has begun — and it cost a consumer a whole second workspace to run `go vet`,
// `go build` or a baseline of its own beside a session. It was one line because
// of one worry, that a command would be run against instrumented sources; and
// that worry is answered for the successful case by what preparation already
// does. `main_restoration` puts the pristine sources back and re-digests the
// tree before the binaries are built, so a `Prepare` that *returned a session*
// left the tree byte-identical to the snapshot `Open` froze: the instrumented
// sources live only in the overlay manifest the session owns.
//
// So the rule is now one line per state, and this file is one test per line. A
// command runs beside a preparation in flight — waiting only for the stretch
// where the sources really are rewritten, which
// `workspace_prepare_overlap_integration_test.go` is entirely about — runs
// after one that succeeded, is refused after one that failed — that last
// because a preparation that stopped part-way makes no promise about the tree
// at all — and goes on running after the *session* closes, because a closed
// session releases binaries rather than the tree.
//
// Two more tests are about what the claim rests on and what it costs. The
// byte-identical tree is checked against the session that rewrites the most:
// one prepared with verification and a probe tree, whose *two* trees must both
// come out as the snapshot. And the consequence a consumer owns is stated as a
// fact: a command that writes into the tree after `Prepare` shows up in
// `Session.Changes`, exactly as a `Session.Exec` target's write does.

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

// execWaitProbe is how long a command that must be blocked is watched for
// before the preparation holding it is released.
//
// It can only ever make this test *pass* vacuously, never fail wrongly: the
// preparation holds the workspace's write lock for the whole of the window, so
// an Exec that returned inside it returned without the lock, which is the
// defect. A tenth of a second is long enough to catch that on any machine and
// short enough that a suite measured in tens of seconds does not notice it.
const execWaitProbe = 100 * time.Millisecond

// writeAfterPrepareEnv gates the injected target that writes into the tree. Its
// value, not merely its presence, is what the target reads — the rule
// [sessionBlockEnv] follows and for the same reason.
const writeAfterPrepareEnv = "WRITE_AFTER_PREPARE"

// writeAfterPrepareArtifact is the file that target writes, relative to the
// module root, which is where a workspace command runs.
const writeAfterPrepareArtifact = "command-artifact.txt"

// writeAfterPrepareTest is a target that writes one file into its own working
// directory when it is asked to.
//
// It is injected rather than checked in because `fixtures/` is a clean tree a
// CI gate enforces, and it is gated because everything else that runs this
// package — discovery's own `go list`, a verification, any later command —
// would otherwise move the tree it is supposed to leave alone.
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

// TestWorkspaceExecAfterPrepareRunsThePristineProgram is the feature: a command
// run after a successful preparation runs against the program the user wrote.
//
// Four claims, and the first three are what make the fourth safe to rely on.
// The command exits zero, so the tree it compiled is a program and not a
// half-instrumented one. `Session.Changes` is empty afterwards, so the command
// left the prepared tree exactly as preparation left it. A fresh snapshot of
// that tree carries `Catalog.WorkspaceDigest`, which is the stronger statement:
// the tree is byte-for-byte the one `Open` froze, instrumentation and all put
// back. And the session is untouched by any of it — a mutant that was killable
// before the command is killable after it — which is the thing a consumer
// running `go vet` beside its session is actually buying.
func TestWorkspaceExecAfterPrepareRunsThePristineProgram(t *testing.T) {
	prepared := controlled(t)
	clamp := mutantkit.APIMutantAt(t, prepared.catalog, "clamp.go", "lt-to-le")

	baseline, err := prepared.workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "test", "./..."},
		// The go command searches every parent for a go.work, and the snapshot
		// sits in a temporary directory this test does not own the parents of.
		Env: []string{"GOWORK=off"},
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

	// The digest, not only the absence of changes: Changes compares against
	// what preparation left, while this compares against what Open froze, so it
	// is the claim that the instrumented sources are gone from the tree
	// entirely rather than merely stable since the binaries were built.
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

// TestAVerifiedAndProbedSessionAlsoLeavesTheSnapshotBehind is the same claim
// against the session that does the most rewriting of the two shared ones.
//
// The control fixture skips verification and builds no probe tree, so it
// instruments one tree and restores it once. The probeable session instruments
// *two* — the mutant tree and the probe tree beside it — verifies the
// instrumented program by running the suite through it, and restores both. That
// is three more chances for a rewritten file to be left behind, and the whole
// licence for running a command after `Prepare` is that none of them is. Both
// trees are checked, because the probe tree is where the second restoration
// would fail and `Catalog.WorkspaceDigest` names what they must both be.
//
// The command is a build rather than a test run: what is under test here is the
// state of the trees, and the shared session has already had its suite run
// against it by verification.
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

// assertTreesAreTheSnapshot freezes a fresh copy of every snapshot tree beside
// a prepared session and requires each to digest as `Catalog.WorkspaceDigest`.
//
// A second snapshot is how preparation itself compares two trees — the probe
// tree is only accepted when its digest matches the mutant tree's — so this is
// the same measurement rather than a private one these tests invented, and
// comparing against the catalogue's value is what makes it a claim about the
// tree `Open` froze rather than about two copies agreeing with each other.
//
// It walks every tree rather than picking the mutant one out by name, because
// the digest they must all carry is the same digest, and a session that
// restored one of two trees is exactly the defect this is here to catch.
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

// TestWorkspaceExecRunsAfterTheSessionIsClosed is the last reachable state of
// the lifecycle, and it follows from what the rule is *about*.
//
// A closed session releases its binaries, its scratch and its probe tree; it
// does not touch the workspace's snapshot, which is still the tree `Open`
// froze. So the reason a command runs beside a live session is untouched by the
// session ending, and refusing one here would be refusing on the strength of a
// state that has nothing to do with the tree. Closing the *workspace* is the
// other thing, and `ErrWorkspaceClosed` is what says so.
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

	// The contrast that makes the claim a claim: it is the *workspace* closing
	// that ends commands, not the session.
	if closeErr := workspace.Close(); closeErr != nil {
		t.Fatalf("closing the workspace: %v", closeErr)
	}
	if _, execErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "version"},
	}); !errors.Is(execErr, gomutants.ErrWorkspaceClosed) {
		t.Errorf("Exec after Workspace.Close = %v, want ErrWorkspaceClosed", execErr)
	}
}

// TestWorkspaceExecAfterFailedPrepareIsRefused is the other half of the rule,
// and it is a sentinel rather than a sentence because a consumer has to act on
// it: its workspace is spent and the answer is to open another one.
//
// A failed preparation is refused whatever it failed at. This one fails at the
// integrity gate, before a single source file has been instrumented — the
// cheapest failed preparation the suite knows how to produce — and it is still
// refused, because "which failures left the tree alone" is not a question a
// caller can be asked to answer, and the engine will not answer it either.
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
	// Named rather than merely non-nil: a preparation that failed for some
	// other reason would satisfy the rest of this test while proving nothing
	// about the state it is supposed to be examining.
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

// TestWorkspaceExecRunsBesideAPreparationOutsideTheWindow is the row of the
// table this change rewrote, and it used to say the opposite.
//
// The old rule was that a command waits for the whole of a preparation, and it
// was one lock held for the whole call. But a preparation is only *dangerous*
// while it is rewriting the tree: `main_validation` instruments the sources in
// place and `main_restoration` puts them back, and everywhere else — discovery,
// the probe copy, verification, both builds — the tree is byte for byte the
// snapshot `Open` froze. So the lock now covers the window rather than the
// call, and this is the half that changed: a command issued while a preparation
// is in one of its safe phases runs, instead of queueing behind minutes of
// compilation.
//
// The preparation is stopped at the start of discovery, which is outside the
// window, and a `go list ./...` has to come back on its own. What it must *not*
// do is wait for the release below — a command that did would be blocked for
// exactly as long as the old rule blocked it, which is what a consumer opened a
// second workspace to avoid. [TestACommandThatStartsInsideTheWindowWaitsForIt]
// is the other half, where a command does wait and must.
//
// Every wait in it is bounded, and that is not tidiness. A test that holds a
// preparation and then fails has to be able to *report* the failure: an
// unreleased preparation would sit there until the package's own ten-minute
// budget expired and the whole tier would come back as a panic naming a
// goroutine instead of the assertion that broke. [holdPreparation] owns that,
// and its release is registered as a cleanup before anything can fail.
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

// TestACommandThatWritesAfterPrepareShowsUpInChanges is the consequence the new
// rule hands to the caller, stated as a fact rather than as a warning.
//
// Nothing stops a command run after `Prepare` from writing into the tree, and
// nothing invalidates the session when one does: the overlay still names the
// frozen sources, the binaries are already built, and executions go on
// answering. What changes is the tree every later target runs in, and
// `Session.Changes` is where that shows up — the same call, against the same
// manifest, that already reports what a *target* wrote. A consumer that writes
// there owns the consequences, and this is how it finds out that it did.
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

	// The session is not invalidated by any of it: the overlay still names the
	// frozen sources and the binaries were built before the write, so an
	// execution still answers.
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
