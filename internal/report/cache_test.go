// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func cacheCandidate(outcome mutation.Outcome, cached bool) candidate {
	c := candidate{
		path: alphaFile, pkg: alphaPackage, rule: "eq-to-neq",
		start: 100, original: "==", replacement: "!=", line: 12, column: 9,
		outcome: outcome, attempts: 1, duration: 10 * time.Millisecond,
		cached: cached,
	}
	if outcome == mutation.OutcomeNotRun {
		c.notRun = report.NotRunInterrupted
		c.attempts = 0
	}
	return c
}

func buildCache(t *testing.T, c candidate, mode report.CacheMode, misses, writes int) (*report.Report, error) {
	t.Helper()
	rows, catalog := located(t, []candidate{c})
	mutants := catalog.Mutants()
	started, err := time.Parse(time.RFC3339, fixtureStarted)
	if err != nil {
		t.Fatalf("parsing the fixture clock: %v", err)
	}
	return report.Build(report.Options{
		ToolVersion:     fixtureToolVersion,
		RunID:           fixtureRunID,
		Status:          report.StatusCompleted,
		Started:         started,
		Finished:        started.Add(time.Second),
		Config:          config.Defaults(),
		Mode:            report.ModeAll,
		Selected:        1,
		ModulePath:      fixtureModulePath,
		GoVersion:       fixtureGoVersion,
		WorkspaceDigest: fixtureDigest,
		Platform:        report.Platform{OS: "linux", Arch: "amd64"},
		Catalog:         catalog,
		Located:         rows,
		Results: []report.MutantResult{{
			ID:           mutants[0].ID,
			Outcome:      c.outcome,
			NotRunReason: c.notRun,
			Duration:     c.duration,
			Attempts:     c.attempts,
			Uncovered:    c.uncovered,
			Cached:       c.cached,
		}},
		TestCommand:      []string{"go", "test", "./..."},
		Baseline:         []time.Duration{time.Second},
		Timeout:          10 * time.Second,
		TimeoutSource:    report.TimeoutDerived,
		CoverageMode:     coverageModeFor(c),
		CoverageBinaries: 1,
		CacheMode:        mode,
		CacheMisses:      misses,
		CacheWrites:      writes,
	})
}

func coverageModeFor(c candidate) report.CoverageMode {
	if c.uncovered {
		return report.CoveragePackage
	}
	return report.CoverageOff
}

func TestCacheHitsAreCountedFromTheRows(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	counted := 0
	for _, m := range r.Mutants {
		if m.Cached {
			counted++
		}
	}
	if r.Cache.Hits != counted {
		t.Errorf("cache.hits = %d, want the %d cached rows", r.Cache.Hits, counted)
	}
	if counted == 0 {
		t.Fatal("the fixture has no cached mutant, so this proves nothing")
	}
	if r.Cache.Mode != report.CacheOn {
		t.Errorf("cache.mode = %q, want %q", r.Cache.Mode, report.CacheOn)
	}
	if r.Cache.Writes > r.Cache.Misses {
		t.Errorf("the fixture stored %d outcomes from %d misses", r.Cache.Writes, r.Cache.Misses)
	}
}

func TestACacheThatWasOffStatesNoNumbersItDidNotMeasure(t *testing.T) {
	t.Parallel()

	r, err := buildCache(t, cacheCandidate(mutation.OutcomeKilled, false), "", 0, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Cache.Mode != report.CacheOff {
		t.Errorf("the zero mode became %q, want %q", r.Cache.Mode, report.CacheOff)
	}
	if r.Cache.Hits != 0 || r.Cache.Misses != 0 || r.Cache.Writes != 0 {
		t.Errorf("a cache that was off reported %+v", r.Cache)
	}
	if err = schemas.Validate(schemas.RunReportV1, mutantkit.MustMarshal(t, r)); err != nil {
		t.Errorf("the document does not satisfy the schema: %v", err)
	}
}

func TestBuildRefusesACacheBlockTheMutantsContradict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		row     candidate
		mode    report.CacheMode
		misses  int
		writes  int
		mustSay string
	}{
		{
			name: "a reused outcome in a run that read nothing",
			row:  cacheCandidate(mutation.OutcomeKilled, true),
			mode: report.CacheOff, mustSay: "cannot have been reused",
		},
		{
			name: "an outcome the cache does not store",
			row:  cacheCandidate(mutation.OutcomeInconclusive, true),
			mode: report.CacheOn, misses: 1, mustSay: "not an outcome the cache stores",
		},
		{
			name: "a not-run mutant claiming to have been reused",
			row:  cacheCandidate(mutation.OutcomeNotRun, true),
			mode: report.CacheOn, misses: 1, mustSay: "not an outcome the cache stores",
		},
		{
			name: "an uncovered mutant claiming to have been reused",
			row: func() candidate {
				c := cacheCandidate(mutation.OutcomeSurvived, true)
				c.uncovered = true
				c.attempts = 0
				c.duration = 0
				return c
			}(),
			mode: report.CacheOn, misses: 1, mustSay: "cached and uncovered",
		},
		{
			name: "more stored than measured",
			row:  cacheCandidate(mutation.OutcomeKilled, false),
			mode: report.CacheOn, misses: 1, writes: 2, mustSay: "only stored for a mutant",
		},
		{
			name: "misses in a run that consulted nothing",
			row:  cacheCandidate(mutation.OutcomeKilled, false),
			mode: report.CacheOff, misses: 3, mustSay: "has no misses",
		},
		{
			name: "a negative counter",
			row:  cacheCandidate(mutation.OutcomeKilled, false),
			mode: report.CacheOn, misses: -1, mustSay: "-1 cache misses",
		},
		{
			name: "a mode this build does not know",
			row:  cacheCandidate(mutation.OutcomeKilled, false),
			mode: report.CacheMode("auto"), mustSay: "is not a cache mode",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildCache(t, c.row, c.mode, c.misses, c.writes)
			if err == nil {
				t.Fatal("Build wrote the contradiction into a document")
			}
			if code := report.CodeOf(err); code != report.CodeInvalidCache {
				t.Errorf("code = %q, want %q (%v)", code, report.CodeInvalidCache, err)
			}
			if !strings.Contains(err.Error(), c.mustSay) {
				t.Errorf("the refusal does not say %q: %v", c.mustSay, err)
			}
		})
	}
}

func TestTheDocumentAndTheStoreAgreeAboutWhatIsReusable(t *testing.T) {
	t.Parallel()

	for _, outcome := range mutation.Outcomes() {
		t.Run(outcome.String(), func(t *testing.T) {
			t.Parallel()

			_, err := buildCache(t, cacheCandidate(outcome, true), report.CacheOn, 1, 0)
			accepted := err == nil
			if want := cache.Cacheable(outcome); accepted != want {
				t.Errorf("a cached %s row is accepted by the document = %t, but the store stores it = %t (%v)",
					outcome, accepted, want, err)
			}
		})
	}
}

func TestTheCacheBlockIsInTheSchemaAndTheModel(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	data := mutantkit.MustMarshal(t, r)
	if err := schemas.Validate(schemas.RunReportV1, data); err != nil {
		t.Fatalf("the fixture does not satisfy the schema: %v", err)
	}
	for _, want := range []string{`"cache"`, `"hits"`, `"misses"`, `"writes"`, `"cached"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the document does not carry %s", want)
		}
	}
	parsed, err := report.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Cache != r.Cache {
		t.Errorf("the cache block round-tripped to %+v, want %+v", parsed.Cache, r.Cache)
	}
	for i, m := range parsed.Mutants {
		if m.Cached != r.Mutants[i].Cached {
			t.Errorf("mutant %d round-tripped cached = %t, want %t", i, m.Cached, r.Mutants[i].Cached)
		}
	}
}

func TestEveryCacheModeIsInTheSchema(t *testing.T) {
	t.Parallel()

	for _, mode := range report.CacheModes() {
		if !mode.Valid() {
			t.Errorf("the mode %q is listed and not valid", mode)
		}
		r, err := buildCache(t, cacheCandidate(mutation.OutcomeKilled, false), mode, missesFor(mode), 0)
		if err != nil {
			t.Fatalf("building a %q run: %v", mode, err)
		}
		if err = schemas.Validate(schemas.RunReportV1, mutantkit.MustMarshal(t, r)); err != nil {
			t.Errorf("a %q run does not satisfy the schema: %v", mode, err)
		}
	}
}

func missesFor(mode report.CacheMode) int {
	if mode == report.CacheOn {
		return 1
	}
	return 0
}
