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
	"testing"

	gomutants "github.com/P4suta/go-mutants"
)

var (
	_ func(context.Context, string, ...gomutants.OpenOptions) (*gomutants.Workspace, error) = gomutants.Open
	_ func(*gomutants.Workspace, context.Context, gomutants.Command) (gomutants.CommandResult, error) = (*gomutants.Workspace).Exec
	_ func(*gomutants.Workspace, context.Context, gomutants.PrepareOptions) (*gomutants.Session, error) = (*gomutants.Workspace).Prepare
	_ func(*gomutants.Workspace) error = (*gomutants.Workspace).Close
	_ func(*gomutants.Session) gomutants.Catalog = (*gomutants.Session).Catalog
	_ func(*gomutants.Session, context.Context, gomutants.ExecRequest) (gomutants.MutantResult, error) = (*gomutants.Session).Exec
	_ func(*gomutants.Session, context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) = (*gomutants.Session).Probe
	_ func(*gomutants.Session) ([]gomutants.Change, error) = (*gomutants.Session).Changes
	_ func(*gomutants.Session) error = (*gomutants.Session).Close
)

func TestPublicDataTypes(t *testing.T) {
	_ = gomutants.OpenOptions{}
	_ = gomutants.Command{}
	_ = gomutants.CommandResult{}
	_ = gomutants.PrepareEvent{}
	_ = gomutants.PreparePhaseDiscovery
	_ = gomutants.PrepareEventStarted
	_ = gomutants.PreparePhaseSucceeded
	_ = gomutants.PrepareOptions{}
	_ = gomutants.Catalog{}
	_ = gomutants.Mutant{}
	_ = gomutants.BranchProof{}
	_ = gomutants.BranchDecreasing
	_ = gomutants.Rejection{}
	_ = gomutants.ExecRequest{}
	_ = gomutants.MutantResult{}
	_ = gomutants.ProbeRequest{}
	_ = gomutants.ProbeResult{}
	_ = gomutants.Artifact{}
	_ = gomutants.Change{}
	_ = gomutants.OutcomeKilled
	_ = gomutants.ProbeMeasured
	_ = gomutants.ProbeTestFailed
	_ = gomutants.ProbeTimedOut
	_ = gomutants.ProbeUnavailable
	_ = gomutants.ErrProbeNotPrepared
	_ = gomutants.ChangeAdded
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
