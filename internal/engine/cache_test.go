// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

type cacheFixture struct {
	opts    Options
	out     *RunOutcome
	catalog string
}

func newCacheFixture(t *testing.T, root string) cacheFixture {
	t.Helper()
	cfg := config.Defaults()
	return cacheFixture{
		opts: Options{
			Config:      cfg,
			ToolVersion: "0.0.0-test",
			CacheRoot:   root,
		},
		out: &RunOutcome{
			WorkspaceDigest: strings.Repeat("ab", 32),
			TestCommand:     config.DefaultTestCommand(),
			Timeout:         10 * time.Second,
			Toolchain: gocmd.Toolchain{Version: gocmd.Version{
				Raw:     "go version go1.26.5 " + runtime.GOOS + "/" + runtime.GOARCH,
				Release: "go1.26.5",
				GOOS:    runtime.GOOS,
				GOARCH:  runtime.GOARCH,
			}},
		},
		catalog: strings.Repeat("cd", 32),
	}
}

func (f cacheFixture) runs() []execute.MutantRun {
	out := make([]execute.MutantRun, 0, len(ids))
	for _, id := range ids {
		out = append(out, execute.MutantRun{ID: id, Timeout: f.out.Timeout})
	}
	return out
}

func newState() *state {
	return &state{
		results: make(map[string]report.MutantResult),
		display: make(map[string]MutantResult),
		notRun:  make(map[string]report.NotRunReason),
	}
}

func measured() []execute.MutantResult {
	return []execute.MutantResult{
		{ID: ids[0], Final: mutation.OutcomeKilled, Duration: 120 * time.Millisecond,
			KilledBy: "example.com/m", Attempts: make([]execute.Attempt, 1), OutputTail: "--- FAIL: TestAdd"},
		{ID: ids[1], Final: mutation.OutcomeSurvived, Duration: 95 * time.Millisecond,
			Attempts: make([]execute.Attempt, 1)},
		{ID: ids[2], Final: mutation.OutcomeInconclusive, Duration: 11 * time.Second,
			Attempts: make([]execute.Attempt, 2)},
		{ID: ids[3], Final: mutation.OutcomeNotRun},
	}
}

func TestTheSecondRunReadsWhatTheFirstWrote(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())

	cold := &session{}
	coldState := newState()
	remaining := cold.cachePhase(f.opts, f.catalog, f.out, f.runs(), coldState)
	if len(remaining) != len(ids) {
		t.Fatalf("a cold cache left %d mutants to execute, want %d", len(remaining), len(ids))
	}
	if coldState.cache.hits != 0 || coldState.cache.misses != len(ids) {
		t.Errorf("cold run: hits=%d misses=%d, want 0 and %d",
			coldState.cache.hits, coldState.cache.misses, len(ids))
	}
	if coldState.cache.Mode() != report.CacheOn {
		t.Errorf("mode = %q, want %q", coldState.cache.Mode(), report.CacheOn)
	}
	if len(cold.warnings) != 0 {
		t.Errorf("a working cache warned: %+v", cold.warnings)
	}

	cold.storeOutcomes(f.opts, measured(), coldState)
	if got, want := coldState.cache.writes, 2; got != want {
		t.Errorf("the run stored %d outcomes, want %d", got, want)
	}

	warm := &session{}
	warmState := newState()
	left := warm.cachePhase(f.opts, f.catalog, f.out, f.runs(), warmState)
	if got, want := warmState.cache.hits, 2; got != want {
		t.Fatalf("the second run had %d hits, want %d", got, want)
	}
	if got, want := len(left), 2; got != want {
		t.Errorf("the second run has %d mutants to execute, want %d", got, want)
	}
	for _, id := range []string{ids[2], ids[3]} {
		if !hasRun(left, id) {
			t.Errorf("mutant %s was not re-measured, and its outcome is not one the cache stores", id[:8])
		}
	}
	killed := warmState.results[ids[0]]
	switch {
	case !killed.Cached:
		t.Error("the adopted outcome is not marked cached")
	case killed.Outcome != mutation.OutcomeKilled:
		t.Errorf("the adopted outcome is %s, want killed", killed.Outcome)
	case killed.Duration != 120*time.Millisecond:
		t.Errorf("the adopted duration is %s, want the one the first run measured", killed.Duration)
	case killed.Attempts != 1:
		t.Errorf("the adopted attempt count is %d, want 1", killed.Attempts)
	case killed.KilledBy != "example.com/m":
		t.Errorf("the adopted killed_by is %q, want the first run's", killed.KilledBy)
	case killed.OutputTail != "--- FAIL: TestAdd":
		t.Errorf("the adopted output tail is %q, want the first run's", killed.OutputTail)
	}
}

func TestACacheHitPublishesBothEvents(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	seed := &session{}
	seedState := newState()
	seed.cachePhase(f.opts, f.catalog, f.out, f.runs(), seedState)
	seed.storeOutcomes(f.opts, measured(), seedState)

	events := make(chan Event, 16)
	warm := &session{events: events}
	warm.cachePhase(f.opts, f.catalog, f.out, f.runs(), newState())
	close(events)

	hits, finished, started := 0, 0, 0
	for event := range events {
		switch e := event.(type) {
		case CacheHit:
			hits++
			if !mutationCacheable(e.Outcome) {
				t.Errorf("a cache hit reported %s, which the cache does not store", e.Outcome)
			}
		case MutantFinished:
			finished++
			if !e.Result.Cached {
				t.Error("the finish of a cache hit is not marked cached")
			}
		case MutantStarted:
			started++
		}
	}
	if hits != 2 || finished != 2 {
		t.Errorf("published %d cache hits and %d finishes, want 2 of each", hits, finished)
	}
	if started != 0 {
		t.Errorf("published %d starts for mutants that never started", started)
	}
}

func TestAnExpectedMutantIsNeverCached(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	f.opts.Config.Mutation.Expect = []config.Expectation{
		{ID: ids[1], Reason: "the flag is only read by the debug logger"},
	}

	first := &session{}
	firstState := newState()
	first.cachePhase(f.opts, f.catalog, f.out, f.runs(), firstState)
	first.storeOutcomes(f.opts, measured(), firstState)
	if got, want := firstState.cache.writes, 1; got != want {
		t.Errorf("the run stored %d outcomes, want %d", got, want)
	}
	if got, want := firstState.cache.misses, len(ids)-1; got != want {
		t.Errorf("the run recorded %d misses, want %d", got, want)
	}

	second := &session{}
	secondState := newState()
	left := second.cachePhase(f.opts, f.catalog, f.out, f.runs(), secondState)
	if !hasRun(left, ids[1]) {
		t.Error("an expected mutant was answered from the cache")
	}
	if got, want := secondState.cache.hits, 1; got != want {
		t.Errorf("the second run had %d hits, want %d", got, want)
	}
}

func TestAutoStandsDownForACustomCommand(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	f.out.TestCommand = []string{"make", "test"}

	s := &session{}
	st := newState()
	left := s.cachePhase(f.opts, f.catalog, f.out, f.runs(), st)
	if len(left) != len(ids) {
		t.Errorf("a stood-down cache narrowed the run to %d mutants", len(left))
	}
	if st.cache.Mode() != report.CacheOff {
		t.Errorf("mode = %q, want %q", st.cache.Mode(), report.CacheOff)
	}
	if len(s.warnings) != 1 {
		t.Fatalf("published %d warnings, want one saying why: %+v", len(s.warnings), s.warnings)
	}
	if code := s.warnings[0].Code; code != "GOM7901" {
		t.Errorf("the warning is %s, want GOM7901", code)
	}
	s.storeOutcomes(f.opts, measured(), st)
	if st.cache.writes != 0 {
		t.Errorf("a stood-down cache stored %d outcomes", st.cache.writes)
	}
}

func TestCacheOffDoesNothingAndSaysNothing(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	f.opts.Config.Cache.Mode = config.CacheOff

	s := &session{}
	st := newState()
	left := s.cachePhase(f.opts, f.catalog, f.out, f.runs(), st)
	if len(left) != len(ids) {
		t.Errorf("a disabled cache narrowed the run to %d mutants", len(left))
	}
	if len(s.warnings) != 0 {
		t.Errorf("turning the cache off warned: %+v", s.warnings)
	}
	if st.cache.Mode() != report.CacheOff || st.cache.hits != 0 || st.cache.misses != 0 {
		t.Errorf("a disabled cache reported %+v", st.cache)
	}
}

func TestOnReusesOutcomesForACustomCommand(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	f.opts.Config.Cache.Mode = config.CacheOn
	f.out.TestCommand = []string{"go", "test", "-count=1", "./..."}

	first := &session{}
	firstState := newState()
	first.cachePhase(f.opts, f.catalog, f.out, f.runs(), firstState)
	first.storeOutcomes(f.opts, measured(), firstState)
	if firstState.cache.writes == 0 {
		t.Fatal("cache.mode on stored nothing for a custom command")
	}
	if len(first.warnings) != 0 {
		t.Errorf("cache.mode on warned about the command the user chose: %+v", first.warnings)
	}

	second := &session{}
	secondState := newState()
	second.cachePhase(f.opts, f.catalog, f.out, f.runs(), secondState)
	if secondState.cache.hits == 0 {
		t.Error("cache.mode on read nothing back")
	}
}

func TestAnEditedWorkspaceIsAllMisses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	f := newCacheFixture(t, root)
	first := &session{}
	firstState := newState()
	first.cachePhase(f.opts, f.catalog, f.out, f.runs(), firstState)
	first.storeOutcomes(f.opts, measured(), firstState)

	edited := newCacheFixture(t, root)
	edited.out.WorkspaceDigest = strings.Repeat("fe", 32)
	second := &session{}
	secondState := newState()
	left := second.cachePhase(edited.opts, edited.catalog, edited.out, edited.runs(), secondState)
	if secondState.cache.hits != 0 {
		t.Errorf("an edited workspace had %d hits", secondState.cache.hits)
	}
	if len(left) != len(ids) {
		t.Errorf("an edited workspace left %d mutants to execute, want %d", len(left), len(ids))
	}
}

func TestAnUnopenableCacheIsAWarningAndNothingElse(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	f.out.WorkspaceDigest = "not-a-digest"

	s := &session{}
	st := newState()
	left := s.cachePhase(f.opts, f.catalog, f.out, f.runs(), st)
	if len(left) != len(ids) {
		t.Errorf("a broken cache narrowed the run to %d mutants", len(left))
	}
	if st.cache.Mode() != report.CacheOff {
		t.Errorf("mode = %q, want %q", st.cache.Mode(), report.CacheOff)
	}
	if len(s.warnings) != 1 {
		t.Fatalf("published %d warnings, want one: %+v", len(s.warnings), s.warnings)
	}
	if message := s.warnings[0].Message; !strings.Contains(message, "measured") {
		t.Errorf("the warning does not say what the run is doing instead: %q", message)
	}
	if strings.Contains(s.warnings[0].Message, "\n") {
		t.Errorf("the warning is not one line: %q", s.warnings[0].Message)
	}
	if strings.Count(s.warnings[0].Message, "GOM") > 1 {
		t.Errorf("the warning repeats a code inside its message: %q", s.warnings[0].Message)
	}
}

func TestTheCacheRootFollowsTheHistoryRoot(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		opts Options
		want string
	}{
		"neither":    {Options{}, ""},
		"history":    {Options{HistoryRoot: "history"}, "history"},
		"cache":      {Options{CacheRoot: "cache"}, "cache"},
		"cache wins": {Options{HistoryRoot: "history", CacheRoot: "cache"}, "cache"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := cacheRoot(c.opts); got != c.want {
				t.Errorf("cacheRoot = %q, want %q", got, c.want)
			}
		})
	}
}

func hasRun(runs []execute.MutantRun, id string) bool {
	for _, run := range runs {
		if run.ID == id {
			return true
		}
	}
	return false
}

func mutationCacheable(o mutation.Outcome) bool {
	return o == mutation.OutcomeKilled || o == mutation.OutcomeSurvived || o == mutation.OutcomeTimedOut
}

func TestAWarmRunExplainsADivergenceTheWayTheColdRunDid(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	results := measured()
	results[0].Final = mutation.OutcomeTimedOut
	results[0].Duration = 20 * time.Millisecond
	results[0].Attempts = []execute.Attempt{{Outcome: mutation.OutcomeTimedOut, Diverged: true}}

	seed := &session{}
	seedState := newState()
	seed.cachePhase(f.opts, f.catalog, f.out, f.runs(), seedState)
	seed.storeOutcomes(f.opts, results, seedState)

	warmOut := f.out
	warmOut.Timeout = 100 * f.out.Timeout

	events := make(chan Event, 16)
	warm := &session{events: events}
	warmState := newState()
	warm.cachePhase(f.opts, f.catalog, warmOut, f.runs(), warmState)
	close(events)

	adopted, found := warmState.results[ids[0]]
	if !found {
		t.Fatal("the warm run did not adopt the divergence at all")
	}
	if !adopted.Cached {
		t.Error("the adopted result is not marked cached")
	}
	if adopted.Outcome != mutation.OutcomeTimedOut {
		t.Errorf("outcome = %s, want %s", adopted.Outcome, mutation.OutcomeTimedOut)
	}
	if !adopted.Diverged {
		t.Error("the warm run lost the fact that a counted loop settled it, so it reports the weaker finding")
	}

	timeouts, diverged := 0, 0
	for event := range events {
		e, ok := event.(MutantFinished)
		if !ok || e.Result.Outcome != mutation.OutcomeTimedOut {
			continue
		}
		timeouts++
		if e.Result.Diverged {
			diverged++
		}
	}
	if timeouts != 1 {
		t.Fatalf("published %d finished timeouts, want 1", timeouts)
	}
	if diverged != 1 {
		t.Error("the event a renderer draws lost the fact that a counted loop settled it")
	}
}
