// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/runner"
)

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

func TestOnlyAnExplicitBoundNobodyWillHoldIsWorthAWarning(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		limit    int64
		source   MemorySource
		enforced bool
		want     string
	}{
		{"an explicit bound nothing will hold", 2 << 30, MemorySourceExplicit, false, "recorded but not enforced"},
		{"a derived bound nothing will hold", 0, MemorySourceUnavailable, false, ""},
		{"a run that measured no peak at all", 0, MemorySourceUnavailable, false, ""},
		{"an explicit bound that is being enforced", 2 << 30, MemorySourceExplicit, true, ""},
		{"a derived bound that is being enforced", 2 << 30, MemorySourceDerived, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, warn := unenforcedMemoryReason(c.limit, c.source, c.enforced)
			if c.want == "" {
				if warn {
					t.Errorf("a warning was published for %s: %q", c.name, got)
				}
				return
			}
			if !warn {
				t.Fatalf("no warning was published for %s", c.name)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("the warning reads %q, want it to say %q", got, c.want)
			}
			if !strings.HasPrefix(got, "the memory bound in test.memory") {
				t.Errorf("the warning opens with %q, which does not say what happened", got)
			}
		})
	}
}

func TestMemoryDerivedIsASealedEvent(t *testing.T) {
	t.Parallel()

	var derived Event = MemoryDerived{
		Limit:  1 << 30,
		Source: MemorySourceDerived,
		Peak:   200 << 20,
	}

	if got := eventNames([]Event{derived}); !slices.Equal(got, []string{"MemoryDerived"}) {
		t.Errorf("events = %v, want the new one", got)
	}
}

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
