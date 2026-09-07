// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The consumer-side contract: a different module compiling and running against
// the published API.
//
// It writes a module, points it at this checkout with a replace directive and
// runs a real `go test` inside it, which is the only way to prove that the
// bridge is usable without an internal package — and is a full toolchain
// invocation per test, so it belongs in the integration tier. The file was
// named external_contract_test.go until the tiering.

package gomutants_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

replace github.com/P4suta/go-mutants => ` + strconv.Quote(filepath.ToSlash(repository)) + "\n"
	source := `package engineapi_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/trace"
)

var (
	_ func(context.Context, string, ...gomutants.OpenOptions) (*gomutants.Workspace, error) = gomutants.Open
	_ func(*gomutants.Workspace, context.Context, gomutants.Command) (gomutants.CommandResult, error) = (*gomutants.Workspace).Exec
	_ func(*gomutants.Workspace, context.Context, gomutants.PrepareOptions) (*gomutants.Session, error) = (*gomutants.Workspace).Prepare
	_ func(*gomutants.Workspace) error = (*gomutants.Workspace).Close
	_ func(*gomutants.Workspace) gomutants.SweepResult = (*gomutants.Workspace).Swept
	_ func(*gomutants.Workspace) []string = (*gomutants.Workspace).Preserved
	_ func(*gomutants.Workspace) string = (*gomutants.Workspace).ToolchainVersion

	// What the frozen module holds, answered by the library rather than by a
	// go list a consumer runs and parses itself. Every field is read by name
	// from outside this module, so the names are as much the contract as the
	// types are.
	_ func(*gomutants.Workspace, context.Context, gomutants.ModuleQuery) (gomutants.Module, error) = (*gomutants.Workspace).Module
	_ []string             = gomutants.ModuleQuery{}.Packages
	_ []string             = gomutants.ModuleQuery{}.Tags
	_ string               = gomutants.Module{}.Path
	_ string               = gomutants.Module{}.GoVersion
	_ string               = gomutants.Module{}.Toolchain
	_ []gomutants.Package  = gomutants.Module{}.Packages
	_ int64                = gomutants.Module{}.TraceSeq
	_ string               = gomutants.Package{}.ImportPath
	_ string               = gomutants.Package{}.Dir
	_ string               = gomutants.Package{}.Name
	_ bool                 = gomutants.Package{}.HasTests
	_ []string             = gomutants.Package{}.GoFiles
	_ []string             = gomutants.Package{}.TestGoFiles
	_ []string             = gomutants.Package{}.XTestGoFiles
	_ []string             = gomutants.Package{}.Imports
	_ []string             = gomutants.Package{}.Deps
	_ []string             = gomutants.Package{}.EmbedFiles

	// The recording half of the workspace. A consumer either hands Open a sink
	// of its own or reads the bounded ring back after Close, and both halves
	// are named from outside this module.
	_ trace.Sink = gomutants.OpenOptions{}.Trace
	_ func(*gomutants.Workspace) []trace.Event = (*gomutants.Workspace).Recording

	// The two paths a consumer needs to reproduce one execution by hand:
	// GOFLAGS=-overlay=<manifest>, in the session's snapshot.
	_ func(*gomutants.Session) string = (*gomutants.Session).OverlayManifest
	_ func(*gomutants.Session) string = (*gomutants.Session).ProbeOverlayManifest
	_ func(*gomutants.Session) gomutants.Catalog = (*gomutants.Session).Catalog
	_ func(*gomutants.Session, context.Context, gomutants.ExecRequest) (gomutants.MutantResult, error) = (*gomutants.Session).Exec
	_ func(*gomutants.Session, context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) = (*gomutants.Session).Probe

	// The control run: the original program through the same prepared binaries,
	// which is what a consumer used to open a second workspace for.
	_ func(*gomutants.Session, context.Context, gomutants.ControlRequest) (gomutants.ControlResult, error) = (*gomutants.Session).Control
	_ string        = gomutants.ControlRequest{}.Package
	_ []string      = gomutants.ControlRequest{}.Args
	_ []string      = gomutants.ControlRequest{}.Env
	_ time.Duration = gomutants.ControlRequest{}.Timeout
	_ int           = gomutants.ControlRequest{}.OutputLimit
	_ string        = gomutants.ControlResult{}.Package
	_ int           = gomutants.ControlResult{}.ExitCode
	_ bool          = gomutants.ControlResult{}.TimedOut
	_ time.Duration = gomutants.ControlResult{}.Duration
	_ []byte        = gomutants.ControlResult{}.Output
	_ bool          = gomutants.ControlResult{}.Truncated
	_ int64         = gomutants.ControlResult{}.TotalBytes
	_ []string      = gomutants.ControlResult{}.Binaries
	_ []int64       = gomutants.ControlResult{}.ExecSeqs
	_ int64         = gomutants.ControlResult{}.TraceSeq

	// The memory half of the paired budget. Every request that starts a test
	// binary can bound one, every result says what it cost and whether the
	// bound stopped it, and a workspace command — which has no derived bound —
	// can bound itself. A consumer that renders "killed by memory" reads
	// MemoryExceeded rather than parsing a message.
	_ int64 = gomutants.Command{}.MemoryLimit
	_ int64 = gomutants.CommandResult{}.PeakMemory
	_ bool  = gomutants.CommandResult{}.MemoryExceeded
	_ int64 = gomutants.ExecRequest{}.MemoryLimit
	_ int64 = gomutants.MutantResult{}.PeakMemory
	_ bool  = gomutants.MutantResult{}.MemoryExceeded
	_ int64 = gomutants.ProbeRequest{}.MemoryLimit
	_ int64 = gomutants.ProbeResult{}.PeakMemory
	_ bool  = gomutants.ProbeResult{}.MemoryExceeded
	_ int64 = gomutants.ControlRequest{}.MemoryLimit
	_ int64 = gomutants.ControlResult{}.PeakMemory
	_ bool  = gomutants.ControlResult{}.MemoryExceeded

	// What a target consulted. A consumer keeping evidence about a (mutant,
	// target) pair reads the log to decide whether that evidence is still about
	// today's repository, so every name here is one it stores and looks up
	// again — and the sentinel is what tells it that a binary refused the flag
	// rather than that a suite went red.
	_ bool                     = gomutants.ExecRequest{}.RecordTestLog
	_ bool                     = gomutants.ProbeRequest{}.RecordTestLog
	_ bool                     = gomutants.ControlRequest{}.RecordTestLog
	_ []gomutants.TestLog      = gomutants.MutantResult{}.TestLogs
	_ []gomutants.TestLog      = gomutants.ProbeResult{}.TestLogs
	_ []gomutants.TestLog      = gomutants.ControlResult{}.TestLogs
	_ string                   = gomutants.TestLog{}.Package
	_ string                   = gomutants.TestLog{}.Dir
	_ []gomutants.TestLogEntry = gomutants.TestLog{}.Entries
	_ bool                     = gomutants.TestLog{}.Complete
	_ string                   = gomutants.TestLog{}.Err
	_ gomutants.TestLogOp      = gomutants.TestLogEntry{}.Op
	_ string                   = gomutants.TestLogEntry{}.Name
	_ error                    = gomutants.ErrTestLogUnsupported

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

	// Which engine build a consumer is running. Every consumer used to write
	// this scan over runtime/debug itself, so the accessors are the contract.
	_ func() (gomutants.BuildInfo, bool) = gomutants.ReadBuildInfo
	_ func() string                      = gomutants.Version

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

// Two constants, declared as constants: a package variable of type string
// would satisfy a var assignment just as well, and a consumer is free to use
// either in a constant expression or a switch case.
//
// OutputTruncatedPrefix is the notice a renderer styles and a consumer matched
// before there was a flag to read; it stays exported and stays a constant so
// that the consumers matching the text keep compiling. ModulePath's value is
// what a consumer keeping its own build-info scan matches entries against.
const (
	_ string = gomutants.OutputTruncatedPrefix
	_ string = gomutants.ModulePath
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
		gomutants.ErrPrepareFailed,
		gomutants.ErrSessionClosed,
		gomutants.ErrInvalidMutantID,
		gomutants.ErrMutantNotFound,
		gomutants.ErrAmbiguousMutant,
		gomutants.ErrMutantRejected,
		gomutants.ErrProbeNotPrepared,
		gomutants.ErrProbeInconsistent,
		gomutants.ErrTestLogUnsupported,
		gomutants.ErrInvalidSelection,
		gomutants.ErrInvalidQuery,
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

// TestTheEngineIsAbsentFromAConsumerTestBinary is the surprise, pinned where a
// consumer will meet it.
//
// The go command fills build information in before a test binary's imports are
// known, so a test binary records its main module and no dep lines at all --
// go version -m on one proves it. From inside this consumer's own go test,
// therefore, the engine it requires and links is simply not named: the reading
// succeeds, the answer is the zero value, and Version() says "unknown". A
// consumer that wants to record which engine produced its evidence has to read
// it from a built program, which the engine module's own suite does.
func TestTheEngineIsAbsentFromAConsumerTestBinary(t *testing.T) {
	if gomutants.ModulePath != "github.com/P4suta/go-mutants" {
		t.Errorf("ModulePath = %q; a consumer scanning build info for it would find nothing", gomutants.ModulePath)
	}
	info, ok := gomutants.ReadBuildInfo()
	if !ok {
		t.Fatal("a test binary the go command built carries no build information at all")
	}
	if info != (gomutants.BuildInfo{}) {
		t.Errorf("a test binary named the engine: %+v", info)
	}
	if got := gomutants.Version(); got != "unknown" {
		t.Errorf("Version() = %q, want unknown", got)
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
	_ = gomutants.TestLog{}
	_ = gomutants.TestLogEntry{}
	_ = gomutants.ErrProbeNotPrepared
	_ = gomutants.ErrProbeInconsistent
	_ = gomutants.ErrTestLogUnsupported
	_ = gomutants.Selection{}
	_ = gomutants.LineRange{}
	_ = gomutants.ErrInvalidSelection
	_ = gomutants.ModuleQuery{}
	_ = gomutants.Module{}
	_ = gomutants.Package{}
	_ = gomutants.ErrInvalidQuery

	// The two fields a consumer keys on. PreparedDigest is what evidence about a
	// prepared session is stored under, and EndLine is what a line range is
	// intersected with; both are read by name from outside this module, so the
	// name is the contract and not only the type.
	_ = gomutants.Catalog{PreparedDigest: ""}
	_ = gomutants.Mutant{EndLine: 0}

	// Selecting by line range, end to end: the ranges a consumer asks for, the
	// per-mutant answer it reads, and the normalised copy the catalogue hands
	// back. All three are read by name — a consumer builds the request, branches
	// on Selected, and reports Selection beside a score — so the names are as
	// much the contract as the types.
	_ = gomutants.LineRange{First: 0, Last: 0}
	_ = gomutants.Selection{Lines: map[string][]gomutants.LineRange{
		"internal/alpha/alpha.go": {{First: 12, Last: 20}},
	}}
	_ = gomutants.PrepareOptions{Selection: nil}
	_ = gomutants.Mutant{Selected: false}
	_ = gomutants.Catalog{Selection: nil}

	// How much output a call is willing to hold, and what every result says
	// about what it could not keep. A consumer reads the flag rather than the
	// notice, so the names are the contract on all three result types at once.
	_ = gomutants.ExecRequest{OutputLimit: 0}
	_ = gomutants.ProbeRequest{OutputLimit: 0}
	_ = gomutants.CommandResult{Output: nil, Truncated: false, TotalBytes: 0}
	_ = gomutants.MutantResult{Output: nil, Truncated: false, TotalBytes: 0, OutputTail: ""}
	_ = gomutants.ProbeResult{Output: nil, Truncated: false, TotalBytes: 0}

	// Every field of the build identity, by name: a consumer records what it
	// linked and decides on Auditable, so a rename here changes what somebody's
	// stored evidence is keyed on.
	_ = gomutants.BuildInfo{
		Version:        "",
		Sum:            "",
		Replaced:       false,
		ReplacePath:    "",
		ReplaceVersion: "",
		Main:           false,
		VCSRevision:    "",
		VCSModified:    false,
		Auditable:      false,
	}

	// The join into a recording, and the binaries a result was measured
	// against. A consumer records its own trace and pairs the two on TraceSeq,
	// so the names and the types are both the contract.
	var (
		_ int64    = gomutants.CommandResult{}.TraceSeq
		_ []string = gomutants.MutantResult{}.Binaries
		_ int64    = gomutants.MutantResult{}.TraceSeq
		_ []string = gomutants.ProbeResult{}.Binaries
		_ int64    = gomutants.ProbeResult{}.TraceSeq
	)

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

	// The action-log vocabulary. It is open — a later Go release may report a
	// kind of access this build has never heard of, and the engine carries it
	// through verbatim — so a consumer with a closed schema pins today's four
	// here rather than assuming them.
	_ = []gomutants.TestLogOp{
		gomutants.TestLogGetenv,
		gomutants.TestLogOpen,
		gomutants.TestLogStat,
		gomutants.TestLogChdir,
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

replace github.com/P4suta/go-mutants => ` + strconv.Quote(filepath.ToSlash(repository)) + "\n"
	source := `package traceapi_test

import (
	"io/fs"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/trace"
)

var (
	_ func(trace.Sink, func() time.Time, trace.StartRecord) *trace.Recorder = trace.New
	_ func(*trace.Recorder, string) func() time.Duration                    = (*trace.Recorder).PhaseStart
	_ func(*trace.Recorder, string, string) func(string) time.Duration      = (*trace.Recorder).Stage
	_ func(*trace.Recorder, string, string, string, time.Duration)          = (*trace.Recorder).Prepare
	_ func(*trace.Recorder, trace.ExecRecord) int64                         = (*trace.Recorder).Exec
	_ func(*trace.Recorder, trace.MutantRecord) int64                       = (*trace.Recorder).MutantExec
	_ func(*trace.Recorder, trace.ProbeRecord) int64                        = (*trace.Recorder).ProbeExec
	_ func(*trace.Recorder, trace.ValidateRecord)                           = (*trace.Recorder).Validate
	_ func(*trace.Recorder, trace.CoverageRecord)                           = (*trace.Recorder).Coverage
	_ func(*trace.Recorder, trace.CacheRecord)                              = (*trace.Recorder).Cache
	_ func(*trace.Recorder, trace.SnapshotRecord)                           = (*trace.Recorder).Snapshot
	_ func(*trace.Recorder, trace.SweepRecord)                              = (*trace.Recorder).Sweep
	_ func(*trace.Recorder, string, string)                                 = (*trace.Recorder).Artifact
	// Note returns the sequence it recorded at, as the three Exec recorders do:
	// a control run has no payload of its own in this contract and is
	// summarised by a note, so ControlResult.TraceSeq needs one to point at.
	_ func(*trace.Recorder, string, string, string) int64                   = (*trace.Recorder).Note
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

// TestABuiltConsumerNamesTheEngineItLinked is the identity contract, proved
// the only way it can be: by building a consumer program and running it.
//
// A test binary names no dependency (see
// TestTheEngineIsAbsentFromAConsumerTestBinary), so an assertion made inside
// one would hold just as well against a stub that returned nothing. This
// builds a real program against a directory replacement -- the way every
// consumer develops against an unreleased engine -- and reads what it says
// about itself. That case is also the one the contract is sharpest about: the
// require line names v0.0.0 and the code that compiled is a working tree, so
// the engine must report the replacement and refuse to call itself auditable,
// leaving the identity of the running bytes to the consumer.
func TestABuiltConsumerNamesTheEngineItLinked(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go toolchain is unavailable: %v", err)
	}
	repository, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	module := `module consumer.example/identity

go 1.26.0

require github.com/P4suta/go-mutants v0.0.0

replace github.com/P4suta/go-mutants => ` + strconv.Quote(filepath.ToSlash(repository)) + "\n"
	program := `package main

import (
	"encoding/json"
	"os"

	gomutants "github.com/P4suta/go-mutants"
)

func main() {
	info, ok := gomutants.ReadBuildInfo()
	record := struct {
		OK             bool
		Version        string
		Sum            string
		Replaced       bool
		ReplacePath    string
		ReplaceVersion string
		Main           bool
		VCSRevision    string
		VCSModified    bool
		Auditable      bool
		Reported       string
		ModulePath     string
	}{
		OK:             ok,
		Version:        info.Version,
		Sum:            info.Sum,
		Replaced:       info.Replaced,
		ReplacePath:    info.ReplacePath,
		ReplaceVersion: info.ReplaceVersion,
		Main:           info.Main,
		VCSRevision:    info.VCSRevision,
		VCSModified:    info.VCSModified,
		Auditable:      info.Auditable,
		Reported:       gomutants.Version(),
		ModulePath:     gomutants.ModulePath,
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(record); encodeErr != nil {
		os.Exit(1)
	}
}
`
	consumer := writeConsumer(t, map[string]string{
		"go.mod":               module,
		"cmd/identity/main.go": program,
	})
	binary := filepath.Join(consumer, "identity")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// -buildvcs=false: what the consumer program says about *itself* is not
	// under test, and a temporary directory that happens to sit under somebody
	// else's repository would otherwise make this a test of that repository's
	// VCS status.
	build := exec.CommandContext(t.Context(), goBinary, "build", "-mod=mod", "-buildvcs=false", "-o", binary, "./cmd/identity")
	build.Dir = consumer
	build.Env = consumerEnvironment()
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("consumer program did not build with %s/%s: %v\n%s", runtime.GOOS, runtime.GOARCH, buildErr, strings.TrimSpace(string(output)))
	}
	run := exec.CommandContext(t.Context(), binary)
	run.Dir = consumer
	output, runErr := run.Output()
	if runErr != nil {
		t.Fatalf("consumer program failed: %v\n%s", runErr, strings.TrimSpace(string(output)))
	}
	var identity struct {
		OK             bool
		Version        string
		Sum            string
		Replaced       bool
		ReplacePath    string
		ReplaceVersion string
		Main           bool
		VCSRevision    string
		VCSModified    bool
		Auditable      bool
		Reported       string
		ModulePath     string
	}
	if decodeErr := json.Unmarshal(output, &identity); decodeErr != nil {
		t.Fatalf("consumer program printed %q: %v", output, decodeErr)
	}
	if !identity.OK {
		t.Fatalf("a program the go command built carries no build information: %+v", identity)
	}
	if identity.ModulePath != "github.com/P4suta/go-mutants" {
		t.Errorf("ModulePath = %q; a consumer scanning build info for it would find nothing", identity.ModulePath)
	}
	if !identity.Replaced {
		t.Errorf("Replaced = false under a directory replacement: %+v", identity)
	}
	if !sameDirectory(identity.ReplacePath, repository) {
		t.Errorf("ReplacePath = %q, want %q", identity.ReplacePath, repository)
	}
	// A directory has no version of its own, and the go command writes its
	// placeholder rather than an empty string.
	if identity.ReplaceVersion != "(devel)" {
		t.Errorf("ReplaceVersion = %q, want (devel)", identity.ReplaceVersion)
	}
	if identity.Version != "v0.0.0" || identity.Reported != "v0.0.0" {
		t.Errorf("Version = %q and Version() = %q, want the required version twice", identity.Version, identity.Reported)
	}
	if identity.Sum != "" {
		t.Errorf("Sum = %q under a replacement; that checksum would cover the replacement", identity.Sum)
	}
	if identity.Main {
		t.Errorf("Main = true for an engine a consumer imported: %+v", identity)
	}
	if identity.Auditable {
		t.Errorf("a directory-replaced engine reported itself auditable: %+v", identity)
	}
}

// sameDirectory compares two paths the way a test on three operating systems
// has to: the go command records a cleaned absolute path, and a temporary
// directory reached through a symlink (macOS /var, for one) is the same
// directory under two names.
func sameDirectory(recorded, want string) bool {
	recorded = filepath.Clean(filepath.FromSlash(recorded))
	want = filepath.Clean(want)
	if recorded == want {
		return true
	}
	resolvedRecorded, recordedErr := filepath.EvalSymlinks(recorded)
	resolvedWant, wantErr := filepath.EvalSymlinks(want)
	return recordedErr == nil && wantErr == nil && resolvedRecorded == resolvedWant
}

// consumerEnvironment is what every synthetic consumer builds under. GOPROXY
// is disabled so that a compile which needed a download fails here rather than
// in somebody's pipeline.
func consumerEnvironment() []string {
	return append(os.Environ(),
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GOSUMDB=off",
	)
}

// writeConsumer writes a synthetic consumer module into a temporary directory
// and returns it. File names are slash-separated and may name a subdirectory.
func writeConsumer(t *testing.T, files map[string]string) string {
	t.Helper()
	consumer := t.TempDir()
	for name, contents := range files {
		path := filepath.Join(consumer, filepath.FromSlash(name))
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(path, []byte(contents), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	return consumer
}

// compileConsumer writes a synthetic consumer module and runs its tests with
// the module proxy disabled, so that a compile which needed a download fails
// here rather than in somebody's pipeline.
func compileConsumer(t *testing.T, goBinary string, files map[string]string) {
	t.Helper()
	consumer := writeConsumer(t, files)
	command := exec.CommandContext(t.Context(), goBinary, "test", "-mod=mod", "./...")
	command.Dir = consumer
	command.Env = consumerEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("external module did not compile with %s/%s: %v\n%s", runtime.GOOS, runtime.GOARCH, err, strings.TrimSpace(string(output)))
	}
}
