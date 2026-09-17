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
	"github.com/P4suta/go-mutants/internal/testkit"
)

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

const crlfProjSource = "package crlf\r\n" +
	"\r\n" +
	"// Even reports whether n is even.\r\n" +
	"func Even(n int) bool {\r\n" +
	"\treturn n%2 == 0\r\n" +
	"}\r\n"

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
			ID:         strings.Repeat("29", 32),
			DisplayID:  "293a4b5c",
			Path:       betaProjPath,
			Line:       10,
			Column:     14,
			Rule:       "return-zero-numeric",
			Diagnostic: "internal/beta/beta.go:10:14: cannot use 0 (untyped int constant) as bool value\n\tin return statement",
		}},
	}
}

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
	testkit.Golden(t, "mutation-report.golden.json", got)

	if err = report.ValidateProjection(got); err != nil {
		t.Fatalf("the golden projection does not validate: %v", err)
	}
}

func TestProjectionIsDeterministic(t *testing.T) {
	t.Parallel()

	root := projectionWorkspace(t)
	first := marshalProjection(t, projectionFixture(t), root)
	second := marshalProjection(t, projectionFixture(t), root)
	if string(first) != string(second) {
		t.Error("two projections of one run produced different bytes")
	}
}

func TestProjectionCoordinatesAreUTF16(t *testing.T) {
	t.Parallel()

	doc := decodeProjection(t, marshalProjection(t, projectionFixture(t), projectionWorkspace(t)))
	beta, ok := doc.Files[betaProjPath]
	if !ok {
		t.Fatalf("the projection has no %s; it has %v", betaProjPath, fileNames(doc))
	}
	mutant := findMutant(t, beta, "e5f60718")
	if mutant.Location.Start.Line != 5 || mutant.Location.Start.Column != 15 {
		t.Errorf("the mutant after ¥🎉 starts at %d:%d, want 5:15 (bytes would say 5:18, runes 5:14)",
			mutant.Location.Start.Line, mutant.Location.Start.Column)
	}
	if mutant.Location.End.Line != 5 || mutant.Location.End.Column != 17 {
		t.Errorf("it ends at %d:%d, want 5:17 — end is exclusive, and `!=` is two units wide",
			mutant.Location.End.Line, mutant.Location.End.Column)
	}
}

func TestProjectionOnCRLF(t *testing.T) {
	t.Parallel()

	doc := decodeProjection(t, marshalProjection(t, projectionFixture(t), projectionWorkspace(t)))
	crlf, ok := doc.Files[crlfProjPath]
	if !ok {
		t.Fatalf("the projection has no %s; it has %v", crlfProjPath, fileNames(doc))
	}
	mutant := findMutant(t, crlf, "0718293a")
	if mutant.Location.Start.Line != 5 || mutant.Location.Start.Column != 13 {
		t.Errorf("the CRLF mutant starts at %d:%d, want 5:13",
			mutant.Location.Start.Line, mutant.Location.Start.Column)
	}
	if !strings.Contains(crlf.Source, "\r\n") {
		t.Error("the projected source lost its carriage returns; the viewer highlights the text it is given")
	}
}

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
		"d4e5f607": "Ignored",
		"e5f60718": "Ignored",
		"f6071829": "RuntimeError",
		"0718293a": "Ignored",
		"18293a4b": "Ignored",
		"293a4b5c": "CompileError",
	} {
		m, ok := byID[id]
		if !ok {
			t.Errorf("mutant %s is missing from the projection", id)
			continue
		}
		if m.Status != want {
			t.Errorf("mutant %s has status %q, want %q", id, m.Status, want)
		}
		hasReason := m.StatusReason != ""
		wantsReason := want == "Ignored" || want == "CompileError"
		if hasReason != wantsReason {
			t.Errorf("mutant %s (%s) has statusReason %q, want one: %v", id, want, m.StatusReason, wantsReason)
		}
	}
}

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

func TestValidateProjectionRefusesNonJSON(t *testing.T) {
	t.Parallel()

	if got := report.CodeOf(report.ValidateProjection([]byte("not a document"))); got != report.CodeProjectionInvalid {
		t.Errorf("ValidateProjection over rubbish reported %q, want %s", got, report.CodeProjectionInvalid)
	}
}

func TestProjectRefusesNoReport(t *testing.T) {
	t.Parallel()

	if _, err := report.Project(report.ProjectionOptions{}); report.CodeOf(err) != report.CodeNoReport {
		t.Errorf("Project(nil report) = %v, want %s", err, report.CodeNoReport)
	}
}

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

func decodeProjection(t *testing.T, data []byte) report.Projection {
	t.Helper()
	var doc report.Projection
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decoding the projection: %v", err)
	}
	return doc
}

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

func fileNames(doc report.Projection) []string {
	names := make([]string, 0, len(doc.Files))
	for name := range doc.Files {
		names = append(names, name)
	}
	return names
}
