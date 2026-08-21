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
	},
	{
		path: betaFile, pkg: betaPackage, rule: "neq-to-eq",
		start: 42, original: "!=", replacement: "==", line: 5, column: 6,
		outcome: mutation.OutcomeSurvived, attempts: 1, duration: 80 * time.Millisecond,
	},
	{
		path: betaFile, pkg: betaPackage, rule: "false-to-true",
		start: 77, original: "false", replacement: "true", line: 9, column: 20,
		outcome: mutation.OutcomeErrored, attempts: 1, duration: 4 * time.Millisecond,
		tail: "exec: the test binary could not be started",
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
// The second is the reason this fixture's coverage mode is `off`: a run with a
// custom test command cannot map a test binary onto the lines it reached, so it
// says so and measures every mutant against every binary. The coverage-guided
// shape of the document is pinned by its own golden; see [coverageOptions].
var fixtureWarnings = []report.Warning{
	{Code: "GOM4040", Message: "the snapshot directory could not be removed: access is denied"},
	{Code: "GOM7601", Message: "coverage-guided selection is off: test.command is not the built-in `go test ./...`"},
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
		// Two of the seven executable mutants were adopted from the cache; the
		// other five were looked up, not found, and measured. Two of those five
		// outcomes were worth storing — the survivor and the other survivor —
		// while the inconclusive, the errored and the interrupted one are not
		// outcomes a later run may reuse. See [report.Cache].
		CacheMode:   report.CacheOn,
		CacheMisses: 5,
		CacheWrites: 2,
		Warnings:    fixtureWarnings,
	}
}

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
	},
	{
		path: coreFile, pkg: corePackage, rule: "neq-to-eq",
		start: 120, original: "!=", replacement: "==", line: 15, column: 9,
		outcome: mutation.OutcomeSurvived, attempts: 1, duration: 90 * time.Millisecond,
		covering: []string{edgePackage},
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
