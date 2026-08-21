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

// cacheFixture is the state and the options one cache-stage test drives.
//
// The workspace digest and the catalogue digest are fixed, so two sessions
// built from one fixture compute the same key and the second really does read
// what the first wrote. That is the property being tested: the stage is only
// worth anything if the second run of an unchanged workspace finds the first
// run's answers.
type cacheFixture struct {
	opts    Options
	out     *RunOutcome
	catalog string
}

// newCacheFixture builds a run over the four [ids], with the cache rooted in a
// temporary directory.
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
			// A located toolchain, because a real run always has one by the
			// time the cache stage is reached and its release is in the key.
			// Without it [cache.Context] refuses to produce a key at all and
			// every test here would be measuring the fail-open path instead.
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

// runs turns the fixture ids into the scheduler's input.
func (f cacheFixture) runs() []execute.MutantRun {
	out := make([]execute.MutantRun, 0, len(ids))
	for _, id := range ids {
		out = append(out, execute.MutantRun{ID: id, Timeout: f.out.Timeout})
	}
	return out
}

// newState is a state with the maps the stage writes into.
func newState() *state {
	return &state{
		results: make(map[string]report.MutantResult),
		display: make(map[string]MutantResult),
		notRun:  make(map[string]report.NotRunReason),
	}
}

// measured is what the scheduler would have produced for the fixture: one of
// each outcome, so that the write-back has both the reusable and the
// non-reusable kinds to sort out.
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

// TestTheSecondRunReadsWhatTheFirstWrote is the whole feature in one test: a
// cold cache is all misses, the reusable outcomes are stored, and a second run
// over the same context adopts exactly those and re-measures the rest.
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
	// Two of the four outcomes are reusable; the inconclusive one and the
	// not-run one are deliberately not.
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

// TestACacheHitPublishesBothEvents pins the pairing contract: [CacheHit] for the
// accounting and [MutantFinished] for the outcome, with no [MutantStarted]
// between them because nothing started.
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

// TestAnExpectedMutantIsNeverCached keeps the promise `docs/configuration.md`
// makes: a mutant in the `[[mutation.expect]]` ledger is measured on every
// invocation. An expectation is evidence to check, and evidence copied from
// yesterday's answer has not been checked.
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
	// ids[1] survived, which is reusable — and it is on the ledger, so it was
	// not stored. Only the kill was.
	if got, want := firstState.cache.writes, 1; got != want {
		t.Errorf("the run stored %d outcomes, want %d", got, want)
	}
	// It was not looked up either, so it is neither a hit nor a miss.
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

// TestAutoStandsDownForACustomCommand: the run says why, does no lookups, and
// reports the cache off.
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
	// And nothing is written back either: the write half of the decision is off
	// with the read half.
	s.storeOutcomes(f.opts, measured(), st)
	if st.cache.writes != 0 {
		t.Errorf("a stood-down cache stored %d outcomes", st.cache.writes)
	}
}

// TestCacheOffDoesNothingAndSaysNothing. Turning the cache off is a decision the
// user made, not a condition to be warned about.
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

// TestOnReusesOutcomesForACustomCommand is the other half of the matrix: `on`
// is how a project promises its own command is reproducible.
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

// TestAnEditedWorkspaceIsAllMisses is the key doing its job from the engine's
// side: a workspace digest that has moved means a different context directory,
// so nothing the previous run proved is reachable.
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

// TestAnUnopenableCacheIsAWarningAndNothingElse is the fail-open contract: the
// cache is a way of answering the user's question faster, so a run that loses
// it still answers the question.
func TestAnUnopenableCacheIsAWarningAndNothingElse(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	// A digest the store will not name a directory with, which is the cheapest
	// way to make the open fail without depending on a file system permission.
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
	// A warning is one line, because the plain renderer prefixes it with a code
	// and the report stores it as one string.
	if strings.Contains(s.warnings[0].Message, "\n") {
		t.Errorf("the warning is not one line: %q", s.warnings[0].Message)
	}
	// And nothing carries two codes: the embedded cause has already had its own
	// stripped off.
	if strings.Count(s.warnings[0].Message, "GOM") > 1 {
		t.Errorf("the warning repeats a code inside its message: %q", s.warnings[0].Message)
	}
}

// TestTheCacheRootFollowsTheHistoryRoot is what keeps every test in this
// repository out of the developer's own cache directory: a caller that
// redirected one store and said nothing about the other plainly meant both.
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

// hasRun reports whether the scheduler was still asked to execute one mutant.
func hasRun(runs []execute.MutantRun, id string) bool {
	for _, run := range runs {
		if run.ID == id {
			return true
		}
	}
	return false
}

// mutationCacheable is the store's rule, restated here so that the event
// assertion above does not have to import it.
func mutationCacheable(o mutation.Outcome) bool {
	return o == mutation.OutcomeKilled || o == mutation.OutcomeSurvived || o == mutation.OutcomeTimedOut
}
