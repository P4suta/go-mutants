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

const (
	DocumentType  = "go-mutants/run-report"
	SchemaVersion = 1
)

var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Status string

const (
	StatusCompleted   Status = "completed"
	StatusInterrupted Status = "interrupted"
	StatusFailed      Status = "failed"
)

func Statuses() []Status { return []Status{StatusCompleted, StatusInterrupted, StatusFailed} }

func (s Status) Valid() bool { return slices.Contains(Statuses(), s) }

func (s Status) String() string { return string(s) }

type SelectionMode string

const (
	ModeAll     SelectionMode = "all"
	ModeMutant  SelectionMode = "mutant"
	ModeChanged SelectionMode = "changed"
	ModeShard   SelectionMode = "shard"
)

func SelectionModes() []SelectionMode {
	return []SelectionMode{ModeAll, ModeMutant, ModeChanged, ModeShard}
}

func (m SelectionMode) Valid() bool { return slices.Contains(SelectionModes(), m) }

func (m SelectionMode) String() string { return string(m) }

type TimeoutSource string

const (
	TimeoutDerived  TimeoutSource = "derived"
	TimeoutExplicit TimeoutSource = "explicit"
)

func (s TimeoutSource) Valid() bool {
	return s == TimeoutDerived || s == TimeoutExplicit
}

type MemorySource string

const (
	MemoryDerived     MemorySource = "derived"
	MemoryExplicit    MemorySource = "explicit"
	MemoryUnavailable MemorySource = "unavailable"
)

func (s MemorySource) Valid() bool {
	return s == MemoryDerived || s == MemoryExplicit || s == MemoryUnavailable
}

type CoverageMode string

const (
	CoverageOff     CoverageMode = "off"
	CoveragePackage CoverageMode = "package"
	CoverageTest    CoverageMode = "test"
)

func CoverageModes() []CoverageMode {
	return []CoverageMode{CoverageOff, CoveragePackage, CoverageTest}
}

func (m CoverageMode) Valid() bool { return slices.Contains(CoverageModes(), m) }

func (m CoverageMode) Narrowed() bool { return m == CoveragePackage || m == CoverageTest }

type TestRef struct {
	Package string `json:"package"`
	Name    string `json:"name"`
}

func (m CoverageMode) String() string { return string(m) }

type CacheMode string

const (
	CacheOff CacheMode = "off"
	CacheOn  CacheMode = "on"
)

func CacheModes() []CacheMode { return []CacheMode{CacheOff, CacheOn} }

func (m CacheMode) Valid() bool { return slices.Contains(CacheModes(), m) }

func (m CacheMode) String() string { return string(m) }

type Outcome string

const (
	OutcomeKilled       Outcome = "killed"
	OutcomeSurvived     Outcome = "survived"
	OutcomeTimedOut     Outcome = "timed-out"
	OutcomeInconclusive Outcome = "inconclusive"
	OutcomeErrored      Outcome = "errored"
	OutcomeNotRun       Outcome = "not-run"
)

var outcomeNames = map[mutation.Outcome]Outcome{
	mutation.OutcomeKilled:       OutcomeKilled,
	mutation.OutcomeSurvived:     OutcomeSurvived,
	mutation.OutcomeTimedOut:     OutcomeTimedOut,
	mutation.OutcomeInconclusive: OutcomeInconclusive,
	mutation.OutcomeErrored:      OutcomeErrored,
	mutation.OutcomeNotRun:       OutcomeNotRun,
}

func Observations() []Outcome {
	return []Outcome{OutcomeKilled, OutcomeSurvived, OutcomeTimedOut, OutcomeErrored}
}

func (o Outcome) Observed() bool { return slices.Contains(Observations(), o) }

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

func (o Outcome) String() string { return string(o) }

type NotRunReason string

const (
	NotRunInterrupted    NotRunReason = "interrupted"
	NotRunOutOfSelection NotRunReason = "out-of-selection"
	NotRunOtherShard     NotRunReason = "other-shard"
)

func NotRunReasons() []NotRunReason {
	return []NotRunReason{NotRunInterrupted, NotRunOutOfSelection, NotRunOtherShard}
}

func (r NotRunReason) Valid() bool { return slices.Contains(NotRunReasons(), r) }

func (r NotRunReason) String() string { return string(r) }

type ExpectationState string

const (
	StateFulfilled   ExpectationState = "fulfilled"
	StateUnfulfilled ExpectationState = "unfulfilled"
	StateStale       ExpectationState = "stale"
)

func (s ExpectationState) String() string { return string(s) }

type StageResult string

const (
	StageSucceeded StageResult = trace.ResultSucceeded
	StageFailed    StageResult = trace.ResultFailed
	StageSkipped   StageResult = trace.ResultSkipped
)

func StageResults() []StageResult {
	return []StageResult{StageSucceeded, StageFailed, StageSkipped}
}

func (r StageResult) Valid() bool { return slices.Contains(StageResults(), r) }

func (r StageResult) String() string { return string(r) }

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
	Shard         *Shard        `json:"shard"`
	Merge         *Merge        `json:"merge,omitempty"`
	Test          Test          `json:"test"`
	Coverage      Coverage      `json:"coverage"`
	Cache         Cache         `json:"cache"`
	Summary       Summary       `json:"summary"`
	Mutants       []Mutant      `json:"mutants"`
	Rejected      []Rejected    `json:"rejected"`
	Skips         []Skip        `json:"skips"`
	Expectations  []Expectation `json:"expectations"`
	Warnings      []Warning     `json:"warnings"`
	Timing        *Timing       `json:"timing,omitempty"`
	Validation    *Validation   `json:"validation,omitempty"`
}

type Timing struct {
	Phases []PhaseTiming `json:"phases"`
	Stages []StageTiming `json:"stages"`
}

type PhaseTiming struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
}

type StageTiming struct {
	Phase      string      `json:"phase"`
	Name       string      `json:"name"`
	DurationMS int64       `json:"duration_ms"`
	Result     StageResult `json:"result"`
}

type Validation struct {
	Builds int `json:"builds"`
}

type SnapshotFacts struct {
	StableDir bool `json:"stable_dir"`
	Files     int  `json:"files"`
}

type ToolchainFacts struct {
	GoBin   string `json:"go_bin"`
	Version string `json:"version"`
}

type Execution struct {
	Attempt         int       `json:"attempt"`
	Worker          int       `json:"worker"`
	Outcome         Outcome   `json:"outcome"`
	KilledBy        string    `json:"killed_by,omitzero"`
	DurationMS      int64     `json:"duration_ms"`
	Binaries        []string  `json:"binaries"`
	Tests           []TestRef `json:"tests,omitzero"`
	MemoryExceeded  bool      `json:"memory_exceeded,omitzero"`
	Diverged        bool      `json:"diverged,omitzero"`
	PeakMemoryBytes int64     `json:"peak_memory_bytes,omitzero"`
}

type Workspace struct {
	ModulePath      string         `json:"module_path"`
	GoVersion       string         `json:"go_version"`
	WorkspaceDigest string         `json:"workspace_digest"`
	Platform        Platform       `json:"platform"`
	Snapshot        *SnapshotFacts `json:"snapshot,omitempty"`
}

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type Selection struct {
	Mode       SelectionMode `json:"mode"`
	ChangedRef *string       `json:"changed_ref"`
	Profile    string        `json:"profile"`
	Operators  []string      `json:"operators"`
	Include    []string      `json:"include"`
	Exclude    []string      `json:"exclude"`
	Candidates int           `json:"candidates"`
	Rejected   int           `json:"rejected"`
	Selected   int           `json:"selected"`
}

type Shard struct {
	Index      int    `json:"index"`
	Total      int    `json:"total"`
	Assignment string `json:"assignment"`
}

func (s Shard) Owns(id string) bool {
	return mutation.ShardIndex(id, s.Total) == s.Index
}

type Merge struct {
	Shards int `json:"shards"`
}

type Test struct {
	Command         []string        `json:"command"`
	Baseline        Baseline        `json:"baseline"`
	TimeoutMS       int64           `json:"timeout_ms"`
	TimeoutSource   TimeoutSource   `json:"timeout_source"`
	MemoryBytes     int64           `json:"memory_bytes,omitzero"`
	MemorySource    MemorySource    `json:"memory_source,omitzero"`
	Toolchain       *ToolchainFacts `json:"toolchain,omitempty"`
	ResolvedCommand []string        `json:"resolved_command,omitzero"`
}

type Baseline struct {
	Runs        int     `json:"runs"`
	DurationsMS []int64 `json:"durations_ms"`
	SlowestMS   int64   `json:"slowest_ms"`
}

type Coverage struct {
	Mode              CoverageMode `json:"mode"`
	UnavailableReason *string      `json:"unavailable_reason,omitempty"`
	BuildFallback     bool         `json:"build_fallback,omitzero"`
	Binaries          *int         `json:"binaries,omitempty"`
	Tests             *int         `json:"tests,omitempty"`
	MutantsUncovered  *int         `json:"mutants_uncovered,omitempty"`
}

type Cache struct {
	Mode   CacheMode `json:"mode"`
	Hits   int       `json:"hits"`
	Misses int       `json:"misses"`
	Writes int       `json:"writes"`
}

type Summary struct {
	Total        int          `json:"total"`
	Killed       int          `json:"killed"`
	Survived     int          `json:"survived"`
	TimedOut     int          `json:"timed_out"`
	Inconclusive int          `json:"inconclusive"`
	Errored      int          `json:"errored"`
	NotRun       int          `json:"not_run"`
	ScorePercent *float64     `json:"score_percent"`
	Policy       PolicyResult `json:"policy"`
}

type PolicyResult struct {
	Strict         bool    `json:"strict"`
	MinimumScore   float64 `json:"minimum_score"`
	RequireMutants bool    `json:"require_mutants"`
	Failure        *string `json:"failure"`
}

type Mutant struct {
	ID                   string      `json:"id"`
	DisplayID            string      `json:"display_id"`
	Path                 string      `json:"path"`
	Package              string      `json:"package"`
	Family               string      `json:"family"`
	Rule                 string      `json:"rule"`
	RuleVersion          int         `json:"rule_version"`
	Line                 int         `json:"line"`
	Column               int         `json:"column"`
	StartByte            uint32      `json:"start_byte"`
	EndByte              uint32      `json:"end_byte"`
	Original             string      `json:"original"`
	Replacement          string      `json:"replacement"`
	Branch               *Branch     `json:"branch,omitempty"`
	Outcome              Outcome     `json:"outcome"`
	NotRunReason         *string     `json:"not_run_reason"`
	DurationMS           int64       `json:"duration_ms"`
	KilledBy             *string     `json:"killed_by"`
	Attempts             int         `json:"attempts"`
	Executions           []Execution `json:"executions,omitzero"`
	OutputTail           *string     `json:"output_tail"`
	CoveringTestPackages []string    `json:"covering_test_packages"`
	CoveringTests        []TestRef   `json:"covering_tests,omitzero"`
	Uncovered            bool        `json:"uncovered"`
	Unobserved           bool        `json:"unobserved,omitzero"`
	Cached               bool        `json:"cached"`
	MemoryExceeded       bool        `json:"memory_exceeded,omitzero"`
	PeakMemoryBytes      int64       `json:"peak_memory_bytes,omitzero"`
	Diverged             bool        `json:"diverged,omitzero"`
}

type Branch struct {
	Direction       string `json:"direction"`
	BodyStartLine   int    `json:"body_start_line"`
	BodyStartColumn int    `json:"body_start_column"`
	BodyEndLine     int    `json:"body_end_line"`
	BodyEndColumn   int    `json:"body_end_column"`
}

type Rejected struct {
	ID         string `json:"id"`
	DisplayID  string `json:"display_id"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Rule       string `json:"rule"`
	Diagnostic string `json:"diagnostic"`
}

type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type Expectation struct {
	ID     string           `json:"id"`
	Reason string           `json:"reason"`
	State  ExpectationState `json:"state"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

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
			case OutcomeSurvived, OutcomeErrored, OutcomeInconclusive, OutcomeNotRun:
			}
		case StateFulfilled:
		}
	}
	return false
}

func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func milliseconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

func text(s string) *string {
	if s == "" {
		return nil
	}
	value := s
	return &value
}
