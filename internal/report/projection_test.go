// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

const orderProjPath = "internal/order/order.go"

const orderProjSource = "package order\n" +
	"\n" +
	"var a = 1 + 2\n"

type orderRow struct {
	displayID string
	family    string
	rule      string
	start     uint32
	end       uint32
	original  string
}

func TestProjectedMutantsAreInATotalOrder(t *testing.T) {
	t.Parallel()

	rows := []orderRow{
		{displayID: "00000005", family: "zzz", rule: "zzz", start: 25, end: 26, original: "+"},
		{displayID: "00000004", family: "aaa", rule: "aaa", start: 25, end: 26, original: "+"},
		{displayID: "00000003", family: "aaa", rule: "aaa", start: 25, end: 26, original: "+"},
		{displayID: "00000001", family: "aaa", rule: "aaa", start: 23, end: 28, original: "1 + 2"},
		{displayID: "00000002", family: "zzz", rule: "zzz", start: 23, end: 24, original: "1"},
	}
	want := []string{"00000002", "00000001", "00000003", "00000004", "00000005"}

	mutants := make([]report.Mutant, 0, len(rows))
	for _, row := range rows {
		mutants = append(mutants, report.Mutant{
			ID:        strings.Repeat(row.displayID, 8),
			DisplayID: row.displayID,
			Path:      orderProjPath,
			Family:    row.family,
			Rule:      row.rule,
			StartByte: row.start,
			EndByte:   row.end,
			Original:  row.original,
			Outcome:   report.OutcomeKilled,
		})
	}

	root := orderWorkspace(t)
	projection, err := report.Project(report.ProjectionOptions{
		Report: &report.Report{
			DocumentType: report.DocumentType, SchemaVersion: report.SchemaVersion, Mutants: mutants,
		},
		WorkspaceRoot: root, High: 80, Low: 60,
	})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	file, ok := projection.Files[orderProjPath]
	if !ok {
		t.Fatalf("the projection has no %s", orderProjPath)
	}
	got := make([]string, 0, len(file.Mutants))
	for _, m := range file.Mutants {
		got = append(got, m.ID)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the mutants came back as\n %v\nwant\n %v", got, want)
	}
}

func TestProjectionRefusesASpanTheFileDoesNotHold(t *testing.T) {
	t.Parallel()

	root := orderWorkspace(t)
	for name, tc := range map[string]struct {
		start, end uint32
		original   string
		refused    bool
	}{
		"the text at the span changed":   {start: 23, end: 24, original: "9", refused: true},
		"the span reaches past the file": {start: 23, end: uint32(len(orderProjSource)) + 8, original: "1", refused: true},
		"the span is reversed":           {start: 26, end: 25, original: "+", refused: true},
		"the span ends at the last byte": {start: uint32(len(orderProjSource)) - 1, end: uint32(len(orderProjSource)), original: "\n"},
		"the span is empty":              {start: 23, end: 23},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := report.Project(report.ProjectionOptions{
				Report: &report.Report{
					DocumentType: report.DocumentType, SchemaVersion: report.SchemaVersion,
					Mutants: []report.Mutant{{
						ID: strings.Repeat("ab", 32), DisplayID: "abababab", Path: orderProjPath,
						Family: "comparison", Rule: "eq-to-neq",
						StartByte: tc.start, EndByte: tc.end, Original: tc.original,
						Outcome: report.OutcomeKilled,
					}},
				},
				WorkspaceRoot: root, High: 80, Low: 60,
			})
			if !tc.refused {
				if err != nil {
					t.Fatalf("Project over a span the file does hold = %v, want no error", err)
				}
				return
			}
			if got := report.CodeOf(err); got != report.CodeProjectionSourceDrift {
				t.Fatalf("Project = %v (code %q), want %s", err, got, report.CodeProjectionSourceDrift)
			}
			if !strings.Contains(err.Error(), "no longer holds the text mutant abababab was built from") {
				t.Errorf("the refusal does not name the mutant and the file: %v", err)
			}
		})
	}
}

func TestProjectionSaysWhichSourceItCouldNotRead(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		stage func(t *testing.T, root string)
		want  string
	}{
		"a file that is gone": {
			stage: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(orderProjPath))); err != nil {
					t.Fatalf("removing the fixture: %v", err)
				}
			},
			want: "is not there any more",
		},
		"a directory where the file was": {
			stage: func(t *testing.T, root string) {
				t.Helper()
				full := filepath.Join(root, filepath.FromSlash(orderProjPath))
				if err := os.Remove(full); err != nil {
					t.Fatalf("removing the fixture: %v", err)
				}
				if err := os.Mkdir(full, 0o755); err != nil {
					t.Fatalf("staging the failure: %v", err)
				}
			},
			want: "could not be read",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := orderWorkspace(t)
			tc.stage(t, root)

			_, err := report.Project(report.ProjectionOptions{
				Report:        orderReport(),
				WorkspaceRoot: root, High: 80, Low: 60,
			})
			if got := report.CodeOf(err); got != report.CodeProjectionSourceUnreadable {
				t.Fatalf("Project = %v (code %q), want %s", err, got, report.CodeProjectionSourceUnreadable)
			}
			if !strings.Contains(err.Error(), "the mutation report needs the source of "+orderProjPath) {
				t.Errorf("the refusal does not name the file the report needs: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say the source %s: %v", tc.want, err)
			}
		})
	}
}

func TestProjectionRefusesEveryPathThatLeavesTheWorkspace(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		root string
		path string
	}{
		"a path that climbs out":  {path: "../secret.go"},
		"the parent itself":       {path: ".."},
		"an absolute path":        {root: "", path: "/outside/secret.go"},
		"a path that climbs deep": {path: "../../elsewhere/secret.go"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := tc.root
			if name != "an absolute path" {
				root = orderWorkspace(t)
			}
			r := orderReport()
			r.Mutants[0].Path = tc.path

			_, err := report.Project(report.ProjectionOptions{
				Report: r, WorkspaceRoot: root, High: 80, Low: 60,
			})
			if got := report.CodeOf(err); got != report.CodeProjectionSourceUnreadable {
				t.Fatalf("Project = %v (code %q), want %s", err, got, report.CodeProjectionSourceUnreadable)
			}
			if !strings.Contains(err.Error(), "the report names a source file outside the workspace") {
				t.Errorf("the refusal is not the one about leaving the workspace: %v", err)
			}
		})
	}
}

func TestProjectionRefusesARejectionWhoseSourceIsGone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	_, err := report.Project(report.ProjectionOptions{
		Report: &report.Report{
			DocumentType: report.DocumentType, SchemaVersion: report.SchemaVersion,
			Rejected: []report.Rejected{{
				ID: strings.Repeat("cd", 32), DisplayID: "cdcdcdcd", Path: orderProjPath,
				Line: 3, Column: 9, Rule: "eq-to-neq", Diagnostic: "cannot use 0 as bool",
			}},
		},
		WorkspaceRoot: root, High: 80, Low: 60,
	})
	if got := report.CodeOf(err); got != report.CodeProjectionSourceUnreadable {
		t.Fatalf("Project = %v (code %q), want %s", err, got, report.CodeProjectionSourceUnreadable)
	}
	if !strings.Contains(err.Error(), "the mutation report needs the source of "+orderProjPath) {
		t.Errorf("the refusal does not name the file: %v", err)
	}
}

func TestProjectedStatusReasonExplainsWhatAStatusCannotSay(t *testing.T) {
	t.Parallel()

	interrupted := report.NotRunInterrupted.String()
	invented := "chased-away"
	for name, tc := range map[string]struct {
		outcome    report.Outcome
		notRun     *string
		wantStatus string
		wantReason string
	}{
		"a reason the format knows": {
			outcome: report.OutcomeNotRun, notRun: &interrupted, wantStatus: "Ignored",
			wantReason: "the run was interrupted before this mutant was measured",
		},
		"no reason at all": {
			outcome: report.OutcomeNotRun, wantStatus: "Ignored",
			wantReason: "this mutant was not executed",
		},
		"a reason this build does not know": {
			outcome: report.OutcomeNotRun, notRun: &invented, wantStatus: "Ignored",
			wantReason: "this mutant was not executed: chased-away",
		},
		"an outcome this projection does not know": {
			outcome: report.Outcome("chased-away"), wantStatus: "Ignored",
			wantReason: `go-mutants recorded an outcome this projection does not know: "chased-away"`,
		},
		"an outcome it does know": {
			outcome: report.OutcomeSurvived, wantStatus: "Survived",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := orderReport()
			r.Mutants[0].Outcome = tc.outcome
			r.Mutants[0].NotRunReason = tc.notRun

			projection, err := report.Project(report.ProjectionOptions{
				Report: r, WorkspaceRoot: orderWorkspace(t), High: 80, Low: 60,
			})
			if err != nil {
				t.Fatalf("Project: %v", err)
			}
			m := projection.Files[orderProjPath].Mutants[0]
			if m.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", m.Status, tc.wantStatus)
			}
			if m.StatusReason != tc.wantReason {
				t.Errorf("statusReason =\n %q\nwant\n %q", m.StatusReason, tc.wantReason)
			}
		})
	}
}

func TestAProjectedRejectionCarriesOneLineOfTheCompiler(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		diagnostic string
		want       string
	}{
		"one line":                 {diagnostic: "  order.go:3:9: undefined: q  ", want: "order.go:3:9: undefined: q"},
		"several lines":            {diagnostic: "order.go:3:9: undefined: q\n\tin var declaration", want: "order.go:3:9: undefined: q"},
		"carriage returns":         {diagnostic: "order.go:3:9: undefined: q\r\n\tin var declaration", want: "order.go:3:9: undefined: q"},
		"a leading blank line":     {diagnostic: "\norder.go:3:9: undefined: q", want: ""},
		"nothing but a blank line": {diagnostic: "\n", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			projection, err := report.Project(report.ProjectionOptions{
				Report: &report.Report{
					DocumentType: report.DocumentType, SchemaVersion: report.SchemaVersion,
					Rejected: []report.Rejected{{
						ID: strings.Repeat("cd", 32), DisplayID: "cdcdcdcd", Path: orderProjPath,
						Line: 3, Column: 9, Rule: "eq-to-neq", Diagnostic: tc.diagnostic,
					}},
				},
				WorkspaceRoot: orderWorkspace(t), High: 80, Low: 60,
			})
			if err != nil {
				t.Fatalf("Project: %v", err)
			}
			m := projection.Files[orderProjPath].Mutants[0]
			if m.Status != report.StatusCompileError {
				t.Errorf("status = %q, want %q", m.Status, report.StatusCompileError)
			}
			if m.StatusReason != tc.want {
				t.Errorf("statusReason = %q, want %q", m.StatusReason, tc.want)
			}
		})
	}
}

func TestValidateProjectionNamesWhereTheDocumentFailed(t *testing.T) {
	t.Parallel()

	const good = `{"schemaVersion":"2","thresholds":{"high":80,"low":60},"files":{}}`
	for name, tc := range map[string]struct {
		document string
		want     string
	}{
		"a document with nothing in it": {
			document: `{}`,
			want:     "schema at the document root",
		},
		"a document that is not an object": {
			document: `[]`,
			want:     "schema at the document root",
		},
		"the wrong report version": {
			document: `{"schemaVersion":"3","thresholds":{"high":80,"low":60},"files":{}}`,
			want:     "schema at /schemaVersion",
		},
		"a mutant with no location": {
			document: `{"schemaVersion":"2","thresholds":{"high":80,"low":60},"files":{"a.go":` +
				`{"language":"go","source":"x","mutants":[{"id":"1","mutatorName":"m","status":"Killed"}]}}}`,
			want: "schema at /files/a.go/mutants/0",
		},
		"a column the format counts from one": {
			document: `{"schemaVersion":"2","thresholds":{"high":80,"low":60},"files":{"a.go":` +
				`{"language":"go","source":"x","mutants":[{"id":"1","mutatorName":"m","status":"Killed",` +
				`"location":{"start":{"line":1,"column":0},"end":{"line":1,"column":2}}}]}}}`,
			want: "schema at /files/a.go/mutants/0/location/start/column",
		},
		"a file name with a slash in it": {
			document: `{"schemaVersion":"2","thresholds":{"high":80,"low":60},"files":{"a/b.go":` +
				`{"language":"go","source":"x","mutants":[{"id":"1","mutatorName":"m","status":"Killed",` +
				`"location":{"start":{"line":0,"column":1},"end":{"line":1,"column":2}}}]}}}`,
			want: "schema at /files/a~1b.go/mutants/0/location/start/line",
		},
		"two files that are both wrong": {
			document: `{"schemaVersion":"2","thresholds":{"high":80,"low":60},"files":{` +
				`"z.go":{"language":"go","source":"x","mutants":null},` +
				`"a.go":{"language":"go","source":"x","mutants":null}}}`,
			want: "schema at /files/a.go/mutants",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := report.ValidateProjection([]byte(tc.document))
			if got := report.CodeOf(err); got != report.CodeProjectionInvalid {
				t.Fatalf("ValidateProjection = %v (code %q), want %s", err, got, report.CodeProjectionInvalid)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not locate the failure at %q:\n%v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "nothing was written, because a document that does not validate is worse than no document") {
				t.Errorf("the refusal does not say what it did about it: %v", err)
			}
		})
	}
	if err := report.ValidateProjection([]byte(good)); err != nil {
		t.Errorf("ValidateProjection of a document the schema accepts = %v, want nil", err)
	}
}

func TestValidateProjectionTellsBadJSONFromABadDocument(t *testing.T) {
	t.Parallel()

	err := report.ValidateProjection([]byte(`{"schemaVersion":`))
	if got := report.CodeOf(err); got != report.CodeProjectionInvalid {
		t.Fatalf("ValidateProjection of truncated bytes = %v (code %q), want %s", err, got, report.CodeProjectionInvalid)
	}
	if !strings.Contains(err.Error(), "the mutation-testing-report projection is not JSON") {
		t.Errorf("the refusal does not say the bytes are not JSON: %v", err)
	}
	if strings.Contains(err.Error(), "does not satisfy the vendored") {
		t.Errorf("bytes that are not JSON were reported as a schema failure: %v", err)
	}
}

func TestValidateProjectionSaysSoWhenTheVendoredSchemaIsUnusable(t *testing.T) {
	const good = `{"schemaVersion":"2","thresholds":{"high":80,"low":60},"files":{}}`

	for name, tc := range map[string]struct {
		source func() []byte
		want   string
	}{
		"a schema that is not JSON": {
			source: func() []byte { return []byte("{not json") },
			want:   "schema is not JSON, so no projection can be checked against it",
		},
		"a schema that does not compile": {
			source: func() []byte { return []byte(`{"$schema":"http://json-schema.org/draft-07/schema#","type":7}`) },
			want:   "schema does not compile, so no projection can be checked against it",
		},
	} {
		t.Run(name, func(t *testing.T) {
			restore := report.UseStrykerSchema(tc.source)
			defer restore()

			err := report.ValidateProjection([]byte(good))
			if got := report.CodeOf(err); got != report.CodeProjectionSchemaUnusable {
				t.Fatalf("ValidateProjection under a broken schema = %v (code %q), want %s",
					err, got, report.CodeProjectionSchemaUnusable)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say what is wrong with the vendored schema: %v", err)
			}
			if second := report.ValidateProjection([]byte(good)); report.CodeOf(second) != report.CodeProjectionSchemaUnusable {
				t.Errorf("the second call under a broken schema = %v, want %s", second, report.CodeProjectionSchemaUnusable)
			}
		})
	}
}

func orderWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, filepath.FromSlash(orderProjPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(orderProjSource), 0o600); err != nil {
		t.Fatalf("writing %s: %v", full, err)
	}
	return root
}

func orderReport() *report.Report {
	return &report.Report{
		DocumentType: report.DocumentType, SchemaVersion: report.SchemaVersion,
		Mutants: []report.Mutant{{
			ID: strings.Repeat("ab", 32), DisplayID: "abababab", Path: orderProjPath,
			Family: "integer-arithmetic", Rule: "add-to-sub",
			StartByte: 25, EndByte: 26, Original: "+", Replacement: "-",
			Outcome: report.OutcomeKilled,
		}},
	}
}

func TestPointerOfRendersAnRFC6901Pointer(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		tokens []string
		want   string
	}{
		"no tokens at all":      {tokens: nil, want: "the document root"},
		"an empty token list":   {tokens: []string{}, want: "the document root"},
		"one token":             {tokens: []string{"files"}, want: "/files"},
		"several":               {tokens: []string{"files", "a.go", "mutants", "0"}, want: "/files/a.go/mutants/0"},
		"a slash in a token":    {tokens: []string{"files", "a/b.go"}, want: "/files/a~1b.go"},
		"a tilde in a token":    {tokens: []string{"files", "a~b.go"}, want: "/files/a~0b.go"},
		"both, in RFC order":    {tokens: []string{"files", "a~1b.go"}, want: "/files/a~01b.go"},
		"an empty token itself": {tokens: []string{""}, want: "/"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := report.PointerOf(tc.tokens); got != tc.want {
				t.Errorf("PointerOf(%q) = %q, want %q", tc.tokens, got, tc.want)
			}
		})
	}
}

func TestFirstFailureOfSomethingThatIsNotAValidationError(t *testing.T) {
	t.Parallel()

	if got := report.FirstFailure(errors.New("something else entirely")); got != "an unlocatable position" {
		t.Errorf("FirstFailure of a foreign error = %q, want %q", got, "an unlocatable position")
	}
}
