// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestModuleDirectoryRefusesEscapesAndNonDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	inside, err := moduleDirectory(root, "./inside")
	if err != nil {
		t.Fatalf("inside directory: %v", err)
	}
	if !samePath(inside, filepath.Join(root, "inside")) {
		t.Errorf("inside = %q, want %q", inside, filepath.Join(root, "inside"))
	}
	for _, invalid := range []string{"../outside", filepath.Join(root, "inside"), "file"} {
		if _, err := moduleDirectory(root, invalid); err == nil {
			t.Errorf("moduleDirectory(%q) succeeded, want refusal", invalid)
		}
	}
}

func TestEnvironmentOverlayProtectsHarnessVariables(t *testing.T) {
	base := []string{"PATH=one", "KEEP=old", "TMP=/engine/tmp"}
	env, err := overlayEnvironment(base, []string{"KEEP=new", "ADDED=value"})
	if err != nil {
		t.Fatalf("overlaying ordinary variables: %v", err)
	}
	if got := environmentValue(env, "KEEP"); got != "new" {
		t.Errorf("KEEP = %q, want new", got)
	}
	if got := environmentValue(env, "ADDED"); got != "value" {
		t.Errorf("ADDED = %q, want value", got)
	}
	for _, entry := range []string{"GO_MUTANTS_ACTIVE=stolen", "go_mutants_other=stolen", "TMP=stolen", "TEMP=stolen", "TMPDIR=stolen"} {
		if _, err := overlayEnvironment(base, []string{entry}); err == nil {
			t.Errorf("overlay accepted reserved %q", entry)
		}
	}
}

func TestSanitiseEnvironmentFreezesACleanScratch(t *testing.T) {
	source := []string{
		"KEEP=value",
		"GO_MUTANTS_ACTIVE=from-parent",
		"go_mutants_other=from-parent",
		"TMP=old",
		"TEMP=old",
		"TMPDIR=old",
	}
	env := sanitiseEnvironment(source, "/session/scratch")
	if got := environmentValue(env, "KEEP"); got != "value" {
		t.Errorf("KEEP = %q, want value", got)
	}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if reservedEnvironment(key) {
			t.Errorf("reserved variable survived: %q", entry)
		}
	}
	for _, key := range temporaryKeys {
		if got := environmentValue(env, key); got != "/session/scratch" {
			t.Errorf("%s = %q, want session scratch", key, got)
		}
	}
}

func TestChangesAreSortedAndNoticeContentAndMode(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.txt", "before")
	write("removed.txt", "gone")
	before, err := scanFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	write("b.txt", "after")
	write("a.txt", "added")
	if removeErr := os.Remove(filepath.Join(root, "removed.txt")); removeErr != nil {
		t.Fatal(removeErr)
	}
	session := &Session{root: root, preparedFiles: before}
	changes, err := session.Changes()
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(changes))
	for i, change := range changes {
		paths[i] = change.Path
	}
	want := []string{"a.txt", "b.txt", "removed.txt"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	if changes[0].Kind != ChangeAdded || changes[1].Kind != ChangeModified || changes[2].Kind != ChangeRemoved {
		t.Errorf("kinds = %v, want added/modified/removed", changes)
	}
	if runtime.GOOS != "windows" {
		if chmodErr := os.Chmod(filepath.Join(root, "b.txt"), 0o600); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		changes, err = session.Changes()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(changes, func(change Change) bool {
			return change.Path == "b.txt" && change.Kind == ChangeModified
		}) {
			t.Errorf("mode-only change was not reported: %v", changes)
		}
	}
}

func TestConcurrentWorkspaceCloseWaitsForTheSameCleanup(t *testing.T) {
	session := &Session{}
	session.mu.RLock()
	locked := true
	defer func() {
		if locked {
			session.mu.RUnlock()
		}
	}()

	workspace := &Workspace{session: session}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- workspace.Close()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		workspace.mu.Lock()
		closing := workspace.closed
		workspace.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first Close did not begin")
		}
		runtime.Gosched()
	}

	// Hold the workspace mutex until the second goroutine has entered Close.
	// Once released, the old implementation returned immediately on closed
	// even though the first caller was still blocked in Session.Close.
	workspace.mu.Lock()
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- workspace.Close()
	}()
	<-secondStarted
	workspace.mu.Unlock()

	secondReturned := false
	select {
	case err := <-secondDone:
		secondReturned = true
		t.Errorf("second Close returned before cleanup completed: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	session.mu.RUnlock()
	locked = false
	waits := map[string]<-chan error{"first": firstDone}
	if !secondReturned {
		waits["second"] = secondDone
	}
	for name, done := range waits {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s Close: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s Close did not finish", name)
		}
	}
}

func TestSessionTargetArgsRecognisesBothStandardFlagPrefixes(t *testing.T) {
	for _, argument := range []string{"-test.fuzz=FuzzX", "--test.fuzz=FuzzX"} {
		t.Run(argument, func(t *testing.T) {
			got, err := sessionTargetArgs([]string{argument}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(got, func(value string) bool {
				return strings.HasPrefix(value, "-test.fuzzcachedir=")
			}) {
				t.Errorf("args = %v, want the session-owned fuzz cache", got)
			}
		})
	}

	for _, argument := range []string{
		"-test.fuzzcachedir=stolen",
		"--test.fuzzcachedir=stolen",
		"-test.fuzzworker",
		"--test.fuzzworker",
	} {
		t.Run(argument, func(t *testing.T) {
			if _, err := sessionTargetArgs([]string{argument}, t.TempDir()); err == nil {
				t.Errorf("sessionTargetArgs accepted reserved %q", argument)
			}
		})
	}
}

func environmentValue(env []string, name string) string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && environmentKeyEqual(key, name) {
			return value
		}
	}
	return ""
}
