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
	"github.com/P4suta/go-mutants/internal/testkit"
)

const (
	testAuthor    = "go-mutants tests"
	testEmail     = "tests@go-mutants.invalid"
	testTimestamp = "2026-02-18T09:15:00+00:00"
)

type repo struct {
	t      *testing.T
	binary string
	dir    string
	env    []string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	git := testkit.GitBinary(t)
	dir := t.TempDir()
	r := &repo{
		t:      t,
		binary: git,
		dir:    dir,
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

func (r *repo) git(args ...string) string {
	r.t.Helper()
	argv := append([]string{"-C", r.dir}, args...)
	cmd := exec.Command(r.binary, argv...)
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

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

func (r *repo) remove(rel string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, filepath.FromSlash(rel))); err != nil {
		r.t.Fatalf("removing %s: %v", rel, err)
	}
}

func (r *repo) commit(message string) string {
	r.t.Helper()
	r.git("add", "--all")
	r.git("commit", "--quiet", "--allow-empty", "--message", message)
	return r.git("rev-parse", "HEAD")
}

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

const (
	alphaV1 = "package alpha\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	alphaV2 = "package alpha\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n"
	betaSrc = "package beta\n\nfunc Ok() bool {\n\treturn true\n}\n"
)

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

func TestTheBaseIsTheForkPointRatherThanTheTip(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.write("alpha.go", alphaV1)
	r.commit("base")
	trunk := r.git("rev-parse", "--abbrev-ref", "HEAD")

	r.git("checkout", "--quiet", "-b", "feature")
	r.write("alpha.go", alphaV2)
	r.commit("mine")

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

var upstreamSpellings = []struct {
	name string
	ref  string
}{
	{name: "silence", ref: ""},
	{name: "longhand", ref: gitdiff.UpstreamRef},
}

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
				_ = testkit.GitBinary(t)
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
	if !errors.Is(err, context.Canceled) {
		t.Errorf("context.Canceled is not reachable through the failure: %v", err)
	}
}
