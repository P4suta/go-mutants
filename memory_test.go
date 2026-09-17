// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/runner"
)

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
				want = 0
			}
			if got := sessionMemoryBound(c.peak); got != want {
				t.Errorf("sessionMemoryBound(%d) = %d, want %d", c.peak, got, want)
			}
		})
	}
}

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
