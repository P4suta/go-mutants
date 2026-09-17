// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const (
	firstModule  = "example.com/ws/first"
	secondModule = "example.com/ws/second"
	sharedFile   = "app.go"

	firstSource  = "package first\n\nfunc Equal(a, b int) bool { return a == b }\n"
	secondSource = "package second\n\nfunc Equal(a, b int) bool { return a == b }\n"
)

func sourceOfModule(module string) string {
	if module == secondModule {
		return secondSource
	}
	return firstSource
}

func TestAWorkspaceReportIsOneModuleOfTheCatalogue(t *testing.T) {
	t.Parallel()

	rep, err := report.Build(workspaceOptions(t, firstModule))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rep.Workspace.ModulePath != firstModule {
		t.Errorf("workspace.module_path = %q, want %q", rep.Workspace.ModulePath, firstModule)
	}
	if len(rep.Mutants) != 1 {
		t.Fatalf("the report holds %d mutants, want the one of %s", len(rep.Mutants), firstModule)
	}
	if got := rep.Mutants[0].Line; got != 11 {
		t.Errorf("the mutant is at line %d, want the line %s's candidate reported", got, firstModule)
	}
	if rep.Summary.Total != 1 {
		t.Errorf("summary.total = %d, want the module's own total", rep.Summary.Total)
	}
}

func TestEveryModuleOfAWorkspaceGetsItsOwnMutants(t *testing.T) {
	t.Parallel()

	seen := make(map[string]string)
	for _, module := range []string{firstModule, secondModule} {
		rep, err := report.Build(workspaceOptions(t, module))
		if err != nil {
			t.Fatalf("Build for %s: %v", module, err)
		}
		for _, m := range rep.Mutants {
			if owner, taken := seen[m.ID]; taken {
				t.Errorf("mutant %s is reported by both %s and %s", m.DisplayID, owner, module)
			}
			seen[m.ID] = module
		}
	}
	if len(seen) != 2 {
		t.Errorf("the two documents hold %d mutants between them, want the catalogue's two", len(seen))
	}
}

func workspaceOptions(t *testing.T, module string) report.Options {
	t.Helper()

	located := []discover.Located{
		workspaceCandidate(t, firstModule, 11),
		workspaceCandidate(t, secondModule, 22),
	}
	builder := mutation.NewBuilder()
	for _, l := range located {
		if err := builder.Add(l.Candidate); err != nil {
			t.Fatalf("cataloguing %s: %v", l.Where(), err)
		}
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	results := make([]report.MutantResult, 0, catalog.Len())
	selected := 0
	for _, m := range catalog.Mutants() {
		if m.ModulePath == module {
			selected++
		}
		results = append(results, report.MutantResult{
			ID:       m.ID,
			Outcome:  mutation.OutcomeSurvived,
			Duration: time.Second,
			Attempts: 1,
			Executions: []report.Execution{{
				Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 1_000,
			}},
		})
	}
	started, err := time.Parse(time.RFC3339, fixtureStarted)
	if err != nil {
		t.Fatalf("parsing the fixture clock: %v", err)
	}
	return report.Options{
		ToolVersion:     fixtureToolVersion,
		RunID:           fixtureRunID,
		Status:          report.StatusCompleted,
		Started:         started,
		Finished:        started.Add(fixtureDuration),
		Config:          config.Defaults(),
		Mode:            report.ModeAll,
		Selected:        selected,
		Module:          module,
		ModulePath:      module,
		GoVersion:       fixtureGoVersion,
		WorkspaceDigest: fixtureDigest,
		Platform:        report.Platform{OS: "linux", Arch: "amd64"},
		Catalog:         catalog,
		Located:         located,
		Results:         results,
		TestCommand:     []string{"go", "test", "./..."},
		Baseline:        []time.Duration{time.Second},
		Timeout:         10 * time.Second,
		TimeoutSource:   report.TimeoutDerived,
		Memory:          1 << 30,
		MemorySource:    report.MemoryDerived,
		CacheMode:       report.CacheOff,
	}
}

func workspaceCandidate(t *testing.T, module string, line int) discover.Located {
	t.Helper()

	rule, ok := mutation.CanonicalRegistry().Lookup("eq-to-neq")
	if !ok {
		t.Fatal("the canonical registry has no eq-to-neq")
	}
	source := sourceOfModule(module)
	start := strings.Index(source, "==")
	if start < 0 {
		t.Fatalf("%q holds no comparison to mutate", source)
	}
	return discover.Located{
		Candidate: mutation.Candidate{
			ModulePath:   module,
			Path:         sharedFile,
			Rule:         rule,
			Span:         mutation.Span{StartByte: uint32(start), EndByte: uint32(start + 2)},
			Original:     "==",
			Replacement:  "!=",
			SourceDigest: mutation.DigestString(source),
		},
		Line:    line,
		Column:  strings.Index(source, "==") - strings.LastIndex(source[:strings.Index(source, "==")], "\n"),
		Package: module,
	}
}

func TestAWorkspaceReportAddsUpTheModulesItHolds(t *testing.T) {
	t.Parallel()

	doc, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	if doc.DocumentType != report.WorkspaceDocumentType {
		t.Errorf("document_type = %q, want %q", doc.DocumentType, report.WorkspaceDocumentType)
	}
	if len(doc.Modules) != 2 {
		t.Fatalf("the document holds %d modules, want the workspace's two", len(doc.Modules))
	}
	if doc.Summary.Total != 2 {
		t.Errorf("summary.total = %d, want the two mutants of the run", doc.Summary.Total)
	}
	if doc.Summary.Survived != 2 {
		t.Errorf("summary.survived = %d, want both", doc.Summary.Survived)
	}
	var counted int
	for _, module := range doc.Modules {
		if module.Report == nil {
			t.Fatalf("module %s carries no report", module.ModulePath)
		}
		if module.Report.Workspace.ModulePath != module.ModulePath {
			t.Errorf("module %s carries a report of %s",
				module.ModulePath, module.Report.Workspace.ModulePath)
		}
		counted += module.Report.Summary.Total
	}
	if counted != doc.Summary.Total {
		t.Errorf("the modules hold %d mutants and the summary says %d", counted, doc.Summary.Total)
	}
	blob, err := doc.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(strings.SplitN(string(blob), `"modules"`, 2)[0], `"module_path"`) {
		t.Error("the workspace block names a module path")
	}
}

func TestAnExpectationGoesToTheModuleWhoseMutantItNames(t *testing.T) {
	t.Parallel()

	opts := workspaceRunOptions(t)
	mutants := opts.Catalog.Mutants()
	opts.Config.Mutation.Expect = []config.Expectation{
		{ID: mutants[0].ID, Reason: "a row of " + mutants[0].ModulePath},
		{ID: mutants[1].ID, Reason: "a row of " + mutants[1].ModulePath},
		{ID: staleID, Reason: "a row of nobody"},
	}
	doc, err := report.BuildWorkspace(opts)
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	for _, module := range doc.Modules {
		if len(module.Report.Expectations) != 1 {
			t.Fatalf("%s holds %d expectation rows, want its own one",
				module.ModulePath, len(module.Report.Expectations))
		}
		row := module.Report.Expectations[0]
		if row.State != report.StateFulfilled {
			t.Errorf("%s reports its own row as %q, want fulfilled", module.ModulePath, row.State)
		}
	}
	if len(doc.Expectations) != 1 || doc.Expectations[0].ID != staleID {
		t.Fatalf("the workspace document holds %+v, want the one row naming nobody", doc.Expectations)
	}
	if doc.Expectations[0].State != report.StateStale {
		t.Errorf("the orphan row is %q, want stale", doc.Expectations[0].State)
	}
}

func workspaceRunOptions(t *testing.T) report.WorkspaceOptions {
	t.Helper()

	base := workspaceOptions(t, "")
	located := base.Located
	base.Located = nil
	base.Module = ""
	base.ModulePath = ""
	base.Selected = len(base.Results)
	modules := make([]report.WorkspaceModule, 0, 2)
	for i, module := range []string{firstModule, secondModule} {
		modules = append(modules, report.WorkspaceModule{
			Dir:      []string{"first", "second"}[i],
			Path:     module,
			Located:  []discover.Located{located[i]},
			Selected: 1,
		})
	}
	return report.WorkspaceOptions{Options: base, Modules: modules}
}

func TestAWorkspaceReportSatisfiesItsSchema(t *testing.T) {
	t.Parallel()

	doc, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	blob, err := doc.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := schemas.Validate(schemas.WorkspaceReportV1, blob); err != nil {
		t.Fatalf("the workspace report does not satisfy its schema: %v", err)
	}
	for _, module := range doc.Modules {
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.MustMarshal(t, module.Report)); err != nil {
			t.Errorf("the report of %s does not satisfy the run report schema: %v",
				module.ModulePath, err)
		}
	}
}

func TestBuildWorkspaceRefusesWhatItCannotReportOn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		break_ func(*report.WorkspaceOptions)
		says   string
	}{
		{
			name:   "no catalogue",
			break_: func(o *report.WorkspaceOptions) { o.Catalog = nil },
			says:   "catalogue",
		},
		{
			name:   "no module",
			break_: func(o *report.WorkspaceOptions) { o.Modules = nil },
			says:   "names no module",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := workspaceRunOptions(t)
			tc.break_(&opts)
			doc, err := report.BuildWorkspace(opts)
			if err == nil {
				t.Fatalf("BuildWorkspace accepted it and returned %+v", doc)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

func TestBuildWorkspaceCarriesUpEveryFailureUnderneathIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		break_ func(*report.WorkspaceOptions)
		says   string
	}{
		{
			name: "a duplicate result",
			break_: func(o *report.WorkspaceOptions) {
				o.Results = append(o.Results, o.Results[0])
			},
			says: "more than one result",
		},
		{
			name: "a module that reports more than it has",
			break_: func(o *report.WorkspaceOptions) {
				o.Modules[0].Selected = 99
			},
			says: "selected",
		},
		{
			name: "an outcome nothing can count",
			break_: func(o *report.WorkspaceOptions) {
				o.Results[0].Outcome = mutation.Outcome(200)
			},
			says: "outcome",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := workspaceRunOptions(t)
			tc.break_(&opts)
			doc, err := report.BuildWorkspace(opts)
			if err == nil {
				t.Fatalf("BuildWorkspace accepted it and returned %+v", doc)
			}
			if doc != nil {
				t.Errorf("a refusal also returned a document: %+v", doc)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the failure does not say %q: %v", tc.says, err)
			}
		})
	}
}

func TestAWorkspaceReportsReportsAreItsModulesInOrder(t *testing.T) {
	t.Parallel()

	doc, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	reports := doc.Reports()
	if len(reports) != len(doc.Modules) {
		t.Fatalf("Reports() gave %d of %d modules", len(reports), len(doc.Modules))
	}
	for i, rep := range reports {
		if rep != doc.Modules[i].Report {
			t.Errorf("Reports()[%d] is not the report of %s", i, doc.Modules[i].ModulePath)
		}
	}
}

func TestAWorkspaceTallyRefusesAModuleItCannotCount(t *testing.T) {
	t.Parallel()

	doc, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	if _, err := doc.Tally(); err != nil {
		t.Fatalf("Tally of a document that built: %v", err)
	}

	missing := *doc
	missing.Modules = append([]report.ModuleReport(nil), doc.Modules...)
	missing.Modules[0].Report = nil
	if _, err := missing.Tally(); err == nil {
		t.Error("Tally counted a module with no report")
	} else if !strings.Contains(err.Error(), missing.Modules[0].ModulePath) {
		t.Errorf("the refusal does not name the module: %v", err)
	}

	uncountable := *doc
	uncountable.Modules = append([]report.ModuleReport(nil), doc.Modules...)
	broken := *doc.Modules[0].Report
	broken.Mutants = append([]report.Mutant(nil), broken.Mutants...)
	broken.Mutants[0].Outcome = report.Outcome("not-an-outcome")
	uncountable.Modules[0].Report = &broken
	if _, err := uncountable.Tally(); err == nil {
		t.Error("Tally counted an outcome nothing can count")
	}
}

func TestAWorkspaceExpectationFailureIsTheLedgerOfTheWholeRun(t *testing.T) {
	t.Parallel()

	clean, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	if clean.ExpectationFailure() {
		t.Error("a run with no ledger at all reports an expectation failure")
	}

	orphaned := workspaceRunOptions(t)
	orphaned.Config.Mutation.Expect = []config.Expectation{
		{ID: staleID, Reason: "a row of nobody"},
	}
	stale, err := report.BuildWorkspace(orphaned)
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	if !stale.ExpectationFailure() {
		t.Error("a row naming no mutant of any module is not reported as a failure")
	}

	contradicted := workspaceRunOptions(t)
	killed := contradicted.Catalog.Mutants()[0]
	for i := range contradicted.Results {
		if contradicted.Results[i].ID == killed.ID {
			contradicted.Results[i].Outcome = mutation.OutcomeKilled
			contradicted.Results[i].KilledBy = "example.com/ws/first"
		}
	}
	contradicted.Config.Mutation.Expect = []config.Expectation{
		{ID: killed.ID, Reason: "a row the run contradicts"},
	}
	doc, err := report.BuildWorkspace(contradicted)
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	if len(doc.Expectations) != 0 {
		t.Errorf("the workspace document holds %+v; that row is a module's", doc.Expectations)
	}
	if !doc.ExpectationFailure() {
		t.Error("a module's contradicted row is not carried up to the run")
	}
}

func TestParsingAWorkspaceReportRefusesEverythingThatIsNotOne(t *testing.T) {
	t.Parallel()

	doc, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	good, err := doc.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	parsed, err := report.ParseWorkspace(good)
	if err != nil {
		t.Fatalf("ParseWorkspace of what Marshal wrote: %v", err)
	}
	if parsed.RunID != doc.RunID || len(parsed.Modules) != len(doc.Modules) {
		t.Errorf("the parsed document is %s with %d modules, want %s with %d",
			parsed.RunID, len(parsed.Modules), doc.RunID, len(doc.Modules))
	}

	for _, tc := range []struct {
		name string
		data string
		says string
	}{
		{
			name: "not JSON at all",
			data: "{",
			says: "not a go-mutants workspace report",
		},
		{
			name: "a field this build does not know",
			data: `{"document_type":"go-mutants/workspace-report","schema_version":1,"invented":true}`,
			says: "not a go-mutants workspace report",
		},
		{
			name: "two documents in one file",
			data: string(good) + string(good),
			says: "more than one document",
		},
		{
			name: "a run report",
			data: `{"document_type":"go-mutants/run-report","schema_version":1}`,
			says: `is "go-mutants/run-report"`,
		},
		{
			name: "a version this build does not read",
			data: `{"document_type":"go-mutants/workspace-report","schema_version":2}`,
			says: "workspace-report v2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := report.ParseWorkspace([]byte(tc.data))
			if err == nil {
				t.Fatalf("ParseWorkspace accepted it and returned %+v", got)
			}
			if got != nil {
				t.Errorf("a refusal also returned a document: %+v", got)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

func TestTheDocumentTypeIsReadFromTheDocument(t *testing.T) {
	t.Parallel()

	got, err := report.DocumentTypeOf([]byte(
		`{"document_type":"go-mutants/workspace-report","schema_version":1}`))
	if err != nil {
		t.Fatalf("DocumentTypeOf: %v", err)
	}
	if got != report.WorkspaceDocumentType {
		t.Errorf("DocumentTypeOf = %q, want %q", got, report.WorkspaceDocumentType)
	}

	for _, tc := range []struct {
		name string
		data string
		says string
	}{
		{name: "not JSON at all", data: "{", says: "not a JSON document"},
		{name: "no discriminator", data: `{"schema_version":1}`, says: "declares no `document_type`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := report.DocumentTypeOf([]byte(tc.data))
			if err == nil {
				t.Fatalf("DocumentTypeOf accepted it and returned %q", got)
			}
			if got != "" {
				t.Errorf("a refusal also returned %q", got)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

func TestAWorkspaceReportThatCannotBeEncodedSaysSo(t *testing.T) {
	t.Parallel()

	doc, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	unencodable := math.NaN()
	doc.Modules[0].Report.Summary.ScorePercent = &unencodable

	data, err := doc.Marshal()
	if err == nil {
		t.Fatalf("Marshal encoded a document holding a NaN: %s", data)
	}
	if data != nil {
		t.Errorf("a failed Marshal also returned %d bytes", len(data))
	}
	if !strings.Contains(err.Error(), "could not be encoded as JSON") {
		t.Errorf("the failure does not say what went wrong: %v", err)
	}
}
