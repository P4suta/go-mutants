// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

// The identity of the document this package writes.
//
// Both constants are spelled out here rather than imported from
// internal/schemas, which this package does not import: the dependency would
// run from the document to its validator, when the validator is what checks the
// document. (`report validate` does link the validator in, from internal/cli,
// which is where a command that validates a file belongs.) The package tests
// assert that a document carrying these values is the document
// internal/schemas validates, so the two cannot drift apart without a test
// failing.
const (
	// DocumentType is the `document_type` every run report carries.
	DocumentType = "go-mutants/run-report"
	// SchemaVersion is the `schema_version` this build writes and reads.
	SchemaVersion = 1
)

// runIDPattern is the shape of a run id, and the same expression the schema
// states. It is enforced when a report is built rather than only when one is
// validated, because the id becomes a file name; see [CodeInvalidRunID].
var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`)

// digestPattern is a lowercase hex SHA-256.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// A Status is how a run ended.
//
// The three values are this document's own, not internal/engine's: a report is
// written by the engine, so the engine imports this package and never the other
// way round. The mapping is one switch in the engine, and the spellings differ
// on purpose — "ok" is a fine thing for an event stream to say and a poor thing
// for a permanent record, which should say what it means.
type Status string

// The v1 statuses.
const (
	// StatusCompleted means the run did everything it set out to do. It says
	// nothing about the score: a completed run may still have failed a policy
	// gate, which is what summary.policy.failure is for.
	StatusCompleted Status = "completed"
	// StatusInterrupted means a signal ended the run early and this is the
	// partial record of it. Everything not executed is a not-run mutant.
	StatusInterrupted Status = "interrupted"
	// StatusFailed means the run stopped on an error, and the counts describe
	// only what was established before it.
	StatusFailed Status = "failed"
)

// Statuses returns every status in document order.
func Statuses() []Status { return []Status{StatusCompleted, StatusInterrupted, StatusFailed} }

// Valid reports whether s is one of the defined statuses.
func (s Status) Valid() bool { return slices.Contains(Statuses(), s) }

// String returns the status as it appears in the document.
func (s Status) String() string { return string(s) }

// A SelectionMode says how the run chose what to execute.
//
// It is a label on the selection block and not the whole of it: a shard of a
// changed run narrows twice, and says so by reporting [ModeShard] with
// [Selection.ChangedRef] filled in. Nothing branches on the mode that could not
// equally read `shard` and `changed_ref`, which is why the two facts are
// recorded separately rather than folded into a mode per combination.
type SelectionMode string

// The v1 selection modes.
const (
	// ModeAll runs every catalogued mutant the profile and the patterns
	// selected.
	ModeAll SelectionMode = "all"
	// ModeMutant runs the single mutant `--mutant` named. Everything else is
	// catalogued and reported as not-run, which is what keeps the score and
	// `policy.require_mutants` honest about the difference between "nothing to
	// find" and "not looked at this time".
	ModeMutant SelectionMode = "mutant"
	// ModeChanged runs the mutants whose lines the diff against
	// [Selection.ChangedRef] touches. Discovery and validation still cover the
	// whole module, so the ids and the rejections are those of a full run and
	// the two are comparable.
	ModeChanged SelectionMode = "changed"
	// ModeShard runs the mutants this shard owns; see [Shard]. It outranks
	// [ModeChanged] as a label because the shard is the outer partition — a
	// changed run split into shards is still one shard of it — and the diff
	// half is not lost, because `changed_ref` is recorded whatever the mode is.
	ModeShard SelectionMode = "shard"
)

// SelectionModes returns every mode in document order.
func SelectionModes() []SelectionMode {
	return []SelectionMode{ModeAll, ModeMutant, ModeChanged, ModeShard}
}

// Valid reports whether m is one of the defined modes.
func (m SelectionMode) Valid() bool { return slices.Contains(SelectionModes(), m) }

// String returns the mode as it appears in the document.
func (m SelectionMode) String() string { return string(m) }

// A TimeoutSource says where the per-mutant timeout came from.
type TimeoutSource string

// The v1 timeout sources.
const (
	// TimeoutDerived is max(10s, slowest baseline × 5).
	TimeoutDerived TimeoutSource = "derived"
	// TimeoutExplicit is the configured `test.timeout` or `--timeout`.
	TimeoutExplicit TimeoutSource = "explicit"
)

// Valid reports whether s is one of the defined sources.
func (s TimeoutSource) Valid() bool {
	return s == TimeoutDerived || s == TimeoutExplicit
}

// A MemorySource says where the per-mutant memory bound came from, or that
// there was none.
//
// It is [TimeoutSource] with one value the clock does not need. A deadline can
// always be enforced; a memory bound cannot, because watching a process tree
// while it runs is not something every supported platform offers, and a
// document has to be able to say "no bound" rather than write a number nothing
// applied.
type MemorySource string

// The v1 memory sources.
const (
	// MemoryDerived is max(1 GiB, largest baseline peak × 4).
	MemoryDerived MemorySource = "derived"
	// MemoryExplicit is the configured `test.memory` or `--memory`. It is
	// recorded even where the platform cannot enforce it, because it is what
	// the user asked for; the run says so in a warning.
	MemoryExplicit MemorySource = "explicit"
	// MemoryUnavailable is no bound at all: nothing measured a peak to derive
	// one from, or this platform cannot enforce one and none was configured.
	MemoryUnavailable MemorySource = "unavailable"
)

// Valid reports whether s is one of the defined sources.
func (s MemorySource) Valid() bool {
	return s == MemoryDerived || s == MemoryExplicit || s == MemoryUnavailable
}

// A CoverageMode says how coverage narrowed the run.
type CoverageMode string

// The v1 coverage modes.
const (
	// CoverageOff means every selected mutant was executed against every test
	// binary. It is what a run with a custom `test.command` does, and what a run
	// whose coverage pass failed falls back to.
	CoverageOff CoverageMode = "off"
	// CoveragePackage means the run profiled each test binary once and executed
	// every mutant only against the binaries whose profile shows a covered
	// statement on the mutant's lines. Mutants no binary covers were not
	// executed at all and carry `uncovered`.
	CoveragePackage CoverageMode = "package"
	// CoverageTest means the run profiled every test of every test binary on
	// its own and executed every mutant only against the tests whose profile
	// shows a covered statement on the mutant's lines, each binary started
	// with those tests selected. Mutants no test covers were not executed at
	// all and carry `uncovered`, exactly as in [CoveragePackage].
	CoverageTest CoverageMode = "test"
)

// CoverageModes returns every mode in document order.
func CoverageModes() []CoverageMode {
	return []CoverageMode{CoverageOff, CoveragePackage, CoverageTest}
}

// Valid reports whether m is one of the defined modes.
func (m CoverageMode) Valid() bool { return slices.Contains(CoverageModes(), m) }

// Narrowed reports whether coverage decided what each mutant was measured
// against — which is what licenses a mutant to be `uncovered`, and what makes
// `binaries` a number the run measured rather than one it never took.
func (m CoverageMode) Narrowed() bool { return m == CoveragePackage || m == CoverageTest }

// A TestRef names one test of one test binary: the import path of the package
// the binary was built from, and the top-level test name as `-test.list`
// prints it. It is the unit a test-narrowed run measures with.
type TestRef struct {
	Package string `json:"package"`
	Name    string `json:"name"`
}

// String returns the mode as it appears in the document.
func (m CoverageMode) String() string { return string(m) }

// A CacheMode says whether this run reused outcomes it had proven before.
//
// It is what the run *did*, not what was configured. The configured
// `cache.mode` has three values and the third one is a question rather than an
// answer: `auto` resolves to on or off before any mutant is executed — off for
// a test command go-mutants cannot reason about, off again when the cache
// directory cannot be opened — and this field records what it resolved to.
// Recording "auto" instead would put the one value a reader cannot act on into
// a document whose whole job is to say what happened; a run that stood down
// says so here and says why in `warnings[]`.
type CacheMode string

// The v1 cache modes.
const (
	// CacheOff means no outcome was read and none was stored. Its counters are
	// all zero, which is the difference between a cache that was off and one
	// that was on and cold.
	CacheOff CacheMode = "off"
	// CacheOn means the run looked every executable mutant up and stored every
	// reusable outcome it measured.
	CacheOn CacheMode = "on"
)

// CacheModes returns every mode in document order.
func CacheModes() []CacheMode { return []CacheMode{CacheOff, CacheOn} }

// Valid reports whether m is one of the defined modes.
func (m CacheMode) Valid() bool { return slices.Contains(CacheModes(), m) }

// String returns the mode as it appears in the document.
func (m CacheMode) String() string { return string(m) }

// An Outcome is what happened to one mutant, in this document's spelling.
//
// It is a separate type from [mutation.Outcome], which is the same six facts
// under different names: the core type is the one the outcome cache and the
// console speak, and it renders "timed_out" and "not_run" with underscores.
// This document's enum is hyphenated, matching the house style of the sibling
// projects' reports and of every other enumerated value here. The keys of the
// summary object stay snake_case — `timed_out`, `not_run` — because they are
// field names rather than enumerated values, and the two conventions must not
// be unified by anybody tidying up: the values are a published enum, and
// renaming one is a breaking change to somebody's jq expression.
type Outcome string

// The v1 outcomes.
const (
	// OutcomeKilled means at least one test failed with the mutant active.
	OutcomeKilled Outcome = "killed"
	// OutcomeSurvived means the whole test suite passed with the mutant active.
	OutcomeSurvived Outcome = "survived"
	// OutcomeTimedOut means a confirmed timeout: it timed out, was retried
	// serially, and timed out again. It counts as a detection.
	OutcomeTimedOut Outcome = "timed-out"
	// OutcomeInconclusive means the run could not decide. Excluded from the
	// score in both directions.
	OutcomeInconclusive Outcome = "inconclusive"
	// OutcomeErrored means the harness itself failed for this mutant.
	OutcomeErrored Outcome = "errored"
	// OutcomeNotRun means the mutant was never executed.
	OutcomeNotRun Outcome = "not-run"
)

// outcomeNames maps the core outcome onto this document's spelling. It is the
// single place the two vocabularies meet, and it is total: a core outcome with
// no entry here is a bug that [OutcomeOf] reports rather than renders.
var outcomeNames = map[mutation.Outcome]Outcome{
	mutation.OutcomeKilled:       OutcomeKilled,
	mutation.OutcomeSurvived:     OutcomeSurvived,
	mutation.OutcomeTimedOut:     OutcomeTimedOut,
	mutation.OutcomeInconclusive: OutcomeInconclusive,
	mutation.OutcomeErrored:      OutcomeErrored,
	mutation.OutcomeNotRun:       OutcomeNotRun,
}

// Observations returns the outcomes one pass over the test binaries can
// observe.
//
// It is the six minus two, and the two are the ones that are judgements rather
// than observations: [OutcomeInconclusive] is what a timeout and a retry that
// disagree come to, and [OutcomeNotRun] is what a mutant nobody measured is.
// Neither is a thing a single pass can see, so neither may appear in an
// [Execution]; the schema's execution rows enumerate exactly this list.
func Observations() []Outcome {
	return []Outcome{OutcomeKilled, OutcomeSurvived, OutcomeTimedOut, OutcomeErrored}
}

// Observed reports whether o is something one pass can have observed.
func (o Outcome) Observed() bool { return slices.Contains(Observations(), o) }

// OutcomeOf renders a core outcome in this document's spelling.
func OutcomeOf(o mutation.Outcome) (Outcome, error) {
	name, ok := outcomeNames[o]
	if !ok {
		return "", &Error{
			Code:    CodeInvalidOutcome,
			Message: fmt.Sprintf("%q is not an outcome this report can write", o),
		}
	}
	return name, nil
}

// Mutation resolves a document outcome back to the core one, so that a report
// read from disk can be counted with the same tally the run used.
func (o Outcome) Mutation() (mutation.Outcome, error) {
	for core, name := range outcomeNames {
		if name == o {
			return core, nil
		}
	}
	return mutation.OutcomeNotRun, &Error{
		Code:    CodeInvalidOutcome,
		Message: fmt.Sprintf("%q is not an outcome this report can read", string(o)),
	}
}

// String returns the outcome as it appears in the document.
func (o Outcome) String() string { return string(o) }

// A NotRunReason says why a mutant carries [OutcomeNotRun].
//
// The enum exists because "not run" on its own is the one outcome a reader
// cannot act on: a mutant nobody selected, a mutant another shard measured, and
// a mutant a signal cut the run short of are three different facts, and only
// the last is a reason to run anything again. It is written exactly when the
// outcome is not-run and null otherwise, which [Build] enforces in both
// directions.
type NotRunReason string

// The v1 not-run reasons.
const (
	// NotRunInterrupted means the mutant was selected and the run ended before
	// it was measured. It is also what a mutant that was started and could not
	// be finished carries, which is why it can arrive with attempts already
	// recorded.
	NotRunInterrupted NotRunReason = "interrupted"
	// NotRunOutOfSelection means the run narrowed itself and this mutant was
	// outside the narrowing: `--mutant` named another one, or `--changed` found
	// no edited line on it.
	NotRunOutOfSelection NotRunReason = "out-of-selection"
	// NotRunOtherShard means `--shard` assigned the mutant to a different
	// shard, which is the run that measured it. `report merge` replaces exactly
	// these rows with the owning shard's.
	NotRunOtherShard NotRunReason = "other-shard"
)

// NotRunReasons returns every reason in document order.
func NotRunReasons() []NotRunReason {
	return []NotRunReason{NotRunInterrupted, NotRunOutOfSelection, NotRunOtherShard}
}

// Valid reports whether r is one of the defined reasons.
func (r NotRunReason) Valid() bool { return slices.Contains(NotRunReasons(), r) }

// String returns the reason as it appears in the document.
func (r NotRunReason) String() string { return string(r) }

// An ExpectationState is what one row of the `[[mutation.expect]]` ledger
// turned out to be worth.
type ExpectationState string

// The v1 expectation states. [StateOf] is the whole state machine.
const (
	// StateFulfilled means the mutant survived, as the ledger predicted.
	StateFulfilled ExpectationState = "fulfilled"
	// StateUnfulfilled means the run did not observe the predicted survival:
	// the tests caught the mutant, or it was never measured. The document says
	// which by carrying the mutant itself; see [StateOf].
	StateUnfulfilled ExpectationState = "unfulfilled"
	// StateStale means the id is not in this catalogue any more. The code it
	// described has changed, and the ledger row now documents nothing.
	StateStale ExpectationState = "stale"
)

// String returns the state as it appears in the document.
func (s ExpectationState) String() string { return string(s) }

// A StageResult is what became of one timed step of the run.
//
// The three values are the recording's own — [trace.ResultSucceeded] and its
// two siblings, referenced rather than retyped — and that is the whole point of
// the type. A run's stages are published twice, once as `stage` events for a
// reader following the run and once in the timing block of the report for a
// reader who has only the file, and a stage that `succeeded` in one document
// and was `ok` in the other would make a consumer joining them learn a mapping
// that exists for no reason. [StageResults] and the schema's enumeration are
// held to the trace's list by the package tests.
type StageResult string

// The v1 stage results.
const (
	// StageSucceeded means the step did what it was there for.
	StageSucceeded StageResult = trace.ResultSucceeded
	// StageFailed means it did not, which is not by itself a failed run: a
	// coverage-instrumented build that will not compile is a failed stage and a
	// run that carries on without coverage.
	StageFailed StageResult = trace.ResultFailed
	// StageSkipped means the step was not attempted. A skipped step is still a
	// row, which is what lets a reader tell an intentional omission from a
	// measurement that was lost.
	StageSkipped StageResult = trace.ResultSkipped
)

// StageResults returns every result in document order.
func StageResults() []StageResult {
	return []StageResult{StageSucceeded, StageFailed, StageSkipped}
}

// Valid reports whether r is one of the defined results.
func (r StageResult) Valid() bool { return slices.Contains(StageResults(), r) }

// String returns the result as it appears in the document.
func (r StageResult) String() string { return string(r) }

// A Report is one run, losslessly.
//
// The field order is the document's order, and the JSON encoder writes struct
// fields in declaration order, so the shape of the file is decided here and
// nowhere else. Every slice is non-nil in a built report: an empty list is
// `[]`, never `null`, because "no warnings" and "warnings unknown" are not the
// same statement and only one of them is ever true.
type Report struct {
	DocumentType  string    `json:"document_type"`
	SchemaVersion int       `json:"schema_version"`
	ToolVersion   string    `json:"tool_version"`
	RunID         string    `json:"run_id"`
	Status        Status    `json:"status"`
	StartedAt     string    `json:"started_at"`
	FinishedAt    string    `json:"finished_at"`
	DurationMS    int64     `json:"duration_ms"`
	Workspace     Workspace `json:"workspace"`
	Selection     Selection `json:"selection"`
	// Shard is which shard of a split run this document is, and nil — written
	// as null — when the run was not split. It is always present, because "this
	// run was not sharded" and "this document does not say" are different
	// statements and only one of them is ever true of a document go-mutants
	// wrote.
	Shard *Shard `json:"shard"`
	// Merge is present only on a document `report merge` produced, and is
	// absent — not null — from every document a run wrote. Absence is the
	// discriminator the schema keys its shard-must-be-null rule off, and a
	// merged document is a different kind of thing rather than a run with a
	// field filled in.
	Merge        *Merge        `json:"merge,omitempty"`
	Test         Test          `json:"test"`
	Coverage     Coverage      `json:"coverage"`
	Cache        Cache         `json:"cache"`
	Summary      Summary       `json:"summary"`
	Mutants      []Mutant      `json:"mutants"`
	Rejected     []Rejected    `json:"rejected"`
	Skips        []Skip        `json:"skips"`
	Expectations []Expectation `json:"expectations"`
	Warnings     []Warning     `json:"warnings"`
	// Timing is where this run's wall-clock time went, and Validation is what
	// establishing which mutants compile cost it. Both are omitted from a
	// merged document and from one an older build wrote; see [Timing].
	Timing     *Timing     `json:"timing,omitempty"`
	Validation *Validation `json:"validation,omitempty"`
}

// Timing is where a run's wall-clock time went, phase by phase and stage by
// stage.
//
// It is the answer to "why was this slow" for a reader who has the report and
// no recording, which is nearly every reader: a trace is opt-in, kept for ten
// runs, and thrown away, while the report is the permanent record. The two say
// the same thing in the same words — see [StageResult] — so a reader who has
// both is not reconciling two accounts.
//
// Both lists are in the order the spans closed, which for the engine's linear
// pipeline is the order they opened, so a consumer can draw the run as a
// timeline without sorting it. The phases do not add up to `duration_ms`: a run
// is more than its phases, and a report that made the arithmetic work by
// inventing a remainder would be describing a phase nobody entered.
//
// It is absent from a merged document. Four shards are four runs on four
// machines, so there is no single timeline to report, and quoting the first
// shard's would be presenting one machine's clock as the run's.
type Timing struct {
	Phases []PhaseTiming `json:"phases"`
	Stages []StageTiming `json:"stages"`
}

// A PhaseTiming is how long one phase of the run took.
type PhaseTiming struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
}

// A StageTiming is how long one step inside a phase took, and what became of
// it.
//
// The phase is carried on every row because a stage name is unique only within
// its phase — `build` happens in the baseline and again in the report — and a
// consumer that keyed a timeline by name alone would add two unrelated numbers
// together.
type StageTiming struct {
	Phase      string      `json:"phase"`
	Name       string      `json:"name"`
	DurationMS int64       `json:"duration_ms"`
	Result     StageResult `json:"result"`
}

// Validation is what proving which catalogued mutants compile cost the run.
//
// One build means the whole catalogue compiled on the first try, which is the
// ordinary case; anything more is a bisection, and is where a slow run's
// minutes went. It is a fact about one run's work, so a merged document omits
// it for the reason it omits [Timing].
type Validation struct {
	Builds int `json:"builds"`
}

// SnapshotFacts is what the disposable copy of the workspace turned out to be.
//
// Neither field is about the code, which is why neither is in the workspace
// digest: a run whose copy could not take the tree's own derived name pays for
// a cold Go build cache, and that is a fact about the machine's temporary
// directory rather than about the program under test. It is exactly the fact a
// reader comparing two runs of one tree needs when one of them took twice as
// long.
type SnapshotFacts struct {
	// StableDir says the copy carried the source tree's own derived name
	// rather than a random one, which is what lets two runs of one workspace
	// share a Go build cache.
	StableDir bool `json:"stable_dir"`
	// Files is how many regular files were copied.
	Files int `json:"files"`
}

// ToolchainFacts is the Go toolchain that ran the tests.
//
// It is not `workspace.go_version`, and the difference is the point: that field
// is the `go` directive of the module under test, while this is the executable
// that compiled and ran it. A run under a toolchain manager can have the two
// disagree, and "which go was this?" is unanswerable from the module's
// directive alone.
type ToolchainFacts struct {
	// GoBin is the resolved absolute path of the go executable, so that a
	// later invocation cannot be re-resolved into a different toolchain by a
	// changed PATH.
	GoBin string `json:"go_bin"`
	// Version is what `go version` printed, verbatim.
	Version string `json:"version"`
}

// An Execution is one pass this run made over the test binaries with one mutant
// active.
//
// It is the same type on both sides of [Build]: the caller hands over the rows
// the document will carry, because there is nothing to translate — an execution
// is the same handful of facts whether it is being reported or being written
// down. What the document adds is the refusals; see [Options.Results].
//
// An execution is not a verdict. A single [OutcomeTimedOut] row is not a
// confirmed timeout, and no row is ever [OutcomeInconclusive], which is a
// judgement about two attempts rather than an observation of one: the mutant's
// own `outcome` is where the verdict is.
type Execution struct {
	// Attempt is which pass this was, counting from one. The rows are in
	// attempt order, so it is redundant with the index and written anyway: a
	// consumer filtering the rows keeps the numbering.
	Attempt int `json:"attempt"`
	// Worker is the scheduler slot that made the pass, counting from zero. The
	// serial retry pass is worker 0, which it has to itself.
	Worker int `json:"worker"`
	// Outcome is what this pass observed.
	Outcome Outcome `json:"outcome"`
	// KilledBy is the import path of the test binary that detected the mutant —
	// the one that failed, or the one it hung. It is omitted for a pass that
	// detected nothing, rather than written as a name that is not a name.
	KilledBy string `json:"killed_by,omitzero"`
	// DurationMS is the wall-clock time this pass took, summed over the
	// binaries it actually ran.
	DurationMS int64 `json:"duration_ms"`
	// Binaries are the test binaries this pass started, in launch order, by
	// import path. The list stops where the pass stopped: a mutant killed by
	// the second of three binaries was measured against two, and naming all
	// three would describe a measurement nobody made.
	Binaries []string `json:"binaries"`
	// Tests are the tests this pass was narrowed to, sorted by package and
	// then by name — the binary in Binaries was started with exactly these
	// selected. Absent when every binary ran whole, which is every pass of a
	// run that is not in [CoverageTest] mode.
	Tests []TestRef `json:"tests,omitzero"`
	// MemoryExceeded reports that this pass was stopped by the run's per-mutant
	// memory bound rather than by a test failing or by the deadline.
	//
	// It is why a row can say `killed` and name a binary that reported no
	// failure. The outcome vocabulary is frozen and a bound is not a new kind of
	// verdict — the original program was measured under the budget the bound was
	// derived from, so a tree that needs several times what the whole suite
	// needed has been changed observably, which is what a kill means — so the
	// fact that tells this kill apart from an assertion's travels beside the
	// outcome rather than inside it.
	//
	// It is optional and absent when false, so a document written before the
	// bound existed is still a document this build reads.
	MemoryExceeded bool `json:"memory_exceeded,omitzero"`
	// PeakMemoryBytes is the highest memory any binary of this pass was
	// observed to hold.
	//
	// It is the maximum over every binary the pass started rather than the
	// deciding binary's: a pass's cost is the worst moment it put the machine
	// through, and the binary that settled it need not be the one that cost the
	// most. It is the resident set on Unix and the job's committed charge on
	// Windows, which are close for a Go program and never equal, and neither is
	// converted into the other.
	//
	// It is recorded for every pass and not only for the bounded ones, because
	// the question it answers — which mutants cost the machine most — is asked
	// after the run and cannot be asked of a run that measured only what it
	// bounded. It is absent where the platform could not say, which is a
	// different statement from a peak of zero and is why zero is omitted rather
	// than written.
	PeakMemoryBytes int64 `json:"peak_memory_bytes,omitzero"`
}

// Workspace names the tree the run read.
type Workspace struct {
	ModulePath      string   `json:"module_path"`
	GoVersion       string   `json:"go_version"`
	WorkspaceDigest string   `json:"workspace_digest"`
	Platform        Platform `json:"platform"`
	// Snapshot is what the disposable copy of that tree turned out to be, or
	// nil for a document that does not say — a merged one, or one an older
	// build wrote. See [SnapshotFacts].
	Snapshot *SnapshotFacts `json:"snapshot,omitempty"`
}

// Platform is the host the run happened on. Build constraints decide which
// files a package even has, so a report is a statement about one platform.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Selection is what was asked for, and the arithmetic of what it produced.
type Selection struct {
	Mode SelectionMode `json:"mode"`
	// ChangedRef is the git ref the changed-line set was taken against — the
	// merge base of it and HEAD is what was diffed — or nil for a run that did
	// not narrow by a diff.
	//
	// It is independent of Mode rather than implied by it, because a shard of a
	// changed run narrows twice and reports [ModeShard]; the ref is how the
	// document still says which diff it was.
	ChangedRef *string  `json:"changed_ref"`
	Profile    string   `json:"profile"`
	Operators  []string `json:"operators"`
	Include    []string `json:"include"`
	Exclude    []string `json:"exclude"`
	Candidates int      `json:"candidates"`
	Rejected   int      `json:"rejected"`
	Selected   int      `json:"selected"`
}

// A Shard is which part of a split run one document describes.
//
// Every shard runs the whole of discovery and validation and executes only the
// mutants it owns, so the ids, the rejections, and the skips are identical
// across the set and the documents are directly comparable — which is what
// `report merge` checks before it combines them. Everything a shard does not
// own is reported as not-run with [NotRunOtherShard], so each document is a
// complete statement about the catalogue rather than a fragment of one.
type Shard struct {
	// Index is 1-based and never exceeds Total.
	Index int `json:"index"`
	// Total is how many shards the run was split into.
	Total int `json:"total"`
	// Assignment names the function that decides which shard owns a mutant, so
	// that a consumer can recompute the partition rather than trust it. It is
	// [mutation.ShardAssignment]; [Build] refuses any other value.
	Assignment string `json:"assignment"`
}

// Owns reports whether this shard is the one that should have executed the
// mutant with the given id.
func (s Shard) Owns(id string) bool {
	return mutation.ShardIndex(id, s.Total) == s.Index
}

// A Merge records that a document is the union of several shard reports rather
// than the record of one run.
type Merge struct {
	// Shards is how many shard documents were merged. Every index from 1 to
	// Shards was present exactly once, which is what makes the union a complete
	// run rather than a partial one.
	Shards int `json:"shards"`
}

// Test is how the project's tests were run and measured.
type Test struct {
	Command       []string      `json:"command"`
	Baseline      Baseline      `json:"baseline"`
	TimeoutMS     int64         `json:"timeout_ms"`
	TimeoutSource TimeoutSource `json:"timeout_source"`
	// MemoryBytes is the per-mutant memory bound and MemorySource says where it
	// came from. They are the timeout's twins and are written together: a
	// document carrying one without the other would be a budget nobody could
	// interpret.
	//
	// Both are optional, because a document written before the bound existed
	// has neither, and MemoryBytes is absent for a run that bounded nothing —
	// which is what [MemoryUnavailable] says in words. A run whose platform
	// cannot enforce a bound still records an explicit one: it is what the user
	// asked for, and the run's warnings say it was not held to.
	MemoryBytes  int64        `json:"memory_bytes,omitzero"`
	MemorySource MemorySource `json:"memory_source,omitzero"`
	// Toolchain is the Go toolchain that ran that command; see
	// [ToolchainFacts].
	Toolchain *ToolchainFacts `json:"toolchain,omitempty"`
	// ResolvedCommand is the argv that was really started: `command` with the
	// located toolchain in place of a bare `go`. The two differ in exactly that
	// one string, and the difference is what a reader chasing "which go ran
	// this?" is looking for — under a toolchain manager, `go` on the child's
	// PATH need not be the `go` this run says it used. It is absent from a
	// merged document, which has several.
	ResolvedCommand []string `json:"resolved_command,omitzero"`
}

// Baseline is every unmutated observation, not just the summary of them. The
// derived timeout is a function of the slowest run, and a reader who wants to
// know why it is what it is needs the numbers it came from.
type Baseline struct {
	Runs        int     `json:"runs"`
	DurationsMS []int64 `json:"durations_ms"`
	SlowestMS   int64   `json:"slowest_ms"`
}

// Coverage is how coverage narrowed the run.
//
// It is an object rather than a bare string, and this is the change it was
// designed for: `mode: "package"` arrives with the two numbers that explain
// what it did, and no consumer had to learn a new top-level shape.
//
// The two numbers are present exactly when the mode is `package`, which is why
// they are pointers. An `off` run carrying `binaries: 0` would be stating a
// measurement it never made — it profiled no binaries because it profiled
// nothing — and a reader cannot tell a real zero from a default. The schema
// refuses them outside `package` mode for the same reason.
type Coverage struct {
	Mode CoverageMode `json:"mode"`
	// UnavailableReason is the whole failure that made the run give up
	// coverage-instrumented test binaries: the coded message and the compiler's
	// own diagnostics under it. It is absent from every run that did not fall
	// back, which is nearly all of them.
	//
	// The console has already been told in one line, and this is the copy for
	// somebody asking why. The two are deliberately different lengths: a run
	// that is about to succeed does not print a compiler blob at the user, and
	// a reader investigating it later needs the blob.
	UnavailableReason *string `json:"unavailable_reason,omitempty"`
	// BuildFallback says the coverage-instrumented build failed and the run
	// built plain test binaries instead — which costs it a wasted compile and
	// every mutant against every binary. It is absent rather than false when
	// nothing fell back, so an older document and a merged one say nothing
	// instead of claiming a measurement.
	BuildFallback bool `json:"build_fallback,omitzero"`
	// Binaries is how many test binaries the coverage pass profiled.
	Binaries *int `json:"binaries,omitempty"`
	// Tests is how many tests the coverage pass profiled on their own, summed
	// over the binaries. Present exactly in [CoverageTest] mode.
	Tests *int `json:"tests,omitempty"`
	// MutantsUncovered is how many mutants no binary covered, and so were
	// reported as survivors without being executed. It is counted from
	// `mutants[]` by [Build] rather than passed in, so the summary line and the
	// rows underneath it cannot disagree.
	MutantsUncovered *int `json:"mutants_uncovered,omitempty"`
}

// Cache is what the outcome cache did for this run.
//
// The three counters are a partition of the mutants the run was about to
// execute, plus what it left behind: `hits` were adopted from the cache and
// never executed, `misses` were looked up, not found, and executed, and
// `writes` are how many of those misses produced an outcome worth storing.
//
// `writes` is therefore at most `misses`, and usually fewer: an inconclusive
// outcome, an errored one, and a mutant an interruption cut short are all
// measured and none of them is stored. Every count is zero when the mode is
// off, which is what makes "the cache was off" and "the cache was empty"
// different statements in this document.
//
// `hits` is counted from `mutants[]` rather than stated, so the number here and
// the rows a reader would count by hand cannot disagree.
type Cache struct {
	Mode   CacheMode `json:"mode"`
	Hits   int       `json:"hits"`
	Misses int       `json:"misses"`
	Writes int       `json:"writes"`
}

// Summary is the counted breakdown and what the policy made of it.
type Summary struct {
	Total        int `json:"total"`
	Killed       int `json:"killed"`
	Survived     int `json:"survived"`
	TimedOut     int `json:"timed_out"`
	Inconclusive int `json:"inconclusive"`
	Errored      int `json:"errored"`
	NotRun       int `json:"not_run"`
	// ScorePercent is nil exactly when the denominator is zero. There is no
	// sentinel percentage, because both plausible sentinels are lies: 0 reads
	// as "your tests caught nothing" and 100 as "your tests caught
	// everything", when the truth is that nothing was measured.
	ScorePercent *float64     `json:"score_percent"`
	Policy       PolicyResult `json:"policy"`
}

// PolicyResult is the gating configuration and the first gate that failed.
//
// One failure is named rather than all of them, and nothing is lost by it:
// every gate is a function of the counts and the settings in this same object,
// so a consumer that cares about the second reason can recompute it.
type PolicyResult struct {
	Strict         bool    `json:"strict"`
	MinimumScore   float64 `json:"minimum_score"`
	RequireMutants bool    `json:"require_mutants"`
	Failure        *string `json:"failure"`
}

// A Mutant is one catalogued mutant and what happened to it.
//
// KilledBy names the test binary that detected it — the one that failed, or the
// one it hung — so a confirmed timeout carries it too. It is nil for an outcome
// that detected nothing, and nil for a detection the harness could not
// attribute.
//
// CoveringTestPackages and Uncovered are always present, whatever the coverage
// mode, and they are two different statements. An `off` run carries an empty
// list and false: it did not ask which packages cover the mutant, and it
// executed the mutant against everything. A `package` run carries the binaries
// the profile named, and Uncovered is true for every mutant coverage
// established nothing reaches — which are also the only ones the run did not
// execute.
type Mutant struct {
	ID          string `json:"id"`
	DisplayID   string `json:"display_id"`
	Path        string `json:"path"`
	Package     string `json:"package"`
	Family      string `json:"family"`
	Rule        string `json:"rule"`
	RuleVersion int    `json:"rule_version"`
	Line        int    `json:"line"`
	Column      int    `json:"column"`
	StartByte   uint32 `json:"start_byte"`
	EndByte     uint32 `json:"end_byte"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
	// Branch is discovery's proof that this edit can only narrow the condition
	// of an `if` or a `for`, carried through so that a consumer reading a
	// report has the same span `list --json` publishes. It is omitted entirely
	// when there is none; see [Branch].
	Branch  *Branch `json:"branch,omitempty"`
	Outcome Outcome `json:"outcome"`
	// NotRunReason says why a not-run mutant was not run, and is nil for every
	// mutant that was measured. See [NotRunReason].
	NotRunReason *string `json:"not_run_reason"`
	DurationMS   int64   `json:"duration_ms"`
	KilledBy     *string `json:"killed_by"`
	Attempts     int     `json:"attempts"`
	// Executions are the passes this run made over the test binaries for this
	// mutant, in attempt order, and there are exactly `attempts` of them for a
	// mutant this run executed. They are `[]` — present and empty — for a
	// cached, uncovered or not-run mutant, each of which was settled without
	// this run starting a process for it.
	//
	// Two of those three can carry an attempt count with no rows under it, and
	// they are the only ways it happens. A cached mutant keeps the count of the
	// run that did measure it, exactly as it keeps that run's duration and
	// killer. And a mutant that timed out once and was interrupted before the
	// serial retry could repeat it is not-run with one attempt: the pass really
	// was made, and the run is not entitled to call an unrepeated timeout a
	// result.
	//
	// The list is absent, rather than empty, from a merged document and from
	// one an older build wrote. See [Execution].
	Executions []Execution `json:"executions,omitzero"`
	OutputTail *string     `json:"output_tail"`
	// CoveringTestPackages are the import paths of the test binaries whose
	// coverage profile reaches this mutant's lines, sorted. Empty is legal and
	// means two different things depending on `coverage.mode`; see the type
	// documentation.
	CoveringTestPackages []string `json:"covering_test_packages"`
	// CoveringTests are the tests whose own coverage profile reaches this
	// mutant's lines, sorted by package and then by name. It is written only
	// by a [CoverageTest] run and only when there are any: an uncovered
	// mutant has none to name, and every other mode never asked.
	CoveringTests []TestRef `json:"covering_tests,omitzero"`
	// Uncovered says the run established that no test binary reaches this
	// mutant's lines and therefore did not execute it. Such a mutant is a
	// survivor — no test could have caught it — with zero attempts.
	Uncovered bool `json:"uncovered"`
	// Cached says this outcome was adopted from the outcome cache rather than
	// measured by this run. The duration, the attempts, the killed_by and the
	// output tail are then the ones the run that first measured it recorded, and
	// they are reported as they stand rather than zeroed: a survivor whose tail
	// explains why it survived is worth exactly as much second-hand.
	//
	// It is never true of an uncovered mutant, of a not-run one, or of an
	// outcome the cache refuses to store; see internal/cache.
	Cached bool `json:"cached"`
	// MemoryExceeded says the run's per-mutant memory bound is what settled
	// this mutant, and PeakMemoryBytes is the highest it was observed to hold.
	//
	// They restate what the execution rows already carry, and the restatement
	// is the point: a *cached* mutant has an attempt count and no rows, because
	// this run started no process for it — so a consumer reading the rows sees
	// nothing, and `explain` on a warm run would report a kill it could not
	// explain. For a mutant this run executed they are the maximum over its
	// rows and whether any of them tripped the bound; for a cached one they are
	// what the run that did measure it recorded.
	//
	// Both are optional, so a document written before the bound existed carries
	// neither, and PeakMemoryBytes is absent — not zero — where the platform
	// could not measure one. MemoryExceeded is only ever true beside an outcome
	// of `killed`; the bound it was measured against is `test.memory_bytes`.
	MemoryExceeded  bool  `json:"memory_exceeded,omitzero"`
	PeakMemoryBytes int64 `json:"peak_memory_bytes,omitzero"`
}

// A Branch is the body span a mutant's condition gates, in the coordinates
// `go test -coverprofile` reports statement blocks in.
//
// A test during which no statement of that body executed cannot distinguish
// the mutant from the original program. Direction names the lemma the span came
// from and is diagnostic; the promise is about the span alone. The proof itself
// is made in internal/discover, which is the only phase with the type
// information to make it; this document only carries it.
type Branch struct {
	Direction       string `json:"direction"`
	BodyStartLine   int    `json:"body_start_line"`
	BodyStartColumn int    `json:"body_start_column"`
	BodyEndLine     int    `json:"body_end_line"`
	BodyEndColumn   int    `json:"body_end_column"`
}

// A Rejected is a catalogued mutant validation refused, with the compiler's
// own words for why.
//
// It carries fewer fields than a [Mutant] on purpose: there is no outcome, no
// duration and no attempt count, because a rejected mutant was never executed.
// Reporting it as an errored or not-run mutant would put a mutant that cannot
// exist into the denominator of a score.
type Rejected struct {
	ID         string `json:"id"`
	DisplayID  string `json:"display_id"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Rule       string `json:"rule"`
	Diagnostic string `json:"diagnostic"`
}

// A Skip is one recorded reason a site was never turned into a candidate,
// aggregated per file.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// An Expectation is one ledger row checked against this run.
type Expectation struct {
	ID     string           `json:"id"`
	Reason string           `json:"reason"`
	State  ExpectationState `json:"state"`
}

// A Warning is one non-fatal diagnostic the run published.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Marshal encodes the report as the bytes that go on disk.
//
// The encoding is deterministic by construction: field order is struct order,
// every array was ordered when the report was built, and no map is ever
// iterated into one. Two runs over one workspace that reach the same outcomes
// therefore produce byte-identical documents apart from the run id and the
// clock.
//
// HTML escaping is off because there is no HTML here: with it on, every `<` in
// a comparison operator would be written as `<`, which is the same string
// to a parser and unreadable to everybody else. Two spaces of indentation and
// the encoder's trailing newline make the file diffable.
func (r *Report) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(r); err != nil {
		return nil, &Error{
			Code:    CodeEncodeFailed,
			Message: "the run report could not be encoded as JSON",
			Err:     err,
		}
	}
	return buf.Bytes(), nil
}

// Tally recounts the report into the core tally.
//
// It exists so that the exit decision is made from the document rather than
// beside it: whatever [mutation.Decide] is asked, it is asked about the numbers
// the user can read in the file. It is also the proof that the document is
// lossless in the one place losslessness is not obvious — the summary counts
// survivors as one number, and the split the score needs is recovered by
// joining the expectations ledger, exactly as an outside consumer would have
// to.
func (r *Report) Tally() (mutation.Tally, error) {
	expected := make(map[string]bool, len(r.Expectations))
	for _, e := range r.Expectations {
		if e.State == StateFulfilled {
			expected[e.ID] = true
		}
	}
	var t mutation.Tally
	for _, m := range r.Mutants {
		outcome, err := m.Outcome.Mutation()
		if err != nil {
			return mutation.Tally{}, err
		}
		if err := t.Record(mutation.Result{
			Outcome:          outcome,
			ExpectedSurvivor: expected[m.ID],
		}); err != nil {
			return mutation.Tally{}, &Error{
				Code:    CodeInvalidOutcome,
				Message: "mutant " + m.DisplayID + " cannot be counted",
				Err:     err,
			}
		}
	}
	return t, nil
}

// ExpectationFailure reports whether the ledger stopped describing reality,
// which is the signal [mutation.Decide] escalates to exit 2.
//
// Two things count and a third deliberately does not. A stale row counts: it
// documents a mutant that no longer exists. An unfulfilled row whose mutant the
// tests actually caught counts: the ledger says "known survivor" about
// something that is now killed, so it is lying to whoever reads it. An
// unfulfilled row whose mutant was simply never measured — not run under
// `--mutant`, rejected by validation, inconclusive — does not count, because
// nothing was learned about it. Collapsing the two kinds of unfulfilled into
// one bit would make every `--mutant` run exit 2 for every unrelated ledger
// row, which is why the distinction is drawn here from the outcomes rather than
// read off the document's three-valued enum.
func (r *Report) ExpectationFailure() bool {
	outcomes := make(map[string]Outcome, len(r.Mutants))
	for _, m := range r.Mutants {
		outcomes[m.ID] = m.Outcome
	}
	for _, e := range r.Expectations {
		switch e.State {
		case StateStale:
			return true
		case StateUnfulfilled:
			switch outcomes[e.ID] {
			case OutcomeKilled, OutcomeTimedOut:
				return true
			}
		}
	}
	return false
}

// FormatTimestamp renders a moment the way this document spells one: RFC 3339
// in UTC, to the second.
//
// Seconds rather than nanoseconds, because a report is compared between runs
// far more often than it is used to measure anything, and the durations
// alongside carry the precision that matters.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// milliseconds renders a duration the way this document counts one: truncated,
// and never negative. A sub-millisecond run honestly reports 0.
func milliseconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

// text returns a pointer to s, or nil for the empty string.
//
// Every optional string in this document is `string | null`, and the empty
// string is not one of the two: "the harness named no test" is null, and a
// killed_by of "" would be a name that is not a name.
func text(s string) *string {
	if s == "" {
		return nil
	}
	value := s
	return &value
}
