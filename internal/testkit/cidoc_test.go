// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	// ciDoc is the page that describes what runs, where, and why.
	ciDoc = "docs/ci.md"
	// workflowsDir holds the workflows it describes.
	workflowsDir = ".github/workflows"
	// successJob is the aggregate every other job of ci.yml feeds.
	successJob = "ci-success"
	// ciWorkflow is the workflow that carries it.
	ciWorkflow = "ci.yml"
)

// jobKey matches the two-space-indented key that opens one job.
var jobKey = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):`)

// needsList matches the `needs:` of a job, in its inline form.
var needsList = regexp.MustCompile(`^    needs:\s*\[(.*)\]`)

// miseInvocation matches a `mise run <task>` anywhere in a workflow.
var miseInvocation = regexp.MustCompile(`mise run ([a-z][a-z0-9-]*)`)

// TestCiDocNamesEveryWorkflowFile keeps the page and the directory equal.
func TestCiDocNamesEveryWorkflowFile(t *testing.T) {
	t.Parallel()

	root := Root(t)
	files := workflowFiles(t, root)
	if len(files) < 4 {
		t.Fatalf("the scan found %d workflows, which is too few to be this repository", len(files))
	}
	page := readDoc(t, filepath.Join(root, filepath.FromSlash(ciDoc)))
	for _, file := range files {
		if !strings.Contains(page, "`"+file+"`") {
			t.Errorf("%s does not name `%s`", ciDoc, file)
		}
	}
	for _, named := range backtickedWorkflows(page) {
		if !slices.Contains(files, named) {
			t.Errorf("%s names `%s`, which %s does not hold", ciDoc, named, workflowsDir)
		}
	}
}

// TestCiDocNamesEveryJob keeps the page equal to the jobs, in both directions.
//
// A job the page does not name is a job nobody can find out the purpose of
// without reading YAML; a job the page names that no workflow defines is a
// promise about something that does not run.
func TestCiDocNamesEveryJob(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, filepath.FromSlash(ciDoc)))
	named := backtickedTokens(page)

	var all []string
	for _, file := range workflowFiles(t, root) {
		jobs := jobsOf(t, root, file)
		if len(jobs) == 0 {
			t.Errorf("%s defines no jobs; the parser has stopped seeing them", file)
		}
		for _, job := range jobs {
			all = append(all, job)
			if !slices.Contains(named, job) {
				t.Errorf("%s defines the job `%s` and %s does not name it", file, job, ciDoc)
			}
		}
	}
	for _, token := range jobsTableRows(t, page) {
		if !slices.Contains(all, token) {
			t.Errorf("%s names the job `%s`, which no workflow defines", ciDoc, token)
		}
	}
}

// TestCiDocNamesEveryMiseTaskItsJobsRun is the same ledger the development page
// keeps, pointed at the workflows.
//
// A task a workflow runs and the page does not name is a step a reader cannot
// reproduce locally, which is the one thing this page exists to make possible.
func TestCiDocNamesEveryMiseTaskItsJobsRun(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, filepath.FromSlash(ciDoc)))
	defined := miseTasks(t, root)

	var invoked []string
	for _, file := range workflowFiles(t, root) {
		source := readDoc(t, filepath.Join(root, workflowsDir, file))
		for _, match := range miseInvocation.FindAllStringSubmatch(source, -1) {
			invoked = append(invoked, match[1])
		}
	}
	slices.Sort(invoked)
	invoked = slices.Compact(invoked)
	if len(invoked) == 0 {
		t.Fatalf("no workflow runs a mise task; the scan has stopped seeing them")
	}

	for _, task := range invoked {
		if !slices.Contains(defined, task) {
			t.Errorf("a workflow runs `mise run %s`, which mise.toml does not define", task)
		}
		if !mentionsTask(page, task) {
			t.Errorf("a workflow runs `mise run %s` and %s does not name it", task, ciDoc)
		}
	}
}

// TestTheAggregateJobWaitsForEveryOtherJob is the check branch protection
// cannot make for itself.
//
// A required check is named by hand in a repository setting nothing in this
// tree can see, so a job added to ci.yml is a job branch protection does not
// wait for until somebody remembers. One aggregate that needs every other job
// turns that into a single required check, and this test is what keeps the
// aggregate's own list from going stale -- which is the way the arrangement
// fails silently.
func TestTheAggregateJobWaitsForEveryOtherJob(t *testing.T) {
	t.Parallel()

	root := Root(t)
	jobs := jobsOf(t, root, ciWorkflow)
	if !slices.Contains(jobs, successJob) {
		t.Fatalf("%s has no `%s` job, so branch protection has to name every check by hand", ciWorkflow, successJob)
	}
	needs := needsOf(t, root, ciWorkflow, successJob)
	var others []string
	for _, job := range jobs {
		if job != successJob {
			others = append(others, job)
		}
	}
	slices.Sort(needs)
	slices.Sort(others)
	if !slices.Equal(needs, others) {
		t.Errorf("`%s` waits for %v and %s defines %v;\n"+
			"\ta job the aggregate does not need is one branch protection does not wait for",
			successJob, needs, ciWorkflow, others)
	}
}

// workflowFiles is every workflow of this repository, in name order.
func workflowFiles(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(workflowsDir)))
	if err != nil {
		t.Fatalf("reading %s: %v", workflowsDir, err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yml") {
			files = append(files, entry.Name())
		}
	}
	slices.Sort(files)
	return files
}

// jobsOf is the job names one workflow defines, in file order.
//
// The two-space-indented key rather than a YAML decode, because this package
// may import nothing from this module and nothing outside the standard library
// but go-cmp -- and the shape wanted is one level of one mapping, which
// actionlint and yamllint already keep well-formed.
func jobsOf(t *testing.T, root, file string) []string {
	t.Helper()

	var jobs []string
	inJobs := false
	for _, line := range strings.Split(readDoc(t, filepath.Join(root, workflowsDir, file)), "\n") {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if inJobs && len(line) > 0 && line[0] != ' ' && line[0] != '#' {
			inJobs = false
		}
		if !inJobs {
			continue
		}
		if match := jobKey.FindStringSubmatch(line); match != nil {
			jobs = append(jobs, match[1])
		}
	}
	return jobs
}

// needsOf is the jobs one job waits for, read from its inline `needs:` list.
func needsOf(t *testing.T, root, file, job string) []string {
	t.Helper()

	inJob := false
	for _, line := range strings.Split(readDoc(t, filepath.Join(root, workflowsDir, file)), "\n") {
		if match := jobKey.FindStringSubmatch(line); match != nil {
			inJob = match[1] == job
			continue
		}
		if !inJob {
			continue
		}
		if match := needsList.FindStringSubmatch(line); match != nil {
			var needs []string
			for _, name := range strings.Split(match[1], ",") {
				if trimmed := strings.TrimSpace(name); trimmed != "" {
					needs = append(needs, trimmed)
				}
			}
			return needs
		}
	}
	t.Errorf("`%s` in %s has no inline `needs: [...]`", job, file)
	return nil
}

// backtickedWorkflows is every `*.yml` the page names.
func backtickedWorkflows(page string) []string {
	var files []string
	for _, token := range backtickedTokens(page) {
		if strings.HasSuffix(token, ".yml") {
			files = append(files, token)
		}
	}
	return files
}

// jobsTableRows is the first backticked token of every table row of the page,
// which is where the jobs are named.
func jobsTableRows(t *testing.T, page string) []string {
	t.Helper()

	var names []string
	for _, line := range strings.Split(page, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		token := strings.Trim(strings.TrimSpace(cells[0]), "`")
		if token == "" || strings.Contains(token, " ") || strings.HasSuffix(token, ".yml") {
			continue
		}
		names = append(names, token)
	}
	return names
}

// backtickedTokens is every `quoted` token of a page.
func backtickedTokens(page string) []string {
	var tokens []string
	rest := page
	for {
		open := strings.Index(rest, "`")
		if open < 0 {
			slices.Sort(tokens)
			return slices.Compact(tokens)
		}
		after := rest[open+1:]
		end := strings.Index(after, "`")
		if end < 0 {
			slices.Sort(tokens)
			return slices.Compact(tokens)
		}
		if token := after[:end]; token != "" {
			tokens = append(tokens, token)
		}
		rest = after[end+1:]
	}
}
