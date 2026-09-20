// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func nullPaths(t *testing.T, node any, path string, found *[]string) {
	t.Helper()
	switch typed := node.(type) {
	case nil:
		*found = append(*found, path)
	case map[string]any:
		for key, value := range typed {
			nullPaths(t, value, path+"."+key, found)
		}
	case []any:
		for _, value := range typed {
			nullPaths(t, value, path+"[]", found)
		}
	}
}

func TestAReportWritesNoNullWhereACollectionBelongs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input report.Report
	}{
		{name: "a report that has recorded nothing", input: report.Report{Schema: report.SchemaV1}},
		{name: "a persisted report", input: persistedFixture()},
		{name: "a report with a resume", input: auditedFixture()},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var document any
			if err := json.Unmarshal(report.JSON(test.input), &document); err != nil {
				t.Fatal(err)
			}
			var found []string
			nullPaths(t, document, "report", &found)
			if len(found) != 0 {
				t.Fatalf("the report wrote null at %q, want an empty collection everywhere", found)
			}
		})
	}
}

func TestAReportKeepsEveryCollectionItWasGivenAndOrdersIt(t *testing.T) {
	t.Parallel()
	input := auditedFixture()
	input.Repository.Packages = []string{"z", "a", "a"}
	input.Repository.Git.ChangedFiles = []string{"z.go", "a.go"}
	input.Execution.TestArgs = []string{"-second", "-first"}
	input.Execution.BuildTags = []string{"second", "first"}
	input.Execution.MutationOperators = []string{"second", "first"}
	input.Scope.Resolved.Modules = []string{"z", "a"}
	input.Scope.Resolved.Packages = []string{"./z", "./a"}
	input.Scope.Resolved.Files = []string{"z.go", "a.go"}
	input.Acceptances = append(input.Acceptances, report.Acceptance{
		ID: "aaa-fixture", Reason: "reviewed", Expires: "2026-12-01T00:00:00Z",
	})
	input.Repairs = append(input.Repairs, report.Repair{ID: "r0", Finding: "f2", Path: "a_test.go", Status: "applied"})

	var decoded report.Report
	if err := json.Unmarshal(report.JSON(input), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		count func(report.Report) int
	}{
		{name: "evidence", count: func(r report.Report) int { return len(r.Evidence) }},
		{name: "findings", count: func(r report.Report) int { return len(r.Findings) }},
		{name: "repairs", count: func(r report.Report) int { return len(r.Repairs) }},
		{name: "acceptances", count: func(r report.Report) int { return len(r.Acceptances) }},
		{name: "limitations", count: func(r report.Report) int { return len(r.Limitations) }},
		{name: "targets", count: func(r report.Report) int { return len(r.Targets) }},
		{name: "test arguments", count: func(r report.Report) int { return len(r.Execution.TestArgs) }},
		{name: "build tags", count: func(r report.Report) int { return len(r.Execution.BuildTags) }},
		{
			name:  "mutation operators",
			count: func(r report.Report) int { return len(r.Execution.MutationOperators) },
		},
		{name: "changed files", count: func(r report.Report) int { return len(r.Repository.Git.ChangedFiles) }},
		{name: "resolved modules", count: func(r report.Report) int { return len(r.Scope.Resolved.Modules) }},
		{name: "resolved packages", count: func(r report.Report) int { return len(r.Scope.Resolved.Packages) }},
		{name: "resolved files", count: func(r report.Report) int { return len(r.Scope.Resolved.Files) }},
	} {
		if got, want := test.count(decoded), test.count(input); got != want {
			t.Errorf("the report read back %d %s, want %d", got, test.name, want)
		}
	}
	if decoded.Resume == nil || decoded.Resume.Attempts != input.Resume.Attempts {
		t.Errorf("the report read back resume metadata %+v, want %+v", decoded.Resume, input.Resume)
	}
	if got := decoded.Repository.Packages; len(got) != 2 || got[0] != "a" || got[1] != "z" {
		t.Errorf("repository packages read back as %q, want them sorted and deduplicated", got)
	}
	if got := decoded.Execution.TestArgs; got[0] != "-second" {
		t.Errorf("test arguments read back as %q, want the order the run used", got)
	}
	if got := decoded.Acceptances; got[0].ID != "aaa-fixture" {
		t.Errorf("acceptances read back starting at %q, want them in identity order", got[0].ID)
	}
	if got := decoded.Repairs; got[0].ID != "r0" {
		t.Errorf("repairs read back starting at %q, want them in identity order", got[0].ID)
	}
	if got := decoded.Findings; got[0].ID != "f1" {
		t.Errorf("findings read back starting at %q, want them in identity order", got[0].ID)
	}
}

func TestLimitationsAreOrderedByCodeAndThenBySummary(t *testing.T) {
	t.Parallel()
	input := auditedFixture()
	input.Limitations = []report.Limitation{
		{Code: report.LimitationWholeTreeBehaviourKeys, Summary: "a"},
		{Code: report.LimitationAssuranceIncomplete, Summary: "z"},
		{Code: report.LimitationWholeTreeBehaviourKeys, Summary: "b"},
		{Code: report.LimitationAssuranceIncomplete, Summary: "y"},
	}
	var decoded report.Report
	if err := json.Unmarshal(report.JSON(input), &decoded); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(decoded.Limitations))
	for _, limitation := range decoded.Limitations {
		got = append(got, limitation.Code+"/"+limitation.Summary)
	}
	want := []string{
		report.LimitationAssuranceIncomplete + "/y",
		report.LimitationAssuranceIncomplete + "/z",
		report.LimitationWholeTreeBehaviourKeys + "/a",
		report.LimitationWholeTreeBehaviourKeys + "/b",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("limitations read back as %q, want %q", got, want)
	}
}

func TestTheLimitationCodesAreACopyNobodyElseCanChange(t *testing.T) {
	t.Parallel()
	codes := report.LimitationCodes()
	if len(codes) == 0 {
		t.Fatal("LimitationCodes named none at all")
	}
	first := codes[0]
	codes[0] = "rewritten-by-a-caller"
	if again := report.LimitationCodes(); again[0] != first {
		t.Fatalf("a caller rewrote the code list: it now starts at %q, want %q", again[0], first)
	}
}

func TestMutantAccountingRequiresEveryColumnAndAdmitsNoOther(t *testing.T) {
	t.Parallel()
	complete := `{"discovered":1,"selected":1,"executed":1,"killed":1,"survived":0,` +
		`"inconclusive":0,"compile_rejected":0,"accepted":0,"out_of_scope":0,"unknown":0,` +
		`"reused_killed":0,"reused_survived":0}`
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "every column", input: complete},
		{
			name:  "a column that is absent",
			input: strings.Replace(complete, `"killed":1,`, "", 1), want: "missing killed",
		},
		{
			name:  "a column written as null",
			input: strings.Replace(complete, `"killed":1`, `"killed":null`, 1), want: "missing killed",
		},
		{
			name:  "a column nothing declares",
			input: strings.Replace(complete, `{`, `{"invented":1,`, 1), want: "unknown field invented",
		},
		{name: "a document that is not an object", input: `1`, want: "cannot unmarshal"},
		{
			name:  "a column of the wrong type",
			input: strings.Replace(complete, `"killed":1`, `"killed":"one"`, 1), want: "cannot unmarshal",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var accounting report.MutantAccounting
			err := json.Unmarshal([]byte(test.input), &accounting)
			switch {
			case test.want == "" && err != nil:
				t.Fatalf("decoding %s reported %v, want none", test.name, err)
			case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
				t.Fatalf("decoding %s reported %v, want it to say %q", test.name, err, test.want)
			}
		})
	}
}

func TestWithoutToolPrefixStripsEveryNameTheToolPutInFront(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ input, want string }{
		{input: "goatest: broken", want: "broken"},
		{input: "goatest: goatest: broken", want: "broken"},
		{input: "broken", want: "broken"},
		{input: "a goatest: broken", want: "a goatest: broken"},
		{input: "", want: ""},
	} {
		t.Run("message "+test.input, func(t *testing.T) {
			t.Parallel()
			if got := report.WithoutToolPrefix(test.input); got != test.want {
				t.Fatalf("WithoutToolPrefix(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestAReportKeepsTheResumeItWasGivenAndDoesNotInventOne(t *testing.T) {
	t.Parallel()
	without := auditedFixture()
	without.Resume = nil
	var decoded report.Report
	if err := json.Unmarshal(report.JSON(without), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Resume != nil {
		t.Fatalf("a report with no resume metadata read back %+v, want none", decoded.Resume)
	}

	with := auditedFixture()
	if err := json.Unmarshal(report.JSON(with), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Resume == nil || decoded.Resume.Attempts != with.Resume.Attempts {
		t.Fatalf("a report with resume metadata read back %+v, want %+v", decoded.Resume, with.Resume)
	}
}

func TestMutantDispositionsAreOrderedByIdentity(t *testing.T) {
	t.Parallel()
	input := mutantFixture()
	input.Mutants = []report.MutantDisposition{
		{ID: "m9", Status: report.MutantKilled},
		{ID: "m1", Status: report.MutantSurvived},
		{ID: "m5", Status: report.MutantInconclusive},
	}
	input.Accounting.Mutants = report.MutantAccounting{
		Discovered: 3, Selected: 3, Executed: 3, Killed: 1, Survived: 1, Inconclusive: 1,
	}
	var decoded report.Report
	if err := json.Unmarshal(report.JSON(input), &decoded); err != nil {
		t.Fatal(err)
	}
	got := []string{decoded.Mutants[0].ID, decoded.Mutants[1].ID, decoded.Mutants[2].ID}
	if got[0] != "m1" || got[1] != "m5" || got[2] != "m9" {
		t.Fatalf("mutants read back as %q, want them in identity order", got)
	}
}
