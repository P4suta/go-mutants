// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

type Phase string

const (
	PhaseDiscover Phase = "discover"
	PhaseBaseline Phase = "baseline"
	PhaseMutate   Phase = "mutate"
	PhaseReport   Phase = "report"
)

func (p Phase) String() string { return string(p) }

func Phases() []Phase {
	return []Phase{PhaseDiscover, PhaseBaseline, PhaseMutate, PhaseReport}
}

type Status string

const (
	StatusOK          Status = "ok"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
)

func (s Status) String() string { return string(s) }

type TimeoutSource string

const (
	TimeoutDerived  TimeoutSource = "derived"
	TimeoutExplicit TimeoutSource = "explicit"
)

func (s TimeoutSource) String() string { return string(s) }

type MemorySource string

const (
	MemorySourceDerived     MemorySource = "derived"
	MemorySourceExplicit    MemorySource = "explicit"
	MemorySourceUnavailable MemorySource = "unavailable"
)

func (s MemorySource) String() string { return string(s) }

type CoverageMode string

const (
	CoverageOff     CoverageMode = "off"
	CoveragePackage CoverageMode = "package"
	CoverageTest    CoverageMode = "test"
)

func (m CoverageMode) String() string { return string(m) }

func (m CoverageMode) Narrowed() bool { return m == CoveragePackage || m == CoverageTest }

type CacheMode string

const (
	CacheOff CacheMode = "off"
	CacheOn  CacheMode = "on"
)

func (m CacheMode) String() string { return string(m) }

type Event interface {
	event()
}

type RunPlanned struct {
	RunID   string
	Workers int
}

type PhaseChanged struct {
	Phase  Phase
	Detail string
}

type PhaseCompleted struct {
	Phase    Phase
	Duration time.Duration
}

type Traced struct {
	Event trace.Event
}

type BaselineProgress struct {
	Run      int
	Of       int
	Duration time.Duration
}

type BaselineCompleted struct {
	Runs          []time.Duration
	Average       time.Duration
	Slowest       time.Duration
	Timeout       time.Duration
	TimeoutSource TimeoutSource
}

type MemoryDerived struct {
	Limit  int64
	Source MemorySource
	Peak   int64
}

type Discovered struct {
	Candidates int
	Skips      int
}

type Validated struct {
	Accepted int
	Rejected int
}

type SelectionNarrowed struct {
	ChangedRef string
	Shard      int
	Shards     int
	Selected   int
	Of         int
}

type CoverageMapped struct {
	Binaries  int
	Tests     int
	Covered   int
	Uncovered int
	Widened   int
}

type Probed struct {
	Binaries  int
	Settled   int
	Narrowed  int
	Remaining int
}

type MutantResult struct {
	ID                   string
	DisplayID            string
	Path                 string
	ModuleDir            string
	Line                 int
	Column               int
	Rule                 string
	Original             string
	Replacement          string
	Outcome              mutation.Outcome
	Duration             time.Duration
	Worker               int
	Uncovered            bool
	Cached               bool
	KilledBy             string
	Attempts             int
	CoveringTestPackages []string
	CoveringTests        []report.TestRef
	PeakMemory           int64
	MemoryExceeded       bool

	Diverged    bool
	MemoryLimit int64
}

func (m MutantResult) clone() MutantResult {
	m.CoveringTestPackages = slices.Clone(m.CoveringTestPackages)
	m.CoveringTests = slices.Clone(m.CoveringTests)
	return m
}

type MutantStarted struct {
	ID        string
	DisplayID string
	Path      string
	ModuleDir string
	Line      int
	Rule      string
	Worker    int
}

type MutantFinished struct {
	Result MutantResult
}

type CacheHit struct {
	ID        string
	DisplayID string
	Outcome   mutation.Outcome
}

type DirectoryKept struct {
	Kind string
	Path string
}

type Warning struct {
	Code    string
	Message string
	Detail  string
}

type ReportPublished struct {
	RunPath        string
	LatestPath     string
	ProjectionPath string
	HTMLPath       string
	TracePath      string
}

type Counts struct {
	Total        int
	Killed       int
	Survived     int
	TimedOut     int
	Inconclusive int
	Errored      int
	NotRun       int
	Rejected     int
	Uncovered    int
	Cached       int
}

type ExpectationCounts struct {
	Fulfilled   int
	Unfulfilled int
	Stale       int
}

func (e ExpectationCounts) Total() int { return e.Fulfilled + e.Unfulfilled + e.Stale }

type SkipCount struct {
	Reason string
	Count  int
}

type RunSummary struct {
	RunID        string
	ExitCode     mutation.ExitCode
	Failure      mutation.Failure
	Notable      []MutantResult
	Counts       Counts
	Coverage     CoverageMode
	Cache        CacheMode
	Score        mutation.Score
	Expectations ExpectationCounts
	Warnings     int
	Skips        []SkipCount
}

func (s RunSummary) clone() RunSummary {
	s.Notable = slices.Clone(s.Notable)
	s.Skips = slices.Clone(s.Skips)
	return s
}

type RunCompleted struct {
	Status  Status
	Summary string
	Run     *RunSummary
}

func (RunPlanned) event()        {}
func (PhaseChanged) event()      {}
func (PhaseCompleted) event()    {}
func (Traced) event()            {}
func (BaselineProgress) event()  {}
func (BaselineCompleted) event() {}
func (MemoryDerived) event()     {}
func (Discovered) event()        {}
func (Validated) event()         {}
func (SelectionNarrowed) event() {}
func (CoverageMapped) event()    {}
func (Probed) event()            {}
func (MutantStarted) event()     {}
func (MutantFinished) event()    {}
func (CacheHit) event()          {}
func (DirectoryKept) event()     {}
func (Warning) event()           {}
func (ReportPublished) event()   {}
func (RunCompleted) event()      {}

func (e BaselineCompleted) clone() BaselineCompleted {
	e.Runs = slices.Clone(e.Runs)
	return e
}

func WorkspaceLocation(moduleDir, path string) string {
	if moduleDir == "" || moduleDir == "." {
		return path
	}
	return moduleDir + "/" + path
}
