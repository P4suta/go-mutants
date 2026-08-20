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
)

// TestWriteStoresTheRunAndThePointer walks one write end to end: the layout on
// disk, the marker, and the two identical documents.
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

	want := mustMarshal(t, r)
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

// TestWorkspaceKeyIsTheHashOfTheDigest pins the naming rule, which a `report
// list` in another package will have to reproduce.
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

// TestLatestFollowsTheNewestRun proves the pointer moves, that the older run
// stays where it was, and that both remain readable.
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
	if !bytes.Equal(latest, mustMarshal(t, second)) {
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

// TestWriteLeavesNothingBehind proves that the temporary files a write goes
// through are gone by the time it returns.
//
// It matters beyond tidiness: `report list` and `report clean` treat everything
// under runs/ as a run, and a leftover temporary file would be reported as one.
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

// TestACrashedWriteDoesNotPoisonTheNextOne pre-creates the leftovers of an
// interrupted write and proves the next write is unaffected by them.
//
// A killed process leaves a temporary file with a partial document in it. That
// file must not be readable as a run, must not be mistaken for one, and must
// not stop the next run from being stored — which is exactly what a temporary
// name plus a rename buys, and this is the test that says so.
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
	want := mustMarshal(t, r)
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

// TestWriteOverAnExistingRunIsAtomic proves a rewrite replaces the document
// whole.
//
// The file is pre-filled with something that is not a report, which is what a
// half-finished write from a killed process would leave behind. Afterwards it
// is the new document exactly — no leading fragment of the old one, nothing
// appended — because a rename replaces rather than edits.
func TestWriteOverAnExistingRunIsAtomic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	r := buildFixture(t)
	history := report.History{Root: root}
	runPath, latestPath, err := history.Write(r)
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}

	garbage := bytes.Repeat([]byte("x"), len(mustMarshal(t, r))*2)
	for _, path := range []string{runPath, latestPath} {
		if writeErr := os.WriteFile(path, garbage, 0o600); writeErr != nil {
			t.Fatalf("truncating %s: %v", path, writeErr)
		}
	}
	if _, _, err = history.Write(r); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	want := mustMarshal(t, r)
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

// TestWriteRefusesAForeignWorkspace is the paranoia the marker exists for.
//
// The directory is named after 16 hex characters of a hash. That is a
// vanishingly unlikely collision and a perfectly likely one for somebody's
// cache-restoring CI image to manufacture, and either way the answer is a
// refusal with a code rather than two projects' histories interleaved.
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
			// The refusal is only worth anything if the other party's marker is
			// still their marker afterwards: a claim that overwrites the record
			// it was checking has destroyed the evidence for the next run.
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

// TestConcurrentClaimsHaveOneWinner is the race the marker exists for.
//
// Two processes on one machine can reach the same history directory for
// different workspaces at the same instant: 16 hex characters of a hash collide
// eventually, and a CI image that restores one cache into two checkouts
// manufactures it on purpose. Every racer reads no marker, and every racer then
// tries to write one.
//
// The marker can only turn that into a refusal if creating it fails against an
// existing one. Written through a rename this test fails, and fails in the way
// that matters: a rename replaces the destination, so each racer overwrites the
// last, each reads back its own bytes, all of them proceed to interleave runs/,
// and the marker ends up naming whichever of them happened to write last —
// which is what a later `report clean` would believe.
func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	t.Parallel()

	const racers = 8
	dir := t.TempDir()
	digests := make([]string, racers)
	for i := range digests {
		// Distinct, and shaped like the digests a real run carries.
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

// TestConcurrentClaimsOfOneWorkspaceAllSucceed is the other half of the race,
// and the one a careless fix breaks.
//
// Two runs of the same project at the same instant — one CI job with two go
// test invocations, a developer and a watcher — reach one directory with one
// digest, and neither of them is a foreign workspace. The refusal must never
// fire here.
//
// It is a real risk rather than a theoretical one, because a claim written as
// "create the name, then write the contents" leaves a window in which the
// marker exists and is empty. A loser reading it in that window would be told
// its own project's directory belongs to something else. The claim is therefore
// built so that the marker's name never exists without its contents.
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

// TestTheFallbackClaimRefusesAnExistingMarker covers the claim used where the
// filesystem will not hard-link.
//
// It cannot be reached through a claim on any machine these tests run on, and
// it is the path that would quietly matter on the machine where it is: what it
// must not do is replace somebody else's marker, which is the whole property
// the claim exists for.
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

// TestClaimingTwiceIsNotAConflict proves the exclusive create does not turn a
// workspace's own second run into a refusal.
//
// It is the sequential half of the race above: the second claim finds the
// marker, does not try to create one, and recognises it as its own.
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

// TestWriteAcceptsItsOwnMarkerAgain proves the marker is written once and then
// only checked: a second run into the same store is not a foreign one.
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

// TestTwoWorkspacesDoNotShareADirectory proves the store keeps two projects
// apart.
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

// TestWriteRefusesWhatCannotBeFiled checks the two values that would otherwise
// reach the filesystem unchecked.
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

// TestWriteRefusesNothing proves a nil report is a diagnosable error rather
// than a panic in the last step of a run.
func TestWriteRefusesNothing(t *testing.T) {
	t.Parallel()

	if _, _, err := (report.History{Root: t.TempDir()}).Write(nil); report.CodeOf(err) != report.CodeNoReport {
		t.Fatalf("code = %q, want %q (%v)", report.CodeOf(err), report.CodeNoReport, err)
	}
}

// TestHistoryCreatesWhatIsMissing proves a store that does not exist yet is
// built rather than reported as broken.
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

// TestWorkspaceDirDoesNotTouchTheDisk proves the path can be computed without
// creating anything, which is what `report list` needs.
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
