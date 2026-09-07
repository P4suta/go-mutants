// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// The fixed inputs of the fixture run. Nothing here is read from the machine
// the tests run on: the platform is named rather than detected and the clock is
// a constant, so the golden document is the same file on every developer's
// laptop and in CI.
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
	// staleID is a well formed id that no fixture mutant has.
	staleID = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

// fixtureDigest is the workspace digest the fixture run reports. It is a real
// 64 hex characters because [report.Build] refuses anything else.
var fixtureDigest = strings.Repeat("ab", 32)

// The run facts of the fixture: what its phases and stages cost, what its
// validation spent, what its snapshot turned out to be, and whose toolchain ran
// it.
//
// They are named here rather than measured for the reason the clock above is: a
// golden document has to be the same file on every machine, and a report that
// carried this machine's `go version` would be a golden of the laptop that
// generated it.
const (
	fixtureGoBin            = "/usr/local/go/bin/go"
	fixtureGoVersionLine    = "go version go1.26.0 linux/amd64"
	fixtureValidationBuilds = 3
	fixtureSnapshotFiles    = 128
)

// fixturePhases is where the fixture run's wall clock went, in run order. The
// four phases are internal/engine's, and they add up to less than the run's own
// duration because a run is not only its phases.
var fixturePhases = []report.PhaseTiming{
	{Name: "discover", DurationMS: 1_200},
	{Name: "baseline", DurationMS: 4_300},
	{Name: "mutate", DurationMS: 35_000},
	{Name: "report", DurationMS: 90},
}

// fixtureStages is the same run one level down, in the order the spans closed.
//
// One of them failed and the run carried on, which is the case a reader of a
// slow run is looking for: the coverage-instrumented build that did not compile
// and cost the run a second one. The stage names are engine stages and the
// results are the trace's vocabulary; see [report.StageResults].
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

// A candidate is one row of the fixture's discovery output, written the way a
// person can read it.
type candidate struct {
	path        string
	pkg         string
	rule        string
	start       uint32
	original    string
	replacement string
	line        int
	column      int
	// outcome is what execution made of it, or the zero value when the
	// candidate is the one validation rejected.
	outcome mutation.Outcome
	// notRun is why a not-run candidate was not run, and is empty for every
	// other outcome — the pairing [report.Build] enforces in both directions.
	notRun report.NotRunReason
	// rejected marks the one candidate validation refused.
	rejected bool
	killedBy string
	attempts int
	duration time.Duration
	tail     string
	// covering are the test binaries a coverage-guided run found reaching this
	// mutant's lines, and uncovered marks the one nothing reaches. Both are
	// zero for the coverage-off fixture, which is what a run that never asked
	// carries.
	covering  []string
	uncovered bool
	// cached marks an outcome this run adopted from the outcome cache instead
	// of measuring. It is only ever set on a reusable outcome, which is the
	// pairing [report.Build] enforces.
	cached bool
	// branch is discovery's branch proof, and is nil in every fixture but the
	// one written to carry it: a proof is a statement about the source, so
	// attaching one to an edit that does not narrow a condition would put an
	// untruth into a golden document.
	branch *discover.BranchProof
	// executions are the passes the run made over the test binaries for this
	// mutant, one per attempt. They are written out beside the attempt count
	// rather than derived from it, because the two are the same fact at two
	// resolutions and a fixture that computed one from the other could not
	// catch a builder that lets them disagree.
	//
	// They are empty for the cached candidates and for the one nothing
	// reached, which is what those mutants really carry: an execution is a
	// pass *this* run made, and neither of them made one.
	executions []report.Execution
}

// fixtureCandidates covers every outcome the document can carry, plus a
// rejection, across two files and two packages.
//
// They are written in catalogue order — by path, then by span — so that the
// results below can be matched to them positionally; [locatedFixtures] asserts
// that the catalogue agrees rather than trusting the reading.
var fixtureCandidates = []candidate{
	{
		path: alphaFile, pkg: alphaPackage, rule: "eq-to-neq",
		start: 100, original: "==", replacement: "!=", line: 12, column: 9,
		outcome: mutation.OutcomeKilled, killedBy: alphaPackage, attempts: 1,
		duration: 120 * time.Millisecond, tail: "--- FAIL: TestAdd (0.00s)",
		// Adopted from the outcome cache, which is why the duration and the tail
		// belong to the run that first measured it rather than to this one.
		cached: true,
	},
	{
		path: alphaFile, pkg: alphaPackage, rule: "true-to-false",
		start: 140, original: "true", replacement: "false", line: 18, column: 16,
		outcome: mutation.OutcomeSurvived, attempts: 1, duration: 95 * time.Millisecond,
		// A survivor is measured against every binary, in launch order, because
		// nothing stopped the pass early.
		// The peak is on an ordinary row, because it is written for every
		// execution and not only for the ones a bound stopped: the question it
		// answers — which mutants cost the machine most — is asked of a run in
		// which nothing went wrong.
		executions: []report.Execution{{
			Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 95,
			Binaries: []string{alphaPackage, betaPackage}, PeakRSSBytes: 41_943_040,
		}},
	},
	{
		path: alphaFile, pkg: alphaPackage, rule: "lt-to-le",
		start: 200, original: "<", replacement: "<=", line: 24, column: 7,
		outcome: mutation.OutcomeTimedOut, killedBy: alphaPackage, attempts: 2,
		duration: 20 * time.Second, tail: "panic: test timed out after 10s",
		// A confirmed timeout is reusable and this one was reused: two attempts
		// agreed once, and the cache is entitled to remember that they did.
		cached: true,
	},
	{
		path: alphaFile, pkg: alphaPackage, rule: "ge-to-gt",
		start: 260, original: ">=", replacement: ">", line: 31, column: 12,
		outcome: mutation.OutcomeInconclusive, attempts: 2, duration: 11 * time.Second,
		// The shape of an inconclusive verdict, which is the whole reason a
		// reader wants the attempts rather than the count of them: one worker
		// timed the mutant out, the serial retry ran it to the end, and the two
		// attempts disagree. No attempt is ever `inconclusive` itself — that is
		// a verdict about the pair.
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
		// The binary that would not start is still the binary the attempt
		// reached, which is what makes an errored mutant investigable at all.
		executions: []report.Execution{{
			Attempt: 1, Worker: 1, Outcome: report.OutcomeErrored, DurationMS: 4,
			Binaries: []string{betaPackage},
		}},
	},
	{
		// Selected — it is inside the `selected` count below — and never
		// reached, which is what a not-run mutant is when nothing narrowed the
		// run: the reason a document gives for one it meant to measure.
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
		// The confirmed timeout this run measured itself, as against the one
		// above that it adopted from the cache. It is here so that the golden
		// shows what a confirmed timeout looks like in detail: two passes, both
		// of which timed out, the second of them alone on the machine. Nothing
		// else in the document can distinguish that from a single timeout the
		// retry did not reproduce — which is the inconclusive mutant, and which
		// looks identical at `attempts: 2`.
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

// fixtureDiagnostic is the compiler's word on the rejected candidate.
const fixtureDiagnostic = "internal/beta/beta.go:19:11: invalid operation: mismatched types"

// fixtureSkips are handed to the builder out of order, so that the sorted
// output proves the builder sorts rather than that discovery did.
var fixtureSkips = []discover.Skip{
	{Path: "internal/gamma/gamma.go", Reason: discover.SkipExcluded, Count: 1},
	{Path: alphaFile, Reason: discover.SkipConstDecl, Count: 4},
	{Path: alphaFile, Reason: discover.SkipArrayLength, Count: 1},
}

// fixtureWarnings are in publication order and stay that way.
//
// The second is the reason this fixture's coverage mode is `off`, and it is one
// half of a pair: the coverage-instrumented test binaries would not compile, so
// the run said so in one line, built plain ones, and measured every mutant
// against every binary. The other half is
// `coverage.build_fallback` with [fixtureCoverageFallback] under it — the same
// event as the console saw it and as somebody asking why needs it — and the
// failed `build-binaries` stage in [fixtureStages] is the same event again, on
// the timeline. A document in which those three disagreed would be describing
// three different runs. The coverage-guided shape of the document is pinned by
// its own golden; see [coverageOptions].
var fixtureWarnings = []report.Warning{
	{Code: "GOM4040", Message: "the snapshot directory could not be removed: access is denied"},
	{Code: "GOM7602", Message: "coverage-guided selection is off because the test binaries do not compile with coverage instrumentation (GOM7505: the test binaries do not compile); every mutant will be measured against every test binary, which is slower and never wrong"},
}

// locatedFixtures turns the main fixture's candidates into discovery output and
// the catalogue built from it.
func locatedFixtures(t *testing.T) ([]discover.Located, *mutation.Catalog) {
	t.Helper()
	return located(t, fixtureCandidates)
}

// located turns any list of candidates into discovery output and the catalogue
// built from it.
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
	// The results below are matched to the candidates positionally, which is
	// only sound while the catalogue keeps them in the order they are written.
	// Catalogue order is (path, span, rule position), so this holds by
	// construction — and is asserted rather than assumed, because a fixture that
	// silently attaches the wrong outcome to the wrong mutant would make every
	// golden below meaningless.
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

// fixtureOptions is one complete, believable run: every outcome, a rejection,
// three skips, two warnings, and a ledger holding one of each expectation
// state.
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
			Executions:   c.executions,
			Cached:       c.cached,
		})
	}

	cfg := config.Defaults()
	cfg.Mutation.Expect = []config.Expectation{
		// Fulfilled: the boolean literal survived, exactly as the ledger says.
		{ID: mutants[1].ID, Reason: "the flag is only read by the debug logger"},
		// Unfulfilled by a detection: the tests now catch this one, so the
		// ledger row is lying.
		{ID: mutants[0].ID, Reason: "the boundary is checked by the caller"},
		// Stale: no such mutant in this catalogue any more.
		{ID: staleID, Reason: "kept to prove a stale row is reported"},
	}

	started, err := time.Parse(time.RFC3339, fixtureStarted)
	if err != nil {
		t.Fatalf("parsing the fixture clock: %v", err)
	}
	return report.Options{
		ToolVersion:     fixtureToolVersion,
		RunID:           fixtureRunID,
		Status:          report.StatusCompleted,
		Started:         started,
		Finished:        started.Add(fixtureDuration),
		Config:          cfg,
		Mode:            report.ModeAll,
		Selected:        len(results),
		ModulePath:      fixtureModulePath,
		GoVersion:       fixtureGoVersion,
		WorkspaceDigest: fixtureDigest,
		Platform:        report.Platform{OS: "linux", Arch: "amd64"},
		Catalog:         catalog,
		Located:         located,
		Skips:           fixtureSkips,
		Results:         results,
		Rejections:      rejections,
		TestCommand:     []string{"go", "test", "./..."},
		Baseline:        []time.Duration{1200 * time.Millisecond, 1500 * time.Millisecond, 1350 * time.Millisecond},
		Timeout:         10 * time.Second,
		TimeoutSource:   report.TimeoutDerived,
		Memory:          1 << 30,
		MemorySource:    report.MemoryDerived,
		// Two of the eight executable mutants were adopted from the cache; the
		// other six were looked up, not found, and measured. Three of those six
		// outcomes were worth storing — two survivors and the confirmed timeout
		// — while the inconclusive, the errored and the interrupted one are not
		// outcomes a later run may reuse. See [report.Cache].
		CacheMode:   report.CacheOn,
		CacheMisses: 6,
		CacheWrites: 3,
		Warnings:    fixtureWarnings,
		// The run facts. They are what makes this document answer "why was this
		// slow" and "how was this mutant really run" without a recording beside
		// it, and the coverage pair is the fixture's failed instrumented build:
		// the stage above that says `failed` is the same event, seen from the
		// timeline.
		Timing:     &report.Timing{Phases: fixturePhases, Stages: fixtureStages},
		Validation: &report.Validation{Builds: fixtureValidationBuilds},
		Snapshot:   &report.SnapshotFacts{StableDir: true, Files: fixtureSnapshotFiles},
		Toolchain:  &report.ToolchainFacts{GoBin: fixtureGoBin, Version: fixtureGoVersionLine},
		// The argv the run really started: the same command with the located
		// toolchain in place of the bare `go`.
		ResolvedCommand:           []string{fixtureGoBin, "test", "./..."},
		CoverageBuildFallback:     true,
		CoverageUnavailableReason: fixtureCoverageFallback,
	}
}

// fixtureCoverageFallback is the whole failure that made the fixture run give
// up coverage-instrumented test binaries: the coded message and, under it, what
// the compiler printed. The console got one line of it; this is the copy for
// somebody asking why.
const fixtureCoverageFallback = "GOM7505: the test binaries do not compile\n" +
	"internal/alpha/alpha.go:12:9: undefined: coverdata"

// buildFixture builds the fixture report, failing the test if it cannot be
// built at all.
func buildFixture(t *testing.T) *report.Report {
	t.Helper()
	r, err := report.Build(fixtureOptions(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}

// The coverage-guided fixture, which is a second, smaller run rather than a
// variation of the one above.
//
// It has to be its own run because `uncovered` is only meaningful next to the
// mutant it describes: an uncovered mutant is a survivor with no attempts, and
// [report.Build] refuses every other combination. Bolting a flag onto the
// candidates of the main fixture would either state that contradiction or leave
// the interesting row untested, so this one is built to say the three things
// that matter and nothing else — a mutant two binaries cover, a mutant one
// covers, and a mutant nothing covers.
const (
	coverageRunID     = "20260218T094500Z-5b1d"
	coreFile          = "internal/core/core.go"
	edgeFile          = "internal/edge/edge.go"
	corePackage       = "example.com/m/internal/core"
	edgePackage       = "example.com/m/internal/edge"
	coverageBinaries  = 2
	coverageStartedAt = "2026-02-18T09:45:00Z"
)

// coverageCandidates are written in catalogue order, as [fixtureCandidates] are.
var coverageCandidates = []candidate{
	{
		path: coreFile, pkg: corePackage, rule: "lt-to-le",
		start: 64, original: "<", replacement: "<=", line: 9, column: 5,
		outcome: mutation.OutcomeKilled, killedBy: corePackage, attempts: 1,
		duration: 140 * time.Millisecond, tail: "--- FAIL: TestClamp (0.00s)",
		covering: []string{corePackage, edgePackage},
		// The pass stopped at the binary that caught it, so it names one of the
		// two binaries coverage said reach this mutant rather than both.
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
		// Coverage narrowed the pass to the one binary that reaches the line,
		// which is the whole point of a coverage-guided run and is visible here
		// and nowhere else in the document.
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

// coverageOptions is one complete coverage-guided run.
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

// buildCoverageFixture builds the coverage-guided fixture report.
func buildCoverageFixture(t *testing.T) *report.Report {
	t.Helper()
	r, err := report.Build(coverageOptions(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}
