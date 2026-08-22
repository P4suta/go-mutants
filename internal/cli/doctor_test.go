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
	"github.com/P4suta/go-mutants/internal/schemas"
)

// diagnosis is the fabricated finding set the rendering tests use. It carries
// one row of each verdict, and names of three different lengths, because the
// alignment is the thing under test.
//
// Every detail is in the shape the check that produces it really produces, and
// [TestDoctorDetailsCarryNoDiagnosticCode] holds the failing row to exactly
// that sentence. A fixture written in a shape no check can report would prove
// the alignment of a table nobody sees and validate a document nobody
// publishes — which is what this one was until the details stopped carrying
// their diagnostic codes.
var diagnosis = []check{
	{checkToolchain, statusOK, "go1.26.5 at /usr/local/go/bin/go"},
	{checkGit, statusWarn, "git is not on PATH; only `run --changed` needs it"},
	{checkModule, statusFail, "there is no go.mod in /tmp/scratch"},
	{checkPlatform, statusOK, "linux/amd64"},
}

// TestDoctorTableAlignsEveryDetail is what makes the table a table: whatever a
// check is called and whatever it found, the details start in one column, so
// the answers can be read down the page rather than hunted for.
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

// TestDoctorTableShoutsOnlyTheFailures. A warn is a fact about an opt-in
// feature and a FAIL is the row the exit status is about; a reader skimming the
// table has to be able to tell them apart at a glance.
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

// TestDoctorSummaryLeavesOutWhatDidNotHappen keeps the ordinary answer short: a
// machine with nothing wrong with it should not be told it has zero failures.
func TestDoctorSummaryLeavesOutWhatDidNotHappen(t *testing.T) {
	text := renderChecks([]check{
		{checkToolchain, statusOK, "go1.26.5 at /usr/local/go/bin/go"},
		{checkPlatform, statusOK, "linux/amd64"},
	})
	if !strings.HasSuffix(text, "2 checks: 2 ok\n") {
		t.Errorf("a clean diagnosis does not end in the short summary:\n%s", text)
	}
}

// TestDoctorJSONSatisfiesTheSchema is the promise `--json` makes. The document
// is validated before it is printed, so this proves both that the schema is
// registered and that what the command emits satisfies it.
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
	// Lowercase in the document whatever the table shouted: the enum is
	// published, and a consumer branches on it.
	if doc.Checks[2].Status != statusFail {
		t.Errorf("the failing check is %q in the document, want %q", doc.Checks[2].Status, statusFail)
	}
}

// TestDoctorFailsWhereThereIsNoModule drives the whole command. A directory
// that is not a module root is the one failure every machine can reproduce, and
// it proves the three things the exit contract rests on: the table is printed,
// the failure is coded, and the status is 2.
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
	// The diagnosis is complete even though a check failed: a machine with two
	// problems must not be told about them one round trip at a time.
	for _, name := range []string{checkToolchain, checkGit, checkCacheDir, checkPlatform, checkConfiguration} {
		if !strings.Contains(stdout, name) {
			t.Errorf("the table stopped before %q:\n%s", name, stdout)
		}
	}
}

// TestDoctorProbesOnlyItsOwnCacheDirectory. The probe proves the directory is
// writable by writing in it, so where it writes is a safety property: go-mutants
// creates and removes a file inside its own directory under the operating
// system's cache root, and touches nothing else there.
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
	// And it leaves nothing of its own behind either.
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

// TestDoctorDetailsCarryNoDiagnosticCode drives the two checks that render a
// failure into a cell.
//
// A code is what `RenderError` writes in front of every line on standard error,
// where it makes a failure greppable. In a table whose first column is already
// the verdict it is noise in front of the sentence, and in `doctor --json` it
// is noise inside a field docs/json-schema.md describes as the reason — a
// consumer branches on `name` and `status`, and reads `detail`.
func TestDoctorDetailsCarryNoDiagnosticCode(t *testing.T) {
	dir := t.TempDir()

	missing := moduleCheck(dir)
	if missing.Status != statusFail {
		t.Fatalf("a directory that is not a module root is not a failing row: %+v", missing)
	}
	if want := "there is no " + moduleFileName + " in " + dir; missing.Detail != want {
		t.Errorf("detail = %q, want %q", missing.Detail, want)
	}
	// And the fixture the rendering tests are written against says what this
	// path really says, rather than a tidier thing nothing produces.
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
	// The code goes and the position stays: the position is the whole value of
	// what the configuration layer worked out.
	if !strings.HasPrefix(broken.Detail, path+":3:") {
		t.Errorf("detail = %q, want it to open at the file and position of the mistake", broken.Detail)
	}

	for _, row := range []check{missing, broken} {
		if _, _, coded := splitCode(row.Detail); coded {
			t.Errorf("the %q row carries a diagnostic code in a table cell: %q", row.Name, row.Detail)
		}
	}
}

// TestDoctorPublishesItsCheckNames pins the strings docs/json-schema.md tells a
// consumer it may branch on, and the order the table and the document print
// them in. Renaming one, or adding a seventh check, is a change to what
// go-mutants publishes; this is what makes it a visible one.
func TestDoctorPublishesItsCheckNames(t *testing.T) {
	isolatedCache(t)

	want := []string{"go toolchain", "module", "git", "cache directory", "platform", "configuration"}
	constants := []string{checkToolchain, checkModule, checkGit, checkCacheDir, checkPlatform, checkConfiguration}
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

// TestDoctorReadsTheConfigurationAndSaysWhere is the row a user runs `doctor`
// for after a failed run: which file was read, and what is wrong with it.
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
	// The position is the whole value of the configuration layer's diagnostics,
	// and a row that dropped it would send somebody reading the file by eye.
	if !strings.Contains(broken.Detail, ":3:") {
		t.Errorf("the row does not carry the position of the mistake: %q", broken.Detail)
	}

	// A pair of values that are each valid and cannot both be right is only
	// caught by resolving the file, not by parsing it.
	if err := os.WriteFile(path, []byte("version = 1\n[report]\nhigh = 10\nlow = 90\n"), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	if crossed := configurationCheck(dir); crossed.Status != statusFail {
		t.Errorf("a configuration a run would refuse is not a failing row: %+v", crossed)
	}
}
