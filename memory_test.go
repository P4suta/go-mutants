// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/runner"
)

// TestASessionsMemoryBoundIsDerivedFromWhatVerificationCost pins the number a
// request that names no bound gets.
//
// A session has no baseline phase, so it derives from the one measurement it
// does make: the verification run of the unmutated tests. Two of the rows are
// the ones a consumer would otherwise be surprised by. A session opened with
// SkipVerify still gets the *floor* rather than nothing — turning verification
// off is asking go-mutants not to measure, and it must not silently also turn
// the bound off — and a platform that cannot enforce one gets zero, because a
// number nothing applies would show up in the consumer's own report as a budget
// that was never held.
func TestASessionsMemoryBoundIsDerivedFromWhatVerificationCost(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20
	for _, c := range []struct {
		name string
		peak int64
		want int64
	}{
		{"nothing was measured", 0, defaultMutantMemory},
		{"a small suite gets the floor", 64 * mib, defaultMutantMemory},
		{"the floor is reached at exactly a quarter of it", defaultMutantMemory / mutantMemoryFactor, defaultMutantMemory},
		{"a large suite gets four times its peak", 2048 * mib, mutantMemoryFactor * 2048 * mib},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := c.want
			if !runner.MemoryBoundSupported() {
				// A bound nothing can hold anybody to is not recorded on the
				// session, because every request that took its default would
				// then carry a budget the runner ignores.
				want = 0
			}
			if got := sessionMemoryBound(c.peak); got != want {
				t.Errorf("sessionMemoryBound(%d) = %d, want %d", c.peak, got, want)
			}
		})
	}
}
