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

// unknownValue is what a workspace field says when the run genuinely does not
// know it — a module too old to declare a `go` directive, or a failure early
// enough that the loader never answered. It is the same word internal/cli uses
// for the same question, and it is preferred to an empty string, which would
// read as a fact rather than as an absence.
const unknownValue = "unknown"

// A MutantResult is what execution learned about one catalogued mutant.
//
// The outcome is the core [mutation.Outcome] rather than this document's
// spelling, so that the execution layer never has to know how a report writes
// things down; [Build] translates.
type MutantResult struct {
	// ID is the full 64 hex character mutant id.
	ID string
	// Outcome is what happened.
	Outcome mutation.Outcome
	// NotRunReason says why a not-run mutant was not run. It is required for
	// [mutation.OutcomeNotRun] and refused for every other outcome: a
	// measurement has no reason for not having been made, and a mutant that was
	// not measured always has one. See [NotRunReason].
	NotRunReason NotRunReason
	// Duration is the wall-clock time the mutant's execution took, summed over
	// every attempt.
	Duration time.Duration
	// KilledBy names the test binary that detected the mutant — the one that
	// failed, or the one it hung — when the harness could name it. Empty
	// becomes null, which is what an outcome that detected nothing carries.
	KilledBy string
	// Attempts is how many times the mutant was executed: zero for a mutant the
	// run never reached, one for an outcome settled first time, two for a
	// confirmed timeout.
	//
	// A not-run mutant is therefore usually zero, but not always. A mutant that
	// timed out once and was interrupted before the serial retry could repeat it
	// is not-run with one recorded attempt: it really was executed, and an
	// unrepeated timeout is not something the run is entitled to call a result.
	// That is internal/execute's documented contract for a cancelled run, and
	// this field reports what happened rather than smoothing it.
	Attempts int
	// OutputTail is the tail of the test output kept for a human. Empty becomes
	// null.
	OutputTail string
	// CoveringTestPackages are the import paths of the test binaries whose
	// coverage profile reaches this mutant's lines. Nil becomes the empty list,
	// which is what a run with coverage off carries for every mutant.
	CoveringTestPackages []string
	// Uncovered says the run established that no test binary reaches this
	// mutant's lines and therefore did not execute it. Such a result is a
	// survivor with no attempts; [Build] refuses any other combination, because
	// a document that recorded a killed mutant as uncovered would be describing
	// a detection nothing performed.
	Uncovered bool
	// Cached says the outcome was adopted from the outcome cache rather than
	// measured by this run. The rest of the fields are then the ones the run
	// that measured it recorded. See [Mutant.Cached] for what [Build] refuses.
	Cached bool
}

// A Rejection is a catalogued mutant validation refused, with the compiler
// diagnostic the delta-debugging pass isolated.
type Rejection struct {
	// ID is the full 64 hex character mutant id.
	ID string
	// Diagnostic is the compiler's own words, already trimmed by validation.
	Diagnostic string
}

// Options is everything [Build] needs. Every field is supplied by the caller;
// this package reads no global state except the platform it is running on.
type Options struct {
	// ToolVersion is the go-mutants version string.
	ToolVersion string
	// RunID is the run identifier, in the fixed `20060102T150405Z-abcd` shape.
	// It becomes a file name, so it is checked here rather than trusted.
	RunID string
	// Status is how the run ended. There is no default.
	Status Status
	// Started and Finished bracket the run. Both are required, and Finished
	// must not precede Started.
	Started  time.Time
	Finished time.Time

	// Config is the fully resolved configuration. The selection block, the
	// policy gates, and the expectations ledger are read from it, so that the
	// report describes the configuration the run actually obeyed rather than a
	// second copy of it assembled by hand.
	Config config.Config
	// Mode is how the run chose what to execute. The zero value is [ModeAll].
	Mode SelectionMode
	// ChangedRef is the git ref a `--changed` run took its diff against. Empty
	// is a run that did not narrow by a diff, and is written as null. A
	// [ModeChanged] run must name one: the mode without the ref would be a
	// document saying it looked at a diff without saying which.
	ChangedRef string
	// Shard is which shard of a split run this is, or nil when the run was not
	// split. A non-nil shard implies [ModeShard]; see [Shard].
	Shard *Shard
	// Selected is how many catalogued mutants the run set out to execute. It is
	// stated rather than counted, because "selected but never reached" — an
	// interrupted run — is a real state that no count of results can express.
	Selected int

	// ModulePath, GoVersion and WorkspaceDigest name the tree that was read.
	// The digest is required and checked; the other two fall back to "unknown".
	ModulePath      string
	GoVersion       string
	WorkspaceDigest string
	// Platform is the host. The zero value is this process's own GOOS and
	// GOARCH, which is the right answer for every run that is not being
	// replayed.
	Platform Platform

	// Catalog is the identified, deduplicated set of mutants. It is the
	// document's backbone: mutants[] and rejected[] are a partition of it, in
	// catalogue order.
	Catalog *mutation.Catalog
	// Located are discovery's candidates, which carry the coordinates the
	// catalogue does not. Every catalogued mutant must be among them.
	Located []discover.Located
	// Skips are discovery's recorded suppressions.
	Skips []discover.Skip

	// Results is one result per catalogued mutant that was not rejected —
	// including an explicit not-run result for every mutant the run did not
	// execute. That is the contract [mutation.Tally] documents, and [Build]
	// enforces it rather than filling in silence: a forgotten mutant would
	// leave the score's denominator without anybody noticing, which is the one
	// way a mutation score can flatter a test suite.
	Results []MutantResult
	// Rejections are the mutants validation refused. A rejected mutant must not
	// also have a result.
	Rejections []Rejection

	// TestCommand is the argv the run measured, as the user wrote it. Empty
	// falls back to `test.command` from the configuration.
	TestCommand []string
	// Baseline holds every baseline observation, in measurement order.
	Baseline []time.Duration
	// Timeout is the per-mutant timeout, and TimeoutSource says where it came
	// from. An empty source is derived from the configuration: explicit exactly
	// when `test.timeout` is set.
	Timeout       time.Duration
	TimeoutSource TimeoutSource

	// CoverageMode is how coverage narrowed the run. The zero value is
	// [CoverageOff], which is what a run with a custom test command or a failed
	// coverage pass reports.
	CoverageMode CoverageMode
	// CoverageBinaries is how many test binaries the coverage pass profiled. It
	// is recorded only in [CoveragePackage] mode; an `off` run states no number
	// rather than a zero it never measured.
	CoverageBinaries int

	// CacheMode is the mode the outcome cache operated in. The zero value is
	// [CacheOff], which is what a run with the cache configured off, a run that
	// could not open the cache directory, and a run whose `auto` stood down for
	// a custom test command all report.
	CacheMode CacheMode
	// CacheMisses is how many mutants were looked up, not found, and executed.
	// CacheWrites is how many of those outcomes were stored for a later run.
	//
	// The hits are not here: they are counted from the results, so the summary
	// and the rows underneath it cannot disagree. See [Cache].
	CacheMisses int
	CacheWrites int

	// Warnings are the warnings the run published, in publication order.
	Warnings []Warning

	// InfrastructureError says the run hit an infrastructure, configuration,
	// snapshot, or baseline failure. It feeds [mutation.Signals], and a
	// [StatusFailed] report should set it: the policy gates are not consulted
	// at all for a run that cannot be trusted.
	InfrastructureError bool
}

// Build assembles one run report.
//
// The order of work is the order the document has to be believable in: check
// the identity of the run, partition the catalogue into executed and rejected,
// judge the expectations ledger against that partition, and only then count.
// Everything that could disagree is checked rather than assumed — a result for
// a mutant that is not in the catalogue, a mutant claimed twice, a mutant with
// no coordinates — because a report is the artefact every other output is
// derived from, and a report that quietly drops a mutant is worse than no
// report at all.
func Build(opts Options) (*Report, error) {
	if err := checkIdentity(opts); err != nil {
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
		},
		Selection: selection,
		Shard:     shard,
		Test: Test{
			Command:       command,
			Baseline:      baselineOf(opts.Baseline),
			TimeoutMS:     milliseconds(opts.Timeout),
			TimeoutSource: timeoutSource(opts),
		},
		Coverage:     coverage,
		Cache:        cache,
		Summary:      summaryOf(tally, opts.Config.Policy, opts.InfrastructureError, expectations, mutants),
		Mutants:      mutants,
		Rejected:     rejected,
		Skips:        skipsOf(opts.Skips),
		Expectations: expectations,
		Warnings:     warningsOf(opts.Warnings),
	}
	return r, nil
}

// checkIdentity refuses a report that cannot be trusted to name itself: the run
// id that will become a file name, the status, the clock, and the digest that
// will name the history directory.
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

// testCommand resolves the argv the report says the run measured.
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

// index turns the results and rejections into lookups, refusing every way the
// two can contradict each other.
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

// partition walks the catalogue once and splits it into the mutants that were
// executed and the ones validation refused.
//
// Both arrays come out in catalogue order, which is (path, span, rule registry
// position, replacement, id) — a total order that internal/mutation already
// proves is a pure function of the candidate set. Sorting here would be a
// second, weaker opinion about the same question: the specified report order,
// (path, start_byte, rule position), is a prefix of it and has ties that
// catalogue order has already broken.
func partition(opts Options, results map[string]MutantResult, rejections map[string]Rejection) ([]Mutant, []Rejected, error) {
	catalogued := opts.Catalog.Mutants()
	located := locate(opts.Located)

	mutants := make([]Mutant, 0, len(catalogued))
	rejected := make([]Rejected, 0, len(rejections))
	for _, m := range catalogued {
		where, ok := located[locationKey{path: m.Path, span: m.Span, rule: m.Rule.Name}]
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
		mutants = append(mutants, Mutant{
			ID:           m.ID,
			DisplayID:    m.DisplayID,
			Path:         m.Path,
			Package:      where.Package,
			Family:       string(m.Rule.Family),
			Rule:         m.Rule.Name,
			RuleVersion:  m.Rule.Version,
			Line:         where.Line,
			Column:       where.Column,
			StartByte:    m.Span.StartByte,
			EndByte:      m.Span.EndByte,
			Original:     m.Original,
			Replacement:  m.Replacement,
			Branch:       branchOf(where.Branch),
			Outcome:      outcome,
			NotRunReason: reason,
			DurationMS:   milliseconds(result.Duration),
			KilledBy:     text(result.KilledBy),
			// Copied rather than clamped. A negative attempt count is a caller
			// bug, and quietly rewriting it to zero would hide the bug behind a
			// document that looks fine; the schema refuses it, which is where a
			// value this package cannot interpret belongs.
			Attempts:             result.Attempts,
			OutputTail:           text(result.OutputTail),
			CoveringTestPackages: stringList(result.CoveringTestPackages),
			Uncovered:            result.Uncovered,
			Cached:               result.Cached,
		})
	}
	if err := checkAccountedFor(opts, len(mutants), len(rejected)); err != nil {
		return nil, nil, err
	}
	return mutants, rejected, nil
}

// notRunReasonOf checks one result's outcome against its not-run reason and
// renders the reason for the document.
//
// The pairing is a biconditional and both halves are refused rather than
// repaired. A not-run mutant with no reason would put the one outcome a reader
// cannot act on into the document with nothing to act on it by — and the
// commonest way to produce one is a new code path that forgot the field, which
// is exactly what this catches. A measured mutant carrying a reason is the
// mirror image: a document explaining why it did not do something it did.
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

// joinReasons lists the not-run reasons for a message, so that the list in an
// error and the enum in the schema cannot drift apart.
func joinReasons() string {
	names := make([]string, 0, len(NotRunReasons()))
	for _, reason := range NotRunReasons() {
		names = append(names, string(reason))
	}
	return strings.Join(names, ", ")
}

// checkAccountedFor proves that every result and every rejection was consumed
// by the catalogue walk.
//
// It is a counting argument rather than a second lookup loop: the walk consumed
// one distinct id per row it produced, the maps hold distinct ids, so equal
// counts mean equal sets. What it catches is a result or a rejection naming a
// mutant the catalogue does not have — which means two phases are looking at
// different catalogues, and everything downstream of that is fiction.
func checkAccountedFor(opts Options, mutants, rejected int) error {
	if len(opts.Results) != mutants {
		return unknownMutant("result", opts.Results, func(r MutantResult) string { return r.ID }, opts.Catalog)
	}
	if len(opts.Rejections) != rejected {
		return unknownMutant("rejection", opts.Rejections, func(r Rejection) string { return r.ID }, opts.Catalog)
	}
	return nil
}

// unknownMutant names the first row whose id the catalogue does not know. The
// rows are already known to be distinct, so there is one.
func unknownMutant[T any](kind string, rows []T, id func(T) string, catalog *mutation.Catalog) error {
	for _, row := range rows {
		if _, known := catalog.ByID(id(row)); !known {
			return &Error{
				Code: CodeUnknownMutant,
				Message: fmt.Sprintf("the %s for mutant %s names an id that is not in this run's catalogue",
					kind, display(id(row))),
			}
		}
	}
	// Unreachable: the counts only disagree when a row was not consumed, and a
	// row is consumed exactly when its id is catalogued. Reported rather than
	// returned as nil, because a nil here would silently produce a report that
	// has lost a mutant.
	return &Error{
		Code:    CodeUnknownMutant,
		Message: "internal error: a " + kind + " could not be matched to the catalogue",
	}
}

// locationKey identifies a candidate by everything the catalogue keeps, which
// is what lets a catalogued mutant be joined back to the coordinates discovery
// found it at. It is the same join internal/cli makes for `list --json`.
type locationKey struct {
	path string
	span mutation.Span
	rule string
}

// branchOf converts discovery's branch proof into the document's. Nil stays
// nil, which is what keeps the property absent rather than null.
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

// locate indexes discovery's candidates by that key, keeping the first of any
// duplicates: two candidates with the same key are the same edit, so they are
// at the same line and column by construction.
func locate(candidates []discover.Located) map[locationKey]discover.Located {
	out := make(map[locationKey]discover.Located, len(candidates))
	for _, candidate := range candidates {
		key := locationKey{path: candidate.Path, span: candidate.Span, rule: candidate.Rule.Name}
		if _, seen := out[key]; !seen {
			out[key] = candidate
		}
	}
	return out
}

// dispositions is what the expectations ledger is judged against: every id the
// catalogue holds, and what this run found out about it.
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

// tallyOf counts the executed mutants, with the expectations ledger deciding
// which survivors are expected.
//
// The counting is [mutation.Tally]'s, not this package's. A second
// implementation of "what counts as a detection" is exactly how a report and a
// console end up disagreeing about a number the user is looking at in both.
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

// selectionOf assembles the selection block and checks its arithmetic.
//
// Two consistency rules are enforced here rather than left to the schema, which
// cannot express either. A [ModeChanged] run must name the ref it diffed
// against, because the mode without the ref is a document saying it looked at a
// diff without saying which one; and a sharded run must report [ModeShard],
// because a document carrying a `shard` block and claiming to have run
// everything would be two contradictory statements about the same run.
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

// joinModes lists the selection modes for a message, so that the list in an
// error and the enum in the schema cannot drift apart.
func joinModes() string {
	names := make([]string, 0, len(SelectionModes()))
	for _, mode := range SelectionModes() {
		names = append(names, string(mode))
	}
	return strings.Join(names, ", ")
}

// shardOf assembles the shard block, filling in the assignment function this
// build implements and refusing a shard it could not honestly describe.
//
// The assignment is defaulted rather than demanded, because there is exactly
// one and a caller spelling it out again is a second place for it to be wrong.
// A caller that names a different one is refused rather than corrected: the
// value is a promise that a consumer can recompute the partition, and quietly
// rewriting it would turn a caller's mistake into a document that lies.
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

// coverageOf assembles the coverage block and checks that the mutants agree
// with it.
//
// Two things are checked rather than trusted, and both are conditions a
// document must never be able to state. An uncovered mutant that is not a
// survivor with zero attempts would be claiming a measurement the run refused
// to make — the whole point of `uncovered` is that nothing was executed — and
// an uncovered mutant in a run with coverage off would be claiming a fact
// nobody went looking for. Either is a caller bug, and a report is the artefact
// every other output is derived from: it is worth failing at the last step
// rather than publishing a document that quietly contradicts itself.
//
// `mutants_uncovered` is counted here from the rows above rather than passed
// in, so the number in the summary and the rows a reader would count by hand
// are the same number by construction.
func coverageOf(opts Options, mutants []Mutant) (Coverage, error) {
	return coverageBlock(opts.CoverageMode, opts.CoverageBinaries, mutants)
}

// coverageBlock is [coverageOf] over the two values rather than over the whole
// options struct, so that `report merge` — which has rows and no options — can
// recount `mutants_uncovered` through this one implementation instead of
// growing a second opinion about what an uncovered mutant is.
func coverageBlock(mode CoverageMode, binaryCount int, mutants []Mutant) (Coverage, error) {
	stated := mode
	if mode == "" {
		mode = CoverageOff
	}
	if !mode.Valid() {
		return Coverage{}, &Error{
			Code:    CodeInvalidCoverage,
			Message: fmt.Sprintf("%q is not a coverage mode: expected off or package", string(stated)),
		}
	}

	uncovered := 0
	for _, m := range mutants {
		if !m.Uncovered {
			continue
		}
		switch {
		case mode != CoveragePackage:
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

	coverage := Coverage{Mode: mode}
	if mode != CoveragePackage {
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
	return coverage, nil
}

// cacheBlock assembles the cache block and checks that the mutants agree with
// it.
//
// It is written over the three values rather than over the whole options
// struct, exactly as [coverageBlock] is and for the same reason: `report merge`
// has rows and no options, and one implementation of "what a cache hit is" is
// the only way the merged document and the shard documents can be counted the
// same way.
//
// Four things are refused rather than trusted, and every one of them is a
// statement a document must never be able to make. A cached mutant carrying an
// outcome the cache will not store — inconclusive, errored, not-run — would be
// claiming an entry that cannot exist. A cached mutant that is also uncovered
// would be claiming a measurement adopted for a mutant nothing ever executed.
// A run with the cache off that reports a hit would be contradicting itself in
// two adjacent lines. And more writes than misses would mean the run stored an
// outcome it did not measure.
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

// reusable reports whether an outcome is one the cache stores, in this
// document's spelling.
//
// It is deliberately a small duplicate of internal/cache's own rule rather than
// a call into it: this package is the document, and a document validator that
// imported the store it is validating would run the dependency the wrong way
// round — internal/cache already reads [Mutant] the other way. The package
// tests hold the two lists together.
func reusable(o Outcome) bool {
	switch o {
	case OutcomeKilled, OutcomeSurvived, OutcomeTimedOut:
		return true
	default:
		return false
	}
}

// countNoun renders "1 attempt" or "3 attempts".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// summaryOf counts the run and asks the policy what it makes of it.
//
// The verdict is computed here rather than passed in so that the number in the
// document and the gate that read it cannot disagree: [mutation.Decide] is
// asked about this report's own tally. Only the first failure is named — see
// [PolicyResult] for why nothing is lost by that.
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

// expectationFailure is [Report.ExpectationFailure] over the pieces, before
// there is a report to ask. The two must answer alike, which the package tests
// hold in place.
func expectationFailure(expectations []Expectation, mutants []Mutant) bool {
	r := Report{Expectations: expectations, Mutants: mutants}
	return r.ExpectationFailure()
}

// baselineOf renders the baseline observations.
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

// skipsOf renders discovery's skips, sorted by (path, reason).
//
// The order is imposed here rather than borrowed from discovery. Discovery does
// sort its skips, but a report that depends on somebody else's ordering promise
// is a report whose determinism is somebody else's business to keep.
//
// Reasons are copied through verbatim rather than checked against the schema's
// enumeration. A reason discovery emits and this schema has not heard of is a
// documentation bug to fix in the same commit as the reason, not grounds for
// failing a run at the very end of it; the package tests assert that every
// reason either package defines is in the schema, which is where that drift
// gets caught.
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

// warningsOf copies the warnings, keeping publication order: a warning about
// the workspace that was published before the baseline ran belongs above one
// about the baseline, and sorting them would destroy the only ordering they
// have.
func warningsOf(warnings []Warning) []Warning {
	out := make([]Warning, 0, len(warnings))
	return append(out, warnings...)
}

// platformOf fills in the running host when the caller named none.
func platformOf(p Platform) Platform {
	if p.OS == "" {
		p.OS = runtime.GOOS
	}
	if p.Arch == "" {
		p.Arch = runtime.GOARCH
	}
	return p
}

// timeoutSource resolves where the timeout came from, deriving it from the
// configuration when the caller did not say.
//
// The fallback reads `test.timeout` rather than defaulting to "derived",
// because a report that labelled a configured timeout as derived would be
// wrong in exactly the case a reader is investigating: why every mutant timed
// out.
func timeoutSource(opts Options) TimeoutSource {
	if opts.TimeoutSource.Valid() {
		return opts.TimeoutSource
	}
	if opts.Config.Test.Timeout > 0 {
		return TimeoutExplicit
	}
	return TimeoutDerived
}

// duplicate builds the error for one mutant claimed twice.
func duplicate(kind, id string) error {
	return &Error{
		Code:    CodeDuplicateEntry,
		Message: fmt.Sprintf("mutant %s has more than one %s", display(id), kind),
	}
}

// display shortens an id for a message, and leaves a short or malformed one
// alone rather than slicing past its end.
func display(id string) string {
	if len(id) <= mutation.DisplayIDLength {
		return id
	}
	return id[:mutation.DisplayIDLength]
}

// stringList returns a non-nil copy of a list. Every array in the document is a
// list that may legitimately be empty, and an empty list is `[]`, never `null`.
func stringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return slices.Clone(values)
}

// or returns value, or fallback when value is empty.
func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
