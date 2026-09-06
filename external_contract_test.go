// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExternalModuleCompilesAgainstTheEngineAPI is the consumer-side contract:
// the bridge must be usable from a different module without importing an
// internal package or relying on an in-repository test-only symbol. GOPROXY is
// disabled so this test also proves that compiling the bridge does not perform
// an implicit network operation once the module's declared dependencies exist.
func TestExternalModuleCompilesAgainstTheEngineAPI(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go toolchain is unavailable: %v", err)
	}
	repository, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	module := `module consumer.example/engineapi

go 1.26.0

require github.com/P4suta/go-mutants v0.0.0

replace github.com/P4suta/go-mutants => ` + filepath.ToSlash(repository) + "\n"
	source := `package engineapi_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
)

var (
	_ func(context.Context, string, ...gomutants.OpenOptions) (*gomutants.Workspace, error) = gomutants.Open
	_ func(*gomutants.Workspace, context.Context, gomutants.Command) (gomutants.CommandResult, error) = (*gomutants.Workspace).Exec
	_ func(*gomutants.Workspace, context.Context, gomutants.PrepareOptions) (*gomutants.Session, error) = (*gomutants.Workspace).Prepare
	_ func(*gomutants.Workspace) error = (*gomutants.Workspace).Close
	_ func(*gomutants.Workspace) gomutants.SweepResult = (*gomutants.Workspace).Swept
	_ func(*gomutants.Workspace) []string = (*gomutants.Workspace).Preserved
	_ func(*gomutants.Workspace) string = (*gomutants.Workspace).ToolchainVersion
	_ func(*gomutants.Session) gomutants.Catalog = (*gomutants.Session).Catalog
	_ func(*gomutants.Session, context.Context, gomutants.ExecRequest) (gomutants.MutantResult, error) = (*gomutants.Session).Exec
	_ func(*gomutants.Session, context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) = (*gomutants.Session).Probe
	_ func(*gomutants.Session) ([]gomutants.Change, error) = (*gomutants.Session).Changes
	_ func(*gomutants.Session) error = (*gomutants.Session).Close

	// The phase vocabulary is open, so a consumer that keeps a closed schema of
	// its own has to be able to read the list this build emits rather than
	// hard-code it. Pinning the function here is what makes that possible from
	// outside the module.
	_ func() []gomutants.PreparePhase = gomutants.KnownPreparePhases

	// PrepareOptions.Trace is part of the contract as a type and not only as a
	// name: a consumer stores its own recorder in it.
	_ func(gomutants.PrepareEvent) = gomutants.PrepareOptions{}.Trace

	// The diagnostic code lives in packages a consumer cannot import, so the
	// accessor is the contract.
	_ func(error) string = gomutants.DiagnosticCode

	// The notice a renderer styles and a consumer matched before there was a
	// flag to read. It stays exported and stays a string constant, so that the
	// consumers that were matching the text keep compiling — the flag beside
	// every capture is what they should be reading instead.
	_ string = gomutants.OutputTruncatedPrefix

	// Every typed error is used through the error interface, and every one of
	// them is reached with errors.As from outside this module.
	_ error = (*gomutants.MutantSelectionError)(nil)
	_ error = (*gomutants.DriftError)(nil)
	_ error = (*gomutants.VerificationError)(nil)
	_ error = (*gomutants.BuildError)(nil)
	_ error = (*gomutants.ExecutionError)(nil)
	_ error = (*gomutants.PackageNotPreparedError)(nil)
	_ error = (*gomutants.ReservedError)(nil)
)

// TestConsumerClassifiesEveryEngineFailure is the consumer-side half of the
// error contract: the sentinels are comparable with errors.Is, every typed
// error is reachable with errors.As through a wrapper of the consumer's own,
// and the fields a caller acts on are named.
//
// Every value here is built by hand, and that is the point. What is under test
// is what a consumer can *do* from outside this module — name these types,
// construct them for its own doubles, and classify them through its own
// wrapping — and not what any particular run produces, which the engine's own
// suite establishes. No message and no field value is asserted: a claim about
// what a hand-built value prints would be a claim about nothing.
func TestConsumerClassifiesEveryEngineFailure(t *testing.T) {
	for _, sentinel := range []error{
		gomutants.ErrWorkspaceClosed,
		gomutants.ErrWorkspacePrepared,
		gomutants.ErrSessionClosed,
		gomutants.ErrInvalidMutantID,
		gomutants.ErrMutantNotFound,
		gomutants.ErrAmbiguousMutant,
		gomutants.ErrMutantRejected,
		gomutants.ErrProbeNotPrepared,
		gomutants.ErrProbeInconsistent,
	} {
		if !errors.Is(fmt.Errorf("wrapped: %w", sentinel), sentinel) {
			t.Errorf("%v does not survive wrapping", sentinel)
		}
	}

	// The named fields, not only the types: these are what a consumer reads to
	// decide whether a failure is its user's or the engine's.
	_ = gomutants.MutantSelectionError{
		Prefix:    "",
		Reason:    nil,
		Matches:   nil,
		Rejection: nil,
	}
	_ = gomutants.DriftError{Stage: "", Changes: nil}
	_ = gomutants.VerificationError{
		Command:    gomutants.Command{},
		ExitCode:   0,
		TimedOut:   false,
		Duration:   0,
		Output:     nil,
		Truncated:  false,
		TotalBytes: 0,
	}
	_ = gomutants.BuildError{
		Phase:    gomutants.PreparePhaseBinaryBuild,
		Package:  "",
		Argv:     nil,
		ExitCode: 0,
		TimedOut: false,
		Output:   "",
		Code:     "",
	}
	_ = gomutants.ExecutionError{Call: "", Package: "", Code: "", Output: ""}
	_ = gomutants.PackageNotPreparedError{Call: "", Package: ""}
	_ = gomutants.ReservedError{Call: "", Flag: "", Variable: "", Owner: ""}

	var (
		selection *gomutants.MutantSelectionError
		drift     *gomutants.DriftError
		verify    *gomutants.VerificationError
		build     *gomutants.BuildError
		execution *gomutants.ExecutionError
		missing   *gomutants.PackageNotPreparedError
		reserved  *gomutants.ReservedError
	)
	failures := []error{
		fmt.Errorf("wrapped: %w", &gomutants.MutantSelectionError{Reason: gomutants.ErrMutantNotFound}),
		fmt.Errorf("wrapped: %w", &gomutants.DriftError{Stage: "commands"}),
		fmt.Errorf("wrapped: %w", &gomutants.VerificationError{ExitCode: 1}),
		fmt.Errorf("wrapped: %w", &gomutants.BuildError{Phase: gomutants.PreparePhaseDiscovery}),
		fmt.Errorf("wrapped: %w", &gomutants.ExecutionError{Call: "exec"}),
		fmt.Errorf("wrapped: %w", &gomutants.PackageNotPreparedError{Call: "probe"}),
		fmt.Errorf("wrapped: %w", &gomutants.ReservedError{Variable: "GO_MUTANTS_ACTIVE"}),
	}
	found := []bool{
		errors.As(failures[0], &selection),
		errors.As(failures[1], &drift),
		errors.As(failures[2], &verify),
		errors.As(failures[3], &build),
		errors.As(failures[4], &execution),
		errors.As(failures[5], &missing),
		errors.As(failures[6], &reserved),
	}
	for i, ok := range found {
		if !ok {
			t.Errorf("errors.As did not reach the typed error in %v", failures[i])
		}
	}
	if !errors.Is(failures[0], gomutants.ErrMutantNotFound) {
		t.Error("a selection error does not carry its reason through a wrap")
	}
	if gomutants.DiagnosticCode(errors.New("no code")) != "" {
		t.Error("DiagnosticCode invented a code for an error that carries none")
	}
}

func TestPublicDataTypes(t *testing.T) {
	_ = gomutants.OpenOptions{}
	_ = gomutants.Command{}
	_ = gomutants.CommandResult{}
	_ = gomutants.PrepareEvent{}
	_ = gomutants.PrepareOptions{}
	_ = gomutants.Catalog{}
	_ = gomutants.Mutant{}
	_ = gomutants.Rejection{}
	_ = gomutants.ExecRequest{}
	_ = gomutants.MutantResult{}
	_ = gomutants.ProbeRequest{}
	_ = gomutants.ProbeResult{}
	_ = gomutants.Artifact{}
	_ = gomutants.Change{}
	_ = gomutants.ErrProbeNotPrepared
	_ = gomutants.ErrProbeInconsistent

	// The two fields a consumer keys on. PreparedDigest is what evidence about a
	// prepared session is stored under, and EndLine is what a line range is
	// intersected with; both are read by name from outside this module, so the
	// name is the contract and not only the type.
	_ = gomutants.Catalog{PreparedDigest: ""}
	_ = gomutants.Mutant{EndLine: 0}

	// How much output a call is willing to hold, and what every result says
	// about what it could not keep. A consumer reads the flag rather than the
	// notice, so the names are the contract on all three result types at once.
	_ = gomutants.ExecRequest{OutputLimit: 0}
	_ = gomutants.ProbeRequest{OutputLimit: 0}
	_ = gomutants.CommandResult{Output: nil, Truncated: false, TotalBytes: 0}
	_ = gomutants.MutantResult{Output: nil, Truncated: false, TotalBytes: 0, OutputTail: ""}
	_ = gomutants.ProbeResult{Output: nil, Truncated: false, TotalBytes: 0}

	// The named fields, not only the type: a consumer reads these by name and a
	// rename is a breaking change whatever the shape of the struct stays.
	_ = gomutants.SweepResult{
		Removed:      nil,
		RemovedBytes: 0,
		Live:         0,
		Kept:         0,
		Err:          nil,
	}
	_ = gomutants.BranchProof{
		Direction:       gomutants.BranchDecreasing,
		BodyStartLine:   0,
		BodyStartColumn: 0,
		BodyEndLine:     0,
		BodyEndColumn:   0,
	}

	// Every phase this build emits. The vocabulary is open — a later engine may
	// emit one that is not here — so a consumer keeping a closed schema pins
	// the list rather than assuming it, and this is where the pin lives for a
	// consumer that cannot see the engine's own tests.
	_ = []gomutants.PreparePhase{
		gomutants.PreparePhaseDiscovery,
		gomutants.PreparePhaseProbeSnapshot,
		gomutants.PreparePhaseMainValidation,
		gomutants.PreparePhaseMainRestoration,
		gomutants.PreparePhaseVerification,
		gomutants.PreparePhaseBinaryBuild,
		gomutants.PreparePhaseProbeValidation,
		gomutants.PreparePhaseProbeCoverageBuild,
		gomutants.PreparePhaseProbeRestoration,
	}
	_ = []gomutants.PrepareEventState{
		gomutants.PrepareEventStarted,
		gomutants.PrepareEventFinished,
	}
	_ = []gomutants.PreparePhaseResult{
		gomutants.PreparePhaseSucceeded,
		gomutants.PreparePhaseFailed,
		gomutants.PreparePhaseSkipped,
	}
	_ = []gomutants.Outcome{
		gomutants.OutcomeNotRun,
		gomutants.OutcomeKilled,
		gomutants.OutcomeSurvived,
		gomutants.OutcomeTimedOut,
		gomutants.OutcomeInconclusive,
		gomutants.OutcomeErrored,
	}
	_ = []gomutants.ProbeOutcome{
		gomutants.ProbeMeasured,
		gomutants.ProbeTestFailed,
		gomutants.ProbeTimedOut,
		gomutants.ProbeUnavailable,
	}
	_ = []gomutants.ChangeKind{
		gomutants.ChangeAdded,
		gomutants.ChangeRemoved,
		gomutants.ChangeModified,
	}
}
`
	compileConsumer(t, goBinary, map[string]string{
		"go.mod":             module,
		"engine_api_test.go": source,
	})
}

// TestTracePackageIsPartOfThePublicContract is the same claim for the trace
// package.
//
// It is a separate consumer module rather than a few more lines in the one
// above, because the two surfaces are used by different callers for different
// reasons: an embedder reaches for the engine API, and a consumer that wants
// one timeline across two tools reaches for a trace sink. GOPROXY is disabled
// here too, which is also how the leaf claim is checked: the trace package is
// stdlib plus the embedded schema, so a dependency added to it would fail this
// test rather than somebody's build.
func TestTracePackageIsPartOfThePublicContract(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go toolchain is unavailable: %v", err)
	}
	repository, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	module := `module consumer.example/traceapi

go 1.26.0

require github.com/P4suta/go-mutants v0.0.0

replace github.com/P4suta/go-mutants => ` + filepath.ToSlash(repository) + "\n"
	source := `package traceapi_test

import (
	"io/fs"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/trace"
)

var (
	_ func(trace.Sink, func() time.Time, trace.StartRecord) *trace.Recorder = trace.New
	_ func(*trace.Recorder, string) func()                                  = (*trace.Recorder).PhaseStart
	_ func(*trace.Recorder, string, string) func(string)                    = (*trace.Recorder).Stage
	_ func(*trace.Recorder, string, string, string, time.Duration)          = (*trace.Recorder).Prepare
	_ func(*trace.Recorder, trace.ExecRecord) int64                         = (*trace.Recorder).Exec
	_ func(*trace.Recorder, trace.MutantRecord)                             = (*trace.Recorder).MutantExec
	_ func(*trace.Recorder, trace.ProbeRecord)                              = (*trace.Recorder).ProbeExec
	_ func(*trace.Recorder, trace.ValidateRecord)                           = (*trace.Recorder).Validate
	_ func(*trace.Recorder, trace.CoverageRecord)                           = (*trace.Recorder).Coverage
	_ func(*trace.Recorder, trace.CacheRecord)                              = (*trace.Recorder).Cache
	_ func(*trace.Recorder, trace.SnapshotRecord)                           = (*trace.Recorder).Snapshot
	_ func(*trace.Recorder, trace.SweepRecord)                              = (*trace.Recorder).Sweep
	_ func(*trace.Recorder, string, string)                                 = (*trace.Recorder).Artifact
	_ func(*trace.Recorder, string, string, string)                         = (*trace.Recorder).Note
	_ func(*trace.Recorder, string, int, error)                             = (*trace.Recorder).RunEnd
	_ func(trace.Event) trace.Event                                         = trace.Event.Clone

	_ func(string, string, trace.Filesystem) (*trace.DirSink, error) = trace.NewDirSink
	_ func(*trace.DirSink) string                                    = (*trace.DirSink).Directory
	_ func(int) *trace.MemorySink                                    = trace.NewMemorySink
	_ func(*trace.MemorySink) []trace.Event                          = (*trace.MemorySink).Events
	_ func(...trace.Sink) *trace.TeeSink                             = trace.NewTeeSink
	_ func(trace.Sink) trace.Sink                                    = trace.Digested

	_ func(string) ([]trace.Event, error)                = trace.Read
	_ func(string) (trace.Summary, error)                = trace.ReadSummary
	_ func(trace.Summary, trace.Summary) trace.SummaryDiff = trace.Diff
	_ func() []byte                                      = trace.JSONSchema
	_ func() []string                                    = trace.ExecKinds
)

// A consumer may implement the sink itself, which is the point of the
// interface: a recording can go wherever the embedder already sends its own.
type consumerSink struct{}

func (consumerSink) Emit(trace.Event) error { return nil }
func (consumerSink) Close() error           { return nil }
func (consumerSink) Dropped() int64         { return 0 }

var (
	_ trace.Sink    = consumerSink{}
	_ trace.Dropper = consumerSink{}
	_ trace.Sink    = (*trace.DirSink)(nil)
	_ trace.Sink    = (*trace.MemorySink)(nil)
	_ trace.Sink    = (*trace.TeeSink)(nil)
)

func TestPublicTraceTypes(t *testing.T) {
	_ = trace.Event{}
	_ = trace.StartRecord{}
	_ = trace.PhaseRecord{}
	_ = trace.StageRecord{}
	_ = trace.PrepareRecord{}
	_ = trace.ExecRecord{}
	_ = trace.MutantRecord{}
	_ = trace.ProbeRecord{}
	_ = trace.ValidateRecord{}
	_ = trace.CoverageRecord{}
	_ = trace.CacheRecord{}
	_ = trace.SnapshotRecord{}
	_ = trace.SweepRecord{}
	_ = trace.ArtifactRecord{}
	_ = trace.NoteRecord{}
	_ = trace.RunRecord{}
	_ = trace.Summary{}
	_ = trace.SummaryDiff{}
	_ = trace.ExecTally{}
	_ = trace.Filesystem{}

	_ = trace.SchemaV1
	_ = trace.FileName
	_ = trace.OutputDirectoryName
	_ = trace.OutputFileLimit
	_ = trace.TruncationMarker
	_ = trace.DefaultRingCapacity
	_ = trace.RetainRuns
	_ = trace.TypeRunStart
	_ = trace.TypePhaseStart
	_ = trace.TypePhaseEnd
	_ = trace.TypeStage
	_ = trace.TypePrepare
	_ = trace.TypeExec
	_ = trace.TypeMutantExec
	_ = trace.TypeProbeExec
	_ = trace.TypeValidate
	_ = trace.TypeCoverageMap
	_ = trace.TypeCache
	_ = trace.TypeSnapshot
	_ = trace.TypeSweep
	_ = trace.TypeArtifact
	_ = trace.TypeNote
	_ = trace.TypeRunEnd
	_ = trace.StartKindRun
	_ = trace.StartKindWorkspace
	_ = trace.PhaseDiscover
	_ = trace.PhaseBaseline
	_ = trace.PhaseMutate
	_ = trace.PhaseReport
	_ = trace.StateStarted
	_ = trace.StateFinished
	_ = trace.ResultSucceeded
	_ = trace.ResultFailed
	_ = trace.ResultSkipped
	_ = trace.PreparePhaseDiscovery
	_ = trace.PreparePhaseProbeRestoration
	_ = trace.ExecKindGoVersion
	_ = trace.ExecKindMutantRun
	_ = trace.ExecKindVerify
	_ = trace.OutcomeKilled
	_ = trace.ProbeOutcomeMeasured
	_ = trace.ValidateTreeMutant
	_ = trace.ValidateOpBuild
	_ = trace.CacheOpLookup
	_ = trace.CacheResultHit
	_ = trace.SnapshotKindWorkspace
	_ = trace.ArtifactReportJSON
	_ = trace.NoteWarning

	// The hooks a consumer's own filesystem would fill in.
	_ = trace.Filesystem{
		MkdirAll:   func(string, fs.FileMode) error { return nil },
		Mkdir:      func(string, fs.FileMode) error { return nil },
		OpenAppend: func(string, fs.FileMode) (trace.File, error) { return nil, nil },
		WriteFile:  func(string, []byte, fs.FileMode) error { return nil },
	}
}
`
	compileConsumer(t, goBinary, map[string]string{
		"go.mod":            module,
		"trace_api_test.go": source,
	})
}

// compileConsumer writes a synthetic consumer module and runs its tests with
// the module proxy disabled, so that a compile which needed a download fails
// here rather than in somebody's pipeline.
func compileConsumer(t *testing.T, goBinary string, files map[string]string) {
	t.Helper()
	consumer := t.TempDir()
	for name, contents := range files {
		if writeErr := os.WriteFile(filepath.Join(consumer, name), []byte(contents), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	command := exec.CommandContext(t.Context(), goBinary, "test", "-mod=mod", "./...")
	command.Dir = consumer
	command.Env = append(os.Environ(),
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GOSUMDB=off",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("external module did not compile with %s/%s: %v\n%s", runtime.GOOS, runtime.GOARCH, err, strings.TrimSpace(string(output)))
	}
}
