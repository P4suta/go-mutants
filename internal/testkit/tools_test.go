// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// toolProbeEnv names the tool the probe child asks for. It does not begin with
// GO_MUTANTS_, because the environment policy strips that whole prefix.
const toolProbeEnv = "TESTKIT_TOOL_PROBE"

// TestToolProbeProcess is not a test. It is the child
// TestGoBinarySkipsOrFailsByPolicy starts: the only way to observe whether a
// helper skipped or failed is to run it in a process whose result can be read.
func TestToolProbeProcess(t *testing.T) {
	switch os.Getenv(toolProbeEnv) {
	case "go":
		t.Logf("go is at %s", GoBinary(t))
	case "git":
		t.Logf("git is at %s", GitBinary(t))
	case "go-after-env":
		// The order CI actually runs: the environment is redirected first, which
		// removes every GO_MUTANTS_ variable from the process, and the tool is
		// looked up afterwards.
		Env(t)
		t.Logf("go is at %s", GoBinary(t))
	}
}

// TestGoBinaryAnswersTheVersionProbe proves the lookup returns a toolchain
// rather than a file called `go`: the harness executes what it hands back
// thousands of times per run, and a path that cannot answer `go version` is
// worth finding out about here rather than in whichever suite runs first.
//
// It cannot be made to fail by emptying PATH from a shell, and that is a
// property of `go test` rather than of this package: since the toolchain puts
// its own $GOROOT/bin at the front of the PATH every test binary inherits, `go`
// is on PATH in any test the go command is running. The skip-or-fail decision
// for a missing `go` is therefore proven in a child process whose PATH this
// package controls — TestGoBinarySkipsOrFailsByPolicy — and the shell-level
// version of that check is the git one below, because git is not in GOROOT/bin.
func TestGoBinaryAnswersTheVersionProbe(t *testing.T) {
	t.Parallel()

	gobin := GoBinary(t)
	result := Exec(t, t.TempDir(), Compose(t, t.TempDir()), gobin, "version")
	RequireExit(t, result, 0, "`go version`")
	RequireOutput(t, result, "`go version`", "go version go")
}

// TestGitBinaryAnswersTheVersionProbe is the same assertion for git, and it is
// the one a shell can drive: `GO_MUTANTS_TEST_REQUIRE_TOOLS=1 PATH=/nonexistent
// go test ./internal/testkit -run TestGitBinary` fails here and names the
// variable, which is the arrangement every CI job runs in.
func TestGitBinaryAnswersTheVersionProbe(t *testing.T) {
	t.Parallel()

	gitbin := GitBinary(t)
	result := Exec(t, t.TempDir(), Compose(t, t.TempDir()), gitbin, "--version")
	RequireExit(t, result, 0, "`git --version`")
	RequireOutput(t, result, "`git --version`", "git version")
}

// TestGoBinarySkipsOrFailsByPolicy pins the one decision every toolchain-driven
// test in this repository inherits.
//
// A developer without `go` on PATH — which happens, because the unit tier is
// meant to run without one — should see the toolchain tests skip rather than
// fail. A CI job without `go` on PATH is a broken job, and a suite that quietly
// narrowed itself to the tests that need no toolchain would report green for a
// run that proved almost nothing. Both are the same helper, and the variable is
// what tells them apart — so the failure names the variable, because somebody
// who set it deliberately needs to recognise its work, and somebody who
// inherited it from a workflow needs to find it.
func TestGoBinarySkipsOrFailsByPolicy(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{"go", "git"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()

			// An empty directory is a PATH with nothing on it that is still a
			// valid PATH, which an empty string is not on every platform.
			base := withEntries(Compose(t, t.TempDir()),
				"PATH="+t.TempDir(),
				toolProbeEnv+"="+tool,
			)
			argv := []string{os.Args[0], "-test.run=^TestToolProbeProcess$", "-test.v"}

			skipped := Exec(t, t.TempDir(), withEntries(base, RequireToolsEnv+"=0"), argv...)
			RequireExit(t, skipped, 0, "the probe with no "+tool+" and no requirement")
			RequireOutput(t, skipped, "the probe", "--- SKIP", tool+" is not on PATH")
			RequireNoOutput(t, skipped, "the probe", "--- FAIL")

			failed := Exec(t, t.TempDir(), withEntries(base, RequireToolsEnv+"=1"), argv...)
			if failed.ExitCode == 0 {
				t.Fatalf("the probe passed with no %s and %s set:\n%s", tool, RequireToolsEnv, failed.Output)
			}
			RequireOutput(t, failed, "the probe", "--- FAIL", RequireToolsEnv, tool+" is not on PATH")
		})
	}
}

// TestTheToolRequirementSurvivesTheEnvironmentPolicy covers the interaction
// between two rules that are each right on their own.
//
// [Env] removes every GO_MUTANTS_ variable from the process, because a
// developer's exported GO_MUTANTS_ACTIVE would otherwise turn an instrumented
// baseline into a mutant — and [RequireToolsEnv] wears the same prefix. A test
// that redirected its environment and then reached for a toolchain would have
// silently turned CI's requirement off, which is the failure mode the
// requirement exists to prevent, so the value the process started with is what
// answers once the variable is gone. This is the arrangement CI runs in, so it
// is proven in a child process rather than reasoned about.
func TestTheToolRequirementSurvivesTheEnvironmentPolicy(t *testing.T) {
	t.Parallel()

	env := withEntries(Compose(t, t.TempDir()),
		"PATH="+t.TempDir(),
		toolProbeEnv+"=go-after-env",
		RequireToolsEnv+"=1",
	)
	result := Exec(t, t.TempDir(), env, os.Args[0], "-test.run=^TestToolProbeProcess$", "-test.v")

	if result.ExitCode == 0 {
		t.Fatalf("the probe passed with no go on PATH and %s set:\n%s", RequireToolsEnv, result.Output)
	}
	RequireOutput(t, result, "the probe", "--- FAIL", RequireToolsEnv)
	RequireNoOutput(t, result, "the probe", "--- SKIP")
}

// TestRequireToolsReadsThePolicyVariable states how the variable is spelled,
// which spellings of "yes" and "no" a person can type into a workflow file or a
// shell, and that the answer survives [Env] removing the variable along with the
// rest of the GO_MUTANTS_ namespace. The workflow-level form of the same rule —
// a value the process started with rather than one a test set — is
// TestTheToolRequirementSurvivesTheEnvironmentPolicy's subject, because a
// t.Setenv cannot reach back before the process began.
func TestRequireToolsReadsThePolicyVariable(t *testing.T) {
	for _, value := range []string{"1", "true", "yes"} {
		t.Setenv(RequireToolsEnv, value)
		if !RequireTools() {
			t.Errorf("%s=%q is not read as a requirement", RequireToolsEnv, value)
		}
	}
	for _, value := range []string{"", "0", "false", "no"} {
		t.Setenv(RequireToolsEnv, value)
		if RequireTools() {
			t.Errorf("%s=%q is read as a requirement", RequireToolsEnv, value)
		}
	}

	t.Setenv(RequireToolsEnv, "1")
	Env(t)
	if _, present := os.LookupEnv(RequireToolsEnv); present {
		t.Fatalf("Env left %s in the environment", RequireToolsEnv)
	}
	if !RequireTools() {
		t.Error("the requirement was lost when Env stripped the GO_MUTANTS_ prefix")
	}
}

// TestGitInitCommitsDeterministically is why the identity and the dates in the
// environment policy are constants.
//
// A commit's hash is derived from its tree, its parents, its message, its author
// and its committer — dates included — so two runs of the same test produce the
// same repository, and two copies of the same fixture produce the same commit.
// Without that, nothing about a commit can be asserted: every test that uses
// `--changed`, a merge base or a diff would have to discover the hash it just
// created and could never state one.
func TestGitInitCommitsDeterministically(t *testing.T) {
	t.Parallel()
	GitBinary(t)

	first := GitInit(t, Copy(t, "simple"))
	second := GitInit(t, Copy(t, "simple"))
	if first != second {
		t.Errorf("two repositories over the same fixture have different heads:\n\t%s\n\t%s", first, second)
	}
	if len(first) != 40 {
		t.Errorf("the head %q is not a full object name", first)
	}
}

// TestGitInitCommitsTheWholeTreeOnMain states the two facts a caller depends on
// without asking: everything the tree held is in the commit, and the branch is
// called `main` whatever the machine's init.defaultBranch says.
func TestGitInitCommitsTheWholeTreeOnMain(t *testing.T) {
	t.Parallel()
	GitBinary(t)

	root := Copy(t, "simple")
	head := GitInit(t, root)

	if got := Git(t, root, "rev-parse", "HEAD"); got != head {
		t.Errorf("GitInit returned %s but HEAD is %s", head, got)
	}
	if got := Git(t, root, "branch", "--show-current"); got != "main" {
		t.Errorf("the branch is %q, want %q", got, "main")
	}
	if got := Git(t, root, "status", "--porcelain"); got != "" {
		t.Errorf("the tree is not clean after GitInit:\n%s", got)
	}
	files := strings.Split(Git(t, root, "ls-files"), "\n")
	if !slices.Contains(files, "go.mod") {
		t.Errorf("the commit does not hold the fixture's go.mod: %q", files)
	}
}

// TestGitCommitReportsTheCommitItMade covers the second commit, which is what
// every test about a diff, a merge base or an upstream needs.
func TestGitCommitReportsTheCommitItMade(t *testing.T) {
	t.Parallel()
	GitBinary(t)

	root := Copy(t, "simple")
	head := GitInit(t, root)
	WriteFile(t, filepath.Join(root, "added.txt"), []byte("added by a test\n"))
	next := GitCommit(t, root, "add a file")

	if next == head {
		t.Fatalf("GitCommit returned the head it started from: %s", next)
	}
	if got := Git(t, root, "rev-parse", "HEAD"); got != next {
		t.Errorf("GitCommit returned %s but HEAD is %s", next, got)
	}
	if got := Git(t, root, "log", "-1", "--pretty=%s"); got != "add a file" {
		t.Errorf("the commit subject is %q, want %q", got, "add a file")
	}
	if got := Git(t, root, "log", "-1", "--pretty=%an <%ae>"); got != GitName+" <"+GitEmail+">" {
		t.Errorf("the author is %q, want the fixed identity", got)
	}
}

// TestGitReturnsWhatTheCommandPrintedRatherThanWhatItSaid is why [Git] reads
// stdout rather than the merged streams.
//
// Almost every caller parses what comes back as data — a hash, a branch name, a
// porcelain listing — and git writes hints, advice and progress to stderr
// whenever it feels the need: a repository with no init.defaultBranch, a
// detached head, a checkout. A helper returning the two concatenated hands the
// caller a "hash" with three lines of advice on the front, and the failure it
// eventually causes is nowhere near the command that caused it.
func TestGitReturnsWhatTheCommandPrintedRatherThanWhatItSaid(t *testing.T) {
	t.Parallel()
	GitBinary(t)

	root := Copy(t, "simple")
	head := GitInit(t, root)

	// `checkout --detach` says "HEAD is now at ..." and prints the detached-head
	// advice, all of it on stderr and none of it on stdout.
	if got := Git(t, root, "checkout", "--detach", "HEAD"); got != "" {
		t.Errorf("`git checkout --detach` returned %q, which is what it wrote to stderr", got)
	}
	got := Git(t, root, "rev-parse", "HEAD")
	if got != head {
		t.Errorf("`git rev-parse HEAD` returned %q, want %q", got, head)
	}
	if len(got) != 40 || strings.ContainsFunc(got, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdef", r)
	}) {
		t.Errorf("`git rev-parse HEAD` returned %q, which is not 40 hex characters", got)
	}
}

// TestGitIgnoresTheRepositoryVariablesInTheEnvironment covers the developer who
// is in the middle of a rebase, or whose shell exports GIT_DIR from a wrapper.
//
// git reads GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY and
// GIT_COMMON_DIR before it looks at `-C`, so an exported one silently points
// every command a test runs at somebody else's repository — which is, in the
// worst case, this one.
func TestGitIgnoresTheRepositoryVariablesInTheEnvironment(t *testing.T) {
	// t.Setenv, so no t.Parallel: the point of the test is what the process's
	// own environment carries.
	GitBinary(t)

	elsewhere := Copy(t, "simple")
	GitInit(t, elsewhere)
	t.Setenv("GIT_DIR", filepath.Join(elsewhere, ".git"))
	t.Setenv("GIT_WORK_TREE", elsewhere)

	root := Copy(t, "killable")
	head := GitInit(t, root)

	if got := Git(t, root, "rev-parse", "--show-toplevel"); !SamePath(got, root) {
		t.Errorf("git worked in %s, want the repository it was pointed at, %s", got, root)
	}
	if got := Git(t, root, "rev-parse", "HEAD"); got != head {
		t.Errorf("`git rev-parse HEAD` = %s, want this repository's head %s", got, head)
	}
}

// TestGitLeavesNothingInTheRepositoryItWasGiven keeps the harness's own
// scaffolding out of the tree under test.
//
// The absent configuration files are named under a directory of the test's own
// rather than under the repository, because a run that ever created one — or a
// git that decided to write a config — would put an untracked file in the
// working tree, and half of what these tests assert is what `git status` says.
func TestGitLeavesNothingInTheRepositoryItWasGiven(t *testing.T) {
	t.Parallel()
	GitBinary(t)

	root := Copy(t, "simple")
	GitInit(t, root)
	if got := Git(t, root, "status", "--porcelain"); got != "" {
		t.Errorf("the repository is not clean after GitInit:\n%s", got)
	}
	for _, entry := range Entries(t, root) {
		if strings.Contains(entry, "absent-git-config") {
			t.Errorf("the harness left %s in the repository", entry)
		}
	}
}

// TestGitIgnoresTheDevelopersOwnConfiguration keeps a machine's `~/.gitconfig`
// out of what these tests observe. A developer with `commit.gpgsign=true`, an
// `init.defaultBranch` of their own or a `core.autocrlf` would otherwise get
// different results from the same test than CI does.
func TestGitIgnoresTheDevelopersOwnConfiguration(t *testing.T) {
	t.Parallel()
	GitBinary(t)

	root := Copy(t, "simple")
	GitInit(t, root)

	config := Git(t, root, "config", "--list")
	for _, key := range []string{"user.name", "user.email", "commit.gpgsign", "core.autocrlf"} {
		if strings.Contains(config, key+"=") {
			t.Errorf("git read %s out of a configuration file:\n%s", key, config)
		}
	}
	if got := Git(t, root, "var", "GIT_AUTHOR_IDENT"); !strings.HasPrefix(got, GitName+" <"+GitEmail+">") {
		t.Errorf("the author identity is %q, want the fixed one from the environment", got)
	}
}

// TestGoEnvUnderTheHermeticEnvironmentAgreesWithThePolicy is the end-to-end half
// of the environment tests: they assert what the policy composed, and this
// asserts that a real `go` command reads it.
func TestGoEnvUnderTheHermeticEnvironmentAgreesWithThePolicy(t *testing.T) {
	e := Env(t)
	gobin := GoBinary(t)

	names := []string{"GOCACHE", "GOMODCACHE", "GOFLAGS", "GOTOOLCHAIN", "GOPROXY"}
	result := Exec(t, Root(t), e.Vars(), append([]string{gobin, "env"}, names...)...)
	RequireExit(t, result, 0, "`go env` under the hermetic environment")

	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(result.Output)), "\r\n", "\n"), "\n")
	if len(lines) != len(names) {
		t.Fatalf("`go env` printed %d line(s), want %d:\n%s", len(lines), len(names), result.Output)
	}
	got := map[string]string{}
	for i, name := range names {
		got[name] = strings.TrimSpace(lines[i])
	}

	if !SamePath(got["GOCACHE"], e.GoCache) {
		t.Errorf("the go command's GOCACHE is %s, want the test-owned %s", got["GOCACHE"], e.GoCache)
	}
	if within(e.Home, got["GOMODCACHE"]) {
		t.Errorf("the go command's GOMODCACHE is %s, below the moved HOME: every build would start cold", got["GOMODCACHE"])
	}
	for name, want := range map[string]string{"GOFLAGS": "-mod=readonly", "GOTOOLCHAIN": "local", "GOPROXY": "off"} {
		if got[name] != want {
			t.Errorf("the go command reads %s as %q, want %q", name, got[name], want)
		}
	}
}
