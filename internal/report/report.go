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
)

// The identity of the document this package writes.
//
// Both constants are spelled out here rather than imported from
// internal/schemas, exactly as in internal/cli: importing them would link the
// JSON Schema validator into the shipped binary for the sake of two strings.
// The schema is a test-time contract — the package tests assert that a document
// carrying these values is the document internal/schemas validates — so the two
// cannot drift apart without a test failing.
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
type SelectionMode string

// The v1 selection modes. `--changed` and `--shard` add their own in a later
// phase, together with the fields that describe them.
const (
	// ModeAll runs every catalogued mutant the profile and the patterns
	// selected.
	ModeAll SelectionMode = "all"
	// ModeMutant runs the single mutant `--mutant` named. Everything else is
	// catalogued and reported as not-run, which is what keeps the score and
	// `policy.require_mutants` honest about the difference between "nothing to
	// find" and "not looked at this time".
	ModeMutant SelectionMode = "mutant"
)

// SelectionModes returns every mode in document order.
func SelectionModes() []SelectionMode { return []SelectionMode{ModeAll, ModeMutant} }

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

// A CoverageMode says how coverage narrowed the run.
type CoverageMode string

// The v1 coverage modes.
const (
	// CoverageOff means every selected mutant was executed against every test
	// binary of its package. It is the only mode this build implements.
	CoverageOff CoverageMode = "off"
)

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

// A Report is one run, losslessly.
//
// The field order is the document's order, and the JSON encoder writes struct
// fields in declaration order, so the shape of the file is decided here and
// nowhere else. Every slice is non-nil in a built report: an empty list is
// `[]`, never `null`, because "no warnings" and "warnings unknown" are not the
// same statement and only one of them is ever true.
type Report struct {
	DocumentType  string        `json:"document_type"`
	SchemaVersion int           `json:"schema_version"`
	ToolVersion   string        `json:"tool_version"`
	RunID         string        `json:"run_id"`
	Status        Status        `json:"status"`
	StartedAt     string        `json:"started_at"`
	FinishedAt    string        `json:"finished_at"`
	DurationMS    int64         `json:"duration_ms"`
	Workspace     Workspace     `json:"workspace"`
	Selection     Selection     `json:"selection"`
	Test          Test          `json:"test"`
	Coverage      Coverage      `json:"coverage"`
	Summary       Summary       `json:"summary"`
	Mutants       []Mutant      `json:"mutants"`
	Rejected      []Rejected    `json:"rejected"`
	Skips         []Skip        `json:"skips"`
	Expectations  []Expectation `json:"expectations"`
	Warnings      []Warning     `json:"warnings"`
}

// Workspace names the tree the run read.
type Workspace struct {
	ModulePath      string   `json:"module_path"`
	GoVersion       string   `json:"go_version"`
	WorkspaceDigest string   `json:"workspace_digest"`
	Platform        Platform `json:"platform"`
}

// Platform is the host the run happened on. Build constraints decide which
// files a package even has, so a report is a statement about one platform.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Selection is what was asked for, and the arithmetic of what it produced.
type Selection struct {
	Mode       SelectionMode `json:"mode"`
	Profile    string        `json:"profile"`
	Operators  []string      `json:"operators"`
	Include    []string      `json:"include"`
	Exclude    []string      `json:"exclude"`
	Candidates int           `json:"candidates"`
	Rejected   int           `json:"rejected"`
	Selected   int           `json:"selected"`
}

// Test is how the project's tests were run and measured.
type Test struct {
	Command       []string      `json:"command"`
	Baseline      Baseline      `json:"baseline"`
	TimeoutMS     int64         `json:"timeout_ms"`
	TimeoutSource TimeoutSource `json:"timeout_source"`
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
// It is an object with one field rather than a bare string, so that
// coverage-guided selection can arrive as `mode: "package"` plus whatever it
// needs to explain itself, instead of forcing every consumer to learn a new
// top-level shape.
type Coverage struct {
	Mode CoverageMode `json:"mode"`
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
type Mutant struct {
	ID          string  `json:"id"`
	DisplayID   string  `json:"display_id"`
	Path        string  `json:"path"`
	Package     string  `json:"package"`
	Family      string  `json:"family"`
	Rule        string  `json:"rule"`
	RuleVersion int     `json:"rule_version"`
	Line        int     `json:"line"`
	Column      int     `json:"column"`
	StartByte   uint32  `json:"start_byte"`
	EndByte     uint32  `json:"end_byte"`
	Original    string  `json:"original"`
	Replacement string  `json:"replacement"`
	Outcome     Outcome `json:"outcome"`
	DurationMS  int64   `json:"duration_ms"`
	KilledBy    *string `json:"killed_by"`
	Attempts    int     `json:"attempts"`
	OutputTail  *string `json:"output_tail"`
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
