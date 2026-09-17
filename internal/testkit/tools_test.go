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

const toolProbeEnv = "TESTKIT_TOOL_PROBE"

func TestToolProbeProcess(t *testing.T) {
	switch os.Getenv(toolProbeEnv) {
	case "go":
		t.Logf("go is at %s", GoBinary(t))
	case "git":
		t.Logf("git is at %s", GitBinary(t))
	case "go-after-env":
		Env(t)
		t.Logf("go is at %s", GoBinary(t))
	}
}

func TestGoBinaryAnswersTheVersionProbe(t *testing.T) {
	t.Parallel()

	gobin := GoBinary(t)
	result := Exec(t, t.TempDir(), Compose(t, t.TempDir()), gobin, "version")
	RequireExit(t, result, 0, "`go version`")
	RequireOutput(t, result, "`go version`", "go version go")
}

func TestGitBinaryAnswersTheVersionProbe(t *testing.T) {
	t.Parallel()

	gitbin := GitBinary(t)
	result := Exec(t, t.TempDir(), Compose(t, t.TempDir()), gitbin, "--version")
	RequireExit(t, result, 0, "`git --version`")
	RequireOutput(t, result, "`git --version`", "git version")
}

func TestGoBinarySkipsOrFailsByPolicy(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{"go", "git"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()

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

func TestGitReturnsWhatTheCommandPrintedRatherThanWhatItSaid(t *testing.T) {
	t.Parallel()
	GitBinary(t)

	root := Copy(t, "simple")
	head := GitInit(t, root)

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

func TestGitIgnoresTheRepositoryVariablesInTheEnvironment(t *testing.T) {
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
