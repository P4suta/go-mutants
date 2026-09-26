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
	ciDoc          = "docs/ci.md"
	workflowsDir   = ".github/workflows"
	successJob     = "ci-success"
	requiredJob    = "required"
	ciWorkflow     = "ci.yml"
	actionsDir     = ".github/actions"
	actionFile     = "action.yml"
	gatesSection   = "The gates"
	nightlySection = "The nightly searches"
	actionSection  = "The composite action"
)

var jobKey = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):`)

var needsKey = regexp.MustCompile(`^    needs:(.*)$`)

var flowSequence = regexp.MustCompile(`^\s*\[(.*)\]\s*$`)

var blockEntry = regexp.MustCompile(`^      - ([a-z][a-z0-9-]*)\s*$`)

var miseInvocation = regexp.MustCompile(`mise run ([a-z][a-z0-9-]*)`)

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

func TestCiDocNamesEveryCompositeAction(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, filepath.FromSlash(ciDoc)))
	actions := compositeActions(t, root)
	if len(actions) == 0 {
		t.Fatalf("%s holds no composite action, so this repository publishes none", actionsDir)
	}
	named := backtickedTokens(page)
	for _, action := range actions {
		if !slices.Contains(named, action) {
			t.Errorf("%s holds `%s` and %s does not name it", actionsDir, action, ciDoc)
		}
	}
	for _, token := range named {
		if !strings.HasSuffix(token, "/"+actionFile) {
			continue
		}
		if !slices.Contains(actions, token) {
			t.Errorf("%s names `%s`, which this repository does not publish", ciDoc, token)
		}
	}
}

func TestCiDocNamesEveryInputAndOutputOfEveryAction(t *testing.T) {
	t.Parallel()

	root := Root(t)
	page := readDoc(t, filepath.Join(root, filepath.FromSlash(ciDoc)))
	documented := tableKeys(t, page, actionSection)

	var declared []string
	for _, action := range compositeActions(t, root) {
		source := readDoc(t, filepath.Join(root, filepath.FromSlash(action)))
		for _, section := range []string{"inputs", "outputs"} {
			keys := keysUnder(source, section)
			if len(keys) == 0 {
				t.Errorf("`%s` declares no %s; the scan has stopped seeing them", action, section)
			}
			for _, key := range keys {
				declared = append(declared, key)
				if !slices.Contains(documented, key) {
					t.Errorf("`%s` declares the %s `%s` and the `%s` section of %s does not name it",
						action, strings.TrimSuffix(section, "s"), key, actionSection, ciDoc)
				}
			}
		}
	}
	for _, key := range documented {
		if !slices.Contains(declared, key) {
			t.Errorf("the `%s` section of %s names `%s`, which no action declares",
				actionSection, ciDoc, key)
		}
	}
}

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

func TestEveryPrimitiveJobReachesTheRequiredCheck(t *testing.T) {
	t.Parallel()

	root := Root(t)
	jobs := jobsOf(t, root, ciWorkflow)
	if !slices.Contains(jobs, successJob) {
		t.Fatalf("%s has no `%s` job, so the terminal check has no aggregate to wrap", ciWorkflow, successJob)
	}
	if !slices.Contains(jobs, requiredJob) {
		t.Fatalf("%s has no `%s` job, so repositories cannot share one required check", ciWorkflow, requiredJob)
	}
	aggregateNeeds := needsOf(t, root, ciWorkflow, successJob)
	var primitives []string
	for _, job := range jobs {
		if job != successJob && job != requiredJob {
			primitives = append(primitives, job)
		}
	}
	slices.Sort(aggregateNeeds)
	slices.Sort(primitives)
	if !slices.Equal(aggregateNeeds, primitives) {
		t.Errorf("`%s` waits for %v and %s defines %v;\n"+
			"\ta primitive job the aggregate does not need cannot reach the required check",
			successJob, aggregateNeeds, ciWorkflow, primitives)
	}
	requiredNeeds := needsOf(t, root, ciWorkflow, requiredJob)
	if !slices.Equal(requiredNeeds, []string{successJob}) {
		t.Errorf("`%s` waits for %v, want only [%s];\n"+
			"\tthe terminal check must wrap the existing aggregate without creating a cycle",
			requiredJob, requiredNeeds, successJob)
	}
}

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

func jobsOf(t *testing.T, root, file string) []string {
	t.Helper()

	var jobs []string
	for _, line := range jobsBlockIn(readDoc(t, filepath.Join(root, workflowsDir, file))) {
		if match := jobKey.FindStringSubmatch(line); match != nil {
			jobs = append(jobs, match[1])
		}
	}
	return jobs
}

func jobsBlockIn(doc string) []string {
	return mappingUnder(doc, "jobs")
}

func mappingUnder(doc, key string) []string {
	var block []string
	inside := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, key+":") {
			inside = true
			continue
		}
		if inside && len(line) > 0 && line[0] != ' ' && line[0] != '#' {
			inside = false
		}
		if inside {
			block = append(block, line)
		}
	}
	return block
}

func keysUnder(doc, key string) []string {
	var keys []string
	for _, line := range mappingUnder(doc, key) {
		if match := jobKey.FindStringSubmatch(line); match != nil {
			keys = append(keys, match[1])
		}
	}
	return keys
}

func compositeActions(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(actionsDir)))
	if err != nil {
		t.Fatalf("reading %s: %v", actionsDir, err)
	}
	var actions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := actionsDir + "/" + entry.Name() + "/" + actionFile
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Errorf("%s/%s holds no %s", actionsDir, entry.Name(), actionFile)
			continue
		}
		actions = append(actions, path)
	}
	slices.Sort(actions)
	return actions
}

func ciSection(t *testing.T, page, heading string) string {
	t.Helper()

	body, ok := sectionOf(page, "## "+heading)
	if !ok {
		t.Errorf("%s has no `## %s` section", ciDoc, heading)
	}
	return body
}

func needsOf(t *testing.T, root, file, job string) []string {
	t.Helper()

	needs, found := needsIn(readDoc(t, filepath.Join(root, workflowsDir, file)), job)
	if !found {
		t.Errorf("`%s` in %s declares no `needs:` this scan can read;\n"+
			"\tit reads `[a, b]` on the key's line, the same on the line under it, and `- a` entries beneath it",
			job, file)
	}
	return needs
}

func needsIn(doc, job string) ([]string, bool) {
	body, ok := jobBodyIn(doc, job)
	if !ok {
		return nil, false
	}
	for i, line := range body {
		key := needsKey.FindStringSubmatch(line)
		if key == nil {
			continue
		}
		if flow := flowSequence.FindStringSubmatch(key[1]); flow != nil {
			return sequenceNames(flow[1]), true
		}
		if strings.TrimSpace(key[1]) != "" {
			return nil, false
		}
		rest := body[i+1:]
		if len(rest) > 0 {
			if flow := flowSequence.FindStringSubmatch(rest[0]); flow != nil {
				return sequenceNames(flow[1]), true
			}
		}
		var names []string
		for _, entry := range rest {
			match := blockEntry.FindStringSubmatch(entry)
			if match == nil {
				break
			}
			names = append(names, match[1])
		}
		return names, len(names) > 0
	}
	return nil, false
}

func jobBodyIn(doc, job string) ([]string, bool) {
	var body []string
	found := false
	for _, line := range jobsBlockIn(doc) {
		if match := jobKey.FindStringSubmatch(line); match != nil {
			if found {
				break
			}
			found = match[1] == job
			continue
		}
		if found {
			body = append(body, line)
		}
	}
	return body, found
}

func sequenceNames(body string) []string {
	var names []string
	for _, name := range strings.Split(body, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	return names
}

func backtickedWorkflows(page string) []string {
	var files []string
	for _, token := range backtickedTokens(page) {
		if strings.HasSuffix(token, ".yml") && !strings.Contains(token, "/") {
			files = append(files, token)
		}
	}
	return files
}

func jobsTableRows(t *testing.T, page string) []string {
	t.Helper()

	return append(
		tableKeys(t, page, gatesSection),
		tableKeys(t, page, nightlySection)...)
}

func tableKeys(t *testing.T, page, heading string) []string {
	t.Helper()

	var names []string
	for _, line := range strings.Split(ciSection(t, page, heading), "\n") {
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

func TestANeedsListIsReadInEverySpellingYamlAllows(t *testing.T) {
	t.Parallel()

	const spelt = `jobs:
  flow-on-the-key:
    needs: [quality, race]
    runs-on: ubuntu-latest
  flow-on-the-next-line:
    needs:
      [quality, race]
    runs-on: ubuntu-latest
  block-sequence:
    needs:
      - quality
      - race
    runs-on: ubuntu-latest
  one-name:
    needs: [quality]
    runs-on: ubuntu-latest
  waits-for-nothing-explicitly:
    needs: []
    runs-on: ubuntu-latest
  waits-for-nothing:
    runs-on: ubuntu-latest
  also-waits-for-nothing:
    runs-on: ubuntu-latest
  last:
    needs:
      - quality
`
	for _, tc := range []struct {
		job   string
		want  []string
		found bool
	}{
		{job: "flow-on-the-key", want: []string{"quality", "race"}, found: true},
		{job: "flow-on-the-next-line", want: []string{"quality", "race"}, found: true},
		{job: "block-sequence", want: []string{"quality", "race"}, found: true},
		{job: "one-name", want: []string{"quality"}, found: true},
		{job: "waits-for-nothing-explicitly", found: true},
		{job: "waits-for-nothing"},
		{job: "also-waits-for-nothing"},
		{job: "last", want: []string{"quality"}, found: true},
		{job: "absent"},
	} {
		got, found := needsIn(spelt, tc.job)
		if found != tc.found {
			t.Errorf("needsIn(_, %q) found = %v, want %v", tc.job, found, tc.found)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("needsIn(_, %q) = %v, want %v", tc.job, got, tc.want)
		}
	}
}
