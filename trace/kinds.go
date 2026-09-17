// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import "time"

const SchemaV1 = "gomutants-trace-v1"

const (
	FileName = "trace.jsonl"

	OutputDirectoryName = "output"

	OutputFileLimit = 1 << 20

	TruncationMarker = "..."

	DefaultRingCapacity = 4096

	RetainRuns = 10
)

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

const (
	StartKindRun       = "run"
	StartKindWorkspace = "workspace"
)

const (
	PhaseDiscover = "discover"
	PhaseBaseline = "baseline"
	PhaseMutate   = "mutate"
	PhaseReport   = "report"
)

const (
	StateStarted  = "started"
	StateFinished = "finished"
)

const (
	ResultSucceeded = "succeeded"
	ResultFailed    = "failed"
	ResultSkipped   = "skipped"
)

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

const (
	ExecKindGoVersion            = "go-version"
	ExecKindScopeList            = "scope-list"
	ExecKindBaselineBuild        = "baseline-build"
	ExecKindBaselineTest         = "baseline-test"
	ExecKindInstrumentedBaseline = "instrumented-baseline"
	ExecKindCovdataTextfmt       = "covdata-textfmt"
	ExecKindGoList               = "go-list"
	ExecKindGoTestC              = "go-test-c"
	ExecKindTestList             = "test-list"
	ExecKindCoverageRun          = "coverage-run"
	ExecKindMutantRun            = "mutant-run"
	ExecKindProbeRun             = "probe-run"
	ExecKindControlRun           = "control-run"
	ExecKindValidateBuild        = "validate-build"
	ExecKindWorkspaceExec        = "workspace-exec"
	ExecKindVerify               = "verify"
)

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
		ExecKindTestList,
		ExecKindCoverageRun,
		ExecKindMutantRun,
		ExecKindProbeRun,
		ExecKindControlRun,
		ExecKindValidateBuild,
		ExecKindWorkspaceExec,
		ExecKindVerify,
	}
}

const (
	OutcomeNotRun       = "not_run"
	OutcomeKilled       = "killed"
	OutcomeSurvived     = "survived"
	OutcomeTimedOut     = "timed_out"
	OutcomeInconclusive = "inconclusive"
	OutcomeErrored      = "errored"
)

const (
	ProbeOutcomeMeasured    = "measured"
	ProbeOutcomeTestFailed  = "test-failed"
	ProbeOutcomeTimedOut    = "timed-out"
	ProbeOutcomeUnavailable = "unavailable"
)

const (
	ValidateTreeMutant = "mutant"
	ValidateTreeProbe  = "probe"
)

const (
	ValidateOpInstrument = "instrument"
	ValidateOpBuild      = "build"
	ValidateOpGate       = "gate"
	ValidateOpIsolate    = "isolate"
	ValidateOpReject     = "reject"
	ValidateOpDone       = "done"
)

const (
	CacheOpOpen   = "open"
	CacheOpLookup = "lookup"
	CacheOpStore  = "store"
)

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

const (
	SnapshotKindWorkspace = "workspace"
	SnapshotKindProbe     = "probe"
	SnapshotKindWorker    = "worker"
)

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

const (
	NoteWarning                = "warning"
	NoteTraceUnavailable       = "trace-unavailable"
	NoteDiagnostics            = "diagnostics"
	NoteDiagnosticsUnavailable = "diagnostics-unavailable"
	NoteWorkerRestored         = "worker-restored"

	NoteProbeTreeRestored   = "probe-tree-restored"
	NoteTraceGC             = "trace-gc"
	NoteCoverageUnavailable = "coverage-unavailable"
	NotePrepareFailed       = "prepare-failed"
	NoteOrderDependentTests = "order-dependent-tests"
	NoteUnreliableTestSet   = "unreliable-test-set"
	NoteControl             = "control"
)

func durationMS(d time.Duration) int64 {
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 0
}
