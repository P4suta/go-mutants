// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestWriteStoresTheRunAndThePointer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	r := buildFixture(t)
	runPath, latestPath, err := report.History{Root: root}.Write(r)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	key := report.WorkspaceKey(r.Workspace.WorkspaceDigest)
	dir := filepath.Join(root, report.WorkspacesDirName, key)
	if want := filepath.Join(dir, report.RunsDirName, r.RunID+".json"); runPath != want {
		t.Errorf("run path = %q, want %q", runPath, want)
	}
	if want := filepath.Join(dir, report.LatestFileName); latestPath != want {
		t.Errorf("latest path = %q, want %q", latestPath, want)
	}

	want := mutantkit.MustMarshal(t, r)
	for _, path := range []string{runPath, latestPath} {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s does not hold the marshalled report", path)
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, report.MarkerFileName))
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	if !strings.Contains(string(marker), r.Workspace.WorkspaceDigest) {
		t.Errorf("the marker does not name the workspace: %q", marker)
	}
}

func TestWorkspaceKeyIsTheHashOfTheDigest(t *testing.T) {
	t.Parallel()

	sum := sha256.Sum256([]byte(fixtureDigest))
	want := hex.EncodeToString(sum[:])[:16]
	if got := report.WorkspaceKey(fixtureDigest); got != want {
		t.Errorf("WorkspaceKey = %q, want %q", got, want)
	}
	if got := report.WorkspaceKey(fixtureDigest); len(got) != 16 {
		t.Errorf("WorkspaceKey is %d characters, want 16", len(got))
	}
	if report.WorkspaceKey(fixtureDigest) == report.WorkspaceKey(strings.Repeat("cd", 32)) {
		t.Error("two workspaces share one key")
	}
}

func TestLatestFollowsTheNewestRun(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	history := report.History{Root: root}

	first := buildFixture(t)
	firstPath, _, err := history.Write(first)
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}

	opts := fixtureOptions(t)
	opts.RunID = "20260218T101500Z-b0b0"
	second, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	secondPath, latestPath, err := history.Write(second)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if firstPath == secondPath {
		t.Fatalf("both runs were written to %s", firstPath)
	}
	if _, statErr := os.Stat(firstPath); statErr != nil {
		t.Errorf("the older run did not survive the newer one: %v", statErr)
	}
	latest, err := os.ReadFile(latestPath)
	if err != nil {
		t.Fatalf("reading the pointer: %v", err)
	}
	if !bytes.Equal(latest, mutantkit.MustMarshal(t, second)) {
		t.Error("latest.json does not hold the newest run")
	}

	runs, err := os.ReadDir(filepath.Dir(firstPath))
	if err != nil {
		t.Fatalf("listing the runs directory: %v", err)
	}
	if len(runs) != 2 {
		t.Errorf("the runs directory holds %d entries, want the two runs", len(runs))
	}
}

func TestWriteLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	r := buildFixture(t)
	runPath, _, err := report.History{Root: root}.Write(r)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := filepath.Dir(filepath.Dir(runPath))
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temporary file survived the write: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the store: %v", err)
	}
}

func TestACrashedWriteDoesNotPoisonTheNextOne(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	r := buildFixture(t)
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(r.Workspace.WorkspaceDigest))
	runs := filepath.Join(dir, report.RunsDirName)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatalf("preparing the store: %v", err)
	}
	stale := filepath.Join(runs, "go-mutants-report-2118496377.tmp")
	if err := os.WriteFile(stale, []byte(`{"document_type": "go-mutants/run-re`), 0o600); err != nil {
		t.Fatalf("writing the leftover: %v", err)
	}

	runPath, latestPath, err := report.History{Root: root}.Write(r)
	if err != nil {
		t.Fatalf("Write over a crashed one: %v", err)
	}
	want := mutantkit.MustMarshal(t, r)
	for _, path := range []string{runPath, latestPath} {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s does not hold the whole report", path)
		}
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("the leftover was removed by a write that did not create it: %v", err)
	}
	if filepath.Base(runPath) != r.RunID+".json" {
		t.Errorf("the run was stored as %q", filepath.Base(runPath))
	}
}

func TestWriteOverAnExistingRunIsAtomic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	r := buildFixture(t)
	history := report.History{Root: root}
	runPath, latestPath, err := history.Write(r)
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}

	garbage := bytes.Repeat([]byte("x"), len(mutantkit.MustMarshal(t, r))*2)
	for _, path := range []string{runPath, latestPath} {
		if writeErr := os.WriteFile(path, garbage, 0o600); writeErr != nil {
			t.Fatalf("truncating %s: %v", path, writeErr)
		}
	}
	if _, _, err = history.Write(r); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	want := mutantkit.MustMarshal(t, r)
	for _, path := range []string{runPath, latestPath} {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s was not replaced whole:\n%s", path, got)
		}
	}
}

func TestWriteRefusesAForeignWorkspace(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
	}{
		{name: "another workspace", content: "go-mutants-workspace-v1\n" + strings.Repeat("cd", 32) + "\n"},
		{name: "another tool", content: "not go-mutants\n"},
		{name: "an empty marker", content: ""},
		{name: "a future marker format", content: "go-mutants-workspace-v2\n" + fixtureDigest + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			r := buildFixture(t)
			dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(r.Workspace.WorkspaceDigest))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("preparing the directory: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, report.MarkerFileName), []byte(c.content), 0o600); err != nil {
				t.Fatalf("writing the foreign marker: %v", err)
			}

			runPath, latestPath, err := report.History{Root: root}.Write(r)
			if code := report.CodeOf(err); code != report.CodeForeignWorkspace {
				t.Fatalf("code = %q, want %q (%v)", code, report.CodeForeignWorkspace, err)
			}
			if runPath != "" || latestPath != "" {
				t.Errorf("a refused write reported paths %q and %q", runPath, latestPath)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("listing the directory: %v", err)
			}
			for _, entry := range entries {
				if entry.Name() != report.MarkerFileName {
					t.Errorf("the refused write left %s behind", entry.Name())
				}
			}
			marker, err := os.ReadFile(filepath.Join(dir, report.MarkerFileName))
			if err != nil {
				t.Fatalf("re-reading the foreign marker: %v", err)
			}
			if string(marker) != c.content {
				t.Errorf("the refused write rewrote the foreign marker as %q, want %q", marker, c.content)
			}
		})
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	t.Parallel()

	const racers = 8
	dir := t.TempDir()
	digests := make([]string, racers)
	for i := range digests {
		digests[i] = fmt.Sprintf("%064x", i+1)
	}

	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = report.Claim(dir, digests[i])
		}()
	}
	close(start)
	wg.Wait()

	marker, err := os.ReadFile(filepath.Join(dir, report.MarkerFileName))
	if err != nil {
		t.Fatalf("reading the marker the race left: %v", err)
	}
	winners := make([]int, 0, 1)
	for i, err := range errs {
		switch {
		case err == nil:
			winners = append(winners, i)
			if !strings.Contains(string(marker), digests[i]) {
				t.Errorf("claim %d succeeded but the marker names somebody else: %q", i, marker)
			}
		case report.CodeOf(err) != report.CodeForeignWorkspace:
			t.Errorf("claim %d = %v, want code %s", i, err, report.CodeForeignWorkspace)
		}
	}
	if len(winners) != 1 {
		t.Errorf("%d of %d concurrent claims succeeded (%v), want exactly one", len(winners), racers, winners)
	}
}

func TestConcurrentClaimsOfOneWorkspaceAllSucceed(t *testing.T) {
	t.Parallel()

	const racers = 8
	dir := t.TempDir()
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = report.Claim(dir, fixtureDigest)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("claim %d of one workspace's own directory = %v, want nil", i, err)
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, report.MarkerFileName))
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	if want := "go-mutants-workspace-v1\n" + fixtureDigest + "\n"; string(marker) != want {
		t.Errorf("the marker the race left is %q, want %q", marker, want)
	}
}

func TestTheFallbackClaimRefusesAnExistingMarker(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), report.MarkerFileName)
	mine := "go-mutants-workspace-v1\n" + fixtureDigest + "\n"
	if err := report.CreateMarkerInPlace(path, mine); err != nil {
		t.Fatalf("creating the marker: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	if string(got) != mine {
		t.Errorf("the marker holds %q, want %q", got, mine)
	}

	theirs := "go-mutants-workspace-v1\n" + strings.Repeat("cd", 32) + "\n"
	err = report.CreateMarkerInPlace(path, theirs)
	if !errors.Is(err, report.ErrMarkerExists) {
		t.Errorf("creating over an existing marker = %v, want the already-claimed answer", err)
	}
	if got, err = os.ReadFile(path); err != nil {
		t.Fatalf("re-reading the marker: %v", err)
	}
	if string(got) != mine {
		t.Errorf("the second create rewrote the marker as %q, want %q", got, mine)
	}
}

func TestClaimingTwiceIsNotAConflict(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := report.Claim(dir, fixtureDigest); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, report.MarkerFileName))
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	if err = report.Claim(dir, fixtureDigest); err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, report.MarkerFileName))
	if err != nil {
		t.Fatalf("re-reading the marker: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the marker changed between claims: %q then %q", before, after)
	}
}

func TestWriteAcceptsItsOwnMarkerAgain(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	history := report.History{Root: root}
	r := buildFixture(t)
	if _, _, err := history.Write(r); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	marker := filepath.Join(root, report.WorkspacesDirName,
		report.WorkspaceKey(r.Workspace.WorkspaceDigest), report.MarkerFileName)
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	if _, _, err = history.Write(r); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("re-reading the marker: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the marker changed between runs: %q then %q", before, after)
	}
}

func TestTwoWorkspacesDoNotShareADirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	history := report.History{Root: root}
	if _, _, err := history.Write(buildFixture(t)); err != nil {
		t.Fatalf("first Write: %v", err)
	}

	opts := fixtureOptions(t)
	opts.WorkspaceDigest = strings.Repeat("cd", 32)
	other, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	otherPath, _, err := history.Write(other)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	workspaces, err := os.ReadDir(filepath.Join(root, report.WorkspacesDirName))
	if err != nil {
		t.Fatalf("listing the workspaces: %v", err)
	}
	if len(workspaces) != 2 {
		t.Errorf("the store holds %d workspaces, want 2", len(workspaces))
	}
	if !strings.Contains(otherPath, report.WorkspaceKey(other.Workspace.WorkspaceDigest)) {
		t.Errorf("the second run was written to %q, outside its own workspace", otherPath)
	}
}

func TestWriteRefusesWhatCannotBeFiled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		break_ func(r *report.Report)
		want   report.Code
	}{
		{
			name:   "a run id that is a path",
			break_: func(r *report.Report) { r.RunID = filepath.Join("..", "..", "escape") },
			want:   report.CodeInvalidRunID,
		},
		{
			name:   "a digest that cannot name a directory",
			break_: func(r *report.Report) { r.Workspace.WorkspaceDigest = "" },
			want:   report.CodeInvalidWorkspaceDigest,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			r := buildFixture(t)
			c.break_(r)
			if _, _, err := (report.History{Root: root}).Write(r); report.CodeOf(err) != c.want {
				t.Fatalf("code = %q, want %q (%v)", report.CodeOf(err), c.want, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("listing the store: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("a refused write created %d entries under the root", len(entries))
			}
		})
	}
}

func TestWriteRefusesNothing(t *testing.T) {
	t.Parallel()

	if _, _, err := (report.History{Root: t.TempDir()}).Write(nil); report.CodeOf(err) != report.CodeNoReport {
		t.Fatalf("code = %q, want %q (%v)", report.CodeOf(err), report.CodeNoReport, err)
	}
}

func TestHistoryCreatesWhatIsMissing(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "cache", "go-mutants")
	if _, _, err := (report.History{Root: root}).Write(buildFixture(t)); err != nil {
		t.Fatalf("Write into a missing store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, report.WorkspacesDirName)); err != nil {
		t.Fatalf("the store was not created: %v", err)
	}
}

func TestWorkspaceDirDoesNotTouchTheDisk(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir, err := (report.History{Root: root}).WorkspaceDir(fixtureDigest)
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	if want := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest)); dir != want {
		t.Errorf("WorkspaceDir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("WorkspaceDir created the directory: %v", err)
	}
}

func TestWriteSaysWhenTheMarkerCannotBeReadBack(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	r := buildFixture(t)
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(r.Workspace.WorkspaceDigest))
	if err := os.MkdirAll(filepath.Join(dir, report.MarkerFileName), 0o700); err != nil {
		t.Fatalf("putting a directory where the marker goes: %v", err)
	}

	runPath, latestPath, err := report.History{Root: root}.Write(r)
	if code := report.CodeOf(err); code != report.CodeHistoryDirectory {
		t.Fatalf("code = %q, want %q (%v)", code, report.CodeHistoryDirectory, err)
	}
	if runPath != "" || latestPath != "" {
		t.Errorf("a refused write reported paths: run=%q latest=%q", runPath, latestPath)
	}
}
