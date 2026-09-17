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
	WorkspaceDocumentType  = "go-mutants/workspace-report"
	WorkspaceSchemaVersion = 1
)

type WorkspaceReport struct {
	DocumentType  string `json:"document_type"`
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	DurationMS    int64  `json:"duration_ms"`

	Workspace WorkspaceFacts `json:"workspace"`

	Summary Summary `json:"summary"`

	Expectations []Expectation `json:"expectations"`

	Modules []ModuleReport `json:"modules"`
}

type WorkspaceFacts struct {
	GoVersion       string         `json:"go_version"`
	WorkspaceDigest string         `json:"workspace_digest"`
	Platform        Platform       `json:"platform"`
	Snapshot        *SnapshotFacts `json:"snapshot,omitempty"`
}

type ModuleReport struct {
	Dir        string  `json:"dir"`
	ModulePath string  `json:"module_path"`
	Report     *Report `json:"report"`
}

type WorkspaceModule struct {
	Dir      string
	Path     string
	Located  []discover.Located
	Skips    []discover.Skip
	Selected int
}

type WorkspaceOptions struct {
	Options
	Modules []WorkspaceModule
}

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

func (w *WorkspaceReport) Reports() []*Report {
	reports := make([]*Report, 0, len(w.Modules))
	for _, module := range w.Modules {
		reports = append(reports, module.Report)
	}
	return reports
}

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

type MergeWorkspaceOptions struct {
	RunID  string
	Shards []*WorkspaceReport
}

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

	return summarised(&merged, policyOf(first.Summary.Policy), false, expectations, mutants)
}

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
