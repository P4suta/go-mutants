// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

const (
	fixtureToolVersion = "0.0.0-test"
	fixtureRunID       = "20260218T091500Z-3f9c"
	fixtureModulePath  = "example.com/m"
	fixtureGoVersion   = "1.26"
	fixtureStarted     = "2026-02-18T09:15:00Z"
	fixtureDuration    = 42 * time.Second
	alphaFile          = "internal/alpha/alpha.go"
	betaFile           = "internal/beta/beta.go"
	alphaPackage       = "example.com/m/internal/alpha"
	betaPackage        = "example.com/m/internal/beta"
	staleID            = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

var fixtureDigest = strings.Repeat("ab", 32)

const (
	fixtureGoBin            = "/usr/local/go/bin/go"
	fixtureGoVersionLine    = "go version go1.26.0 linux/amd64"
	fixtureValidationBuilds = 3
	fixtureSnapshotFiles    = 128
)

var fixturePhases = []report.PhaseTiming{
	{Name: "discover", DurationMS: 1_200},
	{Name: "baseline", DurationMS: 4_300},
	{Name: "mutate", DurationMS: 35_000},
	{Name: "report", DurationMS: 90},
}

var fixtureStages = []report.StageTiming{
	{Phase: "discover", Name: "toolchain", DurationMS: 40, Result: report.StageSucceeded},
	{Phase: "discover", Name: "snapshot", DurationMS: 1_100, Result: report.StageSucceeded},
	{Phase: "baseline", Name: "build", DurationMS: 2_800, Result: report.StageSucceeded},
	{Phase: "baseline", Name: "test", DurationMS: 1_500, Result: report.StageSucceeded},
	{Phase: "mutate", Name: "validate", DurationMS: 9_000, Result: report.StageSucceeded},
	{Phase: "mutate", Name: "build-binaries", DurationMS: 3_000, Result: report.StageFailed},
	{Phase: "mutate", Name: "build-binaries", DurationMS: 2_400, Result: report.StageSucceeded},
	{Phase: "mutate", Name: "execute", DurationMS: 20_600, Result: report.StageSucceeded},
	{Phase: "report", Name: "build", DurationMS: 20, Result: report.StageSucceeded},
}

type candidate struct {
	path          string
	pkg           string
	rule          string
	start         uint32
	original      string
	replacement   string
	line          int
	column        int
	outcome       mutation.Outcome
	notRun        report.NotRunReason
	rejected      bool
	killedBy      string
	attempts      int
	duration      time.Duration
	tail          string
	covering      []string
	uncovered     bool
	coveringTests []report.TestRef
	cached        bool
	branch        *discover.BranchProof
	executions    []report.Execution
}

var fixtureCandidates = []candidate{
	{
		path: alphaFile, pkg: alphaPackage, rule: "eq-to-neq",
		start: 100, original: "==", replacement: "!=", line: 12, column: 9,
		outcome: mutation.OutcomeKilled, killedBy: alphaPackage, attempts: 1,
		duration: 120 * time.Millisecond, tail: "--- FAIL: TestAdd (0.00s)",
		cached: true,
	},
	{
		path: alphaFile, pkg: alphaPackage, rule: "true-to-false",
		start: 140, original: "true", replacement: "false", line: 18, column: 16,
		outcome: mutation.OutcomeSurvived, attempts: 1, duration: 95 * time.Millisecond,
		executions: []report.Execution{{
			Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 95,
			Binaries: []string{alphaPackage, betaPackage}, PeakMemoryBytes: 41_943_040,
		}},
	},
	{
		path: alphaFile, pkg: alphaPackage, rule: "lt-to-le",
		start: 200, original: "<", replacement: "<=", line: 24, column: 7,
		outcome: mutation.OutcomeTimedOut, killedBy: alphaPackage, attempts: 2,
		duration: 20 * time.Second, tail: "panic: test timed out after 10s",
		cached: true,
	},
	{
		path: alphaFile, pkg: alphaPackage, rule: "ge-to-gt",
		start: 260, original: ">=", replacement: ">", line: 31, column: 12,
		outcome: mutation.OutcomeInconclusive, attempts: 2, duration: 11 * time.Second,
		executions: []report.Execution{
			{
				Attempt: 1, Worker: 1, Outcome: report.OutcomeTimedOut, KilledBy: alphaPackage,
				DurationMS: 10_000, Binaries: []string{alphaPackage},
			},
			{
				Attempt: 2, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 1_000,
				Binaries: []string{alphaPackage, betaPackage},
			},
		},
	},
	{
		path: betaFile, pkg: betaPackage, rule: "neq-to-eq",
		start: 42, original: "!=", replacement: "==", line: 5, column: 6,
		outcome: mutation.OutcomeSurvived, attempts: 1, duration: 80 * time.Millisecond,
		executions: []report.Execution{{
			Attempt: 1, Worker: 1, Outcome: report.OutcomeSurvived, DurationMS: 80,
			Binaries: []string{alphaPackage, betaPackage},
		}},
	},
	{
		path: betaFile, pkg: betaPackage, rule: "false-to-true",
		start: 77, original: "false", replacement: "true", line: 9, column: 20,
		outcome: mutation.OutcomeErrored, attempts: 1, duration: 4 * time.Millisecond,
		tail: "exec: the test binary could not be started",
		executions: []report.Execution{{
			Attempt: 1, Worker: 1, Outcome: report.OutcomeErrored, DurationMS: 4,
			Binaries: []string{betaPackage},
		}},
	},
	{
		path: betaFile, pkg: betaPackage, rule: "le-to-lt",
		start: 120, original: "<=", replacement: "<", line: 14, column: 8,
		outcome: mutation.OutcomeNotRun, notRun: report.NotRunInterrupted,
	},
	{
		path: betaFile, pkg: betaPackage, rule: "eq-to-neq",
		start: 150, original: "==", replacement: "!=", line: 19, column: 11,
		rejected: true,
	},
	{
		path: betaFile, pkg: betaPackage, rule: "gt-to-ge",
		start: 180, original: ">", replacement: ">=", line: 24, column: 7,
		outcome: mutation.OutcomeTimedOut, killedBy: betaPackage, attempts: 2,
		duration: 21 * time.Second, tail: "panic: test timed out after 10s",
		executions: []report.Execution{
			{
				Attempt: 1, Worker: 1, Outcome: report.OutcomeTimedOut, KilledBy: betaPackage,
				DurationMS: 10_000, Binaries: []string{betaPackage},
			},
			{
				Attempt: 2, Worker: 0, Outcome: report.OutcomeTimedOut, KilledBy: betaPackage,
				DurationMS: 11_000, Binaries: []string{betaPackage},
			},
		},
	},
}

const fixtureDiagnostic = "internal/beta/beta.go:19:11: invalid operation: mismatched types"

var fixtureSkips = []discover.Skip{
	{Path: "internal/gamma/gamma.go", Reason: discover.SkipExcluded, Count: 1},
	{Path: alphaFile, Reason: discover.SkipConstDecl, Count: 4},
	{Path: alphaFile, Reason: discover.SkipArrayLength, Count: 1},
}

var fixtureWarnings = []report.Warning{
	{Code: "GOM4040", Message: "the snapshot directory could not be removed: access is denied"},
	{Code: "GOM7602", Message: "coverage-guided selection is off because the test binaries do not compile with coverage instrumentation (GOM7505: the test binaries do not compile); every mutant will be measured against every test binary, which is slower and never wrong"},
}

func locatedFixtures(t *testing.T) ([]discover.Located, *mutation.Catalog) {
	t.Helper()
	return located(t, fixtureCandidates)
}

func located(t *testing.T, candidates []candidate) ([]discover.Located, *mutation.Catalog) {
	t.Helper()
	registry := mutation.CanonicalRegistry()
	rows := make([]discover.Located, 0, len(candidates))
	for _, c := range candidates {
		rule, ok := registry.Lookup(c.rule)
		if !ok {
			t.Fatalf("the canonical registry has no rule %q", c.rule)
		}
		span, err := mutation.NewSpan(c.start, c.start+uint32(len(c.original)))
		if err != nil {
			t.Fatalf("span for %s: %v", c.rule, err)
		}
		rows = append(rows, discover.Located{
			Candidate: mutation.Candidate{
				Path:         c.path,
				Rule:         rule,
				Span:         span,
				Original:     c.original,
				Replacement:  c.replacement,
				SourceDigest: mutation.DigestString(c.path),
			},
			Line:    c.line,
			Column:  c.column,
			Package: c.pkg,
			Branch:  c.branch,
		})
	}
	catalog, err := discover.BuildCatalog(discover.Result{Candidates: rows})
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	mutants := catalog.Mutants()
	if len(mutants) != len(candidates) {
		t.Fatalf("catalogue holds %d mutants, want %d", len(mutants), len(candidates))
	}
	for i, m := range mutants {
		want := candidates[i]
		if m.Path != want.path || m.Span.StartByte != want.start || m.Rule.Name != want.rule {
			t.Fatalf("catalogue position %d is %s %s %s, want %s %d %s",
				i, m.Path, m.Span, m.Rule.Name, want.path, want.start, want.rule)
		}
	}
	return rows, catalog
}

func fixtureOptions(t *testing.T) report.Options {
	t.Helper()
	located, catalog := locatedFixtures(t)
	mutants := catalog.Mutants()

	results := make([]report.MutantResult, 0, len(mutants))
	rejections := make([]report.Rejection, 0, 1)
	for i, m := range mutants {
		c := fixtureCandidates[i]
		if c.rejected {
			rejections = append(rejections, report.Rejection{ID: m.ID, Diagnostic: fixtureDiagnostic})
			continue
		}
		results = append(results, report.MutantResult{
			ID:           m.ID,
			Outcome:      c.outcome,
			NotRunReason: c.notRun,
			Duration:     c.duration,
			KilledBy:     c.killedBy,
			Attempts:     c.attempts,
			OutputTail:   c.tail,
			Executions:   slices.Clone(c.executions),
			Cached:       c.cached,
		})
	}

	cfg := config.Defaults()
	cfg.Mutation.Expect = []config.Expectation{
		{ID: mutants[1].ID, Reason: "the flag is only read by the debug logger"},
		{ID: mutants[0].ID, Reason: "the boundary is checked by the caller"},
		{ID: staleID, Reason: "kept to prove a stale row is reported"},
	}

	started, err := time.Parse(time.RFC3339, fixtureStarted)
	if err != nil {
		t.Fatalf("parsing the fixture clock: %v", err)
	}
	return report.Options{
		ToolVersion:               fixtureToolVersion,
		RunID:                     fixtureRunID,
		Status:                    report.StatusCompleted,
		Started:                   started,
		Finished:                  started.Add(fixtureDuration),
		Config:                    cfg,
		Mode:                      report.ModeAll,
		Selected:                  len(results),
		ModulePath:                fixtureModulePath,
		GoVersion:                 fixtureGoVersion,
		WorkspaceDigest:           fixtureDigest,
		Platform:                  report.Platform{OS: "linux", Arch: "amd64"},
		Catalog:                   catalog,
		Located:                   located,
		Skips:                     fixtureSkips,
		Results:                   results,
		Rejections:                rejections,
		TestCommand:               []string{"go", "test", "./..."},
		Baseline:                  []time.Duration{1200 * time.Millisecond, 1500 * time.Millisecond, 1350 * time.Millisecond},
		Timeout:                   10 * time.Second,
		TimeoutSource:             report.TimeoutDerived,
		Memory:                    1 << 30,
		MemorySource:              report.MemoryDerived,
		CacheMode:                 report.CacheOn,
		CacheMisses:               6,
		CacheWrites:               3,
		Warnings:                  fixtureWarnings,
		Timing:                    &report.Timing{Phases: fixturePhases, Stages: fixtureStages},
		Validation:                &report.Validation{Builds: fixtureValidationBuilds},
		Snapshot:                  &report.SnapshotFacts{StableDir: true, Files: fixtureSnapshotFiles},
		Toolchain:                 &report.ToolchainFacts{GoBin: fixtureGoBin, Version: fixtureGoVersionLine},
		ResolvedCommand:           []string{fixtureGoBin, "test", "./..."},
		CoverageBuildFallback:     true,
		CoverageUnavailableReason: fixtureCoverageFallback,
	}
}

const fixtureCoverageFallback = "GOM7505: the test binaries do not compile\n" +
	"internal/alpha/alpha.go:12:9: undefined: coverdata"

func buildFixture(t *testing.T) *report.Report {
	t.Helper()
	r, err := report.Build(fixtureOptions(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}

const (
	coverageRunID     = "20260218T094500Z-5b1d"
	coreFile          = "internal/core/core.go"
	edgeFile          = "internal/edge/edge.go"
	corePackage       = "example.com/m/internal/core"
	edgePackage       = "example.com/m/internal/edge"
	coverageBinaries  = 2
	coverageStartedAt = "2026-02-18T09:45:00Z"
)

var coverageCandidates = []candidate{
	{
		path: coreFile, pkg: corePackage, rule: "lt-to-le",
		start: 64, original: "<", replacement: "<=", line: 9, column: 5,
		outcome: mutation.OutcomeKilled, killedBy: corePackage, attempts: 1,
		duration: 140 * time.Millisecond, tail: "--- FAIL: TestClamp (0.00s)",
		covering: []string{corePackage, edgePackage},
		executions: []report.Execution{{
			Attempt: 1, Worker: 0, Outcome: report.OutcomeKilled, KilledBy: corePackage,
			DurationMS: 140, Binaries: []string{corePackage},
		}},
	},
	{
		path: coreFile, pkg: corePackage, rule: "neq-to-eq",
		start: 120, original: "!=", replacement: "==", line: 15, column: 9,
		outcome: mutation.OutcomeSurvived, attempts: 1, duration: 90 * time.Millisecond,
		covering: []string{edgePackage},
		executions: []report.Execution{{
			Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 90,
			Binaries: []string{edgePackage},
		}},
	},
	{
		path: edgeFile, pkg: edgePackage, rule: "true-to-false",
		start: 30, original: "true", replacement: "false", line: 7, column: 9,
		outcome: mutation.OutcomeSurvived, uncovered: true,
	},
}

func coverageOptions(t *testing.T) report.Options {
	t.Helper()
	located, catalog := located(t, coverageCandidates)
	mutants := catalog.Mutants()

	results := make([]report.MutantResult, 0, len(mutants))
	for i, m := range mutants {
		c := coverageCandidates[i]
		results = append(results, report.MutantResult{
			ID:                   m.ID,
			Outcome:              c.outcome,
			NotRunReason:         c.notRun,
			Duration:             c.duration,
			KilledBy:             c.killedBy,
			Attempts:             c.attempts,
			OutputTail:           c.tail,
			Executions:           c.executions,
			CoveringTestPackages: c.covering,
			CoveringTests:        c.coveringTests,
			Uncovered:            c.uncovered,
		})
	}

	started, err := time.Parse(time.RFC3339, coverageStartedAt)
	if err != nil {
		t.Fatalf("parsing the fixture clock: %v", err)
	}
	return report.Options{
		ToolVersion:      fixtureToolVersion,
		RunID:            coverageRunID,
		Status:           report.StatusCompleted,
		Started:          started,
		Finished:         started.Add(fixtureDuration),
		Config:           config.Defaults(),
		Mode:             report.ModeAll,
		Selected:         len(results),
		ModulePath:       fixtureModulePath,
		GoVersion:        fixtureGoVersion,
		WorkspaceDigest:  fixtureDigest,
		Platform:         report.Platform{OS: "linux", Arch: "amd64"},
		Catalog:          catalog,
		Located:          located,
		Results:          results,
		TestCommand:      []string{"go", "test", "./..."},
		Baseline:         []time.Duration{900 * time.Millisecond},
		Timeout:          10 * time.Second,
		TimeoutSource:    report.TimeoutDerived,
		Memory:           1 << 30,
		MemorySource:     report.MemoryDerived,
		CoverageMode:     report.CoveragePackage,
		CoverageBinaries: coverageBinaries,
	}
}

func buildCoverageFixture(t *testing.T) *report.Report {
	t.Helper()
	r, err := report.Build(coverageOptions(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}

const (
	testCoverageRunID = "20260218T101500Z-7e2a"
	testCoverageTests = 3
)

func testCoverageCandidates() []candidate {
	candidates := make([]candidate, len(coverageCandidates))
	copy(candidates, coverageCandidates)

	candidates[0].coveringTests = []report.TestRef{
		{Package: corePackage, Name: "TestClamp"},
		{Package: edgePackage, Name: "TestEdges"},
	}
	candidates[0].executions = []report.Execution{{
		Attempt: 1, Worker: 0, Outcome: report.OutcomeKilled, KilledBy: corePackage,
		DurationMS: 140, Binaries: []string{corePackage},
		Tests: []report.TestRef{{Package: corePackage, Name: "TestClamp"}},
	}}
	candidates[1].coveringTests = []report.TestRef{{Package: edgePackage, Name: "TestEdges"}}
	candidates[1].executions = []report.Execution{{
		Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 90,
		Binaries: []string{edgePackage},
		Tests:    []report.TestRef{{Package: edgePackage, Name: "TestEdges"}},
	}}
	return candidates
}

func testCoverageOptions(t *testing.T) report.Options {
	t.Helper()
	candidates := testCoverageCandidates()
	located, catalog := located(t, candidates)
	mutants := catalog.Mutants()

	results := make([]report.MutantResult, 0, len(mutants))
	for i, m := range mutants {
		c := candidates[i]
		results = append(results, report.MutantResult{
			ID:                   m.ID,
			Outcome:              c.outcome,
			NotRunReason:         c.notRun,
			Duration:             c.duration,
			KilledBy:             c.killedBy,
			Attempts:             c.attempts,
			OutputTail:           c.tail,
			Executions:           c.executions,
			CoveringTestPackages: c.covering,
			CoveringTests:        c.coveringTests,
			Uncovered:            c.uncovered,
		})
	}

	opts := coverageOptions(t)
	opts.RunID = testCoverageRunID
	opts.Catalog = catalog
	opts.Located = located
	opts.Results = results
	opts.CoverageMode = report.CoverageTest
	opts.CoverageTests = testCoverageTests
	return opts
}

func buildTestCoverageFixture(t *testing.T) *report.Report {
	t.Helper()
	r, err := report.Build(testCoverageOptions(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}
