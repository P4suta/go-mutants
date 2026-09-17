//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/testkit"
)

const (
	workflowPath = "../.github/workflows/ci.yml"

	ciDocumentation = "docs/ci.md"

	jobTableHeading = "| Job | What it runs |"

	miseInvocation = "mise run "

	miseAction = "jdx/mise-action@"
)

var miseTaskInvocation = regexp.MustCompile(`mise run ([a-z][a-z0-9-]*)`)

var workflowJob = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):$`)

const jobsKey = "jobs:"

var backquotedName = regexp.MustCompile("`([^`]+)`")

func TestEveryJobThatReadsHistoryAsksForIt(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(workflowPath)))
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	var shallow []string
	job := ""
	depth := false
	checksOut := false
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == jobsKey {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if match := workflowJob.FindStringSubmatch(line); match != nil {
			if job != "" && checksOut && !depth {
				shallow = append(shallow, job)
			}
			job, depth, checksOut = match[1], false, false
			continue
		}
		if strings.Contains(line, "fetch-depth:") {
			depth = true
		}
		if strings.Contains(line, "actions/checkout") {
			checksOut = true
		}
	}
	if job != "" && checksOut && !depth {
		shallow = append(shallow, job)
	}
	if len(shallow) > 0 {
		t.Errorf("%d job(s) check out a shallow clone: %s\n\n"+
			"Without fetch-depth a job holds no tags and a truncated history, so a\n"+
			"question about a range it does not have is answered \"nothing changed\".",
			len(shallow), strings.Join(shallow, ", "))
	}
}

func TestTheCheckoutIsNotShallowHere(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	command := exec.CommandContext(t.Context(), testkit.GitBinary(t), "-C", root, "rev-parse", "--is-shallow-repository")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("ask git whether the clone is shallow: %v", err)
	}
	if strings.TrimSpace(string(output)) == "true" {
		t.Fatal("this clone is shallow, so a changeset scope here would be told that nothing changed")
	}
}

func workflowJobs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	var jobs []string
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == jobsKey {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if match := workflowJob.FindStringSubmatch(line); match != nil {
			jobs = append(jobs, match[1])
		}
	}
	return jobs
}

func documentedJobs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", ciDocumentation, err)
	}
	lines := strings.Split(string(data), "\n")
	heading := slices.Index(lines, jobTableHeading)
	if heading < 0 {
		t.Fatalf("%s no longer holds the job table heading %q", ciDocumentation, jobTableHeading)
	}
	var documented []string
	for _, line := range lines[heading+2:] {
		if !strings.HasPrefix(line, "| `") {
			break
		}
		match := backquotedName.FindStringSubmatch(line)
		if match == nil {
			break
		}
		documented = append(documented, match[1])
	}
	return documented
}

func TestEveryJobThatRunsAMiseTaskInstallsMise(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(workflowPath)))
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	var broken []string
	job, runs, installs := "", false, false
	settle := func() {
		if job != "" && runs && !installs {
			broken = append(broken, job)
		}
	}
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == jobsKey {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if match := workflowJob.FindStringSubmatch(line); match != nil {
			settle()
			job, runs, installs = match[1], false, false
			continue
		}
		if strings.Contains(line, miseInvocation) {
			runs = true
		}
		if strings.Contains(line, miseAction) {
			installs = true
		}
	}
	settle()
	if len(broken) > 0 {
		t.Errorf("%d job(s) run a mise task without installing mise: %s\n\n"+
			"The task exists, is correct and is pinned by a ledger; the job still fails\n"+
			"on `mise: command not found`, and only the workflow can tell you.",
			len(broken), strings.Join(broken, ", "))
	}
}
