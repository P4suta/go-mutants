// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

// The fixture workspace is three files chosen for what their bytes do to a
// coordinate: one pure ASCII, one whose mutants sit after a two-byte rune and
// an astral emoji on the same line, and one written with CRLF endings. The
// files are never compiled — the projection reads them as text — so they are
// short and say what they are for.
const (
	alphaProjPath = "internal/alpha/alpha.go"
	betaProjPath  = "internal/beta/beta.go"
	crlfProjPath  = "internal/crlf/crlf.go"
)

const alphaProjSource = `package alpha

import (
	"fmt"
	"io"
)

// Add adds, unless it does not.
func Add(a, b int) int {
	if a == b {
		return a + b
	}
	return a - b
}

// Less reports whether a is less than b. Its operator is why this fixture is
// used by the HTML test too: every less-than in the projection has to leave the
// JSON island as an escape.
func Less(a, b int) bool {
	return a < b
}

// Log writes both numbers.
func Log(w io.Writer, a, b int) {
	fmt.Fprintf(w,
		"%d %d", a, b)
}
`

// betaProjSource puts a two-byte rune and a four-byte one in front of the
// mutant on line 5, which is the case a byte column and a rune column both get
// wrong and in opposite directions.
const betaProjSource = `package beta

// Ready reports whether the party is this one.
func Ready(s string) bool {
	return "¥🎉" != s
}

// Level reports whether n is enough.
func Level(n int) bool {
	return n >= 3
}
`

// crlfProjSource is the same kind of file written on Windows. The '\r' is an
// ordinary character on the line it terminates; see TestUTF16PositionOnCRLF.
const crlfProjSource = "package crlf\r\n" +
	"\r\n" +
	"// Even reports whether n is even.\r\n" +
	"func Even(n int) bool {\r\n" +
	"\treturn n%2 == 0\r\n" +
	"}\r\n"

// projectionWorkspace writes the fixture files into a temporary tree and
// returns its root.
func projectionWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, source := range map[string]string{
		alphaProjPath: alphaProjSource,
		betaProjPath:  betaProjSource,
		crlfProjPath:  crlfProjSource,
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(source), 0o600); err != nil {
			t.Fatalf("writing %s: %v", full, err)
		}
	}
	return root
}

// span locates a unique piece of text in a fixture source and returns its
// half-open byte range, which is exactly what discovery would have recorded.
//
// Uniqueness is required rather than assumed: a fixture whose needle matched
// twice would silently pin the projection against the wrong half of a file, and
// the golden would then be a record of the mistake.
func span(t *testing.T, source, needle string) (uint32, uint32) {
	t.Helper()
	i := strings.Index(source, needle)
	if i < 0 {
		t.Fatalf("the fixture source does not contain %q", needle)
	}
	if strings.Contains(source[i+1:], needle) {
		t.Fatalf("%q is not unique in the fixture source", needle)
	}
	return uint32(i), uint32(i + len(needle))
}

// projectionMutant is one row of the fixture, written the way a person can read
// it. The span is derived from the source rather than written down, so the
// fixture cannot drift from the files it describes.
type projectionMutant struct {
	displayID   string
	path        string
	source      string
	family      string
	rule        string
	needle      string
	replacement string
	outcome     report.Outcome
	notRun      report.NotRunReason
}

// projectionFixture covers every outcome the document can carry, in every file
// shape, plus the one row that is not a mutant at all: a rejection.
func projectionFixture(t *testing.T) *report.Report {
	t.Helper()
	rows := []projectionMutant{
		{
			displayID: "a1b2c3d4", path: alphaProjPath, source: alphaProjSource,
			family: "comparison", rule: "eq-to-neq",
			needle: "==", replacement: "!=", outcome: report.OutcomeKilled,
		},
		{
			displayID: "b2c3d4e5", path: alphaProjPath, source: alphaProjSource,
			family: "integer-arithmetic", rule: "add-to-sub",
			needle: "a + b", replacement: "a - b", outcome: report.OutcomeSurvived,
		},
		{
			displayID: "c3d4e5f6", path: alphaProjPath, source: alphaProjSource,
			family: "integer-arithmetic", rule: "sub-to-add",
			needle: "a - b", replacement: "a + b", outcome: report.OutcomeTimedOut,
		},
		{
			displayID: "1a2b3c4d", path: alphaProjPath, source: alphaProjSource,
			family: "comparison", rule: "lt-to-le",
			needle: "a < b", replacement: "a <= b", outcome: report.OutcomeKilled,
		},
		{
			// A multi-line original, which is what a statement deletion of a
			// wrapped call looks like, and an empty replacement.
			displayID: "d4e5f607", path: alphaProjPath, source: alphaProjSource,
			family: "statement-deletion", rule: "delete-call-statement",
			needle:  "fmt.Fprintf(w,\n\t\t\"%d %d\", a, b)",
			outcome: report.OutcomeNotRun, notRun: report.NotRunOutOfSelection,
		},
		{
			displayID: "e5f60718", path: betaProjPath, source: betaProjSource,
			family: "comparison", rule: "neq-to-eq",
			needle: "!=", replacement: "==", outcome: report.OutcomeInconclusive,
		},
		{
			displayID: "f6071829", path: betaProjPath, source: betaProjSource,
			family: "comparison", rule: "ge-to-gt",
			needle: ">=", replacement: ">", outcome: report.OutcomeErrored,
		},
		{
			displayID: "0718293a", path: crlfProjPath, source: crlfProjSource,
			family: "comparison", rule: "eq-to-neq",
			needle: "==", replacement: "!=",
			outcome: report.OutcomeNotRun, notRun: report.NotRunInterrupted,
		},
		{
			displayID: "18293a4b", path: crlfProjPath, source: crlfProjSource,
			family: "integer-arithmetic", rule: "rem-to-mul",
			needle: "%", replacement: "*",
			outcome: report.OutcomeNotRun, notRun: report.NotRunOtherShard,
		},
	}

	mutants := make([]report.Mutant, 0, len(rows))
	for _, row := range rows {
		start, end := span(t, row.source, row.needle)
		m := report.Mutant{
			ID:          strings.Repeat(row.displayID, 8),
			DisplayID:   row.displayID,
			Path:        row.path,
			Family:      row.family,
			Rule:        row.rule,
			StartByte:   start,
			EndByte:     end,
			Original:    row.needle,
			Replacement: row.replacement,
			Outcome:     row.outcome,
		}
		if row.notRun != "" {
			reason := row.notRun.String()
			m.NotRunReason = &reason
		}
		mutants = append(mutants, m)
	}

	return &report.Report{
		DocumentType:  report.DocumentType,
		SchemaVersion: report.SchemaVersion,
		Mutants:       mutants,
		Rejected: []report.Rejected{{
			ID:        strings.Repeat("29", 32),
			DisplayID: "293a4b5c",
			Path:      betaProjPath,
			// The `3` in `n >= 3`, whose line is pure ASCII, so the coordinate
			// a rejection carries needs no conversion to be checkable by eye.
			Line:       10,
			Column:     14,
			Rule:       "return-zero-numeric",
			Diagnostic: "internal/beta/beta.go:10:14: cannot use 0 (untyped int constant) as bool value\n\tin return statement",
		}},
	}
}

// TestProjectionGolden pins the whole document, byte for byte.
//
// It is a golden rather than a set of field assertions because every part of
// this file is a promise to somebody else's tool: the key order, the status
// spellings, the shape of a location, and the two-space indentation are all
// things a reader of `mutation.json` can see, and a golden is the only kind of
// test that notices when one of them moves.
func TestProjectionGolden(t *testing.T) {
	t.Parallel()

	projection, err := report.Project(report.ProjectionOptions{
		Report:        projectionFixture(t),
		WorkspaceRoot: projectionWorkspace(t),
		High:          80,
		Low:           60,
	})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	got, err := projection.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path := filepath.Join("testdata", "mutation-report.golden.json")
	if *updateGolden {
		if writeErr := os.WriteFile(path, got, 0o644); writeErr != nil {
			t.Fatalf("rewriting %s: %v", path, writeErr)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("the marshalled projection does not match %s\n--- got ---\n%s\n--- want ---\n%s",
			path, got, want)
	}

	// The same bytes, through the vendored schema. A golden that matched a
	// document the format refuses would be a very precise record of a mistake.
	if err = report.ValidateProjection(got); err != nil {
		t.Fatalf("the golden projection does not validate: %v", err)
	}
}

// TestProjectionIsDeterministic proves two projections of one run are the same
// file, which is what makes `mutation.json` diffable between runs.
func TestProjectionIsDeterministic(t *testing.T) {
	t.Parallel()

	root := projectionWorkspace(t)
	first := marshalProjection(t, projectionFixture(t), root)
	second := marshalProjection(t, projectionFixture(t), root)
	if string(first) != string(second) {
		t.Error("two projections of one run produced different bytes")
	}
}

// TestProjectionCoordinatesAreUTF16 reads the two coordinates out of the
// document that a byte column would get wrong, and states the arithmetic in the
// failure message.
func TestProjectionCoordinatesAreUTF16(t *testing.T) {
	t.Parallel()

	doc := decodeProjection(t, marshalProjection(t, projectionFixture(t), projectionWorkspace(t)))
	beta, ok := doc.Files[betaProjPath]
	if !ok {
		t.Fatalf("the projection has no %s; it has %v", betaProjPath, fileNames(doc))
	}
	mutant := findMutant(t, beta, "e5f60718")
	// Line 5 is "\treturn \"¥🎉\" != s". Before the '!' stand a tab, `return`,
	// a space, a quote, ¥ (one UTF-16 unit, two bytes), 🎉 (two units, four
	// bytes), a quote, and a space: fourteen units, so the column is 15. A byte
	// column would say 18 and a rune column 14.
	if mutant.Location.Start.Line != 5 || mutant.Location.Start.Column != 15 {
		t.Errorf("the mutant after ¥🎉 starts at %d:%d, want 5:15 (bytes would say 5:18, runes 5:14)",
			mutant.Location.Start.Line, mutant.Location.Start.Column)
	}
	if mutant.Location.End.Line != 5 || mutant.Location.End.Column != 17 {
		t.Errorf("it ends at %d:%d, want 5:17 — end is exclusive, and `!=` is two units wide",
			mutant.Location.End.Line, mutant.Location.End.Column)
	}
}

// TestProjectionOnCRLF pins that a file written on Windows projects to the same
// coordinates a viewer splitting it on '\n' would compute.
func TestProjectionOnCRLF(t *testing.T) {
	t.Parallel()

	doc := decodeProjection(t, marshalProjection(t, projectionFixture(t), projectionWorkspace(t)))
	crlf, ok := doc.Files[crlfProjPath]
	if !ok {
		t.Fatalf("the projection has no %s; it has %v", crlfProjPath, fileNames(doc))
	}
	// "\treturn n%2 == 0\r\n" is line 5: tab, `return`, space, `n`, `%`, `2`,
	// space — the `==` starts at column 13.
	mutant := findMutant(t, crlf, "0718293a")
	if mutant.Location.Start.Line != 5 || mutant.Location.Start.Column != 13 {
		t.Errorf("the CRLF mutant starts at %d:%d, want 5:13",
			mutant.Location.Start.Line, mutant.Location.Start.Column)
	}
	if !strings.Contains(crlf.Source, "\r\n") {
		t.Error("the projected source lost its carriage returns; the viewer highlights the text it is given")
	}
}

// TestProjectionMapsEveryOutcome states the whole status mapping in one place,
// including the two that need a reason to be worth reading.
func TestProjectionMapsEveryOutcome(t *testing.T) {
	t.Parallel()

	doc := decodeProjection(t, marshalProjection(t, projectionFixture(t), projectionWorkspace(t)))
	byID := map[string]report.ProjectionMutant{}
	for _, file := range doc.Files {
		for _, m := range file.Mutants {
			byID[m.ID] = m
		}
	}
	for id, want := range map[string]string{
		"a1b2c3d4": "Killed",
		"1a2b3c4d": "Killed",
		"b2c3d4e5": "Survived",
		"c3d4e5f6": "Timeout",
		"d4e5f607": "Ignored",      // not run: out of selection
		"e5f60718": "Ignored",      // inconclusive
		"f6071829": "RuntimeError", // the harness failed
		"0718293a": "Ignored",      // not run: interrupted
		"18293a4b": "Ignored",      // not run: another shard
		"293a4b5c": "CompileError", // the rejection
	} {
		m, ok := byID[id]
		if !ok {
			t.Errorf("mutant %s is missing from the projection", id)
			continue
		}
		if m.Status != want {
			t.Errorf("mutant %s has status %q, want %q", id, m.Status, want)
		}
		// Every status a reader cannot act on says why, and no other one does.
		hasReason := m.StatusReason != ""
		wantsReason := want == "Ignored" || want == "CompileError"
		if hasReason != wantsReason {
			t.Errorf("mutant %s (%s) has statusReason %q, want one: %v", id, want, m.StatusReason, wantsReason)
		}
	}
}

// TestProjectionRefusesDriftedSource is the check that turns a silently wrong
// document into a refusal: a file edited while the run was in flight.
func TestProjectionRefusesDriftedSource(t *testing.T) {
	t.Parallel()

	root := projectionWorkspace(t)
	edited := strings.Replace(alphaProjSource, "if a == b {", "if a != b {", 1)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(alphaProjPath)), []byte(edited), 0o600); err != nil {
		t.Fatalf("editing the fixture: %v", err)
	}
	_, err := report.Project(report.ProjectionOptions{
		Report:        projectionFixture(t),
		WorkspaceRoot: root,
		High:          80, Low: 60,
	})
	if got := report.CodeOf(err); got != report.CodeProjectionSourceDrift {
		t.Fatalf("Project over an edited file = %v (code %q), want %s", err, got, report.CodeProjectionSourceDrift)
	}
}

// TestProjectionRefusesMissingSource proves the empty string is never
// substituted for a file that is not there. A viewer given an empty source
// shows blank code with mutants pointing into nothing, which looks like a bug
// in the tests rather than a missing file.
func TestProjectionRefusesMissingSource(t *testing.T) {
	t.Parallel()

	root := projectionWorkspace(t)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(betaProjPath))); err != nil {
		t.Fatalf("removing the fixture: %v", err)
	}
	_, err := report.Project(report.ProjectionOptions{
		Report:        projectionFixture(t),
		WorkspaceRoot: root,
		High:          80, Low: 60,
	})
	if got := report.CodeOf(err); got != report.CodeProjectionSourceUnreadable {
		t.Fatalf("Project over a deleted file = %v (code %q), want %s", err, got, report.CodeProjectionSourceUnreadable)
	}
}

// TestProjectionRefusesAPathOutsideTheWorkspace covers the one way a document
// could be made to read a file nobody meant to publish. A report is a file, a
// file can be edited, and `../../.ssh/id_rsa` in a path is not something to
// find out about after the projection has embedded it.
func TestProjectionRefusesAPathOutsideTheWorkspace(t *testing.T) {
	t.Parallel()

	fixture := projectionFixture(t)
	fixture.Mutants[0].Path = "../outside.go"
	_, err := report.Project(report.ProjectionOptions{
		Report:        fixture,
		WorkspaceRoot: projectionWorkspace(t),
		High:          80, Low: 60,
	})
	if got := report.CodeOf(err); got != report.CodeProjectionSourceUnreadable {
		t.Fatalf("Project over an escaping path = %v (code %q), want %s", err, got, report.CodeProjectionSourceUnreadable)
	}
}

// TestValidateProjectionRefusesTheWrongSchemaVersion is the trap the vendored
// schema exists to catch, written down as a test so that nobody "fixes" the
// version to match the npm package it was vendored from.
func TestValidateProjectionRefusesTheWrongSchemaVersion(t *testing.T) {
	t.Parallel()

	doc := marshalProjection(t, projectionFixture(t), projectionWorkspace(t))
	broken := strings.Replace(string(doc), `"schemaVersion": "2"`, `"schemaVersion": "3"`, 1)
	if broken == string(doc) {
		t.Fatal("the fixture document does not carry schemaVersion 2, so this test proves nothing")
	}
	err := report.ValidateProjection([]byte(broken))
	if got := report.CodeOf(err); got != report.CodeProjectionInvalid {
		t.Fatalf("ValidateProjection over schemaVersion 3 = %v (code %q), want %s",
			err, got, report.CodeProjectionInvalid)
	}
	if !strings.Contains(err.Error(), "/schemaVersion") {
		t.Errorf("the diagnostic does not locate the failure: %v", err)
	}
}

// TestValidateProjectionRefusesAMissingLocation covers the other half: a
// document whose required fields are gone.
func TestValidateProjectionRefusesAMissingLocation(t *testing.T) {
	t.Parallel()

	err := report.ValidateProjection([]byte(`{
	  "schemaVersion": "2",
	  "thresholds": {"high": 80, "low": 60},
	  "files": {"a.go": {"language": "go", "source": "package a\n", "mutants": [{"id": "1", "mutatorName": "x", "status": "Killed"}]}}
	}`))
	if got := report.CodeOf(err); got != report.CodeProjectionInvalid {
		t.Fatalf("ValidateProjection over a mutant with no location = %v (code %q), want %s",
			err, got, report.CodeProjectionInvalid)
	}
}

// TestValidateProjectionRefusesNonJSON proves the validator says what is wrong
// rather than panicking on input that never reached the encoder.
func TestValidateProjectionRefusesNonJSON(t *testing.T) {
	t.Parallel()

	if got := report.CodeOf(report.ValidateProjection([]byte("not a document"))); got != report.CodeProjectionInvalid {
		t.Errorf("ValidateProjection over rubbish reported %q, want %s", got, report.CodeProjectionInvalid)
	}
}

// TestProjectRefusesNoReport is the caller's slip, diagnosed rather than
// dereferenced.
func TestProjectRefusesNoReport(t *testing.T) {
	t.Parallel()

	if _, err := report.Project(report.ProjectionOptions{}); report.CodeOf(err) != report.CodeNoReport {
		t.Errorf("Project(nil report) = %v, want %s", err, report.CodeNoReport)
	}
}

// marshalProjection projects and encodes a report, failing the test on either.
func marshalProjection(t *testing.T, r *report.Report, root string) []byte {
	t.Helper()
	projection, err := report.Project(report.ProjectionOptions{
		Report: r, WorkspaceRoot: root, High: 80, Low: 60,
	})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	data, err := projection.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return data
}

// decodeProjection reads an encoded projection back, which is what a consumer
// of the file does and therefore the only honest way to assert about it.
func decodeProjection(t *testing.T, data []byte) report.Projection {
	t.Helper()
	var doc report.Projection
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decoding the projection: %v", err)
	}
	return doc
}

// findMutant returns the projected mutant with an id, or fails.
func findMutant(t *testing.T, file *report.ProjectionFile, id string) report.ProjectionMutant {
	t.Helper()
	for _, m := range file.Mutants {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no mutant %s in the projected file", id)
	return report.ProjectionMutant{}
}

// fileNames lists the projected files, for a failure message.
func fileNames(doc report.Projection) []string {
	names := make([]string, 0, len(doc.Files))
	for name := range doc.Files {
		names = append(names, name)
	}
	return names
}
