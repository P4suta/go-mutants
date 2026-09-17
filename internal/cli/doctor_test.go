// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
)

var diagnosis = []check{
	{checkToolchain, statusOK, "go1.26.5 at /usr/local/go/bin/go"},
	{checkGit, statusWarn, "git is not on PATH; only `run --changed` needs it"},
	{checkModule, statusFail, "there is no go.mod in /tmp/scratch"},
	{checkPlatform, statusOK, "linux/amd64"},
}

func TestDoctorTableAlignsEveryDetail(t *testing.T) {
	text := renderChecks(diagnosis)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != len(diagnosis)+2 {
		t.Fatalf("the table is %d lines for %d checks, a header and a summary:\n%s",
			len(lines), len(diagnosis), text)
	}
	if !strings.HasPrefix(lines[0], "go-mutants "+Version) {
		t.Errorf("the first line does not name the build: %q", lines[0])
	}

	column := -1
	for i, c := range diagnosis {
		line := lines[i+1]
		at := strings.Index(line, c.Detail)
		if at < 0 {
			t.Fatalf("row %q does not carry its detail: %q", c.Name, line)
		}
		if column == -1 {
			column = at
		}
		if at != column {
			t.Errorf("the detail of %q starts at column %d, and the first row's at %d:\n%s",
				c.Name, at, column, text)
		}
		if !strings.Contains(line, c.Name) {
			t.Errorf("row %d does not name the check: %q", i, line)
		}
	}
}

func TestDoctorTableDistinguishesWarnFromFail(t *testing.T) {
	text := renderChecks(diagnosis)
	for _, want := range []string{"ok  ", "warn", "FAIL"} {
		if !strings.Contains(text, want) {
			t.Errorf("the table does not render %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "fail ") {
		t.Errorf("a failure is rendered in lower case, where it reads as one more row:\n%s", text)
	}
	if !strings.HasSuffix(text, "4 checks: 2 ok, 1 warn, 1 FAIL\n") {
		t.Errorf("the summary line does not count the verdicts:\n%s", text)
	}
}

func TestDoctorSummaryLeavesOutWhatDidNotHappen(t *testing.T) {
	text := renderChecks([]check{
		{checkToolchain, statusOK, "go1.26.5 at /usr/local/go/bin/go"},
		{checkPlatform, statusOK, "linux/amd64"},
	})
	if !strings.HasSuffix(text, "2 checks: 2 ok\n") {
		t.Errorf("a clean diagnosis does not end in the short summary:\n%s", text)
	}
}

func TestDoctorJSONSatisfiesTheSchema(t *testing.T) {
	o := &doctorOptions{json: true}
	text, err := o.render(diagnosis)
	if err != nil {
		t.Fatalf("rendering the document: %v", err)
	}
	if err = schemas.Validate(schemas.DoctorV1, []byte(text)); err != nil {
		t.Fatalf("the document does not satisfy %s: %v\n%s", schemas.DoctorV1, err, text)
	}

	var doc doctorDocument
	if err = json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("the document does not decode: %v\n%s", err, text)
	}
	if doc.DocumentType != schemas.DoctorV1 || doc.SchemaVersion != 1 || doc.ToolVersion != Version {
		t.Errorf("document identity = %q v%d from %q", doc.DocumentType, doc.SchemaVersion, doc.ToolVersion)
	}
	if len(doc.Checks) != len(diagnosis) {
		t.Fatalf("the document carries %d checks, and %d were run", len(doc.Checks), len(diagnosis))
	}
	if doc.Checks[2].Status != statusFail {
		t.Errorf("the failing check is %q in the document, want %q", doc.Checks[2].Status, statusFail)
	}
}

func TestDoctorFailsWhereThereIsNoModule(t *testing.T) {
	isolatedCache(t)

	code, stdout, stderr := execute(t, "doctor")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "FAIL  "+checkModule) {
		t.Errorf("the table does not fail the module check:\n%s", stdout)
	}
	if !strings.Contains(stderr, "error "+string(CodeEnvironmentUnusable)) {
		t.Errorf("stderr = %q, want the coded failure", stderr)
	}
	for _, name := range []string{checkToolchain, checkGit, checkCacheDir, checkPlatform, checkMemory, checkConfiguration} {
		if !strings.Contains(stdout, name) {
			t.Errorf("the table stopped before %q:\n%s", name, stdout)
		}
	}
}

func TestDoctorProbesOnlyItsOwnCacheDirectory(t *testing.T) {
	root := isolatedCache(t)
	base := filepath.Dir(root)
	neighbour := filepath.Join(base, "somebody-else")
	if err := os.MkdirAll(neighbour, 0o700); err != nil {
		t.Fatalf("creating the neighbouring directory: %v", err)
	}

	got := cacheCheck()
	if got.Status != statusOK {
		t.Fatalf("the cache check failed against a writable directory: %+v", got)
	}
	if !strings.Contains(got.Detail, root) {
		t.Errorf("the check does not name the directory it probed: %q", got.Detail)
	}
	entries, err := os.ReadDir(neighbour)
	if err != nil {
		t.Fatalf("reading the neighbouring directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the probe wrote %d entries into a directory that is not go-mutants': %v", len(entries), entries)
	}
	entries, err = os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the cache root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "go-mutants-doctor-") {
			t.Errorf("the probe file %s was left behind", entry.Name())
		}
	}
}

func TestDoctorDetailsCarryNoDiagnosticCode(t *testing.T) {
	dir := t.TempDir()

	missing := moduleCheck(dir)
	if missing.Status != statusFail {
		t.Fatalf("a directory that is not a module root is not a failing row: %+v", missing)
	}
	if want := "there is no " + moduleFileName + " in " + dir; missing.Detail != want {
		t.Errorf("detail = %q, want %q", missing.Detail, want)
	}
	if want := "there is no " + moduleFileName + " in /tmp/scratch"; diagnosis[2].Detail != want {
		t.Errorf("the fixture's failing row is %q, which is not the shape moduleCheck produces (%q)",
			diagnosis[2].Detail, want)
	}

	path := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(path, []byte("version = 1\n[mutation]\nnope = 1\n"), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	broken := configurationCheck(dir)
	if broken.Status != statusFail {
		t.Fatalf("an unknown key is not a failing row: %+v", broken)
	}
	if !strings.HasPrefix(broken.Detail, path+":3:") {
		t.Errorf("detail = %q, want it to open at the file and position of the mistake", broken.Detail)
	}

	for _, row := range []check{missing, broken} {
		if _, _, coded := splitCode(row.Detail); coded {
			t.Errorf("the %q row carries a diagnostic code in a table cell: %q", row.Name, row.Detail)
		}
	}
}

func TestDoctorPublishesItsCheckNames(t *testing.T) {
	want := publishedCheckNames(t)

	isolatedCache(t)
	constants := []string{
		checkToolchain, checkModule, checkGit, checkCacheDir,
		checkPlatform, checkMemory, checkConfiguration,
	}
	if !slices.Equal(constants, want) {
		t.Errorf("the check names are %q, and the published set is %q", constants, want)
	}

	names := []string{}
	for _, c := range diagnose(t.Context(), t.TempDir()) {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, want) {
		t.Errorf("the diagnosis runs %q, and the documented order is %q", names, want)
	}
}

func TestDoctorReadsTheConfigurationAndSaysWhere(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)

	absent := configurationCheck(dir)
	if absent.Status != statusOK || !strings.Contains(absent.Detail, config.FileName) {
		t.Errorf("an absent configuration is not a clean row: %+v", absent)
	}

	if err := os.WriteFile(path, []byte("version = 1\n[mutation]\nprofile = \"balanced\"\n"), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	valid := configurationCheck(dir)
	if valid.Status != statusOK || !strings.Contains(valid.Detail, path) {
		t.Errorf("a valid configuration is not a clean row: %+v", valid)
	}

	if err := os.WriteFile(path, []byte("version = 1\n[mutation]\nnope = 1\n"), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	broken := configurationCheck(dir)
	if broken.Status != statusFail {
		t.Fatalf("an unknown key is not a failing row: %+v", broken)
	}
	if !strings.Contains(broken.Detail, ":3:") {
		t.Errorf("the row does not carry the position of the mistake: %q", broken.Detail)
	}

	if err := os.WriteFile(path, []byte("version = 1\n[report]\nhigh = 10\nlow = 90\n"), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	if crossed := configurationCheck(dir); crossed.Status != statusFail {
		t.Errorf("a configuration a run would refuse is not a failing row: %+v", crossed)
	}
}

func TestDoctorSaysWhetherAMemoryBoundIsEnforced(t *testing.T) {
	t.Parallel()

	got := memoryCheck()
	if got.Name != checkMemory {
		t.Errorf("the memory check is named %q, want %q", got.Name, checkMemory)
	}

	switch runner.MemoryBound() {
	case runner.MemoryEnforcedByKernel, runner.MemoryEnforcedBySampler:
		if got.Status != statusOK {
			t.Errorf("a bound this platform enforces is reported %q: %s", got.Status, got.Detail)
		}
	case runner.MemoryUnenforced:
		if got.Status != statusWarn {
			t.Errorf("a bound this platform does not enforce is reported %q, not a warning: %s",
				got.Status, got.Detail)
		}
	default:
		t.Fatalf("runner.MemoryBound() is %q, which this test does not know how to judge",
			runner.MemoryBound())
	}

	if got.Detail == "" {
		t.Error("the memory check says nothing; a doctor line with no message cannot be acted on")
	}
}

func publishedCheckNames(t *testing.T) []string {
	t.Helper()

	const anchor = "The check names are stable within the schema version"
	page := readRepoFile(t, testkit.Root(t), "docs/json-schema.md")
	start := strings.Index(page, anchor)
	if start < 0 {
		t.Fatalf("docs/json-schema.md no longer says %q, so this test is reading a page that moved", anchor)
	}
	paragraph := page[start:]
	if end := strings.Index(paragraph, "\n\n"); end >= 0 {
		paragraph = paragraph[:end]
	}

	var names []string
	for rest := paragraph; ; {
		open := strings.Index(rest, "`")
		if open < 0 {
			break
		}
		rest = rest[open+1:]
		close := strings.Index(rest, "`")
		if close < 0 {
			break
		}
		names = append(names, rest[:close])
		rest = rest[close+1:]
	}
	if len(names) < 5 {
		t.Fatalf("docs/json-schema.md names %d checks in that paragraph, which is fewer than it has ever had: %q",
			len(names), names)
	}
	return names
}
