// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"strings"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

type cacheState struct {
	mode   report.CacheMode
	hits   int
	misses int
	writes int
}

func (c cacheState) Mode() report.CacheMode {
	if c.mode == "" {
		return report.CacheOff
	}
	return c.mode
}

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
		s.trace.Cache(trace.CacheRecord{
			Op:     trace.CacheOpOpen,
			Result: trace.CacheResultUnavailable,
			Error:  err.Error(),
		})
		s.cacheUnavailable(err)
		return runs
	}
	s.cache = store
	st.cache.mode = report.CacheOn
	s.trace.Cache(trace.CacheRecord{
		Op:         trace.CacheOpOpen,
		Result:     trace.CacheResultOpened,
		Directory:  store.Dir(),
		ContextKey: store.ContextKey(),
	})

	expected := expectedIDs(opts.Config.Mutation.Expect)
	misses := make([]execute.MutantRun, 0, len(runs))
	for _, run := range runs {
		if expected[run.ID] {
			s.trace.Cache(trace.CacheRecord{
				Op:       trace.CacheOpLookup,
				MutantID: run.ID,
				Result:   trace.CacheResultExpected,
			})
			misses = append(misses, run)
			continue
		}
		entry, found, lookupErr := store.Lookup(run.ID)
		record := trace.CacheRecord{Op: trace.CacheOpLookup, MutantID: run.ID}
		switch {
		case lookupErr != nil:
			record.Result = trace.CacheResultCorrupt
			record.Error = lookupErr.Error()
		case found:
			record.Result = trace.CacheResultHit
			record.Outcome = string(entry.Outcome)
		default:
			record.Result = trace.CacheResultMiss
		}
		s.trace.Cache(record)
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

func (s *session) openCache(opts Options, catalogDigest string, out *RunOutcome) (*cache.Cache, error) {
	digest, err := cache.ToolDigest()
	if err != nil {
		return nil, err
	}
	return cache.Open(cache.Options{
		Root:        cacheRoot(opts),
		Directory:   opts.Config.Cache.Directory,
		Timeout:     out.Timeout,
		MemoryLimit: enforcedMemory(out.Memory),
		Context: cache.Context{
			ToolVersion:       or(opts.ToolVersion, unknownValue),
			ToolDigest:        digest,
			ToolchainVersion:  out.Toolchain.Version.Release,
			WorkspaceDigest:   out.WorkspaceDigest,
			CatalogDigest:     catalogDigest,
			TestCommand:       out.TestCommand,
			ConfiguredTimeout: opts.Config.Test.Timeout,
			Env:               cache.CurrentEnv(),
		},
	})
}

func memoryExceeded(result execute.MutantResult) bool {
	for _, attempt := range result.Attempts {
		if attempt.MemoryExceeded {
			return true
		}
	}
	return false
}

func diverged(result execute.MutantResult) bool {
	for _, attempt := range result.Attempts {
		if attempt.Diverged {
			return true
		}
	}
	return false
}

func peakMemory(result execute.MutantResult) int64 {
	var peak int64
	for _, attempt := range result.Attempts {
		peak = max(peak, attempt.PeakMemory)
	}
	return peak
}

func enforcedMemory(limit int64) int64 {
	if !runner.MemoryBoundSupported() {
		return 0
	}
	return limit
}

func cacheRoot(opts Options) string {
	if opts.CacheRoot != "" {
		return opts.CacheRoot
	}
	return opts.HistoryRoot
}

func (s *session) adopt(id string, entry cache.Entry, st *state) {
	st.results[id] = report.MutantResult{
		ID:                   id,
		Outcome:              entry.Outcome,
		Duration:             entry.Duration(),
		KilledBy:             entry.KilledBy,
		Attempts:             entry.Attempts,
		OutputTail:           entry.OutputTail,
		CoveringTestPackages: st.coverage.covering[id],
		CoveringTests:        st.coverage.coveringTests[id],
		Cached:               true,
		MemoryExceeded:       entry.MemoryExceeded,
		PeakMemory:           entry.PeakMemory,
		Diverged:             entry.Diverged,
	}
	st.cache.hits++

	shown := st.display[id]
	shown.Outcome = entry.Outcome
	shown.Duration = entry.Duration()
	shown.Cached = true
	shown.KilledBy = entry.KilledBy
	shown.Attempts = entry.Attempts
	shown.CoveringTestPackages = st.coverage.covering[id]
	shown.CoveringTests = st.coverage.coveringTests[id]
	shown.MemoryExceeded = entry.MemoryExceeded
	shown.PeakMemory = entry.PeakMemory
	shown.MemoryLimit = entry.MemoryBytes
	shown.Diverged = entry.Diverged
	s.emit(CacheHit{ID: id, DisplayID: shown.DisplayID, Outcome: entry.Outcome})
	s.emit(MutantFinished{Result: shown.clone()})
}

func (s *session) storeOutcomes(opts Options, results []execute.MutantResult, st *state) {
	if s.cache == nil {
		return
	}
	expected := expectedIDs(opts.Config.Mutation.Expect)
	for _, result := range results {
		record := trace.CacheRecord{
			Op:       trace.CacheOpStore,
			MutantID: result.ID,
			Outcome:  string(result.Final),
		}
		switch {
		case expected[result.ID]:
			record.Result = trace.CacheResultExpected
		case !cache.Cacheable(result.Final):
			record.Result = trace.CacheResultNotCacheable
		}
		if record.Result != "" {
			s.trace.Cache(record)
			continue
		}
		err := s.cache.Put(result.ID, cache.Entry{
			Outcome:        result.Final,
			DurationMS:     result.Duration.Milliseconds(),
			KilledBy:       result.KilledBy,
			Attempts:       len(result.Attempts),
			OutputTail:     result.OutputTail,
			MemoryExceeded: memoryExceeded(result),
			PeakMemory:     peakMemory(result),
			Diverged:       diverged(result),
		})
		if err != nil {
			record.Result = trace.CacheResultFailed
			record.Error = err.Error()
			s.trace.Cache(record)
			if !s.cacheWriteWarned {
				s.cacheWriteWarned = true
				s.warnCode(string(cache.CodeOf(err)), storeFailed(err))
			}
			continue
		}
		record.Result = trace.CacheResultWritten
		s.trace.Cache(record)
		st.cache.writes++
	}
}

func storeFailed(err error) string {
	return firstLine(err.Error()) + "; the run is unaffected and the mutant will simply be measured again next time"
}

func (s *session) cacheUnavailable(err error) {
	code := string(cache.CodeOf(err))
	if code == "" {
		code = string(cache.CodeUnavailable)
	}
	s.warnCode(code, "the outcome cache is off because "+
		strings.TrimSuffix(firstLine(uncoded(err.Error())), ".")+
		"; every mutant will be measured, which is slower and never wrong")
}

func (s *session) corrupt(err error) {
	if s.cacheCorruptWarned {
		return
	}
	s.cacheCorruptWarned = true
	s.warnCode(string(cache.CodeCorruptEntry), firstLine(uncoded(err.Error()))+
		"; any other unreadable entry in this cache will be treated the same way and reported only here")
}

func uncoded(message string) string {
	const width = len("GOM0000: ")
	if len(message) > width && strings.HasPrefix(message, "GOM") && message[width-2] == ':' {
		return message[width:]
	}
	return message
}

func cacheMode(mode report.CacheMode) CacheMode {
	if mode == report.CacheOn {
		return CacheOn
	}
	return CacheOff
}

func expectedIDs(ledger []config.Expectation) map[string]bool {
	expected := make(map[string]bool, len(ledger))
	for _, row := range ledger {
		expected[row.ID] = true
	}
	return expected
}
