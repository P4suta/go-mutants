// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of `run --changed`: a real repository, a real
// module, and the document the command actually writes.
//
// It lives here rather than in internal/engine because what is under test is
// the whole sentence a user types. The engine can be handed any ref a test
// likes; only the command line can produce the one the bare flag carries, and
// the two failures this file pins were both invisible from underneath —
// a report that documented the lookup instead of the comparison, and a
// selection that could not see a file git had never been told about.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testsupport"
)

// The work this run is meant to notice: a file that has never been added, and
// the test beside it. A whole new file rather than an edit, because that is the
// case `git diff` cannot report at all — it has no index entry to be compared
// against — so every mutant in it is either selected by the untracked scan or
// by nothing.
const (
	freshFile = "fresh.go"
	freshTest = "fresh_test.go"

	freshSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

// Fresh is uncommitted, unstaged work: written, saved, and never handed to git.
func Fresh(a, b int) int {
	if a > b {
		return a - b
	}
	return a + b
}
`

	freshTestSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

import "testing"

func TestFresh(t *testing.T) {
	if got := Fresh(7, 2); got != 5 {
		t.Errorf("Fresh(7, 2) = %d, want 5", got)
	}
	if got := Fresh(2, 7); got != 9 {
		t.Errorf("Fresh(2, 7) = %d, want 9", got)
	}
}
`
)

// branchedModule copies the killable fixture into a temporary directory, commits
// it, and cuts a branch that tracks the trunk — which is what makes a bare
// `--changed` a question with an answer. It returns the workspace root and the
// upstream branch's own name.
//
// The environment is redirected first so that nothing here — the snapshot, the
// report, the history store — reaches the developer's own directories.
func branchedModule(t *testing.T) (root, upstream string) {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "killable"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}

	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	testsupport.CacheDir(t)
	neutralGitEnvironment(t)

	root = filepath.Join(t.TempDir(), "killable")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatalf("copying the killable fixture: %v", err)
	}

	gitCommand(t, root, "init", "--quiet")
	gitCommand(t, root, "add", "--all")
	gitCommand(t, root, "commit", "--quiet", "--message", "the fixture as it was")
	upstream = gitCommand(t, root, "rev-parse", "--abbrev-ref", "HEAD")
	gitCommand(t, root, "checkout", "--quiet", "-b", "feature")
	gitCommand(t, root, "branch", "--quiet", "--set-upstream-to="+upstream, "feature")
	return root, upstream
}

// TestBareChangedNamesTheUpstreamAndMeasuresUntrackedWork is both halves of what
// a bare `--changed` promises, read off the document the command wrote.
//
// The two used to fail together and for related reasons — the feature stopping
// one step short of the tree it claims to read. `changed_ref` recorded
// `@{upstream}`, which documents a lookup rather than a comparison and makes
// two shards that diffed different upstreams look congruent; and the selection
// was taken from `git diff` alone, so an afternoon's work in a file that had
// never been `git add`ed came back as `0 of N mutants selected`, `score N/A`,
// and exit 0 — the green that proves nothing, which every other part of this
// feature fails closed to avoid.
func TestBareChangedNamesTheUpstreamAndMeasuresUntrackedWork(t *testing.T) {
	root, upstream := branchedModule(t)
	// Written into the working tree and left there: never added, never staged,
	// never committed.
	for name, source := range map[string]string{freshFile: freshSource, freshTest: freshTestSource} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	t.Chdir(root)

	code, stdout, stderr := execute(t, "run", "--changed", "--json", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --changed --json` exited %d\nstderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, gitdiff.UpstreamRef) {
		t.Errorf("the document records the notation the base was looked up with rather than the branch it found:\n%s", stdout)
	}

	var rep report.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("the run did not write a document: %v\n%s", err, stdout)
	}
	if rep.Selection.Mode != report.ModeChanged {
		t.Errorf("selection.mode = %q, want %q", rep.Selection.Mode, report.ModeChanged)
	}
	if rep.Selection.ChangedRef == nil || *rep.Selection.ChangedRef != upstream {
		t.Errorf("selection.changed_ref = %v, want the upstream branch's own name (%q)",
			rep.Selection.ChangedRef, upstream)
	}

	var measured, elsewhere int
	for _, m := range rep.Mutants {
		if m.Path != freshFile {
			elsewhere++
			if m.Outcome != report.OutcomeNotRun {
				t.Errorf("mutant %s at %s:%d was measured and is not on new work",
					m.DisplayID, m.Path, m.Line)
			}
			continue
		}
		if m.Outcome == report.OutcomeNotRun {
			t.Errorf("mutant %s is in a file git has never seen and was not run: %v",
				m.DisplayID, m.NotRunReason)
			continue
		}
		measured++
	}
	if measured == 0 {
		t.Fatal("nothing in the untracked file was measured, so --changed cannot see work that was never added")
	}
	if elsewhere == 0 {
		t.Fatal("the catalogue holds nothing outside the untracked file, so nothing was narrowed away")
	}
	if rep.Selection.Selected != measured {
		t.Errorf("selection.selected = %d and %d mutants were measured", rep.Selection.Selected, measured)
	}
	// The tests beside the new file catch some of what it carries, so the run is
	// a measurement rather than only a selection.
	if rep.Summary.Killed == 0 {
		t.Error("nothing in the untracked file was killed, so the run proved nothing about it")
	}
}
