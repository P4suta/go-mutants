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
	// actionsDir holds the composite actions this repository publishes.
	actionsDir = ".github/actions"
	// actionFile is the one file a composite action is.
	actionFile = "action.yml"
	// gatesSection and nightlySection are the two headings of the page whose
	// tables have a job in the first column. Scoping the scan to them is what
	// lets the page carry other tables -- of inputs, of outputs -- without
	// their first column being read as the name of a job.
	gatesSection   = "The gates"
	nightlySection = "The nightly searches"
	// actionSection is the heading whose tables name what the composite action
	// takes and what it gives back.
	actionSection = "The composite action"
)

// jobKey matches the two-space-indented key that opens one job.
var jobKey = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):`)

// needsKey matches the `needs:` of a job and whatever shares its line.
var needsKey = regexp.MustCompile(`^    needs:(.*)$`)

// flowSequence matches YAML's bracketed one-line sequence, whether it sits on
// the key's own line or on the line under it.
var flowSequence = regexp.MustCompile(`^\s*\[(.*)\]\s*$`)

// blockEntry matches one entry of the block sequence under a `needs:`.
var blockEntry = regexp.MustCompile(`^      - ([a-z][a-z0-9-]*)\s*$`)

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

// TestCiDocNamesEveryCompositeAction keeps the page equal to `.github/actions`.
//
// A composite action is this repository's published surface for somebody
// else's pipeline, and the only thing that makes one discoverable is a page
// that names it. In the other direction, a page that names an action nobody
// publishes sends a reader to a path `uses:` cannot resolve -- a failure they
// meet in their own CI rather than in ours.
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

// TestCiDocNamesEveryInputAndOutputOfEveryAction is the ledger that matters
// most about an action, because an action *is* its inputs and its outputs.
//
// Nothing compiles a composite action. An input the page does not name is one
// a caller never learns exists; an output it names that the action does not
// produce is an empty string a caller will read as a zero. Both directions,
// and both sets, against the tables the page carries for them.
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
	for _, line := range jobsBlockIn(readDoc(t, filepath.Join(root, workflowsDir, file))) {
		if match := jobKey.FindStringSubmatch(line); match != nil {
			jobs = append(jobs, match[1])
		}
	}
	return jobs
}

// jobsBlockIn is the lines a workflow's `jobs:` mapping holds, which is where
// a two-space-indented key means a job rather than, say, a trigger.
func jobsBlockIn(doc string) []string {
	return mappingUnder(doc, "jobs")
}

// mappingUnder is the lines nested under one top-level key of a YAML document,
// which is as much structure as this file reads.
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

// keysUnder is the two-space-indented keys of one top-level mapping.
func keysUnder(doc, key string) []string {
	var keys []string
	for _, line := range mappingUnder(doc, key) {
		if match := jobKey.FindStringSubmatch(line); match != nil {
			keys = append(keys, match[1])
		}
	}
	return keys
}

// compositeActions is every composite action this repository publishes, by the
// repository-relative path of its one file.
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

// ciSection is the body under one `## ` heading of this page, and a failure
// when the page does not carry it -- a missing section is the way a ledger
// over one would otherwise pass by comparing two empty sets.
func ciSection(t *testing.T, page, heading string) string {
	t.Helper()

	body, ok := sectionOf(page, "## "+heading)
	if !ok {
		t.Errorf("%s has no `## %s` section", ciDoc, heading)
	}
	return body
}

// needsOf is the jobs one job waits for, read from its `needs:` list.
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

// needsIn is the jobs one job of a workflow document waits for, in whichever
// of YAML's three spellings the file uses, and whether it declares one at all.
//
// The three are not interchangeable in practice: a flow sequence on the key's
// own line grows past this repository's 80-column rule somewhere around the
// seventh job name. Which spelling a workflow uses is therefore a consequence
// of how many jobs it has, and reading all three leaves that decision with
// whoever writes the YAML instead of with this file.
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
			// An anchor, a template expression, something else this does not
			// read. Reporting it as absent is the fail-closed answer: the
			// caller says so out loud rather than comparing against a guess.
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

// jobBodyIn is the lines one job holds, from its own key to the next job's or
// the end of the mapping -- so that a list belongs to the job it is indented
// under and to no other.
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

// sequenceNames is the names a flow sequence's comma-separated body holds.
func sequenceNames(body string) []string {
	var names []string
	for _, name := range strings.Split(body, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	return names
}

// backtickedWorkflows is every bare `*.yml` name the page uses.
//
// Bare, because a token with a slash in it is a path rather than a workflow
// name, and the paths this page carries point at the composite actions, whose
// own ledger owns them.
func backtickedWorkflows(page string) []string {
	var files []string
	for _, token := range backtickedTokens(page) {
		if strings.HasSuffix(token, ".yml") && !strings.Contains(token, "/") {
			files = append(files, token)
		}
	}
	return files
}

// jobsTableRows is the first backticked token of every table row of the two
// sections whose tables are about jobs.
func jobsTableRows(t *testing.T, page string) []string {
	t.Helper()

	return append(
		tableKeys(t, page, gatesSection),
		tableKeys(t, page, nightlySection)...)
}

// tableKeys is the first backticked token of every table row of one section of
// the page, which is where that section's table names what it is about.
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

// TestANeedsListIsReadInEverySpellingYamlAllows keeps the aggregate's ledger
// from doubling as a rule about how the workflow is typed.
//
// YAML spells a sequence three ways and the `needs:` of an aggregate that
// waits for seven jobs cannot use the first of them: on the key's own line it
// is 86 columns, and yamllint refuses a line past 80 everywhere in this
// repository. A scan that read only that spelling would report "no needs
// list" for a workflow that is correct -- a fact about the scan, printed as if
// it were a fact about the file -- and the way out would be to shorten job
// names for the benefit of a regular expression.
//
// So the spelling is what this reads past, and the set of names is what it
// returns. What it must not do is read past the *job*: a list belongs to the
// job it is indented under, and attributing the next job's needs to this one
// would make the ledger agree with itself while naming the wrong thing.
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
		// An empty list is a declaration, not an absence: a job that waits for
		// nothing on purpose reads back as waiting for nothing, and the ledger
		// says so rather than saying the key is missing.
		{job: "waits-for-nothing-explicitly", found: true},
		// A job with no `needs:` at all, followed by one that also has none:
		// nothing may be borrowed from further down the file.
		{job: "waits-for-nothing"},
		// The same, where the next job *does* declare one.
		{job: "also-waits-for-nothing"},
		// The last job of the file, where the body ends at EOF rather than at
		// the next key.
		{job: "last", want: []string{"quality"}, found: true},
		// A job the document does not define at all.
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
