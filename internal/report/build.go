// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
)

const unknownValue = "unknown"

type MutantResult struct {
	ID                   string
	Outcome              mutation.Outcome
	NotRunReason         NotRunReason
	Duration             time.Duration
	KilledBy             string
	Attempts             int
	OutputTail           string
	MemoryExceeded       bool
	PeakMemory           int64
	Diverged             bool
	Executions           []Execution
	CoveringTestPackages []string
	CoveringTests        []TestRef
	Uncovered            bool
	Unobserved           bool
	Cached               bool
}

type Rejection struct {
	ID         string
	Diagnostic string
}

type Options struct {
	ToolVersion string
	RunID       string
	Status      Status
	Started     time.Time
	Finished    time.Time

	Config     config.Config
	Mode       SelectionMode
	ChangedRef string
	Shard      *Shard
	Selected   int

	Module string

	ModulePath      string
	GoVersion       string
	WorkspaceDigest string
	Platform        Platform

	Catalog *mutation.Catalog
	Located []discover.Located
	Skips   []discover.Skip

	Results    []MutantResult
	Rejections []Rejection

	TestCommand   []string
	Baseline      []time.Duration
	Timeout       time.Duration
	TimeoutSource TimeoutSource
	Memory        int64
	MemorySource  MemorySource

	CoverageMode              CoverageMode
	CoverageBinaries          int
	CoverageTests             int
	CoverageUnavailableReason string
	CoverageBuildFallback     bool

	CacheMode   CacheMode
	CacheMisses int
	CacheWrites int

	Timing          *Timing
	Validation      *Validation
	Snapshot        *SnapshotFacts
	Toolchain       *ToolchainFacts
	ResolvedCommand []string

	Warnings []Warning

	InfrastructureError bool
}

func Build(opts Options) (*Report, error) {
	if err := checkIdentity(opts); err != nil {
		return nil, err
	}
	if err := checkMemory(opts); err != nil {
		return nil, err
	}
	if opts.Catalog == nil {
		return nil, &Error{
			Code:    CodeNoCatalog,
			Message: "the run report has no catalogue: every run has one, even an empty one",
		}
	}

	command, err := testCommand(opts)
	if err != nil {
		return nil, err
	}
	results, rejections, err := index(opts)
	if err != nil {
		return nil, err
	}
	mutants, rejected, err := partition(opts, results, rejections)
	if err != nil {
		return nil, err
	}

	expectations := Evaluate(opts.Config.Mutation.Expect, dispositions(mutants, rejected))
	tally, err := tallyOf(mutants, results, expectations)
	if err != nil {
		return nil, err
	}
	selection, err := selectionOf(opts, len(mutants)+len(rejected), len(rejected))
	if err != nil {
		return nil, err
	}
	shard, err := shardOf(opts)
	if err != nil {
		return nil, err
	}
	coverage, err := coverageOf(opts, mutants)
	if err != nil {
		return nil, err
	}
	cache, err := cacheBlock(opts.CacheMode, opts.CacheMisses, opts.CacheWrites, mutants)
	if err != nil {
		return nil, err
	}

	r := &Report{
		DocumentType:  DocumentType,
		SchemaVersion: SchemaVersion,
		ToolVersion:   opts.ToolVersion,
		RunID:         opts.RunID,
		Status:        opts.Status,
		StartedAt:     FormatTimestamp(opts.Started),
		FinishedAt:    FormatTimestamp(opts.Finished),
		DurationMS:    milliseconds(opts.Finished.Sub(opts.Started)),
		Workspace: Workspace{
			ModulePath:      or(opts.ModulePath, unknownValue),
			GoVersion:       or(opts.GoVersion, unknownValue),
			WorkspaceDigest: opts.WorkspaceDigest,
			Platform:        platformOf(opts.Platform),
			Snapshot:        snapshotOf(opts.Snapshot),
		},
		Selection: selection,
		Shard:     shard,
		Test: Test{
			Command:         command,
			Baseline:        baselineOf(opts.Baseline),
			TimeoutMS:       milliseconds(opts.Timeout),
			TimeoutSource:   timeoutSource(opts),
			MemoryBytes:     max(opts.Memory, 0),
			MemorySource:    memorySource(opts),
			Toolchain:       toolchainOf(opts.Toolchain),
			ResolvedCommand: resolvedCommand(opts.ResolvedCommand),
		},
		Coverage:     coverage,
		Cache:        cache,
		Summary:      summaryOf(tally, opts.Config.Policy, opts.InfrastructureError, expectations, mutants),
		Mutants:      mutants,
		Rejected:     rejected,
		Skips:        skipsOf(opts.Skips),
		Expectations: expectations,
		Warnings:     warningsOf(opts.Warnings),
		Timing:       timingOf(opts.Timing),
		Validation:   validationOf(opts.Validation),
	}
	return r, nil
}

func timingOf(timing *Timing) *Timing {
	if timing == nil {
		return nil
	}
	return &Timing{
		Phases: append(make([]PhaseTiming, 0, len(timing.Phases)), timing.Phases...),
		Stages: append(make([]StageTiming, 0, len(timing.Stages)), timing.Stages...),
	}
}

func validationOf(validation *Validation) *Validation {
	if validation == nil {
		return nil
	}
	facts := *validation
	return &facts
}

func snapshotOf(snapshot *SnapshotFacts) *SnapshotFacts {
	if snapshot == nil {
		return nil
	}
	facts := *snapshot
	return &facts
}

func toolchainOf(toolchain *ToolchainFacts) *ToolchainFacts {
	if toolchain == nil || (toolchain.GoBin == "" && toolchain.Version == "") {
		return nil
	}
	return &ToolchainFacts{
		GoBin:   or(toolchain.GoBin, unknownValue),
		Version: or(toolchain.Version, unknownValue),
	}
}

func resolvedCommand(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	return slices.Clone(argv)
}

func checkIdentity(opts Options) error {
	if !runIDPattern.MatchString(opts.RunID) {
		return &Error{
			Code: CodeInvalidRunID,
			Message: fmt.Sprintf("%q is not a run id: expected a UTC timestamp and four hex digits, as in 20260218T091500Z-3f9c",
				opts.RunID),
		}
	}
	if !opts.Status.Valid() {
		return &Error{
			Code:    CodeInvalidStatus,
			Message: fmt.Sprintf("%q is not a run status: expected completed, interrupted, or failed", string(opts.Status)),
		}
	}
	switch {
	case opts.Started.IsZero() || opts.Finished.IsZero():
		return &Error{
			Code:    CodeInvalidTimestamps,
			Message: "the run report needs both a start and a finish time",
		}
	case opts.Finished.Before(opts.Started):
		return &Error{
			Code: CodeInvalidTimestamps,
			Message: fmt.Sprintf("the run finished at %s, before it started at %s",
				FormatTimestamp(opts.Finished), FormatTimestamp(opts.Started)),
		}
	}
	if !digestPattern.MatchString(opts.WorkspaceDigest) {
		return &Error{
			Code: CodeInvalidWorkspaceDigest,
			Message: fmt.Sprintf("%q is not a workspace digest: expected 64 lowercase hex characters",
				opts.WorkspaceDigest),
		}
	}
	return nil
}

func testCommand(opts Options) ([]string, error) {
	command := opts.TestCommand
	if len(command) == 0 {
		command = opts.Config.Test.Command
	}
	if len(command) == 0 {
		return nil, &Error{
			Code:    CodeInvalidTestCommand,
			Message: "the run report has no test command: neither the caller nor test.command named one",
		}
	}
	return slices.Clone(command), nil
}

func index(opts Options) (map[string]MutantResult, map[string]Rejection, error) {
	results := make(map[string]MutantResult, len(opts.Results))
	for _, result := range opts.Results {
		if _, seen := results[result.ID]; seen {
			return nil, nil, duplicate("result", result.ID)
		}
		results[result.ID] = result
	}
	rejections := make(map[string]Rejection, len(opts.Rejections))
	for _, rejection := range opts.Rejections {
		if _, seen := rejections[rejection.ID]; seen {
			return nil, nil, duplicate("rejection", rejection.ID)
		}
		if _, both := results[rejection.ID]; both {
			return nil, nil, &Error{
				Code: CodeDuplicateEntry,
				Message: fmt.Sprintf("mutant %s is both rejected and executed: a mutant that does not compile cannot have an outcome",
					display(rejection.ID)),
			}
		}
		rejections[rejection.ID] = rejection
	}
	return results, rejections, nil
}

func partition(opts Options, results map[string]MutantResult, rejections map[string]Rejection) ([]Mutant, []Rejected, error) {
	catalogued := opts.Catalog.Mutants()
	located := locate(opts.Located)

	mutants := make([]Mutant, 0, len(catalogued))
	rejected := make([]Rejected, 0, len(rejections))
	for _, m := range catalogued {
		if m.ModulePath != opts.Module {
			continue
		}
		where, ok := located[keyOf(m.ModulePath, m.Path, m.Span, m.Rule.Name)]
		if !ok {
			return nil, nil, &Error{
				Code: CodeMissingLocation,
				Message: fmt.Sprintf("mutant %s (%s at %s %s) is not one of the candidates discovery reported",
					m.DisplayID, m.Rule.Name, m.Path, m.Span),
			}
		}
		if rejection, isRejected := rejections[m.ID]; isRejected {
			rejected = append(rejected, Rejected{
				ID:         m.ID,
				DisplayID:  m.DisplayID,
				Path:       m.Path,
				Line:       where.Line,
				Column:     where.Column,
				Rule:       m.Rule.Name,
				Diagnostic: rejection.Diagnostic,
			})
			continue
		}
		result, ok := results[m.ID]
		if !ok {
			return nil, nil, &Error{
				Code: CodeMissingResult,
				Message: fmt.Sprintf("mutant %s (%s at %s:%d) has no result: pass an explicit not-run result for every mutant the run did not execute",
					m.DisplayID, m.Rule.Name, m.Path, where.Line),
			}
		}
		outcome, err := OutcomeOf(result.Outcome)
		if err != nil {
			return nil, nil, err
		}
		reason, err := notRunReasonOf(m, result, outcome)
		if err != nil {
			return nil, nil, err
		}
		if err = checkExecutions(m, result, outcome); err != nil {
			return nil, nil, err
		}
		mutants = append(mutants, Mutant{
			ID:                   m.ID,
			DisplayID:            m.DisplayID,
			Path:                 m.Path,
			Package:              where.Package,
			Family:               string(m.Rule.Family),
			Rule:                 m.Rule.Name,
			RuleVersion:          m.Rule.Version,
			Line:                 where.Line,
			Column:               where.Column,
			StartByte:            m.Span.StartByte,
			EndByte:              m.Span.EndByte,
			Original:             m.Original,
			Replacement:          m.Replacement,
			Branch:               branchOf(where.Branch),
			Outcome:              outcome,
			NotRunReason:         reason,
			DurationMS:           milliseconds(result.Duration),
			KilledBy:             text(result.KilledBy),
			Attempts:             result.Attempts,
			Executions:           executionsOf(result.Executions),
			OutputTail:           text(result.OutputTail),
			CoveringTestPackages: stringList(result.CoveringTestPackages),
			CoveringTests:        testRefs(result.CoveringTests),
			Uncovered:            result.Uncovered,
			Unobserved:           result.Unobserved,
			Cached:               result.Cached,
			MemoryExceeded:       result.MemoryExceeded || anyExecutionExceeded(result.Executions),
			PeakMemoryBytes:      max(result.PeakMemory, highestExecutionPeak(result.Executions)),
			Diverged:             result.Diverged || anyExecutionDiverged(result.Executions),
		})
	}
	if err := checkAccountedFor(opts, len(mutants), len(rejected)); err != nil {
		return nil, nil, err
	}
	return mutants, rejected, nil
}

func notRunReasonOf(m mutation.Mutant, result MutantResult, outcome Outcome) (*string, error) {
	reason := result.NotRunReason
	switch {
	case outcome != OutcomeNotRun && reason != "":
		return nil, &Error{
			Code: CodeInvalidNotRunReason,
			Message: fmt.Sprintf("mutant %s is %s and carries the not-run reason %q: a mutant that was measured has no reason for not having been",
				m.DisplayID, outcome, string(reason)),
		}
	case outcome != OutcomeNotRun:
		return nil, nil
	case reason == "":
		return nil, &Error{
			Code: CodeInvalidNotRunReason,
			Message: fmt.Sprintf("mutant %s was not run and does not say why: pass one of %s",
				m.DisplayID, joinReasons()),
		}
	case !reason.Valid():
		return nil, &Error{
			Code: CodeInvalidNotRunReason,
			Message: fmt.Sprintf("%q is not a reason a mutant can be not-run for: expected one of %s",
				string(reason), joinReasons()),
		}
	}
	return text(string(reason)), nil
}

func checkExecutions(m mutation.Mutant, result MutantResult, outcome Outcome) error {
	if len(result.Executions) == 0 {
		return nil
	}
	switch {
	case result.Cached:
		return &Error{
			Code: CodeInvalidExecutions,
			Message: fmt.Sprintf("mutant %s is marked cached and carries %s: an outcome adopted from the cache was measured by another run",
				m.DisplayID, countNoun(len(result.Executions), "execution")),
		}
	case result.Uncovered:
		return &Error{
			Code: CodeInvalidExecutions,
			Message: fmt.Sprintf("mutant %s is marked uncovered and carries %s: coverage settles a mutant without starting a process",
				m.DisplayID, countNoun(len(result.Executions), "execution")),
		}
	case result.Unobserved:
		return &Error{
			Code: CodeInvalidExecutions,
			Message: fmt.Sprintf("mutant %s is marked unobserved and carries %s: a probe settles a mutant without starting a process",
				m.DisplayID, countNoun(len(result.Executions), "execution")),
		}
	case outcome == OutcomeNotRun:
		return &Error{
			Code: CodeInvalidExecutions,
			Message: fmt.Sprintf("mutant %s was not run and carries %s: a mutant nothing measured has nothing to show for it",
				m.DisplayID, countNoun(len(result.Executions), "execution")),
		}
	case len(result.Executions) != result.Attempts:
		return &Error{
			Code: CodeInvalidExecutions,
			Message: fmt.Sprintf("mutant %s reports %s and %s: the two are the same fact at two resolutions",
				m.DisplayID, countNoun(result.Attempts, "attempt"), countNoun(len(result.Executions), "execution")),
		}
	}
	for i, execution := range result.Executions {
		if !execution.Outcome.Observed() {
			return &Error{
				Code: CodeInvalidExecutions,
				Message: fmt.Sprintf("attempt %d of mutant %s is %s, which is a verdict about several passes rather than something one pass saw: expected one of %s",
					i+1, m.DisplayID, execution.Outcome, joinObservations()),
			}
		}
		if execution.MemoryExceeded && execution.Outcome != OutcomeKilled {
			return &Error{
				Code: CodeInvalidExecutions,
				Message: fmt.Sprintf("attempt %d of mutant %s is %s and says a memory bound settled it: a tree the bound killed is %s",
					i+1, m.DisplayID, execution.Outcome, OutcomeKilled),
			}
		}
	}
	return nil
}

func joinObservations() string {
	names := make([]string, 0, len(Observations()))
	for _, outcome := range Observations() {
		names = append(names, string(outcome))
	}
	return strings.Join(names, ", ")
}

func executionsOf(executions []Execution) []Execution {
	out := make([]Execution, 0, len(executions))
	for i, execution := range executions {
		execution.Attempt = i + 1
		execution.Binaries = stringList(execution.Binaries)
		execution.Tests = testRefs(execution.Tests)
		out = append(out, execution)
	}
	return out
}

func testRefs(refs []TestRef) []TestRef {
	if len(refs) == 0 {
		return nil
	}
	return slices.Clone(refs)
}

func joinReasons() string {
	names := make([]string, 0, len(NotRunReasons()))
	for _, reason := range NotRunReasons() {
		names = append(names, string(reason))
	}
	return strings.Join(names, ", ")
}

func checkAccountedFor(opts Options, mutants, rejected int) error {
	if err := checkRowsOf(opts, "result", opts.Results,
		func(r MutantResult) string { return r.ID }, mutants); err != nil {
		return err
	}
	return checkRowsOf(opts, "rejection", opts.Rejections,
		func(r Rejection) string { return r.ID }, rejected)
}

func checkRowsOf[T any](opts Options, kind string, rows []T, id func(T) string, consumed int) error {
	mine := 0
	for _, row := range rows {
		m, known := opts.Catalog.ByID(id(row))
		if !known {
			return &Error{
				Code: CodeUnknownMutant,
				Message: fmt.Sprintf("the %s for mutant %s names an id that is not in this run's catalogue",
					kind, display(id(row))),
			}
		}
		if m.ModulePath == opts.Module {
			mine++
		}
	}
	if mine == consumed {
		return nil
	}
	return &Error{
		Code:    CodeUnknownMutant,
		Message: "internal error: a " + kind + " could not be matched to the catalogue",
	}
}

type locationKey struct {
	module string
	path   string
	span   mutation.Span
	rule   string
}

func keyOf(module, path string, span mutation.Span, rule string) locationKey {
	return locationKey{module: module, path: path, span: span, rule: rule}
}

func branchOf(proof *discover.BranchProof) *Branch {
	if proof == nil {
		return nil
	}
	return &Branch{
		Direction:       proof.Direction,
		BodyStartLine:   proof.BodyStartLine,
		BodyStartColumn: proof.BodyStartColumn,
		BodyEndLine:     proof.BodyEndLine,
		BodyEndColumn:   proof.BodyEndColumn,
	}
}

func locate(candidates []discover.Located) map[locationKey]discover.Located {
	out := make(map[locationKey]discover.Located, len(candidates))
	for _, candidate := range candidates {
		key := keyOf(candidate.ModulePath, candidate.Path, candidate.Span, candidate.Rule.Name)
		if _, seen := out[key]; !seen {
			out[key] = candidate
		}
	}
	return out
}

func dispositions(mutants []Mutant, rejected []Rejected) map[string]Disposition {
	known := make(map[string]Disposition, len(mutants)+len(rejected))
	for _, m := range mutants {
		known[m.ID] = Disposition{Present: true, Outcome: m.Outcome}
	}
	for _, r := range rejected {
		known[r.ID] = Disposition{Present: true, Rejected: true}
	}
	return known
}

func tallyOf(mutants []Mutant, results map[string]MutantResult, expectations []Expectation) (mutation.Tally, error) {
	expected := make(map[string]bool, len(expectations))
	for _, e := range expectations {
		if e.State == StateFulfilled {
			expected[e.ID] = true
		}
	}
	rows := make([]mutation.Result, 0, len(mutants))
	for _, m := range mutants {
		rows = append(rows, mutation.Result{
			Outcome:          results[m.ID].Outcome,
			ExpectedSurvivor: expected[m.ID],
		})
	}
	tally, err := mutation.TallyOf(rows)
	if err != nil {
		return mutation.Tally{}, &Error{
			Code:    CodeInvalidOutcome,
			Message: "the run's outcomes could not be counted",
			Err:     err,
		}
	}
	return tally, nil
}

func selectionOf(opts Options, candidates, rejected int) (Selection, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModeAll
	}
	switch {
	case !mode.Valid():
		return Selection{}, &Error{
			Code: CodeInvalidSelection,
			Message: fmt.Sprintf("%q is not a selection mode: expected one of %s",
				string(opts.Mode), joinModes()),
		}
	case mode == ModeChanged && opts.ChangedRef == "":
		return Selection{}, &Error{
			Code:    CodeInvalidSelection,
			Message: "the run narrowed itself to the changed lines but names no ref it compared against",
		}
	case opts.Shard != nil && mode != ModeShard:
		return Selection{}, &Error{
			Code: CodeInvalidSelection,
			Message: fmt.Sprintf("the run reports shard %d of %d and a selection mode of %q: a shard executes its own share, so its mode is %q",
				opts.Shard.Index, opts.Shard.Total, string(mode), string(ModeShard)),
		}
	case mode == ModeShard && opts.Shard == nil:
		return Selection{}, &Error{
			Code:    CodeInvalidSelection,
			Message: "the run reports a sharded selection without saying which shard it is",
		}
	case opts.Selected < 0 || opts.Selected > candidates:
		return Selection{}, &Error{
			Code: CodeInvalidSelection,
			Message: fmt.Sprintf("the run reports %d of %d catalogued mutants as selected",
				opts.Selected, candidates),
		}
	}
	return Selection{
		Mode:       mode,
		ChangedRef: text(opts.ChangedRef),
		Profile:    opts.Config.Mutation.Profile.String(),
		Operators:  stringList(opts.Config.Mutation.Operators),
		Include:    stringList(opts.Config.Mutation.Include),
		Exclude:    stringList(opts.Config.Mutation.Exclude),
		Candidates: candidates,
		Rejected:   rejected,
		Selected:   opts.Selected,
	}, nil
}

func joinModes() string {
	names := make([]string, 0, len(SelectionModes()))
	for _, mode := range SelectionModes() {
		names = append(names, string(mode))
	}
	return strings.Join(names, ", ")
}

func shardOf(opts Options) (*Shard, error) {
	if opts.Shard == nil {
		return nil, nil
	}
	shard := *opts.Shard
	if shard.Assignment == "" {
		shard.Assignment = mutation.ShardAssignment
	}
	switch {
	case shard.Total < 1 || shard.Index < 1 || shard.Index > shard.Total:
		return nil, &Error{
			Code: CodeInvalidShard,
			Message: fmt.Sprintf("shard %d of %d is not a shard: the index is 1-based and never exceeds the total",
				shard.Index, shard.Total),
		}
	case shard.Assignment != mutation.ShardAssignment:
		return nil, &Error{
			Code: CodeInvalidShard,
			Message: fmt.Sprintf("this build assigns mutants to shards by %q and cannot write a report claiming %q",
				mutation.ShardAssignment, shard.Assignment),
		}
	}
	return &shard, nil
}

func coverageOf(opts Options, mutants []Mutant) (Coverage, error) {
	coverage, err := coverageBlock(opts.CoverageMode, opts.CoverageBinaries, opts.CoverageTests, mutants)
	if err != nil {
		return Coverage{}, err
	}
	coverage.UnavailableReason = text(opts.CoverageUnavailableReason)
	coverage.BuildFallback = opts.CoverageBuildFallback
	return coverage, nil
}

func coverageBlock(mode CoverageMode, binaryCount, testCount int, mutants []Mutant) (Coverage, error) {
	stated := mode
	if mode == "" {
		mode = CoverageOff
	}
	if !mode.Valid() {
		return Coverage{}, &Error{
			Code:    CodeInvalidCoverage,
			Message: fmt.Sprintf("%q is not a coverage mode: expected off, package or test", string(stated)),
		}
	}

	uncovered := 0
	for _, m := range mutants {
		if err := checkCoverageFacts(mode, m); err != nil {
			return Coverage{}, err
		}
		if !m.Uncovered {
			continue
		}
		switch {
		case !mode.Narrowed():
			return Coverage{}, &Error{
				Code: CodeInvalidCoverage,
				Message: fmt.Sprintf("mutant %s is marked uncovered in a run whose coverage mode is %q: only a coverage-guided run knows what covers a mutant",
					m.DisplayID, string(mode)),
			}
		case m.Outcome != OutcomeSurvived || m.Attempts != 0:
			return Coverage{}, &Error{
				Code: CodeInvalidCoverage,
				Message: fmt.Sprintf("mutant %s is marked uncovered but is %s after %s: an uncovered mutant is a survivor the run never executed",
					m.DisplayID, m.Outcome, countNoun(m.Attempts, "attempt")),
			}
		}
		uncovered++
	}
	for _, m := range mutants {
		if err := checkUnobserved(m); err != nil {
			return Coverage{}, err
		}
	}

	coverage := Coverage{Mode: mode}
	if !mode.Narrowed() {
		return coverage, nil
	}
	if binaryCount < 0 {
		return Coverage{}, &Error{
			Code:    CodeInvalidCoverage,
			Message: fmt.Sprintf("the coverage pass reports %d test binaries", binaryCount),
		}
	}
	binaries := binaryCount
	coverage.Binaries = &binaries
	coverage.MutantsUncovered = &uncovered
	if mode != CoverageTest {
		return coverage, nil
	}
	if testCount < 0 {
		return Coverage{}, &Error{
			Code:    CodeInvalidCoverage,
			Message: fmt.Sprintf("the coverage pass reports %d tests", testCount),
		}
	}
	tests := testCount
	coverage.Tests = &tests
	return coverage, nil
}

func checkUnobserved(m Mutant) error {
	if !m.Unobserved {
		return nil
	}
	switch {
	case m.Uncovered:
		return &Error{
			Code: CodeInvalidCoverage,
			Message: fmt.Sprintf("mutant %s is marked both uncovered and unobserved: coverage settles a mutant "+
				"nothing reaches before a probe is asked whether anything could see it", m.DisplayID),
		}
	case m.Outcome != OutcomeSurvived || m.Attempts != 0:
		return &Error{
			Code: CodeInvalidCoverage,
			Message: fmt.Sprintf("mutant %s is marked unobserved but is %s after %s: an unobserved mutant is a "+
				"survivor the run never executed", m.DisplayID, m.Outcome, countNoun(m.Attempts, "attempt")),
		}
	}
	return nil
}

func checkCoverageFacts(mode CoverageMode, m Mutant) error {
	if mode == CoverageTest {
		return nil
	}
	if len(m.CoveringTests) > 0 {
		return &Error{
			Code: CodeInvalidCoverage,
			Message: fmt.Sprintf("mutant %s names %s in a run whose coverage mode is %q: only a test-narrowed run knows which tests cover a mutant",
				m.DisplayID, countNoun(len(m.CoveringTests), "covering test"), string(mode)),
		}
	}
	for _, execution := range m.Executions {
		if len(execution.Tests) > 0 {
			return &Error{
				Code: CodeInvalidCoverage,
				Message: fmt.Sprintf("attempt %d of mutant %s was narrowed to %s in a run whose coverage mode is %q: only a test-narrowed run selects tests",
					execution.Attempt, m.DisplayID, countNoun(len(execution.Tests), "test"), string(mode)),
			}
		}
	}
	return nil
}

func cacheBlock(mode CacheMode, misses, writes int, mutants []Mutant) (Cache, error) {
	stated := mode
	if mode == "" {
		mode = CacheOff
	}
	if !mode.Valid() {
		return Cache{}, &Error{
			Code:    CodeInvalidCache,
			Message: fmt.Sprintf("%q is not a cache mode: expected off or on", string(stated)),
		}
	}

	hits := 0
	for _, m := range mutants {
		if !m.Cached {
			continue
		}
		switch {
		case mode != CacheOn:
			return Cache{}, &Error{
				Code: CodeInvalidCache,
				Message: fmt.Sprintf("mutant %s is marked cached in a run whose cache mode is %q: an outcome nothing read cannot have been reused",
					m.DisplayID, string(mode)),
			}
		case m.Uncovered:
			return Cache{}, &Error{
				Code: CodeInvalidCache,
				Message: fmt.Sprintf("mutant %s is marked both cached and uncovered: coverage settles a mutant before the cache is asked about it",
					m.DisplayID),
			}
		case !reusable(m.Outcome):
			return Cache{}, &Error{
				Code: CodeInvalidCache,
				Message: fmt.Sprintf("mutant %s is marked cached and is %s, which is not an outcome the cache stores",
					m.DisplayID, m.Outcome),
			}
		}
		hits++
	}

	switch {
	case misses < 0 || writes < 0:
		return Cache{}, &Error{
			Code:    CodeInvalidCache,
			Message: fmt.Sprintf("the run reports %d cache misses and %d writes", misses, writes),
		}
	case writes > misses:
		return Cache{}, &Error{
			Code: CodeInvalidCache,
			Message: fmt.Sprintf("the run stored %s from %s: an outcome is only stored for a mutant the cache did not already have",
				countNoun(writes, "outcome"), countNoun(misses, "cache miss")),
		}
	case mode == CacheOff && (misses > 0 || writes > 0):
		return Cache{}, &Error{
			Code: CodeInvalidCache,
			Message: fmt.Sprintf("the run reports the cache off and %s with %s: a cache that was not consulted has no misses",
				countNoun(misses, "cache miss"), countNoun(writes, "write")),
		}
	}
	return Cache{Mode: mode, Hits: hits, Misses: misses, Writes: writes}, nil
}

func reusable(o Outcome) bool {
	switch o {
	case OutcomeKilled, OutcomeSurvived, OutcomeTimedOut:
		return true
	case OutcomeErrored, OutcomeInconclusive, OutcomeNotRun:
	}
	return false
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func summaryOf(t mutation.Tally, policy mutation.Policy, infrastructure bool, expectations []Expectation, mutants []Mutant) Summary {
	summary := Summary{
		Total:        t.Total(),
		Killed:       t.Killed,
		Survived:     t.Survived(),
		TimedOut:     t.TimedOut,
		Inconclusive: t.Inconclusive,
		Errored:      t.Errored,
		NotRun:       t.NotRun,
		Policy: PolicyResult{
			Strict:         policy.Strict,
			MinimumScore:   policy.MinimumScore,
			RequireMutants: policy.RequireMutants,
		},
	}
	if percent, defined := mutation.ScoreOf(t).Percent(); defined {
		value := percent
		summary.ScorePercent = &value
	}
	verdict := mutation.Decide(t, policy, mutation.Signals{
		InfrastructureError: infrastructure,
		ExpectationFailure:  expectationFailure(expectations, mutants),
	})
	if len(verdict.Failures) > 0 {
		summary.Policy.Failure = text(string(verdict.Failures[0].Reason))
	}
	return summary
}

func expectationFailure(expectations []Expectation, mutants []Mutant) bool {
	r := Report{Expectations: expectations, Mutants: mutants}
	return r.ExpectationFailure()
}

func baselineOf(runs []time.Duration) Baseline {
	durations := make([]int64, 0, len(runs))
	var slowest int64
	for _, run := range runs {
		ms := milliseconds(run)
		durations = append(durations, ms)
		slowest = max(slowest, ms)
	}
	return Baseline{Runs: len(runs), DurationsMS: durations, SlowestMS: slowest}
}

func skipsOf(skips []discover.Skip) []Skip {
	out := make([]Skip, 0, len(skips))
	for _, skip := range skips {
		out = append(out, Skip{Path: skip.Path, Reason: string(skip.Reason), Count: skip.Count})
	}
	slices.SortFunc(out, func(x, y Skip) int {
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		return strings.Compare(x.Reason, y.Reason)
	})
	return out
}

func warningsOf(warnings []Warning) []Warning {
	out := make([]Warning, 0, len(warnings))
	return append(out, warnings...)
}

func platformOf(p Platform) Platform {
	if p.OS == "" {
		p.OS = runtime.GOOS
	}
	if p.Arch == "" {
		p.Arch = runtime.GOARCH
	}
	return p
}

func timeoutSource(opts Options) TimeoutSource {
	if opts.TimeoutSource.Valid() {
		return opts.TimeoutSource
	}
	if opts.Config.Test.Timeout > 0 {
		return TimeoutExplicit
	}
	return TimeoutDerived
}

func memorySource(opts Options) MemorySource {
	if opts.MemorySource.Valid() {
		return opts.MemorySource
	}
	return ""
}

func checkMemory(opts Options) error {
	source, memory := opts.MemorySource, opts.Memory
	switch {
	case source == "" && memory == 0:
		return nil
	case source == "":
		return &Error{
			Code: CodeInvalidMemory,
			Message: fmt.Sprintf("the run reports a memory bound of %d bytes and does not say where it came from: expected one of %s",
				memory, joinMemorySources()),
		}
	case !source.Valid():
		return &Error{
			Code: CodeInvalidMemory,
			Message: fmt.Sprintf("the memory bound came from %q, which is not a source: expected one of %s",
				source, joinMemorySources()),
		}
	case source == MemoryUnavailable && memory != 0:
		return &Error{
			Code: CodeInvalidMemory,
			Message: fmt.Sprintf("the run reports no memory bound and a bound of %d bytes: a run with none has no number to report",
				memory),
		}
	case source != MemoryUnavailable && memory <= 0:
		return &Error{
			Code: CodeInvalidMemory,
			Message: fmt.Sprintf("the run reports a %s memory bound of %d bytes: a bound nothing could fit in is not one",
				source, memory),
		}
	}
	return nil
}

func joinMemorySources() string {
	names := make([]string, 0, 3)
	for _, source := range []MemorySource{MemoryExplicit, MemoryDerived, MemoryUnavailable} {
		names = append(names, string(source))
	}
	return strings.Join(names, ", ")
}

func anyExecutionExceeded(executions []Execution) bool {
	for _, execution := range executions {
		if execution.MemoryExceeded {
			return true
		}
	}
	return false
}

func anyExecutionDiverged(executions []Execution) bool {
	for _, execution := range executions {
		if execution.Diverged {
			return true
		}
	}
	return false
}

func highestExecutionPeak(executions []Execution) int64 {
	var peak int64
	for _, execution := range executions {
		peak = max(peak, execution.PeakMemoryBytes)
	}
	return peak
}

func duplicate(kind, id string) error {
	return &Error{
		Code:    CodeDuplicateEntry,
		Message: fmt.Sprintf("mutant %s has more than one %s", display(id), kind),
	}
}

func display(id string) string {
	if len(id) <= mutation.DisplayIDLength {
		return id
	}
	return id[:mutation.DisplayIDLength]
}

func stringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return slices.Clone(values)
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
