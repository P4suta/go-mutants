// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func TestPreparedBaselineRequestAndResultKeepExactProtocol(t *testing.T) {
	target := BaselineTarget{
		Target:      baselineTestTarget("TestValue/a+b"),
		Environment: []string{"RESOURCE=ready"},
	}
	for _, test := range []struct {
		name   string
		framed bool
		want   []string
	}{
		{
			name: "plain",
			want: []string{"-test.run=^TestValue/a\\+b$", "-test.coverprofile=value.cover", "-test.count=1", "-test.short=true"},
		},
		{
			name: "framed", framed: true,
			want: []string{"-test.v=test2json", "-test.run=^TestValue/a\\+b$", "-test.coverprofile=value.cover", "-test.count=1", "-test.short=true"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := preparedBaselineTargetRequest("fixture.example/module", "value.cover", target, 3*time.Second, BaselineOptions{
				UseTestFraming: test.framed,
				TestArgs:       []string{"-test.short=true"},
			})
			if request.Package != "fixture.example/module" || !slices.Equal(request.Args, test.want) ||
				!slices.Equal(request.Env, target.Environment) || request.Timeout != 3*time.Second {
				t.Fatalf("request = %+v, want args %q", request, test.want)
			}
			target.Environment[0] = "MUTATED=yes"
			if request.Env[0] != "RESOURCE=ready" {
				t.Fatal("request aliases the target environment")
			}
			target.Environment[0] = "RESOURCE=ready"
		})
	}
	for _, test := range []struct {
		outcome gomutants.ProbeOutcome
		want    bool
	}{
		{outcome: gomutants.ProbeMeasured},
		{outcome: gomutants.ProbeTimedOut, want: true},
		{outcome: gomutants.ProbeTestFailed},
	} {
		got := commandResultFromProbe(gomutants.ProbeResult{
			ExitCode: 2, Outcome: test.outcome, Duration: time.Second, Output: []byte("output"),
		})
		if got.ExitCode != 2 || got.TimedOut != test.want || got.Duration != time.Second || string(got.Output) != "output" {
			t.Fatalf("command result for %q = %+v", test.outcome, got)
		}
	}
}

func TestValidatedBaselineProbeAcceptsOnlyMeasuredKnownIndices(t *testing.T) {
	indices := map[uint32]struct{}{1: {}, 3: {}}
	for _, test := range []struct {
		name     string
		result   gomutants.ProbeResult
		probed   bool
		infected []uint32
	}{
		{name: "unmeasured", result: gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut, Infected: []uint32{1}}},
		{name: "unknown", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{2}}},
		{name: "known", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{3, 1, 3}}, probed: true, infected: []uint32{1, 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			probed, infected := validatedBaselineProbe(test.result, indices)
			if probed != test.probed || !reflect.DeepEqual(infected, test.infected) {
				t.Fatalf("validated probe = (%t, %v), want (%t, %v)", probed, infected, test.probed, test.infected)
			}
		})
	}
}

func TestExecuteBaselineTargetDecidesFramingSkipFailureAndSuccess(t *testing.T) {
	target := BaselineTarget{Target: baselineTestTarget("TestValue")}
	coverage := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
	for _, test := range []struct {
		name       string
		framed     bool
		result     gomutants.CommandResult
		write      string
		wantStatus string
		wantError  string
	}{
		{name: "plain success", write: coverage, wantStatus: "passed"},
		{name: "framing error", framed: true, result: gomutants.CommandResult{Output: []byte(commandOutputTruncatedPrefix)}, wantError: "classify framed test output"},
		{name: "unframed ignores classification error", result: gomutants.CommandResult{Output: []byte(commandOutputTruncatedPrefix)}, write: coverage, wantStatus: "passed"},
		{name: "skip", framed: true, result: gomutants.CommandResult{Output: []byte("\x16--- SKIP: TestValue\n")}, wantStatus: "skipped"},
		{name: "timeout", result: gomutants.CommandResult{TimedOut: true, Duration: time.Second}, wantStatus: "failed"},
		{name: "failed", result: gomutants.CommandResult{ExitCode: 2, Duration: time.Second}, wantStatus: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifactDirectory := t.TempDir()
			workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
				hasFrame := slices.Contains(command.Argv, "-test.v=test2json")
				if hasFrame != test.framed {
					t.Fatalf("framed command = %t, want %t: %q", hasFrame, test.framed, command.Argv)
				}
				if test.write != "" {
					if err := os.WriteFile(coverageProfileArgument(command), []byte(test.write), filemode.PrivateFile); err != nil {
						t.Fatal(err)
					}
				}
				return test.result, nil
			}}
			run := executeBaselineTarget(t.Context(), workspace, "fixture.example/module", "fixture.example/module", ".", "module.test", target, time.Second, BaselineOptions{
				ArtifactDirectory: artifactDirectory,
				UseTestFraming:    test.framed,
			})
			if test.wantError != "" {
				if run.err == nil || !strings.Contains(run.err.Error(), test.wantError) {
					t.Fatalf("run error = %v, want %q", run.err, test.wantError)
				}
				return
			}
			if run.err != nil || run.unit.Inventory.Status != test.wantStatus {
				t.Fatalf("run = %+v, err=%v, want status %q", run, run.err, test.wantStatus)
			}
			wantExecuted := test.wantStatus != "not-run"
			wantSkipped := test.wantStatus == "skipped"
			if run.unit.Executed != wantExecuted || run.unit.Skipped != wantSkipped {
				t.Fatalf("unit flags = executed %t skipped %t", run.unit.Executed, run.unit.Skipped)
			}
		})
	}
}

func TestExecutePreparedBaselineTargetDecidesEveryTerminal(t *testing.T) {
	target := BaselineTarget{Target: baselineTestTarget("TestValue")}
	coverage := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
	sentinel := errors.New("probe failed")
	for _, test := range []struct {
		name       string
		result     gomutants.ProbeResult
		err        error
		write      string
		framed     bool
		wantStatus string
		wantError  string
		wholeTree  bool
	}{
		{name: "probe error", err: sentinel, wantError: "baseline target TestValue"},
		{name: "framing error", framed: true, result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Output: []byte(commandOutputTruncatedPrefix)}, wantError: "classify framed test output"},
		{name: "skip", framed: true, result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Output: []byte("\x16--- SKIP: TestValue\n")}, wantStatus: "skipped"},
		{name: "unmeasured", result: gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut}, wantStatus: "failed"},
		{name: "missing coverage", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, wantError: "read coverage for TestValue"},
		{name: "invalid coverage", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, write: "invalid", wantError: "coverage for TestValue"},
		{name: "success", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{7}}, write: coverage, wantStatus: "passed", wholeTree: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifactDirectory := t.TempDir()
			session := &mutationUnitSession{probe: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				if test.write != "" {
					profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
					if err := os.WriteFile(profile, []byte(test.write), filemode.PrivateFile); err != nil {
						t.Fatal(err)
					}
				}
				return test.result, test.err
			}}
			var observer *RepositoryObserver
			if test.wholeTree {
				observer = newRepositoryObserver(t.TempDir(), t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
					"fixture.example/module": {Unobservable: true},
				}, targetKeySources{})
			}
			run := executePreparedBaselineTarget(t.Context(), session, "fixture.example/module", "fixture.example/module", target, time.Second, BaselineOptions{
				ArtifactDirectory:  artifactDirectory,
				UseTestFraming:     test.framed,
				RepositoryObserver: observer,
				probeIndices:       map[uint32]struct{}{7: {}},
			})
			if test.wantError != "" {
				if run.err == nil || !strings.Contains(run.err.Error(), test.wantError) {
					t.Fatalf("run error = %v, want %q", run.err, test.wantError)
				}
				return
			}
			if run.err != nil || run.unit.Inventory.Status != test.wantStatus {
				t.Fatalf("run = %+v, err=%v, want status %q", run, run.err, test.wantStatus)
			}
			if test.wantStatus == "passed" && (run.evidence == nil || !run.evidence.Probed || !run.evidence.WholeTree || !slices.Equal(run.evidence.Infected, []uint32{7})) {
				t.Fatalf("passed evidence = %+v", run.evidence)
			}
		})
	}
}

func TestPackageSuiteCoverageDecidesEveryUnpreparedTerminal(t *testing.T) {
	coverage := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
	sentinel := errors.New("suite failed")
	for _, test := range []struct {
		name        string
		result      gomutants.CommandResult
		err         error
		write       string
		wantOutcome gomutants.ProbeOutcome
		wantError   string
	}{
		{name: "exec error", err: sentinel, wantOutcome: gomutants.ProbeUnavailable, wantError: "baseline package suite"},
		{name: "timeout", result: gomutants.CommandResult{TimedOut: true}, wantOutcome: gomutants.ProbeTimedOut},
		{name: "test failure", result: gomutants.CommandResult{ExitCode: 1}, wantOutcome: gomutants.ProbeTestFailed},
		{name: "missing coverage", wantOutcome: gomutants.ProbeUnavailable, wantError: "read package-suite coverage"},
		{name: "invalid coverage", write: "invalid", wantOutcome: gomutants.ProbeUnavailable, wantError: "package-suite coverage"},
		{name: "measured", write: coverage, result: gomutants.CommandResult{Duration: 2 * time.Second}, wantOutcome: gomutants.ProbeMeasured},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
				if test.write != "" {
					if err := os.WriteFile(coverageProfileArgument(command), []byte(test.write), filemode.PrivateFile); err != nil {
						t.Fatal(err)
					}
				}
				return test.result, test.err
			}}
			suite, outcome, err := collectPackageSuiteCoverage(t.Context(), workspace, "fixture.example/module", "fixture.example/module", ".", "module.test", time.Second, nil, BaselineOptions{ArtifactDirectory: t.TempDir()})
			if outcome != test.wantOutcome || (test.wantError == "") != (err == nil) {
				t.Fatalf("suite = (%+v, %q, %v), want outcome %q error %q", suite, outcome, err, test.wantOutcome, test.wantError)
			}
			if test.wantError != "" && !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if outcome == gomutants.ProbeMeasured && (suite.Duration != 2*time.Second || len(suite.Covered) != 1) {
				t.Fatalf("measured suite = %+v", suite)
			}
		})
	}
}

func TestPreparedPackageSuiteCoverageDecidesEveryTerminal(t *testing.T) {
	coverage := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
	sentinel := errors.New("suite probe failed")
	for _, test := range []struct {
		name        string
		result      gomutants.ProbeResult
		err         error
		write       string
		wantOutcome gomutants.ProbeOutcome
		wantError   string
		wantProbe   bool
	}{
		{name: "probe error", err: sentinel, wantError: "baseline package suite"},
		{name: "unmeasured", result: gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut}, wantOutcome: gomutants.ProbeTimedOut},
		{name: "missing coverage", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, wantError: "read package-suite coverage"},
		{name: "invalid coverage", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, write: "invalid", wantError: "package-suite coverage"},
		{name: "measured unknown probe", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{8}}, write: coverage, wantOutcome: gomutants.ProbeMeasured},
		{name: "measured known probe", result: gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{7}, Duration: 2 * time.Second}, write: coverage, wantOutcome: gomutants.ProbeMeasured, wantProbe: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &mutationUnitSession{probe: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				if test.write != "" {
					profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
					if err := os.WriteFile(profile, []byte(test.write), filemode.PrivateFile); err != nil {
						t.Fatal(err)
					}
				}
				return test.result, test.err
			}}
			run := collectPreparedPackageSuiteCoverage(t.Context(), session, "fixture.example/module", "fixture.example/module", time.Second, nil, BaselineOptions{
				ArtifactDirectory: t.TempDir(), probeIndices: map[uint32]struct{}{7: {}},
			})
			if (test.wantError == "") != (run.err == nil) || run.outcome != test.wantOutcome {
				t.Fatalf("run = %+v, want outcome %q error %q", run, test.wantOutcome, test.wantError)
			}
			if test.wantError != "" && !strings.Contains(run.err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", run.err, test.wantError)
			}
			if (run.probe != nil) != test.wantProbe {
				t.Fatalf("probe evidence = %+v, want present %t", run.probe, test.wantProbe)
			}
		})
	}
}

func TestBaselineCoverageCheckpointAndAggregationKeepNilAndCounts(t *testing.T) {
	coverage := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}}
	if got := restrictBaselineCoverage(goanalysis.Coverage{Covered: coverage}, nil); !reflect.DeepEqual(got.Covered, coverage) {
		t.Fatalf("unrestricted coverage = %+v", got)
	}
	if got := restrictBaselineCoverage(goanalysis.Coverage{Covered: coverage}, []goanalysis.Package{}); len(got.Covered) != 0 {
		t.Fatalf("restricted coverage = %+v, want empty", got)
	}
	if checkpointCoverage(nil) != nil {
		t.Fatal("nil coverage did not remain nil")
	}
	empty := checkpointCoverage([]goanalysis.FileCoverage{})
	if empty == nil || empty.Files == nil || len(empty.Files) != 0 {
		t.Fatalf("empty coverage checkpoint = %+v", empty)
	}
	if restored := restoreCheckpointCoverage(nil); restored != nil {
		t.Fatalf("nil restored coverage = %#v", restored)
	}
	result := BaselineResult{}
	unit := checkpoint.BaselineTarget{
		ID: "target", Executed: true, Skipped: true,
		Evidence: []report.Evidence{{ID: "evidence"}}, Findings: []report.Finding{{ID: "finding"}},
		Inventory: report.TargetDisposition{ID: "target"},
	}
	appendBaselineUnit(&result, unit, nil)
	if result.Executed != 1 || result.Skipped != 1 || len(result.Evidence) != 1 || len(result.Findings) != 1 || len(result.Inventory) != 1 {
		t.Fatalf("aggregated result = %+v", result)
	}
	appendBaselineUnit(&result, checkpoint.BaselineTarget{}, nil)
	if result.Executed != 1 || result.Skipped != 1 {
		t.Fatalf("false flags changed counts: %+v", result)
	}
}

func TestBaselineRoutingAndSuiteCheckpointRespectExactOptions(t *testing.T) {
	coverage := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}}
	run := packageSuiteCoverageRun{
		importPath: "fixture.example/module", outcome: gomutants.ProbeMeasured,
		suite: PackageSuiteCoverage{Covered: coverage, Instrumented: coverage},
	}
	unit := checkpointBaselineSuite(run)
	if unit.Covered == nil || unit.Instrumented == nil || len(unit.Covered.Files) != 1 || len(unit.Instrumented.Files) != 1 {
		t.Fatalf("nonempty suite checkpoint = %+v", unit)
	}
	routing := checkpointBaselineRouting(coverage, map[string]PackageSuiteCoverage{"fixture.example/module": run.suite})
	for _, enabled := range []bool{false, true} {
		instrumented, suites := restoreBaselineRouting(*routing, enabled)
		if len(instrumented) != 1 || (suites != nil) != enabled {
			t.Fatalf("restored routing enabled=%t: instrumented=%+v suites=%+v", enabled, instrumented, suites)
		}
		if enabled && len(suites) != 1 {
			t.Fatalf("restored suites = %+v", suites)
		}
	}
}

func TestNeededBaselineSuitesFilterRejectedEmptyAndRoutedMutants(t *testing.T) {
	block := []goanalysis.FileCoverage{{Path: "value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}}
	target := TargetEvidence{Target: baselineTestTarget("TestValue"), CoveredFiles: []string{"value.go"}, Covered: block, Instrumented: block}
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "rejected", Package: "fixture.example/rejected", Path: "rejected.go", Line: 1, Column: 1},
		{ID: "empty", Accepted: true, Path: "empty.go", Line: 1, Column: 1},
		{ID: "routed", Accepted: true, Package: "fixture.example/module", Path: "value.go", Line: 7, Column: 2},
		{ID: "needed", Accepted: true, Package: "fixture.example/needed", Path: "other.go", Line: 1, Column: 1},
	}}
	if got := neededBaselineSuitePackages(catalog, []TargetEvidence{target}, block); !slices.Equal(got, []string{"fixture.example/needed"}) {
		t.Fatalf("needed suites = %q", got)
	}
}

func TestCollectBaselineWorkersPreserveEmptyAndCancellationBoundaries(t *testing.T) {
	if runs := collectPackageSuiteCoverages(t.Context(), &baselineFakeWorkspace{}, "module", nil, time.Second, nil, BaselineOptions{}, nil); runs == nil || len(runs) != 0 {
		t.Fatalf("empty suite runs = %#v, want a nonnil empty result", runs)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	executed := false
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		executed = true
		return gomutants.CommandResult{}, nil
	}}
	err := collectPackageBaselineTargets(ctx, workspace, "module", "module", ".", "module.test", []BaselineTarget{{Target: baselineTestTarget("TestValue")}}, time.Second, BaselineOptions{}, func(baselineTargetRun) {})
	if !errors.Is(err, context.Canceled) || executed {
		t.Fatalf("canceled collection = %v, executed=%t", err, executed)
	}
}

func TestClassifyFramingAndTrimDurationChooseEveryDelimiter(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		skipped bool
		kind    string
	}{
		{name: "newline", output: "\x16--- SKIP: TestValue\n", skipped: true, kind: "skipped-target"},
		{name: "next marker", output: "\x16ordinary\x16--- SKIP: TestValue\n", skipped: true, kind: "skipped-target"},
		{name: "marker terminates target", output: "\x16--- SKIP: TestValue\x16ordinary", skipped: true, kind: "skipped-target"},
		{name: "unterminated", output: "\x16--- SKIP: TestValue", skipped: true, kind: "skipped-target"},
		{name: "newline advances", output: "\x16ordinary\n\x16--- SKIP: TestValue\n", skipped: true, kind: "skipped-target"},
		{name: "unframed", output: "ordinary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			skipped, kind, _, err := classifyTestFraming("TestValue", []byte(test.output))
			if err != nil || skipped != test.skipped || kind != test.kind {
				t.Fatalf("classification = (%t, %q, %v)", skipped, kind, err)
			}
		})
	}
	for _, test := range []struct{ input, want string }{
		{input: "TestValue (1.25", want: "TestValue (1.25"},
		{input: "TestValue (1.25s)", want: "TestValue"},
		{input: "TestValue (quickly)", want: "TestValue (quickly)"},
	} {
		if got := trimTestDuration(test.input); got != test.want {
			t.Fatalf("trimTestDuration(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestCollectBaselineKeepsProbeAndPackageSuiteOptionsIndependent(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{
		Index: 7, ID: "mutant", Accepted: true, Probed: true, Package: "fixture.example/module",
	}}}}
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), ProbeSession: session,
	})
	if err != nil || result.Suites != nil || result.ProbeSuites != nil || len(session.probeRequests()) != 0 {
		t.Fatalf("disabled package suites = (%+v, %v), probes=%+v", result, err, session.probeRequests())
	}
	result, err = CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true,
	})
	if err != nil || result.Suites == nil || result.ProbeSuites != nil {
		t.Fatalf("unprepared package suites = (%+v, %v)", result, err)
	}
	result, err = CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(),
		Resume: &checkpoint.Baseline{
			BuildVetComplete: true,
			Suites:           []checkpoint.BaselineSuite{{Package: "fixture.example/module", Measured: true}},
		},
		StopAfterChecks: true,
	})
	if err != nil || result.Suites != nil {
		t.Fatalf("disabled resumed suites = (%+v, %v)", result, err)
	}
}

func TestCollectBaselineIndexesOnlyProbeCapableMutants(t *testing.T) {
	coverage := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{{
		Index: 7, ID: "unprobed", Accepted: true, Package: "fixture.example/module",
	}}}}
	session.probe = func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
		if err := os.WriteFile(profile, []byte(coverage), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured, Infected: []uint32{7}}, nil
	}
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: baselineTestTarget("TestValue")}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), ProbeSession: session,
	})
	if err != nil || len(result.Targets) != 1 || result.Targets[0].Probed || result.Targets[0].Infected != nil {
		t.Fatalf("unprobed catalog result = (%+v, %v)", result, err)
	}
}

func TestCollectBaselineRestoresOnlyExactTargetAndRoutingState(t *testing.T) {
	target := baselineTestTarget("TestResumed")
	evidence := TargetEvidence{
		Target:       target,
		CoveredFiles: []string{"outside/value.go"},
		Covered:      []goanalysis.FileCoverage{{Path: "outside/value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}},
		Instrumented: []goanalysis.FileCoverage{{Path: "outside/value.go", Blocks: []goanalysis.CoverageBlock{infectionBlock()}}},
	}
	for _, test := range []struct {
		name   string
		resume *checkpoint.Baseline
		want   int
	}{
		{
			name: "target without evidence",
			resume: &checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{{
				ID: target.ID, Inventory: report.TargetDisposition{ID: target.ID, Status: "not-run"},
			}}},
		},
		{
			name: "target with restricted evidence",
			resume: &checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{{
				ID: target.ID, Inventory: report.TargetDisposition{ID: target.ID, Status: "passed"}, Target: checkpointTargetEvidence(evidence),
			}}},
			want: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
				t.Fatal("completed target was executed")
				return gomutants.CommandResult{}, nil
			}}
			model := goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{{
				ImportPath: "fixture.example/module", RelativeDir: "pkg",
			}}}
			result, err := CollectBaseline(t.Context(), workspace, model, []BaselineTarget{{Target: target}}, BaselineOptions{
				ArtifactDirectory: t.TempDir(), Resume: test.resume,
			})
			if err != nil || len(result.Targets) != test.want || len(result.Instrumented) != 0 {
				t.Fatalf("restored baseline = (%+v, %v), want %d target", result, err, test.want)
			}
			if test.want != 0 && (len(result.Targets[0].Covered) != 0 || len(result.Targets[0].Instrumented) != 0) {
				t.Fatalf("unscoped coverage was restored: %+v", result.Targets[0])
			}
		})
	}
	routing := &checkpoint.BaselineRouting{Instrumented: checkpoint.Coverage{Files: []checkpoint.FileCoverage{{Path: "restored.go"}}}}
	for _, test := range []struct {
		name   string
		resume *checkpoint.Baseline
	}{
		{name: "incomplete with routing", resume: &checkpoint.Baseline{BuildVetComplete: true, Routing: routing}},
		{name: "complete without routing", resume: &checkpoint.Baseline{BuildVetComplete: true, Complete: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
				t.Fatal("empty resumed baseline executed a command")
				return gomutants.CommandResult{}, nil
			}}
			result, err := CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
				ArtifactDirectory: t.TempDir(), PackageSuites: true, Resume: test.resume,
			})
			if err != nil || len(result.Instrumented) != 0 {
				t.Fatalf("non-restorable routing = (%+v, %v)", result, err)
			}
		})
	}
}

func TestCollectBaselineDecidesProjectCheckTerminalsAndCheckpointBoundary(t *testing.T) {
	targetA := baselineTestTarget("TestA")
	targetB := baselineTestTarget("TestB")
	for _, test := range []struct {
		name       string
		result     gomutants.CommandResult
		classify   bool
		wantError  bool
		wantResult bool
	}{
		{name: "timeout", result: gomutants.CommandResult{TimedOut: true, Output: []byte("slow")}, wantError: true},
		{name: "unclassified exit", result: gomutants.CommandResult{ExitCode: 2, Output: []byte("broken")}, wantError: true},
		{name: "classified exit", result: gomutants.CommandResult{ExitCode: 2, Output: []byte("broken")}, classify: true, wantResult: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
				calls++
				return test.result, nil
			}}
			resume := &checkpoint.Baseline{Targets: []checkpoint.BaselineTarget{{
				ID: targetA.ID, Inventory: report.TargetDisposition{ID: targetA.ID, Status: "passed"},
			}}}
			result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: targetA}, {Target: targetB}}, BaselineOptions{
				ArtifactDirectory: t.TempDir(), ClassifyUserFailures: test.classify, Resume: resume,
			})
			if (err != nil) != test.wantError {
				t.Fatalf("project check = (%+v, %v), want error %t", result, err, test.wantError)
			}
			if test.name == "timeout" && (calls != 1 || !strings.Contains(err.Error(), "go vet failed")) {
				t.Fatalf("timeout calls=%d error=%v, want the first check to stop the run", calls, err)
			}
			if test.wantResult {
				if err != nil || len(result.Findings) != 1 || len(result.Inventory) != 2 || result.Inventory[0].Status != "passed" || result.Inventory[1].Status != "not-run" || result.Skipped != 1 {
					t.Fatalf("classified result = (%+v, %v)", result, err)
				}
			}
		})
	}
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		t.Fatal("completed checks were rerun")
		return gomutants.CommandResult{}, nil
	}}
	checkpoints := 0
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), StopAfterChecks: true,
		Resume:     &checkpoint.Baseline{BuildVetComplete: true},
		Checkpoint: func(checkpoint.Baseline) { checkpoints++ },
	})
	if err != nil || !reflect.DeepEqual(result, BaselineResult{}) || checkpoints != 0 {
		t.Fatalf("resumed checks = (%+v, %v), checkpoints=%d", result, err, checkpoints)
	}
}

func TestCollectBaselineFiltersCatalogPackagesAndCompletedSuites(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	session := &mutationUnitSession{catalog: gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "rejected", Package: "fixture.example/missing"},
		{ID: "empty", Accepted: true},
	}}}
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true, ProbeSession: session,
	})
	if err != nil || len(session.probeRequests()) != 0 || len(result.Suites) != 0 {
		t.Fatalf("filtered catalog = (%+v, %v), probes=%+v", result, err, session.probeRequests())
	}
	session.catalog.Mutants = []gomutants.Mutant{{ID: "done", Accepted: true, Probed: true, Package: "fixture.example/module"}}
	result, err = CollectBaseline(t.Context(), workspace, baselineModel(), nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true, ProbeSession: session,
		Resume: &checkpoint.Baseline{
			BuildVetComplete: true,
			Suites:           []checkpoint.BaselineSuite{{Package: "fixture.example/module"}},
		},
	})
	if err != nil || len(session.probeRequests()) != 0 {
		t.Fatalf("completed suite = (%+v, %v), probes=%+v", result, err, session.probeRequests())
	}
}

func TestCollectBaselineClassifiesCompileFailureAndCheckpointsExactStates(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
		if len(command.Argv) > 2 && command.Argv[0] == "go" && command.Argv[1] == "test" && command.Argv[2] == "-c" {
			return gomutants.CommandResult{ExitCode: 2, Output: []byte("compile failed")}, nil
		}
		return gomutants.CommandResult{}, nil
	}}
	var states []checkpoint.Baseline
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: baselineTestTarget("TestValue")}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true, ClassifyUserFailures: true,
		Checkpoint: func(state checkpoint.Baseline) { states = append(states, state) },
	})
	complete := make([]bool, 0, len(states))
	for _, state := range states {
		complete = append(complete, state.Complete)
	}
	if err != nil || len(result.Findings) != 1 || result.Findings[0].Kind != "test-binary-build-failure" ||
		len(result.Inventory) != 1 || result.Inventory[0].Status != "not-run" || result.Skipped != 1 ||
		!slices.Equal(complete, []bool{false, false, true}) || len(states[1].Suites) != 1 || states[1].Suites[0].Package != "fixture.example/module" {
		t.Fatalf("compile classification = (%+v, %v), checkpoints=%v", result, err, complete)
	}
	states = nil
	result, err = CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: baselineTestTarget("TestValue")}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(), ClassifyUserFailures: true,
		Checkpoint: func(state checkpoint.Baseline) { states = append(states, state) },
	})
	for _, state := range states {
		if len(state.Suites) != 0 {
			t.Fatalf("disabled package suites checkpointed %+v", state.Suites)
		}
	}
	if err != nil || len(result.Findings) != 1 {
		t.Fatalf("compile classification without suites = (%+v, %v)", result, err)
	}
}

func TestCollectBaselineCommitsATerminalTargetWithoutInventingEvidence(t *testing.T) {
	workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
		if len(command.Argv) != 0 && command.Argv[0] != "go" {
			return gomutants.CommandResult{TimedOut: true, Duration: time.Second}, nil
		}
		return gomutants.CommandResult{}, nil
	}}
	result, err := CollectBaseline(t.Context(), workspace, baselineModel(), []BaselineTarget{{Target: baselineTestTarget("TestValue")}}, BaselineOptions{
		ArtifactDirectory: t.TempDir(),
	})
	if err != nil || len(result.Inventory) != 1 || result.Inventory[0].Status != "failed" || len(result.Targets) != 0 {
		t.Fatalf("terminal target = (%+v, %v)", result, err)
	}
}

func TestCollectBaselineSuiteCommitKeepsAllOutcomesAndRejectsErrors(t *testing.T) {
	model := goanalysis.Model{ModulePath: "fixture.example/module", Packages: []goanalysis.Package{
		{ImportPath: "fixture.example/module/a", RelativeDir: "a"},
		{ImportPath: "fixture.example/module/b", RelativeDir: "b"},
	}}
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{ID: "a", Accepted: true, Probed: true, Package: "fixture.example/module/a"},
		{ID: "b", Accepted: true, Probed: true, Package: "fixture.example/module/b"},
	}}
	workspace := &baselineFakeWorkspace{exec: func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}}
	session := &mutationUnitSession{catalog: catalog, probe: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		if request.Package == "fixture.example/module/a" {
			return gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut}, nil
		}
		return gomutants.ProbeResult{Outcome: gomutants.ProbeTestFailed}, nil
	}}
	var complete []bool
	result, err := CollectBaseline(t.Context(), workspace, model, nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true, ProbeSession: session, Jobs: 1,
		Checkpoint: func(state checkpoint.Baseline) { complete = append(complete, state.Complete) },
	})
	if err != nil || len(result.UnmeasuredSuites) != 2 || result.UnmeasuredSuites["fixture.example/module/a"] != gomutants.ProbeTimedOut ||
		result.UnmeasuredSuites["fixture.example/module/b"] != gomutants.ProbeTestFailed ||
		!slices.Equal(complete, []bool{false, false, false, true}) {
		t.Fatalf("unmeasured suite result = (%+v, %v), checkpoints=%v", result, err, complete)
	}
	sentinel := errors.New("suite probe failed")
	session = &mutationUnitSession{catalog: gomutants.Catalog{Mutants: catalog.Mutants[:1]}, probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{}, sentinel
	}}
	complete = nil
	result, err = CollectBaseline(t.Context(), workspace, goanalysis.Model{ModulePath: model.ModulePath, Packages: model.Packages[:1]}, nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), PackageSuites: true, ProbeSession: session,
		Checkpoint: func(state checkpoint.Baseline) { complete = append(complete, state.Complete) },
	})
	if err == nil || !errors.Is(err, sentinel) || !reflect.DeepEqual(result, BaselineResult{}) || !slices.Equal(complete, []bool{false}) {
		t.Fatalf("failed suite result = (%+v, %v), checkpoints=%v", result, err, complete)
	}
}

func TestPreparedBaselineObservationFailuresKeepTargetAndSuiteTraceRoles(t *testing.T) {
	const pkg = "fixture.example/module"
	observer := newRepositoryObserver(t.TempDir(), t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		pkg: {},
	}, targetKeySources{model: baselineModel()})
	for _, test := range []struct {
		name   string
		suite  bool
		target string
	}{
		{name: "target", target: baselineTestTarget("TestValue").ID},
		{name: "suite", suite: true, target: packageSuiteProbeTarget(pkg)},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &mutationUnitSession{probe: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				log, found := repositoryTestLogPath(request.Args)
				if !found {
					t.Fatalf("observed request has no test log: %+v", request)
				}
				return gomutants.ProbeResult{Output: []byte("testing: open " + log + ": access denied")}, nil
			}}
			recording, recorder := newProbeRecording()
			options := BaselineOptions{ArtifactDirectory: t.TempDir(), RepositoryObserver: observer, Trace: recorder}
			var err error
			if test.suite {
				err = collectPreparedPackageSuiteCoverage(t.Context(), session, pkg, pkg, time.Second, nil, options).err
			} else {
				err = executePreparedBaselineTarget(t.Context(), session, pkg, pkg, BaselineTarget{Target: baselineTestTarget("TestValue")}, time.Second, options).err
			}
			if err == nil || !strings.Contains(err.Error(), "repository observation") {
				t.Fatalf("observation error = %v", err)
			}
			record := probeRecords(t, recording)[test.target]
			if record.Error == "" || record.Suite != test.suite {
				t.Fatalf("trace record = %+v, want suite=%t error", record, test.suite)
			}
		})
	}
	workspace := &baselineFakeWorkspace{exec: func(command gomutants.Command) (gomutants.CommandResult, error) {
		log, found := repositoryTestLogPath(command.Argv)
		if !found {
			t.Fatalf("observed command has no test log: %+v", command)
		}
		return gomutants.CommandResult{Output: []byte("testing: open " + log + ": access denied")}, nil
	}}
	suite, outcome, err := collectPackageSuiteCoverage(t.Context(), workspace, pkg, pkg, ".", "module.test", time.Second, nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), RepositoryObserver: observer,
	})
	if err == nil || outcome != gomutants.ProbeUnavailable || !reflect.DeepEqual(suite, PackageSuiteCoverage{}) || !strings.Contains(err.Error(), "repository observation") {
		t.Fatalf("unprepared observation failure = (%+v, %q, %v)", suite, outcome, err)
	}
}

func TestPreparedBaselineExecutionFailuresKeepTargetAndSuiteTraceRoles(t *testing.T) {
	const pkg = "fixture.example/module"
	sentinel := errors.New("probe failed")
	for _, test := range []struct {
		name   string
		suite  bool
		target string
	}{
		{name: "target", target: baselineTestTarget("TestValue").ID},
		{name: "suite", suite: true, target: packageSuiteProbeTarget(pkg)},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &mutationUnitSession{probe: func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
				return gomutants.ProbeResult{}, sentinel
			}}
			recording, recorder := newProbeRecording()
			options := BaselineOptions{ArtifactDirectory: t.TempDir(), Trace: recorder}
			var err error
			if test.suite {
				err = collectPreparedPackageSuiteCoverage(t.Context(), session, pkg, pkg, time.Second, nil, options).err
			} else {
				err = executePreparedBaselineTarget(t.Context(), session, pkg, pkg, BaselineTarget{Target: baselineTestTarget("TestValue")}, time.Second, options).err
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("execution error = %v", err)
			}
			record := probeRecords(t, recording)[test.target]
			if record.Error == "" || record.Suite != test.suite {
				t.Fatalf("trace record = %+v, want suite=%t error", record, test.suite)
			}
		})
	}
}

func TestPreparedPackageSuiteTraceMarksWholeTreeObservation(t *testing.T) {
	const pkg = "fixture.example/module"
	coverage := "mode: set\nfixture.example/module/value.go:1.1,2.1 1 1\n"
	observer := newRepositoryObserver(t.TempDir(), t.TempDir(), map[string]goanalysis.RepositoryReadCandidate{
		pkg: {Unobservable: true},
	}, targetKeySources{})
	session := &mutationUnitSession{probe: func(request gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		profile := coverageProfileArgument(gomutants.Command{Argv: request.Args})
		if err := os.WriteFile(profile, []byte(coverage), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, nil
	}}
	recording, recorder := newProbeRecording()
	run := collectPreparedPackageSuiteCoverage(t.Context(), session, pkg, pkg, time.Second, nil, BaselineOptions{
		ArtifactDirectory: t.TempDir(), RepositoryObserver: observer, Trace: recorder,
	})
	if run.err != nil || run.outcome != gomutants.ProbeMeasured {
		t.Fatalf("suite = %+v", run)
	}
	record := probeRecords(t, recording)[packageSuiteProbeTarget(pkg)]
	if !record.WholeTree || record.WholeTreeReason == string(wholeTreeObserved) {
		t.Fatalf("whole-tree trace record = %+v", record)
	}
}

func TestAppendCompletedBaselineTargetsSkipsMissingUnits(t *testing.T) {
	first := BaselineTarget{Target: baselineTestTarget("TestFirst")}
	missing := BaselineTarget{Target: baselineTestTarget("TestMissing")}
	result := BaselineResult{}
	appendCompletedBaselineTargets(&result, []BaselineTarget{first, missing}, map[string]checkpoint.BaselineTarget{
		first.Target.ID: {ID: first.Target.ID, Inventory: report.TargetDisposition{ID: first.Target.ID}},
	}, nil)
	if len(result.Inventory) != 1 || result.Inventory[0].ID != first.Target.ID {
		t.Fatalf("completed baseline targets = %+v", result.Inventory)
	}
}
