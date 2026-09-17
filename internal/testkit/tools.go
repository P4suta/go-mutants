// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const InitialCommitMessage = "import the tree"

func GoBinary(t testing.TB) string {
	t.Helper()
	path := toolPath(t, "go")
	rememberToolchain(t, path)
	logInputs(t, "toolchain="+path)
	return path
}

func GitBinary(t testing.TB) string {
	t.Helper()
	path := toolPath(t, "git")
	rememberToolchain(t, path)
	logInputs(t, "toolchain="+path)
	return path
}

func RequireTools() bool {
	return truthy(harnessSetting(RequireToolsEnv, pinnedRequireTools))
}

var pinnedRequireTools = os.Getenv(RequireToolsEnv)

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

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

func GitInit(t testing.TB, root string) string {
	t.Helper()
	Git(t, root, "init", "--quiet")
	Git(t, root, "symbolic-ref", "HEAD", "refs/heads/main")
	Git(t, root, "add", "--all")
	Git(t, root, "commit", "--quiet", "--allow-empty", "--message", InitialCommitMessage)
	return Git(t, root, "rev-parse", "HEAD")
}

func GitCommit(t testing.TB, root, message string) string {
	t.Helper()
	Git(t, root, "add", "--all")
	Git(t, root, "commit", "--quiet", "--message", message)
	return Git(t, root, "rev-parse", "HEAD")
}

func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	git := toolPath(t, "git")
	argv := append([]string{git, "-C", dir}, args...)
	result := Exec(t, dir, gitEnv(t), argv...)
	RequireExit(t, result, 0, "`git "+strings.Join(args, " ")+"`")
	return strings.TrimSpace(string(result.Stdout))
}

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
