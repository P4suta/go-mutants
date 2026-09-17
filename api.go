// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testlog"
	"github.com/P4suta/go-mutants/trace"
)

const OutputTruncatedPrefix = runner.OutputTruncatedPrefix

type OpenOptions struct {
	GoBinary        string
	SnapshotExclude []string
	ReportDirectory string
	TempDirectory   string
	KeepTemp        bool
	Env             []string
	Trace           trace.Sink
}

type SweepResult struct {
	Removed      []string
	RemovedBytes int64
	Live         int
	Kept         int
	Err          error
}

type Command struct {
	Argv        []string
	Dir         string
	Env         []string
	MemoryLimit int64

	Timeout     time.Duration
	OutputLimit int
}

type CommandResult struct {
	ExitCode       int
	TimedOut       bool
	Duration       time.Duration
	Output         []byte
	Truncated      bool
	TotalBytes     int64
	PeakMemory     int64
	MemoryExceeded bool
	TraceSeq       int64
}

type PreparePhase string

const (
	PreparePhaseDiscovery          PreparePhase = "discovery"
	PreparePhaseProbeSnapshot      PreparePhase = "probe_snapshot"
	PreparePhaseMainValidation     PreparePhase = "main_validation"
	PreparePhaseMainRestoration    PreparePhase = "main_restoration"
	PreparePhaseVerification       PreparePhase = "verification"
	PreparePhaseBinaryBuild        PreparePhase = "binary_build"
	PreparePhaseProbeValidation    PreparePhase = "probe_validation"
	PreparePhaseProbeCoverageBuild PreparePhase = "probe_coverage_build"
	PreparePhaseProbeRestoration   PreparePhase = "probe_restoration"
)

func KnownPreparePhases() []PreparePhase {
	return []PreparePhase{
		PreparePhaseDiscovery,
		PreparePhaseProbeSnapshot,
		PreparePhaseMainValidation,
		PreparePhaseMainRestoration,
		PreparePhaseVerification,
		PreparePhaseBinaryBuild,
		PreparePhaseProbeValidation,
		PreparePhaseProbeCoverageBuild,
		PreparePhaseProbeRestoration,
	}
}

type PrepareEventState string

const (
	PrepareEventStarted  PrepareEventState = "started"
	PrepareEventFinished PrepareEventState = "finished"
)

type PreparePhaseResult string

const (
	PreparePhaseSucceeded PreparePhaseResult = "succeeded"
	PreparePhaseFailed    PreparePhaseResult = "failed"
	PreparePhaseSkipped   PreparePhaseResult = "skipped"
)

type PrepareEvent struct {
	Phase    PreparePhase
	State    PrepareEventState
	Result   PreparePhaseResult
	Duration time.Duration
}

type PrepareOptions struct {
	Profile            string
	Operators          []string
	Include            []string
	Exclude            []string
	DiscoveryPackages  []string
	Packages           []string
	ProbeCoverPackages []string
	Selection          *Selection
	Jobs               int
	BuildTimeout       time.Duration
	MutantTimeout      time.Duration
	Verify             Command
	SkipVerify         bool
	Probe              bool
	Trace              func(PrepareEvent)
}

func DefaultJobs() int { return config.DefaultJobs() }

type Catalog struct {
	WorkspaceDigest string
	Digest          string
	PreparedDigest  string
	ModulePath      string
	GoVersion       string
	Toolchain       string
	Profile         string
	Mutants         []Mutant
	Rejections      []Rejection
	TestPackages    []string
	Selection       *Selection
}

type Mutant struct {
	Index        uint32
	ID           string
	DisplayID    string
	Path         string
	Package      string
	Line         int
	Column       int
	EndLine      int
	StartByte    uint32
	EndByte      uint32
	Family       string
	Rule         string
	RuleVersion  int
	SourceDigest string
	Original     string
	Replacement  string
	Accepted     bool
	Branch       *BranchProof
	Probed       bool
	Selected     bool
}

const BranchDecreasing = "decreasing"

type BranchProof struct {
	Direction       string
	BodyStartLine   int
	BodyStartColumn int
	BodyEndLine     int
	BodyEndColumn   int
}

type Rejection struct {
	ID         string
	DisplayID  string
	Path       string
	Line       int
	Column     int
	Rule       string
	Diagnostic string
}

type ExecRequest struct {
	Mutant        string
	Package       string
	Args          []string
	Env           []string
	Timeout       time.Duration
	MemoryLimit   int64
	OutputLimit   int
	RecordTestLog bool
}

type Outcome string

const (
	OutcomeNotRun       Outcome = "not_run"
	OutcomeKilled       Outcome = "killed"
	OutcomeSurvived     Outcome = "survived"
	OutcomeTimedOut     Outcome = "timed_out"
	OutcomeInconclusive Outcome = "inconclusive"
	OutcomeErrored      Outcome = "errored"
)

func KnownOutcomes() []Outcome {
	return []Outcome{
		OutcomeNotRun,
		OutcomeKilled,
		OutcomeSurvived,
		OutcomeTimedOut,
		OutcomeInconclusive,
		OutcomeErrored,
	}
}

type MutantResult struct {
	ID             string
	DisplayID      string
	Outcome        Outcome
	KilledBy       string
	Duration       time.Duration
	OutputTail     string
	Output         []byte
	Truncated      bool
	TotalBytes     int64
	PeakMemory     int64
	MemoryExceeded bool
	Artifacts      []Artifact
	Binaries       []string
	TraceSeq       int64
	TestLogs       []TestLog
}

var ErrProbeNotPrepared = errors.New("gomutants: the session was prepared without a probe tree")

type ProbeRequest struct {
	Package       string
	Args          []string
	Env           []string
	Timeout       time.Duration
	MemoryLimit   int64
	OutputLimit   int
	RecordTestLog bool
}

type ProbeOutcome string

const (
	ProbeMeasured    ProbeOutcome = "measured"
	ProbeTestFailed  ProbeOutcome = "test-failed"
	ProbeTimedOut    ProbeOutcome = "timed-out"
	ProbeUnavailable ProbeOutcome = "unavailable"
)

func KnownProbeOutcomes() []ProbeOutcome {
	return []ProbeOutcome{
		ProbeMeasured,
		ProbeTestFailed,
		ProbeTimedOut,
		ProbeUnavailable,
	}
}

type ProbeResult struct {
	Outcome        ProbeOutcome
	Infected       []uint32
	ExitCode       int
	Duration       time.Duration
	Output         []byte
	Truncated      bool
	TotalBytes     int64
	PeakMemory     int64
	MemoryExceeded bool
	Binaries       []string
	TraceSeq       int64
	TestLogs       []TestLog
}

type ControlRequest struct {
	Package       string
	Args          []string
	Env           []string
	Timeout       time.Duration
	MemoryLimit   int64
	OutputLimit   int
	RecordTestLog bool
}

type ControlResult struct {
	Package        string
	ExitCode       int
	TimedOut       bool
	Duration       time.Duration
	Output         []byte
	Truncated      bool
	TotalBytes     int64
	PeakMemory     int64
	MemoryExceeded bool
	Binaries       []string
	ExecSeqs       []int64
	TraceSeq       int64
	TestLogs       []TestLog
}

type TestLogOp string

const (
	TestLogGetenv TestLogOp = "getenv"
	TestLogOpen   TestLogOp = "open"
	TestLogStat   TestLogOp = "stat"
	TestLogChdir  TestLogOp = "chdir"
)

type TestLogEntry struct {
	Op   TestLogOp
	Name string
}

type TestLog struct {
	Package  string
	Dir      string
	Entries  []TestLogEntry
	Complete bool
	Err      string
}

var ErrTestLogUnsupported = testlog.ErrUnsupported

type Artifact struct {
	Path   string
	SHA256 string
	Data   []byte
}

type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"
	ChangeRemoved  ChangeKind = "removed"
	ChangeModified ChangeKind = "modified"
)

type Change struct {
	Kind         ChangeKind
	Path         string
	BeforeSHA256 string
	AfterSHA256  string
}
