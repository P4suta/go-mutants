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

// TestAFuzzRunGetsNoDerivedMemoryBound is the shape rule the derived bound
// rests on.
//
// A derived bound is `max(floor, what the unmutated tests cost × 4)`, and every
// word of that is about a run of the *baseline's shape*: one process, running
// the suite once. A fuzz run is not that shape. `go test -fuzz` starts a
// coordinator plus one worker process per core, and each of them maps the same
// 100 MiB shared-memory region the fuzzing engine communicates through — so on
// a four-core machine the run's floor is half a gibibyte of mappings before a
// single test has executed, and on an eight-core one it is more than the whole
// derived bound. Applying a number derived from something else would kill a
// legitimate fuzz run for being what it is.
//
// So a request that starts fuzzing inherits no bound. What it does inherit is
// the timeout, which is about wall-clock time and is the same question whatever
// shape the run has, and any limit the caller names explicitly — a caller who
// knows what their fuzzing costs may still cap it.
func TestAFuzzRunGetsNoDerivedMemoryBound(t *testing.T) {
	t.Parallel()

	const session, explicit = 1 << 30, 512 << 20
	for _, c := range []struct {
		name    string
		args    []string
		request int64
		want    int64
	}{
		{"an ordinary target inherits the session's", []string{"-test.run=^TestClamp$"}, 0, session},
		{"a fuzz target inherits nothing", []string{"-test.fuzz=^FuzzRoundTrip$"}, 0, 0},
		{"the bare fuzz flag counts too", []string{"-test.fuzz", "^FuzzRoundTrip$"}, 0, 0},
		{"the long spelling counts too", []string{"--test.fuzz=^FuzzRoundTrip$"}, 0, 0},
		{"a fuzz target keeps a limit the caller named", []string{"-test.fuzz=^F$"}, explicit, explicit},
		{"and so does an ordinary one", []string{"-test.run=^T$"}, explicit, explicit},
		{"no arguments at all is an ordinary run", nil, 0, session},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveMemoryLimit(c.request, session, c.args); got != c.want {
				t.Errorf("effectiveMemoryLimit(%d, %d, %q) = %d, want %d",
					c.request, int64(session), c.args, got, c.want)
			}
		})
	}
}
