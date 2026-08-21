// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"strings"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/report"
)

// The outcome cache stage, which is the last thing to narrow a run and the only
// one that can answer a question instead of merely skipping it.
//
// It sits between coverage narrowing and the scheduler, and the order is load
// bearing in both directions. Coverage comes first because an uncovered mutant
// is settled without being executed and must never be cached: the coverage pass
// fails open, so the same key can describe a run that profiled successfully and
// one that did not, and a cached "survived (uncovered)" adopted by the second
// would be a survivor nothing ever ran. The scheduler comes after because a hit
// is a mutant that must not be started at all — that is the whole saving.

// a cacheState is what the cache did for one run, as the report writes it down.
//
// The zero value is a run with the cache off, which is what every path that
// never reached the stage leaves behind: an early failure, a mode of off, an
// `auto` that stood down, a directory that could not be opened.
type cacheState struct {
	mode   report.CacheMode
	hits   int
	misses int
	writes int
}

// Mode renders the mode for the report, defaulting the zero value to off.
func (c cacheState) Mode() report.CacheMode {
	if c.mode == "" {
		return report.CacheOff
	}
	return c.mode
}

// cachePhase partitions the mutants a run was about to execute into the ones it
// can answer from the cache and the ones it has to measure, and returns the
// second.
//
// Every failure here is a warning and a run that measures everything. That is
// the same judgement internal/coverage makes and the opposite of the one
// `--changed` gets: the cache is a way of answering the user's question faster,
// so a run that loses it still answers the question. Failing a run because a
// directory in the operating system's cache could not be written would be
// letting an optimisation break the thing it was optimising.
func (s *session) cachePhase(
	opts Options,
	catalogDigest string,
	out *RunOutcome,
	runs []execute.MutantRun,
	st *state,
) []execute.MutantRun {
	decision := cache.Resolve(opts.Config.Cache.Mode, out.TestCommand)
	if decision.Reason != "" {
		s.warnCode(string(cache.CodeUnavailable), decision.Reason)
	}
	if !decision.Enabled() {
		return runs
	}

	store, err := s.openCache(opts, catalogDigest, out)
	if err != nil {
		s.cacheUnavailable(err)
		return runs
	}
	s.cache = store
	st.cache.mode = report.CacheOn

	expected := expectedIDs(opts.Config.Mutation.Expect)
	misses := make([]execute.MutantRun, 0, len(runs))
	for _, run := range runs {
		if expected[run.ID] {
			// Never looked up and never stored: `docs/configuration.md` promises
			// that a mutant in the `[[mutation.expect]]` ledger is measured on
			// every invocation, and it is a promise worth keeping. An expectation
			// is evidence to check, and evidence copied from yesterday's answer
			// has not been checked — the run that matters is the one after
			// somebody wrote the test that finally kills it.
			//
			// It is counted as neither a hit nor a miss, because the cache was
			// not asked. See [report.Cache].
			misses = append(misses, run)
			continue
		}
		entry, found, lookupErr := store.Lookup(run.ID)
		if lookupErr != nil {
			s.corrupt(lookupErr)
		}
		if !found {
			misses = append(misses, run)
			st.cache.misses++
			continue
		}
		s.adopt(run.ID, entry, st)
	}
	return misses
}

// openCache resolves the cache directory for this run and claims it.
func (s *session) openCache(opts Options, catalogDigest string, out *RunOutcome) (*cache.Cache, error) {
	digest, err := cache.ToolDigest()
	if err != nil {
		return nil, err
	}
	return cache.Open(cache.Options{
		Root:      cacheRoot(opts),
		Directory: opts.Config.Cache.Directory,
		// The bound this run will apply, which every lookup is judged against.
		// It is deliberately not in the key; see [cache.Context].
		Timeout: out.Timeout,
		Context: cache.Context{
			ToolVersion: or(opts.ToolVersion, unknownValue),
			ToolDigest:  digest,
			// The compiler and standard library the outcomes were measured
			// against. Nothing else in the key carries it: TestCommand is the
			// argv as the user wrote it, so the default command hashes the word
			// `go` and not the toolchain [resolveProgram] substitutes for it.
			ToolchainVersion: out.Toolchain.Version.Release,
			WorkspaceDigest:  out.WorkspaceDigest,
			CatalogDigest:    catalogDigest,
			TestCommand:      out.TestCommand,
			// `test.timeout` as configured, not the derived number: a derived
			// bound is a wall-clock measurement and moves on every run.
			ConfiguredTimeout: opts.Config.Test.Timeout,
			Env:               cache.CurrentEnv(),
		},
	})
}

// cacheRoot is the directory the outcome cache lives under.
//
// A caller that redirected the run history and said nothing about the cache
// gets the cache redirected too, which is what [Options.CacheRoot] documents
// and what every test in this repository relies on: the two stores share a
// workspace directory in production, so a test that sends one to a temporary
// directory and leaves the other pointing at the developer's own cache would be
// writing into it by accident.
func cacheRoot(opts Options) string {
	if opts.CacheRoot != "" {
		return opts.CacheRoot
	}
	return opts.HistoryRoot
}

// adopt files a cached outcome as this run's answer for one mutant.
//
// Two events are published for it and both are needed. [CacheHit] is the
// accounting: it is what a renderer counts to say how much of the run did not
// happen. [MutantFinished] is the outcome, and it is published for a mutant
// that was never started for the same reason [session.recordUncovered]
// publishes one — a renderer's counts and the report's have to agree, and a
// mutant that settled without a MutantFinished would be missing from one of
// them. No [MutantStarted] precedes either: nothing started.
func (s *session) adopt(id string, entry cache.Entry, st *state) {
	st.results[id] = report.MutantResult{
		ID:                   id,
		Outcome:              entry.Outcome,
		Duration:             entry.Duration(),
		KilledBy:             entry.KilledBy,
		Attempts:             entry.Attempts,
		OutputTail:           entry.OutputTail,
		CoveringTestPackages: st.coverage.covering[id],
		Cached:               true,
	}
	st.cache.hits++

	shown := st.display[id]
	shown.Outcome = entry.Outcome
	shown.Duration = entry.Duration()
	shown.Cached = true
	s.emit(CacheHit{ID: id, DisplayID: shown.DisplayID, Outcome: entry.Outcome})
	s.emit(MutantFinished{Result: shown})
}

// storeOutcomes writes back what this run measured.
//
// It is called with whatever [execute.Schedule] produced, interruption
// included, and the filter is [cache.Cacheable] rather than the run's status: a
// mutant that settled before the signal arrived settled, and its answer is as
// good as any other. Everything a cancelled run leaves unsettled carries
// not-run, which is not a reusable outcome, so nothing has to know that the run
// ended early.
//
// A write that fails is warned about once and then given up on. The commonest
// cause is a full or read-only cache directory, which will fail for every
// remaining mutant too, and a warning per mutant would bury the run's actual
// findings under hundreds of copies of one sentence.
func (s *session) storeOutcomes(opts Options, results []execute.MutantResult, st *state) {
	if s.cache == nil {
		return
	}
	expected := expectedIDs(opts.Config.Mutation.Expect)
	for _, result := range results {
		if expected[result.ID] || !cache.Cacheable(result.Final) {
			continue
		}
		err := s.cache.Put(result.ID, cache.Entry{
			Outcome:    result.Final,
			DurationMS: result.Duration.Milliseconds(),
			KilledBy:   result.KilledBy,
			Attempts:   len(result.Attempts),
			OutputTail: result.OutputTail,
		})
		if err != nil {
			if !s.cacheWriteWarned {
				s.cacheWriteWarned = true
				s.warnCode(string(cache.CodeOf(err)), storeFailed(err))
			}
			continue
		}
		st.cache.writes++
	}
}

// storeFailed is what [cache.CodeEntryNotWritten] says: that an outcome could
// not be kept, and that nothing else about the run changes because of it.
func storeFailed(err error) string {
	return firstLine(err.Error()) + "; the run is unaffected and the mutant will simply be measured again next time"
}

// cacheUnavailable publishes the fail-open warning: what went wrong, and what
// the run is doing about it.
//
// The second half is not padding, for the reason [session.unavailable] gives
// about coverage: a warning that said only "the cache failed" leaves a reader
// wondering whether the results can be trusted, and the answer is that they can
// — the run is about to do strictly more work than it would have.
func (s *session) cacheUnavailable(err error) {
	code := string(cache.CodeOf(err))
	if code == "" {
		code = string(cache.CodeUnavailable)
	}
	s.warnCode(code, "the outcome cache is off because "+
		strings.TrimSuffix(firstLine(uncoded(err.Error())), ".")+
		"; every mutant will be measured, which is slower and never wrong")
}

// corrupt publishes the once-per-run warning about entries that are on disk and
// are not entries.
//
// Once, and not once per entry: a cache directory that has been truncated by a
// full disk or half-restored from a CI archive produces one of these for every
// mutant in the run, and hundreds of copies of the same sentence would bury the
// survivors the user is actually looking for. The first one names a file, which
// is enough to go and look.
func (s *session) corrupt(err error) {
	if s.cacheCorruptWarned {
		return
	}
	s.cacheCorruptWarned = true
	s.warnCode(string(cache.CodeCorruptEntry), firstLine(uncoded(err.Error()))+
		"; any other unreadable entry in this cache will be treated the same way and reported only here")
}

// uncoded strips a leading "GOM####: " from a message that is about to be
// embedded in another one, so that a warning does not print two codes.
func uncoded(message string) string {
	const width = len("GOM0000: ")
	if len(message) > width && strings.HasPrefix(message, "GOM") && message[width-2] == ':' {
		return message[width:]
	}
	return message
}

// cacheMode renders the document's cache mode as the event stream's, which is
// the same enumeration under this package's own name. It is the mirror of
// [reportCoverageMode], and exists for the same reason: a renderer should not
// have to import internal/report to read a summary block.
func cacheMode(mode report.CacheMode) CacheMode {
	if mode == report.CacheOn {
		return CacheOn
	}
	return CacheOff
}

// expectedIDs is the set of mutants the `[[mutation.expect]]` ledger names.
func expectedIDs(ledger []config.Expectation) map[string]bool {
	expected := make(map[string]bool, len(ledger))
	for _, row := range ledger {
		expected[row.ID] = true
	}
	return expected
}
