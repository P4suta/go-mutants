// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

const coverageCallerPackage = "fixture.example/coverage/caller"

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

const treeCallerSource = `package caller

import "fixture.example/coverage/core"

func Changed(a, b int) bool { return !core.Differs(a, b) }
`

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

func snapshotRoot(t *testing.T, parent string) string {
	t.Helper()
	snapshots := snapshotDirectories(t, parent)
	if len(snapshots) != 1 {
		t.Fatalf("%s holds %d snapshots, want the one Open froze", parent, len(snapshots))
	}
	return filepath.Join(parent, snapshots[0], snapshot.TreeName)
}

func frozenPath(t *testing.T, parent, relative string) string {
	t.Helper()
	return filepath.Join(snapshotRoot(t, parent), relative)
}

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
