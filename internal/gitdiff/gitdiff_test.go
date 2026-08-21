// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gitdiff"
)

// The tests in this file drive a real git against a repository they script into
// a temporary directory, because every interesting thing about this package is
// what git actually prints: which commit a merge base resolves to, how a hunk
// header is spelled, what a missing upstream says. A fake git would be a second
// implementation of the thing under test.
//
// They skip rather than fail when git is absent. `--changed` is the one feature
// that needs a tool go-mutants does not ship, and a developer without git
// installed should still be able to run the suite — the skip says why, so an
// empty result is never mistaken for a passing one.

// The repository's fixed identity. Nothing here is read from the machine: the
// global and system configurations are pointed at files that do not exist, so a
// developer's own `~/.gitconfig` — a signing key, a commit template, a
// `diff.noprefix` — cannot change what these tests observe.
const (
	testAuthor    = "go-mutants tests"
	testEmail     = "tests@go-mutants.invalid"
	testTimestamp = "2026-02-18T09:15:00+00:00"
)

// A repo is one scripted git repository.
type repo struct {
	t   *testing.T
	dir string
	env []string
}

// newRepo initialises an empty repository in a temporary directory, or skips
// the test when git cannot be found.
func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH, so the --changed machinery cannot be exercised here: %v", err)
	}
	dir := t.TempDir()
	r := &repo{
		t:   t,
		dir: dir,
		env: append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+filepath.Join(dir, "absent-global-config"),
			"GIT_CONFIG_SYSTEM="+filepath.Join(dir, "absent-system-config"),
			"GIT_AUTHOR_NAME="+testAuthor,
			"GIT_AUTHOR_EMAIL="+testEmail,
			"GIT_AUTHOR_DATE="+testTimestamp,
			"GIT_COMMITTER_NAME="+testAuthor,
			"GIT_COMMITTER_EMAIL="+testEmail,
			"GIT_COMMITTER_DATE="+testTimestamp,
		),
	}
	r.git("init", "--quiet")
	return r
}

// git runs one command in the repository and returns its trimmed standard
// output, failing the test if it does not succeed.
func (r *repo) git(args ...string) string {
	r.t.Helper()
	argv := append([]string{"-C", r.dir, "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", argv...)
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// write creates or replaces a file, creating the directories above it.
func (r *repo) write(rel, content string) {
	r.t.Helper()
	path := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatalf("creating the directory for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatalf("writing %s: %v", rel, err)
	}
}

// remove deletes a file from the working tree.
func (r *repo) remove(rel string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, filepath.FromSlash(rel))); err != nil {
		r.t.Fatalf("removing %s: %v", rel, err)
	}
}

// commit stages everything and commits it, returning the new commit's hash.
func (r *repo) commit(message string) string {
	r.t.Helper()
	r.git("add", "--all")
	r.git("commit", "--quiet", "--allow-empty", "--message", message)
	return r.git("rev-parse", "HEAD")
}

// resolve runs the package against the repository, or a subdirectory of it.
func (r *repo) resolve(sub, ref string) (gitdiff.Changed, error) {
	r.t.Helper()
	root := r.dir
	if sub != "" {
		root = filepath.Join(root, filepath.FromSlash(sub))
	}
	return gitdiff.Resolve(r.t.Context(), gitdiff.Options{
		Root: root,
		Ref:  ref,
		Env:  r.env,
	})
}

// The two fixture sources. Both are ordinary Go so that the line numbers in the
// assertions can be counted by hand from the text.
const (
	alphaV1 = "package alpha\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	alphaV2 = "package alpha\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n"
	betaSrc = "package beta\n\nfunc Ok() bool {\n\treturn true\n}\n"
)

// TestChangedLinesOfACommittedEdit is the ordinary case: one line rewritten and
// one file added, both committed, diffed against the commit before them.
func TestChangedLinesOfACommittedEdit(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("alpha.go", alphaV1)
	base := r.commit("base")

	r.write("alpha.go", alphaV2)
	r.write("beta.go", betaSrc)
	r.commit("work")

	changed, err := r.resolve("", base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := changed.Paths(); !slices.Equal(got, []string{"alpha.go", "beta.go"}) {
		t.Fatalf("Paths() = %v, want alpha.go and beta.go", got)
	}
	// `return a + b` is the fourth line, and it is the only one that moved.
	if got := changed.Lines("alpha.go"); !slices.Equal(got, []gitdiff.Range{{First: 4, Last: 4}}) {
		t.Errorf("alpha.go = %v, want line 4 alone", got)
	}
	if got := changed.Lines("beta.go"); !slices.Equal(got, []gitdiff.Range{{First: 1, Last: 5}}) {
		t.Errorf("beta.go = %v, want the whole file", got)
	}
	if changed.Base != base {
		t.Errorf("Base = %q, want %q", changed.Base, base)
	}
	if changed.Ref != base {
		t.Errorf("Ref = %q, want the ref as it was written (%q)", changed.Ref, base)
	}
	if !changed.Touches("alpha.go", 4, 4) || changed.Touches("alpha.go", 3, 3) {
		t.Error("Touches disagrees with the ranges it was built from")
	}
}

// TestUncommittedWorkCounts proves the diff is taken against the working tree.
//
// It is the case `--changed` is most often reached for: somebody has just
// written something and wants to know whether their tests notice it being
// broken, and they have not committed it yet.
func TestUncommittedWorkCounts(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("alpha.go", alphaV1)
	base := r.commit("base")
	r.write("alpha.go", alphaV2)

	changed, err := r.resolve("", base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := changed.Lines("alpha.go"); !slices.Equal(got, []gitdiff.Range{{First: 4, Last: 4}}) {
		t.Errorf("alpha.go = %v, want line 4 alone", got)
	}
}

// TestDeletedFilesTouchNothing proves a removal contributes no changed lines:
// there is nothing left in that file to mutate.
func TestDeletedFilesTouchNothing(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("alpha.go", alphaV1)
	r.write("beta.go", betaSrc)
	base := r.commit("base")
	r.remove("beta.go")

	changed, err := r.resolve("", base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(changed.Paths()) != 0 {
		t.Errorf("Paths() = %v, want nothing", changed.Paths())
	}
}

// TestTheBaseIsTheForkPointRatherThanTheTip is the whole reason a merge base is
// taken at all.
//
// The branch is diffed against the commit it left, so work pushed to the target
// branch afterwards is somebody else's and does not select mutants here. Diffing
// against the tip would make `--changed origin/main` mean "everything anybody
// has changed this week" on a branch that is a few days old.
func TestTheBaseIsTheForkPointRatherThanTheTip(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("alpha.go", alphaV1)
	r.commit("base")
	trunk := r.git("rev-parse", "--abbrev-ref", "HEAD")

	r.git("checkout", "--quiet", "-b", "feature")
	r.write("alpha.go", alphaV2)
	r.commit("mine")

	// Somebody else's work, landed on the trunk after this branch was cut.
	r.git("checkout", "--quiet", trunk)
	r.write("theirs.go", betaSrc)
	r.commit("theirs")
	r.git("checkout", "--quiet", "feature")

	changed, err := r.resolve("", trunk)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := changed.Paths(); !slices.Equal(got, []string{"alpha.go"}) {
		t.Errorf("Paths() = %v, want alpha.go alone: theirs.go is not this branch's work", got)
	}
}

// TestPathsAreRelativeToTheWorkspaceRoot covers a module inside a larger
// repository, which is what a monorepo looks like.
func TestPathsAreRelativeToTheWorkspaceRoot(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("services/api/alpha.go", alphaV1)
	r.write("docs/notes.md", "# notes\n")
	base := r.commit("base")

	r.write("services/api/alpha.go", alphaV2)
	r.write("services/api/beta.go", betaSrc)
	r.write("docs/notes.md", "# notes\n\nmore\n")
	r.commit("work")

	changed, err := r.resolve("services/api", base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := changed.Paths(); !slices.Equal(got, []string{"alpha.go", "beta.go"}) {
		t.Errorf("Paths() = %v, want the module's own two files", got)
	}
}

// upstreamSpellings is every way of asking for the upstream of HEAD: silence,
// which is what [gitdiff.Options.Ref] holds when a caller named nothing, and
// git's own notation, which is what internal/cli gives the bare `--changed`
// flag and what a user writing it out longhand types.
//
// The two are one request and are tested as one, in the same bodies, because
// the two drifting apart is the bug this list exists to prevent: a test of
// silence alone once passed while every invocation the CLI could produce went
// down the other path, recording `@{upstream}` in reports and making
// [gitdiff.CodeNoUpstream] unreachable.
var upstreamSpellings = []struct {
	name string
	ref  string
}{
	{name: "silence", ref: ""},
	{name: "longhand", ref: gitdiff.UpstreamRef},
}

// TestBareChangedFollowsTheUpstream proves that a `--changed` with no ref of its
// own resolves the upstream branch, and records it by name rather than by the
// notation that found it.
func TestBareChangedFollowsTheUpstream(t *testing.T) {
	t.Parallel()

	for _, spelling := range upstreamSpellings {
		t.Run(spelling.name, func(t *testing.T) {
			t.Parallel()

			r := newRepo(t)
			r.write("alpha.go", alphaV1)
			r.commit("base")
			trunk := r.git("rev-parse", "--abbrev-ref", "HEAD")

			r.git("checkout", "--quiet", "-b", "feature")
			r.write("alpha.go", alphaV2)
			r.commit("mine")
			r.git("branch", "--quiet", "--set-upstream-to="+trunk, "feature")

			changed, err := r.resolve("", spelling.ref)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if changed.Ref != trunk {
				t.Errorf("Ref = %q, want the upstream branch's own name (%q)", changed.Ref, trunk)
			}
			if got := changed.Paths(); !slices.Equal(got, []string{"alpha.go"}) {
				t.Errorf("Paths() = %v, want alpha.go", got)
			}
		})
	}
}

// TestUntrackedFilesAreWhollyChanged is the file `git diff` cannot see: written,
// never added, and every line of it new.
//
// Each case is asserted twice — once while the file is untracked and once after
// it is committed — because the property is not "some range is recorded" but
// that `git add` changes nothing about which mutants a run selects. A selection
// that moved when somebody staged a file would measure a different thing on a
// laptop than in CI.
//
// The two spellings of the same five lines are what makes the count git's own.
// A final line with nothing after it is still a line — git says so, with `\ No
// newline at end of file` — so a count that dropped it would take the last line
// of every new file out of the selection.
func TestUntrackedFilesAreWhollyChanged(t *testing.T) {
	t.Parallel()

	sources := []struct {
		name   string
		source string
	}{
		{name: "with a trailing newline", source: betaSrc},
		{name: "without a trailing newline", source: strings.TrimSuffix(betaSrc, "\n")},
	}
	for _, c := range sources {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			r := newRepo(t)
			r.write("alpha.go", alphaV1)
			base := r.commit("base")
			r.write("beta.go", c.source)

			untracked, err := r.resolve("", base)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := untracked.Paths(); !slices.Equal(got, []string{"beta.go"}) {
				t.Fatalf("Paths() = %v, want the untracked beta.go", got)
			}
			// betaSrc is five lines, and all five of them are new.
			if got := untracked.Lines("beta.go"); !slices.Equal(got, []gitdiff.Range{{First: 1, Last: 5}}) {
				t.Errorf("beta.go = %v, want the whole file", got)
			}

			r.commit("add beta")
			tracked, err := r.resolve("", base)
			if err != nil {
				t.Fatalf("Resolve after the commit: %v", err)
			}
			if got, want := tracked.Lines("beta.go"), untracked.Lines("beta.go"); !slices.Equal(got, want) {
				t.Errorf("committing beta.go changed its changed lines from %v to %v", want, got)
			}
		})
	}
}

// TestIgnoredAndEmptyUntrackedFilesTouchNothing covers the two untracked files
// that are not work.
//
// An ignored file is one the repository's owner has said is not source, and a
// file with no lines has nothing in it to mutate — so neither belongs in a
// changed set, and an empty range list is never stored.
func TestIgnoredAndEmptyUntrackedFilesTouchNothing(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("alpha.go", alphaV1)
	r.write(".gitignore", "generated.go\n")
	base := r.commit("base")
	r.write("generated.go", betaSrc)
	r.write("empty.go", "")

	changed, err := r.resolve("", base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(changed.Paths()) != 0 {
		t.Errorf("Paths() = %v, want nothing: one file is ignored and the other is empty", changed.Paths())
	}
}

// TestUntrackedPathsAreRelativeToTheWorkspaceRoot proves the untracked half of
// the set is mapped exactly as the diff's half is.
//
// A module inside a larger repository asks git for its own subtree and speaks
// module-relative paths, so an untracked file above the module is not this
// run's business and one inside it has to arrive under the name the catalogue
// uses.
func TestUntrackedPathsAreRelativeToTheWorkspaceRoot(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("services/api/alpha.go", alphaV1)
	base := r.commit("base")
	r.write("services/api/beta.go", betaSrc)
	r.write("elsewhere.go", betaSrc)

	changed, err := r.resolve("services/api", base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := changed.Paths(); !slices.Equal(got, []string{"beta.go"}) {
		t.Errorf("Paths() = %v, want the module's own new file alone", got)
	}
}

// TestFailures covers every way resolving can fail, and pins the code each one
// carries: they are what internal/cli turns into an exit status and what a user
// searches for.
func TestFailures(t *testing.T) {
	t.Parallel()

	type failure struct {
		name  string
		setup func(t *testing.T) (root, ref string, env []string)
		code  gitdiff.Code
		says  string
	}

	cases := []failure{
		{
			name: "outside a repository",
			setup: func(t *testing.T) (string, string, []string) {
				if _, err := exec.LookPath("git"); err != nil {
					t.Skipf("git is not on PATH: %v", err)
				}
				return t.TempDir(), "HEAD", nil
			},
			code: gitdiff.CodeNotARepository,
			says: "not inside a git working tree",
		},
		{
			name: "before the first commit",
			setup: func(t *testing.T) (string, string, []string) {
				r := newRepo(t)
				r.write("alpha.go", alphaV1)
				return r.dir, "HEAD", r.env
			},
			code: gitdiff.CodeUnknownRef,
			says: "no commits yet",
		},
		{
			name: "against a ref that does not exist",
			setup: func(t *testing.T) (string, string, []string) {
				r := newRepo(t)
				r.write("alpha.go", alphaV1)
				r.commit("base")
				return r.dir, "origin/nonexistent", r.env
			},
			code: gitdiff.CodeUnknownRef,
			says: "merge base",
		},
		{
			name: "with no git to run",
			setup: func(t *testing.T) (string, string, []string) {
				r := newRepo(t)
				r.write("alpha.go", alphaV1)
				r.commit("base")
				return r.dir, "HEAD", r.env
			},
			code: gitdiff.CodeGitUnavailable,
			says: "needs git on PATH",
		},
	}
	// The branch with nothing to follow, generated from [upstreamSpellings]
	// rather than written out, so that a way of asking for the upstream can
	// never be covered here for one spelling and forgotten for the other.
	for _, spelling := range upstreamSpellings {
		cases = append(cases, failure{
			name: "with no upstream, asked for by " + spelling.name,
			setup: func(t *testing.T) (string, string, []string) {
				r := newRepo(t)
				r.write("alpha.go", alphaV1)
				r.commit("base")
				return r.dir, spelling.ref, r.env
			},
			code: gitdiff.CodeNoUpstream,
			says: "--changed=origin/main",
		})
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			root, ref, env := c.setup(t)
			opts := gitdiff.Options{Root: root, Ref: ref, Env: env}
			if c.code == gitdiff.CodeGitUnavailable {
				opts.Program = filepath.Join(root, "no-such-git")
			}
			_, err := gitdiff.Resolve(t.Context(), opts)
			if err == nil {
				t.Fatal("Resolve succeeded")
			}
			if code := gitdiff.CodeOf(err); code != c.code {
				t.Fatalf("code = %q, want %q (%v)", code, c.code, err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the message does not mention %q: %v", c.says, err)
			}
		})
	}
}

// TestCancellationIsNotABrokenGit proves a cancelled context comes back as a
// cancellation rather than as a missing tool, so that Ctrl-C during the
// selection stage exits 130 like every other interruption.
func TestCancellationIsNotABrokenGit(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("alpha.go", alphaV1)
	r.commit("base")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := gitdiff.Resolve(ctx, gitdiff.Options{Root: r.dir, Ref: "HEAD", Env: r.env})
	if err == nil {
		t.Fatal("Resolve succeeded against a cancelled context")
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("the failure does not report an interruption: %v", err)
	}
	// The sentinel stays reachable, which is what internal/engine and
	// internal/cli recognise a cancelled run by.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("context.Canceled is not reachable through the failure: %v", err)
	}
}
