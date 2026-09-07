// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// InitialCommitMessage is the subject [GitInit] commits the tree under.
//
// It is a constant because the message is part of the commit's hash, and a
// deterministic repository is the whole point of [GitInit].
const InitialCommitMessage = "import the tree"

// GoBinary returns the path of the `go` command, skipping or failing the test
// when there is none.
//
// Which of the two it does is the policy [RequireTools] states, and the split
// matters in both directions: a developer running the unit tier without a
// toolchain should see the toolchain tests skip, and a CI job without one is a
// broken job rather than a smaller suite.
func GoBinary(t testing.TB) string {
	t.Helper()
	path := toolPath(t, "go")
	rememberToolchain(t, path)
	logInputs(t, "toolchain="+path)
	return path
}

// GitBinary returns the path of the `git` command, under the same policy as
// [GoBinary].
func GitBinary(t testing.TB) string {
	t.Helper()
	path := toolPath(t, "git")
	rememberToolchain(t, path)
	logInputs(t, "toolchain="+path)
	return path
}

// RequireTools reports whether a missing tool must fail rather than skip.
//
// It reads [RequireToolsEnv] from the environment, and falls back to the value
// the variable had before any test ran. The fallback is not a nicety: [Env]
// removes every GO_MUTANTS_ variable from the process — that is how a developer's
// exported GO_MUTANTS_ACTIVE is kept from turning a baseline into a mutant — and
// this variable wears the same prefix. Without the fallback, a test that
// redirected its environment and then reached for a tool would have quietly
// turned CI's requirement off.
func RequireTools() bool {
	return truthy(harnessSetting(RequireToolsEnv, pinnedRequireTools))
}

// pinnedRequireTools is [RequireToolsEnv] as the process started with it.
var pinnedRequireTools = os.Getenv(RequireToolsEnv)

// truthy reads the spellings a person types into a workflow file or a shell.
func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// toolPath looks one tool up on PATH and applies the skip policy.
//
// It does not log, because [Git] resolves the binary on every call and a test
// that scripts a history would otherwise print a line per command. The two
// exported lookups log; the internal callers do not.
func toolPath(t testing.TB, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		if RequireTools() {
			t.Fatalf("%s is not on PATH and %s is set, so this test may not be skipped: %v",
				name, RequireToolsEnv, err)
		} else {
			t.Skipf("%s is not on PATH, so this test cannot be run here (set %s=1 to make this a failure): %v",
				name, RequireToolsEnv, err)
		}
		return ""
	}
	return path
}

// GitInit makes root a git repository with the whole tree in one commit on
// `main`, and returns that commit.
//
// Three things are pinned, and every one of them is something a developer's
// machine would otherwise decide: the branch name, because init.defaultBranch
// varies and a test that names a branch has to be right on every machine; the
// configuration files, which are pointed at paths that do not exist so no
// ~/.gitconfig is read; and the identity with both dates, because they are part
// of the commit's hash.
//
// Signing needs no flag, and that is a deliberate absence. There is no
// configuration file to carry commit.gpgsign, so a repository created here
// cannot be signing anything — and a `-c commit.gpgsign=false` would be the
// harness asking git to turn signing off, which is precisely what the signing
// wrappers some developers install exist to refuse. The result was a suite that
// could not run at all on such a machine, for a setting that was already off.
//
// The commit is allowed to be empty, so that an empty directory is still a
// repository with a head — a test that scripts a history from nothing starts
// there.
func GitInit(t testing.TB, root string) string {
	t.Helper()
	Git(t, root, "init", "--quiet")
	// Rather than `init --initial-branch`, which needs a git from 2020 and
	// prints a hint on the ones that have it.
	Git(t, root, "symbolic-ref", "HEAD", "refs/heads/main")
	Git(t, root, "add", "--all")
	Git(t, root, "commit", "--quiet", "--allow-empty", "--message", InitialCommitMessage)
	return Git(t, root, "rev-parse", "HEAD")
}

// GitCommit stages everything in the tree and commits it, returning the new
// commit.
//
// Unlike [GitInit] it does not allow an empty commit: a test that asked for a
// commit and had nothing to commit has a defect in it, and an empty commit would
// hide that behind a hash that looks like progress.
func GitCommit(t testing.TB, root, message string) string {
	t.Helper()
	Git(t, root, "add", "--all")
	Git(t, root, "commit", "--quiet", "--message", message)
	return Git(t, root, "rev-parse", "HEAD")
}

// Git runs one git command in dir and returns its trimmed standard output,
// failing the test if it did not succeed.
//
// Standard output, not the merged streams: almost every caller parses what comes
// back as data — a hash, a branch name, a porcelain listing — and git writes
// hints, advice and progress to stderr whenever it feels the need. Returning the
// two concatenated hands the caller a "hash" with three lines of advice on the
// front, and the failure that eventually causes is nowhere near the command that
// caused it. Both streams are still quoted when the command fails, because the
// explanation is almost always the one on stderr.
//
// The environment is composed rather than inherited, so the command reads
// exactly what this package decided and nothing a developer exported. Being
// composed is also what lets a test call this without having called [Env]: the
// git half of the policy travels with the command.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	git := toolPath(t, "git")
	// -C so that a caller never has to be in the repository, and nothing else:
	// see [GitInit] on why there is no `-c commit.gpgsign=false` here.
	argv := append([]string{git, "-C", dir}, args...)
	result := Exec(t, dir, gitEnv(t), argv...)
	RequireExit(t, result, 0, "`git "+strings.Join(args, " ")+"`")
	return strings.TrimSpace(string(result.Stdout))
}

// gitEnv is the git half of the environment policy over the process's own
// environment.
//
// The five repository variables are dropped, and that is the part worth
// explaining twice. git reads GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE,
// GIT_OBJECT_DIRECTORY and GIT_COMMON_DIR *before* it looks at `-C`, so one of
// them exported — by a shell wrapper, or by a rebase the developer is in the
// middle of — silently points every command a test runs at a different
// repository, which in the worst case is this one. They are removed rather than
// blanked because a blank is not "unset" to git: it reads an empty GIT_DIR as a
// path and refuses it with "the empty string is not a valid path", which turns
// every git command in the suite into an error.
//
// GIT_TERMINAL_PROMPT=0 turns a prompt into an error. A test that has somehow
// reached a credential or passphrase prompt is a test that hangs until CI's job
// timeout, and an error naming the command is a better way to learn that.
//
// The absent configuration files are named under a directory of the test's own
// rather than under the repository, so nothing the harness names can ever appear
// as an untracked file in a working tree these tests then assert on.
func gitEnv(t testing.TB) []string {
	t.Helper()
	global, system := absentGitConfig(t.TempDir())
	base := withoutEntries(os.Environ(),
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR")
	return withEntries(base,
		"GIT_CONFIG_GLOBAL="+global,
		"GIT_CONFIG_SYSTEM="+system,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME="+GitName,
		"GIT_AUTHOR_EMAIL="+GitEmail,
		"GIT_AUTHOR_DATE="+GitDate,
		"GIT_COMMITTER_NAME="+GitName,
		"GIT_COMMITTER_EMAIL="+GitEmail,
		"GIT_COMMITTER_DATE="+GitDate,
	)
}
