// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// What the test binaries are compiled *from*.
//
// A preparation's window ends at `main_restoration` and the session is
// published after the binaries are built. The whole of that stretch — the
// longest phase of a real preparation — used to read the tree: the overlay
// replaced the instrumented sources and the compiler read every other file
// where it lay. A command writing there had its bytes compiled into the
// binaries, and the re-digest that caught it could only see a write that was
// still there when the build ended; one made and undone while the compiler was
// between two files was compiled in and gone before anything looked.
//
// So the build no longer reads the tree at all. Every file the snapshot froze
// is copied into the preparation's own directory at the top of the window,
// while the tree is held exclusively and has just been proved byte-identical to
// the manifest, and the overlay maps every one of them. The instrumented
// sources keep their own mapping and win, as they must.
//
// The two tests here are the two halves of that claim, and both of them prove
// it the same way: change the tree while the binaries are being built, and read
// the answer out of the *binaries* rather than out of a digest.
package gomutants_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// coverageCallerPackage is the import path of the fixture package that is never
// selected for mutation and is compiled anyway.
const coverageCallerPackage = "fixture.example/coverage/caller"

// treeClampTest replaces the frozen `clamp_test.go` in the tree while the
// binaries are compiled, and it is written so that *either* half of the answer
// gives the defect away.
//
// `TestClamp` keeps its name and loses its assertions, so a binary compiled
// from this file cannot kill the `lt-to-le` mutant that the frozen file kills.
// `TestTheTreeWasCompiledIn` exists only here, so a control run — which starts
// every test in the package with no mutant active — is red the moment this file
// reaches the compiler.
const treeClampTest = `package killable

import "testing"

func TestClamp(t *testing.T) {
	// Deliberately empty: a binary built from this file kills nothing.
	_ = Clamp
}

func TestTheTreeWasCompiledIn(t *testing.T) {
	t.Fatal("the test binaries were compiled from the tree, not from the frozen manifest")
}
`

// treeCallerSource replaces `caller/caller.go` — a package no include pattern
// selects and no mutant lives in — while the binaries are compiled. Its own
// package's test is what notices: `Changed` now answers the opposite of what
// `TestChanged` requires.
const treeCallerSource = `package caller

import "fixture.example/coverage/core"

func Changed(a, b int) bool { return !core.Differs(a, b) }
`

// TestAWriteDuringTheBinaryBuildIsNotCompiledIn is the feature.
//
// A command writes a source of the frozen tree while the test binaries are
// being compiled, and leaves it there. That used to fail the preparation, with
// `Stage: "test binaries"`, and the refusal was the honest thing to do while
// the compiler was reading the tree — but it was also one transient write short
// of sound, because a write undone before the build ended was compiled in and
// invisible. The build now reads the frozen copies instead, so there is nothing
// left for a re-digest to protect and the write is what it is: a change to the
// tree, made after the tree stopped being an input, reported by
// `Session.Changes` and refused by nothing.
//
// Three claims, and the last two are what make the first one worth having. The
// preparation *succeeds*. `Session.Changes` names the file, so the write is not
// silently swallowed — the drift it used to be is now a change the caller is
// told about. And the binaries behave as the frozen program: a control passes,
// which it cannot do if the file in the tree reached the compiler, and the
// mutant the frozen `TestClamp` kills is still killed, which it cannot be if
// the assertion-free `TestClamp` in the tree reached it instead.
//
// The write comes from the phase callback rather than from a `Workspace.Exec`
// child, exactly as `TestCommandDriftDuringDiscoveryFailsPrepare` writes: the
// bytes appearing in the frozen tree are all the build can see, and a callback
// that started a command would be the preparation's own goroutine waiting on a
// lock a queued `Close` could take first.
func TestAWriteDuringTheBinaryBuildIsNotCompiledIn(t *testing.T) {
	parent := t.TempDir()
	root := copyFixture(t, "killable")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	frozen := frozenPath(t, parent, "clamp_test.go")
	writeErr := make(chan error, 1)
	session, err := workspace.Prepare(context.Background(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		SkipVerify:    true,
		MutantTimeout: 30 * time.Second,
		Trace: func(event gomutants.PrepareEvent) {
			if event.Phase != gomutants.PreparePhaseBinaryBuild ||
				event.State != gomutants.PrepareEventStarted {
				return
			}
			select {
			case writeErr <- os.WriteFile(frozen, []byte(treeClampTest), 0o644):
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Prepare while a command rewrote a test file during the binary build = %v,"+
			" want a session: the build no longer reads the tree", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("the write that was meant to move the tree: %v", err)
		}
	default:
		t.Fatal("the binary build never started, so nothing was written and this test proves nothing")
	}

	changes, err := session.Changes()
	if err != nil {
		t.Fatalf("Session.Changes: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("Changes = %+v, want the one file the write left behind", changes)
	}
	if changes[0].Kind != gomutants.ChangeModified || changes[0].Path != "clamp_test.go" {
		t.Errorf("Changes[0] = %+v, want clamp_test.go modified", changes[0])
	}

	control, err := session.Control(t.Context(), gomutants.ControlRequest{Package: killableModule})
	if err != nil {
		t.Fatalf("Session.Control: %v", err)
	}
	if control.TimedOut || control.ExitCode != 0 {
		t.Errorf("Control = exit %d timeout=%v, want the frozen suite to pass: the file in the"+
			" tree was compiled into the binaries\n%s", control.ExitCode, control.TimedOut, control.Output)
	}

	clamp := mutantkit.APIMutantAt(t, session.Catalog(), "clamp.go", "lt-to-le")
	killed, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.ID,
		Package: killableModule,
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("executing %s: %v", clamp.DisplayID, err)
	}
	if killed.Outcome != gomutants.OutcomeKilled {
		t.Errorf("%s = %s, want the kill the frozen TestClamp gives: the assertion-free TestClamp"+
			" in the tree was compiled into the binaries\n%s",
			clamp.DisplayID, killed.Outcome, killed.OutputTail)
	}
}

// TestTheOverlayNamesEveryGoFileOfTheModule is the other half: the file set.
//
// A tested package imports module packages an include pattern never selected,
// and a test file is never instrumented at all, so an overlay built from the
// *instrumented* sources alone leaves most of a real module being read off the
// disk. The overlay therefore names every file the snapshot froze — every Go
// file of every package, the test files, `go.mod`, `go.sum` and everything else
// a build can reach — and the instrumented sources are the mapping that wins
// where the two meet.
//
// The fixture is chosen for the gap: mutation is narrowed to `./core` and it is
// `caller`, which holds no mutant and is selected by nothing, whose source is
// rewritten while the binaries build. `caller`'s own test is what would notice,
// and it does not, because the compiler never saw the rewrite.
func TestTheOverlayNamesEveryGoFileOfTheModule(t *testing.T) {
	parent := t.TempDir()
	root := copyFixture(t, "coverage")
	recorded := trace.NewMemorySink(0)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Trace:         recorded,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	frozen := frozenPath(t, parent, filepath.Join("caller", "caller.go"))
	writeErr := make(chan error, 1)
	session, err := workspace.Prepare(context.Background(), gomutants.PrepareOptions{
		DiscoveryPackages: []string{"./core"},
		Operators:         []string{"comparison"},
		SkipVerify:        true,
		MutantTimeout:     30 * time.Second,
		Trace: func(event gomutants.PrepareEvent) {
			if event.Phase != gomutants.PreparePhaseBinaryBuild ||
				event.State != gomutants.PrepareEventStarted {
				return
			}
			select {
			case writeErr <- os.WriteFile(frozen, []byte(treeCallerSource), 0o644):
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Prepare while a command rewrote an unselected package during the binary build"+
			" = %v, want a session", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("the write that was meant to move the tree: %v", err)
		}
	default:
		t.Fatal("the binary build never started, so nothing was written and this test proves nothing")
	}

	// Every file of the frozen tree is named by the overlay, and every backing
	// file is the preparation's own rather than the tree's.
	replace := overlayReplacements(t, session.OverlayManifest())
	treeRoot := snapshotRoot(t, parent)
	for _, relative := range frozenFiles(t, treeRoot) {
		backing, mapped := replace[filepath.Join(treeRoot, relative)]
		if !mapped {
			t.Errorf("the overlay does not name %s, so the compiler read it off the disk", relative)
			continue
		}
		if strings.HasPrefix(backing, treeRoot+string(filepath.Separator)) {
			t.Errorf("the overlay maps %s to %s, which is inside the tree", relative, backing)
		}
	}

	control, err := session.Control(t.Context(), gomutants.ControlRequest{Package: coverageCallerPackage})
	if err != nil {
		t.Fatalf("Session.Control: %v", err)
	}
	if control.TimedOut || control.ExitCode != 0 {
		t.Errorf("Control of %s = exit %d timeout=%v, want the frozen package to pass: the"+
			" rewritten caller.go was compiled into the binaries\n%s",
			coverageCallerPackage, control.ExitCode, control.TimedOut, control.Output)
	}

	changes, err := session.Changes()
	if err != nil {
		t.Fatalf("Session.Changes: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != filepath.ToSlash(filepath.Join("caller", "caller.go")) {
		t.Errorf("Changes = %+v, want the one unselected source the write left behind", changes)
	}
	assertFreezeStageWasRecorded(t, recorded.Events())
}

// assertFreezeStageWasRecorded requires the copy to be in the recording.
//
// The copy is a whole-tree read inside the exclusive window — time a command
// waits for — and it is not a `PreparePhase`, because that vocabulary is
// goatest's and closed. A trace stage is where it belongs instead, and a
// preparation that spent a third of itself copying has to be able to say so.
func assertFreezeStageWasRecorded(t *testing.T, events []trace.Event) {
	t.Helper()
	var started, finished *trace.StageRecord
	for _, event := range events {
		if event.Stage == nil || event.Stage.Name != "freeze-build-inputs" {
			continue
		}
		switch event.Stage.State {
		case trace.StateStarted:
			started = event.Stage
		case trace.StateFinished:
			finished = event.Stage
		}
	}
	if started == nil || finished == nil {
		t.Fatalf("the recording holds %v/%v of the freeze-build-inputs stage, want both:"+
			" a whole-tree copy inside the window that no reader can see", started, finished)
	}
	if !strings.Contains(started.Detail, "files") || !strings.Contains(started.Detail, "bytes") {
		t.Errorf("the stage detail is %q, want the file count and byte total of the copy",
			started.Detail)
	}
	if finished.Result != trace.ResultSucceeded {
		t.Errorf("the stage finished as %q, want %q", finished.Result, trace.ResultSucceeded)
	}
	if finished.DurationMS == nil {
		t.Error("the finished stage carries no duration, so nothing can say what the copy cost")
	}
}

// snapshotRoot is the one snapshot tree under a workspace's temporary parent.
func snapshotRoot(t *testing.T, parent string) string {
	t.Helper()
	snapshots := snapshotDirectories(t, parent)
	if len(snapshots) != 1 {
		t.Fatalf("%s holds %d snapshots, want the one Open froze", parent, len(snapshots))
	}
	return filepath.Join(parent, snapshots[0], snapshot.TreeName)
}

// frozenPath is one file of the frozen tree, by its module-relative path.
func frozenPath(t *testing.T, parent, relative string) string {
	t.Helper()
	return filepath.Join(snapshotRoot(t, parent), relative)
}

// frozenFiles are every regular file under the frozen tree, module-relative.
func frozenFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, relative)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	slices.Sort(files)
	return files
}

// overlayReplacements is the `Replace` map of a `go` overlay manifest.
func overlayReplacements(t *testing.T, manifest string) map[string]string {
	t.Helper()
	if manifest == "" {
		t.Fatal("the session has no overlay manifest")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("reading the overlay manifest: %v", err)
	}
	var overlay struct {
		Replace map[string]string `json:"Replace"`
	}
	if err := json.Unmarshal(data, &overlay); err != nil {
		t.Fatalf("parsing the overlay manifest: %v", err)
	}
	return overlay.Replace
}

// TestAFailedPreparationLeavesNoFrozenCopyBehind is the cost of taking the copy
// early, paid where it is incurred.
//
// The copy is taken at the top of the instrumentation window, which is well
// before a preparation can be sure it will finish: verification runs after the
// window and a repository whose own suite is red fails there. The session that
// would have owned the scratch directory is never returned, so nothing but this
// knows the copy exists — and it is a whole copy of somebody's module, held
// until `Workspace.Close`. A preparation that gives up therefore removes it, on
// the rule the probe tree already follows.
func TestAFailedPreparationLeavesNoFrozenCopyBehind(t *testing.T) {
	parent := t.TempDir()
	root := copyFixture(t, "failing-baseline")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{})
	if session != nil {
		_ = session.Close()
	}
	var verification *gomutants.VerificationError
	if !errors.As(err, &verification) {
		t.Fatalf("Prepare of a repository whose own suite is red = %v, want a *VerificationError", err)
	}
	if left := sessionScratchDirectories(t, parent); len(left) != 0 {
		t.Errorf("a failed preparation left %v behind: a copy of the whole module, owned by a"+
			" session that was never returned", left)
	}
}

// TestAFailedPreparationKeepsItsFrozenCopyWhenTempIsKept is the other half of
// the same rule, and the reason the removal is conditional.
//
// `OpenOptions.KeepTemp` exists so that somebody debugging a preparation can
// look at what it built. The frozen copy is exactly what the binaries would
// have been compiled from, so it is among the most useful things there — and it
// needs no artifact of its own, because it lives inside the workspace's scratch
// directory, which `Workspace.Close` already preserves and records as
// `kept-scratch`.
func TestAFailedPreparationKeepsItsFrozenCopyWhenTempIsKept(t *testing.T) {
	parent := t.TempDir()
	root := copyFixture(t, "failing-baseline")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		KeepTemp:      true,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Close is idempotent, so the explicit Close below that Preserved reads
	// from is unaffected; this one only makes sure a Fatalf between here and
	// there does not leave the workspace holding its directories open when
	// t.TempDir tries to remove them, which on Windows is a failed removal.
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{})
	if session != nil {
		_ = session.Close()
	}
	var verification *gomutants.VerificationError
	if !errors.As(err, &verification) {
		t.Fatalf("Prepare of a repository whose own suite is red = %v, want a *VerificationError", err)
	}
	kept := sessionScratchDirectories(t, parent)
	if len(kept) != 1 {
		t.Fatalf("a kept workspace holds %d session scratch directories, want the one the failed"+
			" preparation made: %v", len(kept), kept)
	}
	// The name is spelled out rather than imported because it is what somebody
	// reading a kept directory sees, and a rename that moved it would be a
	// change to that.
	if _, statErr := os.Stat(filepath.Join(kept[0], "frozen-inputs")); statErr != nil {
		t.Errorf("the kept scratch holds no frozen copy: %v", statErr)
	}
	if closeErr := workspace.Close(); closeErr != nil {
		t.Fatalf("closing the workspace: %v", closeErr)
	}
	preserved := workspace.Preserved()
	if !slices.ContainsFunc(preserved, func(path string) bool {
		return strings.HasPrefix(kept[0], path+string(filepath.Separator))
	}) {
		t.Errorf("Preserved() = %v, want a directory holding the frozen copy at %s",
			preserved, kept[0])
	}
	if _, statErr := os.Stat(filepath.Join(kept[0], "frozen-inputs")); statErr != nil {
		t.Errorf("Close removed the frozen copy a kept workspace was asked to preserve: %v", statErr)
	}
}

// sessionScratchDirectories are the per-session scratch directories under a
// workspace's temporary parent, whichever workspace scratch holds them.
func sessionScratchDirectories(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading %s: %v", parent, err)
	}
	var found []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "go-mutants-api-") {
			continue
		}
		scratch := filepath.Join(parent, entry.Name())
		sessions, readErr := os.ReadDir(scratch)
		if readErr != nil {
			t.Fatalf("reading %s: %v", scratch, readErr)
		}
		for _, session := range sessions {
			if session.IsDir() && strings.HasPrefix(session.Name(), "session-") {
				found = append(found, filepath.Join(scratch, session.Name()))
			}
		}
	}
	slices.Sort(found)
	return found
}
