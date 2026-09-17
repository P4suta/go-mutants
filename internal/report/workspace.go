// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
)

const (
	// WorkspaceDocumentType is what a workspace run publishes instead of a run
	// report, and the discriminator a consumer branches on.
	WorkspaceDocumentType = "go-mutants/workspace-report"
	// WorkspaceSchemaVersion is the version of that document this build writes.
	WorkspaceSchemaVersion = 1
)

// A WorkspaceReport is one run over a `go.work`, and every module's run report
// inside it.
//
// It exists because `workspace.module_path` is required of a run report and a
// workspace has no single answer for it: a workspace is measured as one run
// over one catalogue that spans its modules — so that a mutant is executed
// against every test that covers it, whichever module compiled that test — and
// reported one module at a time. See ADR 0012.
//
// The module documents are *embedded* rather than filed beside this one, and
// that is what makes one run one file: a history store names a run's document
// by its run id, and N documents sharing a run id would name one file. Each of
// them is a complete, schema-valid run report, so anything that reads one reads
// one of these, and `jq '.modules[0].report'` is a run report on its own.
type WorkspaceReport struct {
	DocumentType  string `json:"document_type"`
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	DurationMS    int64  `json:"duration_ms"`

	// Workspace is what every module's report says about the tree, said once:
	// the digest the run is keyed on, the platform, the snapshot, and the `go`
	// directive. What it does not carry is a module path, which is the whole
	// reason this document type exists.
	Workspace WorkspaceFacts `json:"workspace"`

	// Summary is the run's, added up across the modules, and the policy verdict
	// is the one that decided the exit code. A module's own summary is in its
	// own report, and the two answer different questions: "did this project
	// pass" and "which part of it did not".
	Summary Summary `json:"summary"`

	// Expectations are the ledger rows that name no mutant of any module, which
	// is the only place they can be reported: a row naming another module's
	// mutant is that module's row, and a row naming nothing is nobody's.
	Expectations []Expectation `json:"expectations"`

	// Modules are the workspace's modules in `use` order, each with its own
	// run report.
	Modules []ModuleReport `json:"modules"`
}

// WorkspaceFacts is what a workspace report says about the tree it measured.
type WorkspaceFacts struct {
	GoVersion       string         `json:"go_version"`
	WorkspaceDigest string         `json:"workspace_digest"`
	Platform        Platform       `json:"platform"`
	Snapshot        *SnapshotFacts `json:"snapshot,omitempty"`
}

// A ModuleReport is one module of a workspace and the run report of it.
type ModuleReport struct {
	// Dir is the module root relative to the workspace root, slash-separated,
	// and "." for a workspace that uses its own root.
	Dir string `json:"dir"`
	// ModulePath is the module's import path, which is also
	// `report.workspace.module_path`.
	ModulePath string `json:"module_path"`
	// Report is the module's own run report, complete and valid on its own.
	Report *Report `json:"report"`
}

// A WorkspaceModule is one module the caller wants reported, and what discovery
// found in it.
//
// The catalogue, the results and the rejections are the run's and are given
// once, in [WorkspaceOptions.Options]; what is per module is only what
// discovery reported about that module and how much of it the run set out to
// execute.
type WorkspaceModule struct {
	Dir      string
	Path     string
	Located  []discover.Located
	Skips    []discover.Skip
	Selected int
}

// WorkspaceOptions is everything [BuildWorkspace] needs.
type WorkspaceOptions struct {
	// Options is the run: the catalogue spanning every module, every result and
	// rejection, and the facts that describe the run rather than a module.
	// Module, ModulePath, Located, Skips and Selected are filled in per module
	// and anything set in them here is replaced.
	Options
	// Modules are the workspace's modules, in `use` order.
	Modules []WorkspaceModule
}

// BuildWorkspace builds one run report per module and the document that holds
// them.
//
// The expectation ledger is partitioned rather than given to every module. A
// row is evaluated against the mutants of the document it is in, so handing the
// whole ledger to each module would report every other module's rows as stale
// in this one and fulfilled in theirs — the same row, two answers, in one run.
// So a row goes to the module whose mutant it names, and a row that names no
// mutant of any module is stale in the one place it can be: here.
func BuildWorkspace(opts WorkspaceOptions) (*WorkspaceReport, error) {
	if opts.Catalog == nil {
		return nil, &Error{
			Code:    CodeNoCatalog,
			Message: "the workspace report has no catalogue: every run has one, even an empty one",
		}
	}
	if len(opts.Modules) == 0 {
		return nil, &Error{
			Code:    CodeNoCatalog,
			Message: "the workspace report names no module: a workspace has at least one",
		}
	}
	ledger, orphans := partitionLedger(opts.Catalog, opts.Config.Mutation.Expect)

	modules := make([]ModuleReport, 0, len(opts.Modules))
	var (
		expectations []Expectation
		mutants      []Mutant
	)
	for _, module := range opts.Modules {
		moduleOpts := opts.Options
		moduleOpts.Module = module.Path
		moduleOpts.ModulePath = module.Path
		moduleOpts.Located = module.Located
		moduleOpts.Skips = module.Skips
		moduleOpts.Selected = module.Selected
		moduleOpts.Config.Mutation.Expect = ledger[module.Path]

		rep, buildErr := Build(moduleOpts)
		if buildErr != nil {
			return nil, buildErr
		}
		modules = append(modules, ModuleReport{Dir: module.Dir, ModulePath: module.Path, Report: rep})
		expectations = append(expectations, rep.Expectations...)
		mutants = append(mutants, rep.Mutants...)
	}
	stale := Evaluate(orphans, nil)
	expectations = append(expectations, stale...)

	doc := &WorkspaceReport{
		DocumentType:  WorkspaceDocumentType,
		SchemaVersion: WorkspaceSchemaVersion,
		ToolVersion:   opts.ToolVersion,
		RunID:         opts.RunID,
		Status:        string(opts.Status),
		StartedAt:     FormatTimestamp(opts.Started),
		FinishedAt:    FormatTimestamp(opts.Finished),
		DurationMS:    milliseconds(opts.Finished.Sub(opts.Started)),
		Workspace: WorkspaceFacts{
			GoVersion:       or(opts.GoVersion, unknownValue),
			WorkspaceDigest: opts.WorkspaceDigest,
			Platform:        platformOf(opts.Platform),
			Snapshot:        snapshotOf(opts.Snapshot),
		},
		Expectations: stale,
		Modules:      modules,
	}
	return summarised(doc, opts.Config.Policy, opts.InfrastructureError, expectations, mutants)
}

// summarised fills in the run's own counts from the modules already in the
// document, and is the value either constructor returns.
//
// The counts come from [WorkspaceReport.Tally] rather than from a derivation of
// their own, so that the summary this document carries and the total a reader
// of it computes are the same number by construction: a second derivation could
// disagree with the documents it is summarising, and then two files of one run
// would be about two runs.
//
// It returns the document rather than filling it in, so that a constructor ends
// with `return summarised(...)` and no branch of its own. There is one failure
// here and it is unreachable from either of them -- every module's report has
// been built or merged by the time this runs, and a report that was is one
// whose every outcome a tally counted -- so being unreachable in one place
// rather than three is the difference between one declared row and three.
func summarised(
	doc *WorkspaceReport,
	policy mutation.Policy,
	infrastructure bool,
	expectations []Expectation,
	mutants []Mutant,
) (*WorkspaceReport, error) {
	total, err := doc.Tally()
	if err != nil {
		return nil, err
	}
	doc.Summary = summaryOf(total, policy, infrastructure, expectations, mutants)
	return doc, nil
}

// partitionLedger sends every expectation row to the module whose mutant it
// names, and collects the rows that name no catalogued mutant at all.
func partitionLedger(
	catalog *mutation.Catalog,
	ledger []config.Expectation,
) (map[string][]config.Expectation, []config.Expectation) {
	byModule := make(map[string][]config.Expectation)
	var orphans []config.Expectation
	for _, row := range ledger {
		m, known := catalog.ByID(row.ID)
		if !known {
			orphans = append(orphans, row)
			continue
		}
		byModule[m.ModulePath] = append(byModule[m.ModulePath], row)
	}
	return byModule, orphans
}

// Marshal encodes the workspace report as the bytes that go on disk.
func (w *WorkspaceReport) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return nil, &Error{
			Code:    CodeEncodeFailed,
			Message: "the workspace report could not be encoded as JSON",
			Err:     err,
		}
	}
	return append(data, '\n'), nil
}

// Reports is every run report this document holds, in module order.
//
// It exists so that a consumer of a workspace run can do to each module what it
// would have done to a single-module run, without knowing how a module is
// spelled here.
func (w *WorkspaceReport) Reports() []*Report {
	reports := make([]*Report, 0, len(w.Modules))
	for _, module := range w.Modules {
		reports = append(reports, module.Report)
	}
	return reports
}

// Tally adds up the modules' tallies, which is what [WorkspaceReport.Summary]
// is built from.
//
// Added up rather than re-derived over the mutants, because each module's
// expectation rows are that module's: a mutant is an expected survivor if its
// own document says so, and a second derivation over a pooled ledger would
// answer differently for a row naming a mutant of a module it is not in.
func (w *WorkspaceReport) Tally() (mutation.Tally, error) {
	var total mutation.Tally
	for _, module := range w.Modules {
		if module.Report == nil {
			return mutation.Tally{}, &Error{
				Code:    CodeNoReport,
				Message: "the workspace report holds no report for " + module.ModulePath,
			}
		}
		t, err := module.Report.Tally()
		if err != nil {
			return mutation.Tally{}, err
		}
		total.Killed += t.Killed
		total.TimedOut += t.TimedOut
		total.UnexpectedSurvivors += t.UnexpectedSurvivors
		total.ExpectedSurvivors += t.ExpectedSurvivors
		total.Inconclusive += t.Inconclusive
		total.Errored += t.Errored
		total.NotRun += t.NotRun
	}
	return total, nil
}

// ExpectationFailure reports whether any module's ledger failed, or whether
// this document holds a row that names no mutant at all.
//
// Both are failures of the same ledger, which is one file: a row nobody can
// match is stale wherever it is reported, and a run whose exit code ignored the
// rows that landed here would be a run that passed because nobody owned them.
func (w *WorkspaceReport) ExpectationFailure() bool {
	for _, row := range w.Expectations {
		if row.State == StateStale {
			return true
		}
	}
	for _, module := range w.Modules {
		if module.Report != nil && module.Report.ExpectationFailure() {
			return true
		}
	}
	return false
}

// ParseWorkspace decodes a workspace report, refusing anything else.
//
// It is [Parse] for the other document type, and it refuses for the same
// reasons: an unknown field is a document this build does not understand, a
// second document in the file is not one document, and a `document_type` or a
// `schema_version` that is not this one is a file that will decode into
// something nobody meant.
func ParseWorkspace(data []byte) (*WorkspaceReport, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var w WorkspaceReport
	if err := decoder.Decode(&w); err != nil {
		return nil, &Error{
			Code:    CodeMalformedDocument,
			Message: "this is not a go-mutants workspace report",
			Err:     err,
		}
	}
	if decoder.More() {
		return nil, &Error{
			Code:    CodeMalformedDocument,
			Message: "the file holds more than one document; a workspace report is a single JSON object",
		}
	}
	switch {
	case w.DocumentType != WorkspaceDocumentType:
		return nil, &Error{
			Code: CodeMalformedDocument,
			Message: fmt.Sprintf("this document is %q, not %q",
				w.DocumentType, WorkspaceDocumentType),
		}
	case w.SchemaVersion != WorkspaceSchemaVersion:
		return nil, &Error{
			Code: CodeMalformedDocument,
			Message: fmt.Sprintf("this document is workspace-report v%d and this build reads v%d",
				w.SchemaVersion, WorkspaceSchemaVersion),
		}
	}
	return &w, nil
}

// DocumentTypeOf reads the `document_type` a file declares, without decoding
// the rest of it.
//
// It is what a command that accepts either kind of document branches on, and it
// is deliberately the document's own field rather than a guess from its shape:
// the discriminator is there so that nobody has to guess.
func DocumentTypeOf(data []byte) (string, error) {
	var head struct {
		DocumentType string `json:"document_type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return "", &Error{
			Code:    CodeMalformedDocument,
			Message: "this file is not a JSON document go-mutants wrote",
			Err:     err,
		}
	}
	if head.DocumentType == "" {
		return "", &Error{
			Code:    CodeMalformedDocument,
			Message: "this document declares no `document_type`, so nothing can say what it is",
		}
	}
	return head.DocumentType, nil
}

// MergeWorkspaceOptions is everything [MergeWorkspaces] needs.
type MergeWorkspaceOptions struct {
	// RunID identifies the merged document, and is minted by the caller for
	// the reason [MergeOptions.RunID] gives.
	RunID string
	// Shards are the workspace documents to merge, in the order the user named
	// them -- which is the order a discrepancy is reported in.
	Shards []*WorkspaceReport
}

// MergeWorkspaces combines the workspace documents of a sharded workspace run.
//
// A shard of a workspace run publishes a whole workspace document holding its
// share of every module, so merging one is merging each module's reports across
// the shards and putting the results back in the same order. The module-level
// merge is [MergeShards] itself rather than a second implementation of it: the
// congruence checks, the completeness check and the ownership check are what
// make a merge trustworthy, and a workspace has no fewer reasons to want them.
//
// What this adds is the one check a per-module merge cannot make: every shard
// has to hold the same modules, in the same order. A shard that measured a
// different workspace is not a shard of this run, and a merge that quietly
// dropped a module would publish a smaller denominator and a higher score.
func MergeWorkspaces(opts MergeWorkspaceOptions) (*WorkspaceReport, error) {
	if len(opts.Shards) == 0 {
		return nil, &Error{
			Code:    CodeNoShardReports,
			Message: "there are no shard reports to merge",
		}
	}
	for i, shard := range opts.Shards {
		if shard == nil {
			return nil, &Error{
				Code:    CodeNoShardReports,
				Message: "the " + ordinal(i+1) + " report to merge is missing",
			}
		}
	}
	first := opts.Shards[0]
	for i, shard := range opts.Shards[1:] {
		if err := sameModules(first, shard, i+2); err != nil {
			return nil, err
		}
	}

	merged := *first
	merged.RunID = opts.RunID
	merged.Modules = make([]ModuleReport, 0, len(first.Modules))
	var (
		mutants      []Mutant
		expectations []Expectation
	)
	for i, module := range first.Modules {
		shards := make([]*Report, 0, len(opts.Shards))
		for _, shard := range opts.Shards {
			shards = append(shards, shard.Modules[i].Report)
		}
		rep, err := MergeShards(MergeOptions{RunID: opts.RunID, Shards: shards})
		if err != nil {
			return nil, err
		}
		merged.Modules = append(merged.Modules, ModuleReport{
			Dir:        module.Dir,
			ModulePath: module.ModulePath,
			Report:     rep,
		})
		mutants = append(mutants, rep.Mutants...)
		expectations = append(expectations, rep.Expectations...)
	}
	expectations = append(expectations, merged.Expectations...)

	// Not an infrastructure failure whatever the shards said. A shard that
	// stopped on one published a document saying so, and a merge of documents
	// is not itself a run that could fail that way -- the failure belongs to
	// the shard's own summary, where it is, and [MergeShards] is what refuses
	// to merge an incomplete set.
	return summarised(&merged, policyOf(first.Summary.Policy), false, expectations, mutants)
}

// sameModules refuses two shards that are not of one workspace.
func sameModules(first, shard *WorkspaceReport, position int) error {
	if len(first.Modules) != len(shard.Modules) {
		return &Error{
			Code: CodeIncongruentShards,
			Message: "the " + ordinal(position) + " report holds " +
				strconv.Itoa(len(shard.Modules)) + " modules and the first holds " +
				strconv.Itoa(len(first.Modules)) + ", so they are not shards of one run",
		}
	}
	for i, module := range first.Modules {
		if shard.Modules[i].ModulePath == module.ModulePath {
			continue
		}
		return &Error{
			Code: CodeIncongruentShards,
			Message: "the " + ordinal(position) + " report's " + ordinal(i+1) +
				" module is " + quote(shard.Modules[i].ModulePath) + " and the first's is " +
				quote(module.ModulePath) + ", so they are not shards of one run",
		}
	}
	return nil
}
