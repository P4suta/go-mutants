// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/runner"
)

// TestMemoryBoundIsDerivedFromTheBaselinePeak is the memory half of the pair
// [TestDeriveTimeout] pins for the clock, and it is written as one table for
// the same reason: the rule is four sentences, and four sentences spread over
// four tests are four places for one of them to be forgotten.
//
// The fourth case is the one this feature exists to make impossible. A peak of
// zero means the platform could not measure one, and the answer there is no
// bound at all — never the floor. A floor applied to a measurement nobody made
// is a number go-mutants invented, and it would kill a legitimate suite on the
// one platform where nothing could have warned it.
func TestMemoryBoundIsDerivedFromTheBaselinePeak(t *testing.T) {
	t.Parallel()

	const (
		gib = 1 << 30
		mib = 1 << 20
	)
	cases := []struct {
		name       string
		explicit   int64
		peak       int64
		want       int64
		wantSource MemorySource
	}{
		{
			name:       "a small suite gets the floor",
			peak:       64 * mib,
			want:       MinDerivedMemory,
			wantSource: MemorySourceDerived,
		},
		{
			name:       "the floor is reached at exactly a quarter of it",
			peak:       MinDerivedMemory / MemoryFactor,
			want:       MinDerivedMemory,
			wantSource: MemorySourceDerived,
		},
		{
			name:       "a large suite gets four times its peak",
			peak:       2 * gib,
			want:       8 * gib,
			wantSource: MemorySourceDerived,
		},
		{
			name:       "an explicit bound is taken as written",
			explicit:   2 * gib,
			peak:       64 * mib,
			want:       2 * gib,
			wantSource: MemorySourceExplicit,
		},
		{
			name:       "an explicit bound is taken as written even under a large peak",
			explicit:   512 * mib,
			peak:       4 * gib,
			want:       512 * mib,
			wantSource: MemorySourceExplicit,
		},
		{
			name:       "an unmeasured peak is no bound at all",
			peak:       0,
			want:       0,
			wantSource: MemorySourceUnavailable,
		},
		{
			name:       "an explicit bound still applies where nothing was measured",
			explicit:   2 * gib,
			peak:       0,
			want:       2 * gib,
			wantSource: MemorySourceExplicit,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, source := deriveMemory(c.explicit, c.peak)
			if got != c.want || source != c.wantSource {
				t.Errorf("deriveMemory(%d, %d) = %d (%s), want %d (%s)",
					c.explicit, c.peak, got, source, c.want, c.wantSource)
			}
		})
	}
}

// TestAPlatformThatCannotEnforceABoundIsUnbounded pins the other way a bound
// goes missing, which is not the same as an unmeasured peak.
//
// macOS reports what a process cost once it is gone and cannot watch one while
// it runs, so a bound derived there would be a promise nothing keeps. The
// derivation asks the runner rather than assuming, and a run on such a platform
// carries no bound and says so once.
func TestAPlatformThatCannotEnforceABoundIsUnbounded(t *testing.T) {
	t.Parallel()

	const peak = 4 << 30
	limit, source := enforceableMemory(deriveMemory(0, peak))
	if runner.MemoryBoundSupported() {
		if limit == 0 || source != MemorySourceDerived {
			t.Errorf("enforceableMemory = %d (%s) on a platform that can enforce one, want the derived bound",
				limit, source)
		}
		return
	}
	if limit != 0 || source != MemorySourceUnavailable {
		t.Errorf("enforceableMemory = %d (%s) on a platform that cannot enforce one, want 0 (%s)",
			limit, source, MemorySourceUnavailable)
	}
}

// TestAnExplicitBoundIsRecordedEvenWhereItCannotBeEnforced pins the difference
// between a bound that is absent and a bound that is merely not held.
//
// The two used to be the same answer, and it made the run lie to the user in
// the one place it was trying to be honest: the warning on macOS invites them
// to set `test.memory` "if you want the bound recorded", and then the bound was
// dropped and nothing recorded it. A number the user wrote down belongs in the
// report whether or not this machine can hold anybody to it — that is what a
// record of a run is for — and what must not survive is a *derived* bound,
// which is go-mutants' own arithmetic and would read as a promise the run never
// made.
func TestAnExplicitBoundIsRecordedEvenWhereItCannotBeEnforced(t *testing.T) {
	t.Parallel()

	const (
		explicit = 2 << 30
		peak     = 4 << 30
	)

	limit, source := enforceableMemory(deriveMemory(explicit, peak))
	if limit != explicit || source != MemorySourceExplicit {
		t.Errorf("an explicit bound came back as %d (%s), want %d (%s) on every platform",
			limit, source, int64(explicit), MemorySourceExplicit)
	}

	derived, derivedSource := enforceableMemory(deriveMemory(0, peak))
	if runner.MemoryBoundSupported() {
		if derived != MemoryFactor*int64(peak) || derivedSource != MemorySourceDerived {
			t.Errorf("a derived bound came back as %d (%s), want %d (%s)",
				derived, derivedSource, MemoryFactor*int64(peak), MemorySourceDerived)
		}
		return
	}
	if derived != 0 || derivedSource != MemorySourceUnavailable {
		t.Errorf("a derived bound came back as %d (%s) where nothing enforces one, want 0 (%s)",
			derived, derivedSource, MemorySourceUnavailable)
	}
}

// TestTheUnenforcedWarningSaysWhichOfTheThreeThingsHappened pins that the one
// warning covers three different situations and says which.
func TestTheUnenforcedWarningSaysWhichOfTheThreeThingsHappened(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		limit  int64
		source MemorySource
		peak   int64
		want   string
	}{
		{"an explicit bound nothing will hold", 2 << 30, MemorySourceExplicit, 1 << 20, "recorded but not enforced"},
		{"a platform that cannot watch a tree", 0, MemorySourceUnavailable, 1 << 20, "cannot watch one while it runs"},
		{"nothing measured at all", 0, MemorySourceUnavailable, 0, "nothing measured what the baseline runs cost"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := unenforcedMemoryReason(c.limit, c.source, c.peak)
			if !strings.Contains(got, c.want) {
				t.Errorf("the warning reads %q, want it to say %q", got, c.want)
			}
			if !strings.HasPrefix(got, "no mutant is bounded in memory") &&
				!strings.HasPrefix(got, "the memory bound in test.memory") {
				t.Errorf("the warning opens with %q, which does not say what happened", got)
			}
		})
	}
}

// TestMemoryDerivedIsASealedEvent is a compile-time assertion: it travels on the
// engine's stream, so it has to be part of the sealed interface a renderer
// switches over.
func TestMemoryDerivedIsASealedEvent(t *testing.T) {
	t.Parallel()

	// The declaration is the assertion: the interface's marker method is
	// unexported, so a type that forgot it would not compile here.
	var derived Event = MemoryDerived{
		Limit:  1 << 30,
		Source: MemorySourceDerived,
		Peak:   200 << 20,
	}

	if got := eventNames([]Event{derived}); !slices.Equal(got, []string{"MemoryDerived"}) {
		t.Errorf("events = %v, want the new one", got)
	}
}

// TestTheMemoryBoundTakesNoPartInTheCacheKey is the invariant that would fail
// silently.
//
// A bound is a budget on evidence and not evidence: the same mutant, measured
// against the same tests in the same tree, is the same mutant whether the run
// that measured it was allowed two gigabytes or eight. A bound that reached
// [cache.Context] would give every run whose machine measured a slightly
// different baseline peak a cache of its own — correct results, no reuse, and
// nothing anywhere saying why.
func TestTheMemoryBoundTakesNoPartInTheCacheKey(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())

	cold := &session{}
	coldState := newState()
	cold.cachePhase(f.opts, f.catalog, f.out, f.runs(), coldState)
	cold.storeOutcomes(f.opts, measured(), coldState)
	if coldState.cache.writes == 0 {
		t.Fatal("the first run stored nothing, so the second has nothing to find")
	}

	// The second run is the first with a bound: configured, derived, and
	// applied. Every one of those is a place a memory number could leak into
	// the key.
	bounded := f
	bounded.opts.Config.Test.Memory = 2 << 30
	bounded.out.Memory = 8 << 30
	bounded.out.MemorySource = MemorySourceExplicit
	bounded.out.PeakBaseline = 512 << 20

	warm := &session{}
	warmState := newState()
	warm.cachePhase(bounded.opts, bounded.catalog, bounded.out, bounded.runs(), warmState)
	if got, want := warmState.cache.hits, coldState.cache.writes; got != want {
		t.Errorf("the bounded run had %d cache hits, want the %d the unbounded run stored: the key moved",
			got, want)
	}
}
