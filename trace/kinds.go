// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import "time"

// SchemaV1 is the format identity a recording carries on its first line. It is
// the version, so a later document shape can never be read as this one.
const SchemaV1 = "gomutants-trace-v1"

const (
	// FileName is the stream inside a run directory.
	FileName = "trace.jsonl"

	// OutputDirectoryName is the directory beside the stream that holds the
	// captured output of the commands the stream digested.
	OutputDirectoryName = "output"

	// OutputFileLimit is how much of one captured output is preserved. Output
	// is a developer's own test output, so it is kept rather than filtered —
	// but a single hung test can produce gigabytes of it, and the tail of a
	// megabyte is where the assertion failure is.
	OutputFileLimit = 1 << 20

	// TruncationMarker ends a preserved output that did not fit.
	TruncationMarker = "..."

	// DefaultRingCapacity is how many events an untraced run keeps in memory.
	// It is a bounded price a run of any length pays once, and it exists
	// because the failure nobody expected is exactly the failure nobody
	// thought to ask for a trace of.
	DefaultRingCapacity = 4096

	// RetainRuns is how many recordings a trace root keeps. Collection is the
	// reason a diagnostic directory does not grow without limit; ten runs is
	// enough to compare a regression against the run before it.
	RetainRuns = 10
)

// The event types. Every line carries exactly one of them, and the payload the
// type names — the schema enforces the pairing in both directions.
const (
	TypeRunStart    = "run-start"
	TypePhaseStart  = "phase-start"
	TypePhaseEnd    = "phase-end"
	TypeStage       = "stage"
	TypePrepare     = "prepare"
	TypeExec        = "exec"
	TypeMutantExec  = "mutant-exec"
	TypeProbeExec   = "probe-exec"
	TypeValidate    = "validate"
	TypeCoverageMap = "coverage-map"
	TypeCache       = "cache"
	TypeSnapshot    = "snapshot"
	TypeSweep       = "sweep"
	TypeArtifact    = "artifact"
	TypeNote        = "note"
	TypeRunEnd      = "run-end"
)

// What opened the recording. A CLI run and a library workspace are the two
// recorder owners, and they differ in what they can say about themselves: a run
// has a run id and an argument vector, a workspace has neither.
const (
	StartKindRun       = "run"
	StartKindWorkspace = "workspace"
)

// The engine phases. They are a sequence rather than a nesting: a phase says
// where the run was, and the run passes through them in this order.
const (
	PhaseDiscover = "discover"
	PhaseBaseline = "baseline"
	PhaseMutate   = "mutate"
	PhaseReport   = "report"
)

// The two states a stage or a preparation stage is recorded in. A step is one
// started/finished pair, so a step that was skipped is still a pair — which is
// what lets a reader tell an intentional omission from an event that was lost.
const (
	StateStarted  = "started"
	StateFinished = "finished"
)

// What became of a stage or a preparation stage.
const (
	ResultSucceeded = "succeeded"
	ResultFailed    = "failed"
	ResultSkipped   = "skipped"
)

// The library's preparation stages. They are spelled exactly as goatest spells
// them, and exactly as [github.com/P4suta/go-mutants.PreparePhase] does, so one
// timeline reads the same in both recordings.
const (
	PreparePhaseDiscovery          = "discovery"
	PreparePhaseProbeSnapshot      = "probe_snapshot"
	PreparePhaseMainValidation     = "main_validation"
	PreparePhaseMainRestoration    = "main_restoration"
	PreparePhaseVerification       = "verification"
	PreparePhaseBinaryBuild        = "binary_build"
	PreparePhaseProbeValidation    = "probe_validation"
	PreparePhaseProbeCoverageBuild = "probe_coverage_build"
	PreparePhaseProbeRestoration   = "probe_restoration"
)

// What a recorded command was. Every subprocess go-mutants starts is labelled
// with one of these, the schema enumerates them, and an unlabelled command is
// therefore a recording that does not validate rather than a command a reader
// has to guess about.
const (
	// ExecKindGoVersion resolves the toolchain: `go version`.
	ExecKindGoVersion = "go-version"
	// ExecKindScopeList expands the configured scope patterns into packages.
	ExecKindScopeList = "scope-list"
	// ExecKindBaselineBuild compiles the unmutated tree.
	ExecKindBaselineBuild = "baseline-build"
	// ExecKindBaselineTest is one unmutated observation of the test command.
	ExecKindBaselineTest = "baseline-test"
	// ExecKindInstrumentedBaseline runs the tests against the instrumented but
	// inactive tree, which is what proves instrumentation changed nothing.
	ExecKindInstrumentedBaseline = "instrumented-baseline"
	// ExecKindCovdataTextfmt converts a coverage directory into a profile.
	ExecKindCovdataTextfmt = "covdata-textfmt"
	// ExecKindGoList lists the packages a test binary set covers.
	ExecKindGoList = "go-list"
	// ExecKindGoTestC compiles one test binary: `go test -c`.
	ExecKindGoTestC = "go-test-c"
	// ExecKindCoverageRun is one profiling run of a test binary.
	ExecKindCoverageRun = "coverage-run"
	// ExecKindMutantRun is one test binary run with one mutant active.
	ExecKindMutantRun = "mutant-run"
	// ExecKindProbeRun is one test binary run against the probe tree.
	ExecKindProbeRun = "probe-run"
	// ExecKindValidateBuild is one compile of the instrumented tree during
	// validation or its bisection.
	ExecKindValidateBuild = "validate-build"
	// ExecKindWorkspaceExec is a command an embedder asked the workspace to
	// run for it.
	ExecKindWorkspaceExec = "workspace-exec"
	// ExecKindVerify re-checks the frozen tree before a session claims to
	// measure it.
	ExecKindVerify = "verify"
)

// ExecKinds returns every command label in this contract, in schema order.
//
// It exists so that a documentation test and the schema cannot drift apart
// silently: a kind added to the enum without a paragraph explaining it is a
// label a reader of a recording cannot act on.
func ExecKinds() []string {
	return []string{
		ExecKindGoVersion,
		ExecKindScopeList,
		ExecKindBaselineBuild,
		ExecKindBaselineTest,
		ExecKindInstrumentedBaseline,
		ExecKindCovdataTextfmt,
		ExecKindGoList,
		ExecKindGoTestC,
		ExecKindCoverageRun,
		ExecKindMutantRun,
		ExecKindProbeRun,
		ExecKindValidateBuild,
		ExecKindWorkspaceExec,
		ExecKindVerify,
	}
}

// What a mutant execution came to. The spelling is
// [github.com/P4suta/go-mutants.Outcome]'s, so a trace and a report say the
// same word about the same mutant.
const (
	OutcomeNotRun       = "not_run"
	OutcomeKilled       = "killed"
	OutcomeSurvived     = "survived"
	OutcomeTimedOut     = "timed_out"
	OutcomeInconclusive = "inconclusive"
	OutcomeErrored      = "errored"
)

// What a probe pass came to. Facts come from a measured pass alone.
const (
	ProbeOutcomeMeasured    = "measured"
	ProbeOutcomeTestFailed  = "test-failed"
	ProbeOutcomeTimedOut    = "timed-out"
	ProbeOutcomeUnavailable = "unavailable"
)

// Which tree a validation step was working on.
const (
	ValidateTreeMutant = "mutant"
	ValidateTreeProbe  = "probe"
)

// The validation and bisection steps.
const (
	// ValidateOpInstrument wrote the whole catalogue into the tree.
	ValidateOpInstrument = "instrument"
	// ValidateOpBuild compiled the tree once.
	ValidateOpBuild = "build"
	// ValidateOpGate found the tree broken with no mutant in it, which is a
	// failure validation refuses to bisect rather than blame on a candidate.
	ValidateOpGate = "gate"
	// ValidateOpIsolate searched one file for the largest subset of its
	// candidates that compiles.
	ValidateOpIsolate = "isolate"
	// ValidateOpReject removed one candidate, with the compiler's reason.
	ValidateOpReject = "reject"
	// ValidateOpDone closed validation with what it spent and decided.
	ValidateOpDone = "done"
)

// What a cache step was doing.
const (
	CacheOpOpen   = "open"
	CacheOpLookup = "lookup"
	CacheOpStore  = "store"
)

// What a cache step decided. A lookup answers hit, miss or corrupt; a store
// answers written, failed, not-cacheable or expected; opening answers opened or
// unavailable.
const (
	CacheResultHit          = "hit"
	CacheResultMiss         = "miss"
	CacheResultCorrupt      = "corrupt"
	CacheResultExpected     = "expected"
	CacheResultWritten      = "written"
	CacheResultFailed       = "failed"
	CacheResultNotCacheable = "not-cacheable"
	CacheResultUnavailable  = "unavailable"
	CacheResultOpened       = "opened"
)

// Which tree a snapshot froze.
const (
	SnapshotKindWorkspace = "workspace"
	SnapshotKindProbe     = "probe"
)

// The files and directories a run reports having written or kept.
const (
	ArtifactReportRun            = "report-run"
	ArtifactReportLatest         = "report-latest"
	ArtifactReportJSON           = "report-json"
	ArtifactReportHTML           = "report-html"
	ArtifactOverlayManifest      = "overlay-manifest"
	ArtifactProbeOverlayManifest = "probe-overlay-manifest"
	ArtifactKeptSnapshot         = "kept-snapshot"
	ArtifactKeptScratch          = "kept-scratch"
	ArtifactKeptProbeTree        = "kept-probe-tree"
	ArtifactKeptExecScratch      = "kept-exec-scratch"
	ArtifactCoverageProfile      = "coverage-profile"
	ArtifactDiagnostics          = "diagnostics"
	ArtifactTrace                = "trace"
)

// The notes a run leaves in its recording. None of them can change a verdict:
// a note is what the run could not do, said once, in the one place a reader
// looks for the account of the run.
const (
	// NoteWarning is a `GOMnnnn` warning the run also reported to its console.
	NoteWarning = "warning"
	// NoteTraceUnavailable is a trace directory that was refused or could not
	// be opened. The run went on recording in memory.
	NoteTraceUnavailable = "trace-unavailable"
	// NoteDiagnostics is a diagnostics bundle the run wrote.
	NoteDiagnostics = "diagnostics"
	// NoteDiagnosticsUnavailable is a bundle it could not write.
	NoteDiagnosticsUnavailable = "diagnostics-unavailable"
	// NoteTraceGC is what collection removed from the trace root.
	NoteTraceGC = "trace-gc"
	// NoteCoverageUnavailable is why a coverage-guided run had no coverage,
	// with the whole reason rather than its first line.
	NoteCoverageUnavailable = "coverage-unavailable"
	// NotePrepareFailed is the preparation stage a session died in.
	NotePrepareFailed = "prepare-failed"
)

// durationMS renders a duration the way every duration in this contract is
// recorded: whole milliseconds, never negative. A clock that went backwards is
// a diagnostic problem, not a reason to write a negative span the schema would
// reject.
func durationMS(d time.Duration) int64 {
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 0
}
