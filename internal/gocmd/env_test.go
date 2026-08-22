// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd_test

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
)

// TestAppendGoflagsMergesRatherThanOverwrites is the whole contract of the
// helper in one table.
//
// Each row is a shape a real child environment arrives in, and the rule is the
// same in all of them: whatever GOFLAGS already said still applies afterwards.
// A helper that set the variable instead would pass the first row and silently
// throw away a `-mod=readonly` or a `-tags=...` in every other one, which is
// the failure this exists to prevent rather than a detail of it.
func TestAppendGoflagsMergesRatherThanOverwrites(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "no GOFLAGS at all: one is added",
			env:  []string{"PATH=/usr/bin", "HOME=/home/dev"},
			want: []string{"PATH=/usr/bin", "HOME=/home/dev", "GOFLAGS=-vet=off"},
		},
		{
			name: "an existing GOFLAGS keeps everything it held",
			env:  []string{"PATH=/usr/bin", "GOFLAGS=-mod=readonly -tags=integration", "HOME=/home/dev"},
			want: []string{"PATH=/usr/bin", "GOFLAGS=-mod=readonly -tags=integration -vet=off", "HOME=/home/dev"},
		},
		{
			name: "the flag is already there: nothing is added twice",
			env:  []string{"GOFLAGS=-vet=off -mod=readonly"},
			want: []string{"GOFLAGS=-vet=off -mod=readonly"},
		},
		{
			// os/exec resolves a duplicate by keeping the last, so the last is
			// what the child would have seen and the last is what is merged
			// into. The entries collapse to one in the first one's position,
			// which changes no meaning and leaves nothing for a reader to have
			// to know.
			name: "duplicate GOFLAGS entries collapse onto the last one's value",
			env:  []string{"GOFLAGS=-mod=mod", "PATH=/usr/bin", "GOFLAGS=-mod=readonly"},
			want: []string{"GOFLAGS=-mod=readonly -vet=off", "PATH=/usr/bin"},
		},
		{
			name: "duplicates whose effective value already has the flag collapse unchanged",
			env:  []string{"GOFLAGS=-mod=mod", "GOFLAGS=-vet=off"},
			want: []string{"GOFLAGS=-vet=off"},
		},
		{
			// `GOFLAGS=` is a setting and not an absence — it overrides a value
			// from a `go env -w` file, which is why internal/cache hashes it
			// differently from an unset one — but it has nothing to keep, so the
			// merge must not leave a leading space behind.
			name: "GOFLAGS set to nothing gains the flag without a leading space",
			env:  []string{"GOFLAGS=", "PATH=/usr/bin"},
			want: []string{"GOFLAGS=-vet=off", "PATH=/usr/bin"},
		},
		{
			// Field comparison, not substring: `-vet=offline` is not `-vet=off`.
			name: "a flag that merely contains the wanted one is not it",
			env:  []string{"GOFLAGS=-vet=offline"},
			want: []string{"GOFLAGS=-vet=offline -vet=off"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			original := slices.Clone(c.env)
			got := gocmd.AppendGoflags(c.env, gocmd.VetOff)
			if !slices.Equal(got, c.want) {
				t.Errorf("AppendGoflags(%q, %q) =\n\t%q\nwant\n\t%q", original, gocmd.VetOff, got, c.want)
			}
			if !slices.Equal(c.env, original) {
				t.Errorf("the input environment was modified in place: %q, want %q", c.env, original)
			}
		})
	}
}

// TestAppendGoflagsDoesNotAliasItsInput pins the copy rather than the values.
//
// The environment handed to this helper is the run's own composed one, shared
// by the pristine baseline, compile validation and the instrumented baseline —
// so a returned slice that aliased its input would put `-vet=off` on the
// *user's* pristine tree the moment anything wrote through it. Equal contents
// would not catch that; a write does.
func TestAppendGoflagsDoesNotAliasItsInput(t *testing.T) {
	t.Parallel()

	env := []string{"PATH=/usr/bin", "GOFLAGS=-mod=readonly"}
	got := gocmd.AppendGoflags(env, gocmd.VetOff)
	got[0] = "PATH=/tampered"
	if env[0] != "PATH=/usr/bin" {
		t.Errorf("writing to the result changed the input: env[0] = %q", env[0])
	}
}

// TestAppendGoflagsAddsNothingForAnEmptyFlag covers the degenerate call.
//
// It is not hypothetical bookkeeping: the alternative is an environment
// carrying `GOFLAGS=` where none was set, and an empty GOFLAGS is not the same
// thing as an unset one to the go command.
func TestAppendGoflagsAddsNothingForAnEmptyFlag(t *testing.T) {
	t.Parallel()

	env := []string{"PATH=/usr/bin"}
	for _, flag := range []string{"", "   "} {
		if got := gocmd.AppendGoflags(env, flag); !slices.Equal(got, env) {
			t.Errorf("AppendGoflags(env, %q) = %q, want the environment unchanged", flag, got)
		}
	}
}

// TestAppendGoflagsMatchesTheVariableTheWayTheSystemDoes is the Windows half.
//
// A variable there answers to any spelling of its name, so an environment
// holding `Goflags=` and one holding `GOFLAGS=` are one variable to the child.
// Treating them as two would append a second entry that wins by being last and
// silently drops whatever the first one said.
func TestAppendGoflagsMatchesTheVariableTheWayTheSystemDoes(t *testing.T) {
	t.Parallel()

	env := []string{"Goflags=-mod=readonly"}
	got := gocmd.AppendGoflags(env, gocmd.VetOff)
	if runtime.GOOS != "windows" {
		want := []string{"Goflags=-mod=readonly", "GOFLAGS=-vet=off"}
		if !slices.Equal(got, want) {
			t.Errorf("AppendGoflags(%q, %q) = %q, want %q", env, gocmd.VetOff, got, want)
		}
		return
	}
	want := []string{"GOFLAGS=-mod=readonly -vet=off"}
	if !slices.Equal(got, want) {
		t.Errorf("AppendGoflags(%q, %q) = %q, want %q", env, gocmd.VetOff, got, want)
	}
}

// TestVetOffIsTheFlagTheGoCommandDefines guards the spelling itself.
//
// Both call sites take it from here, so a typo would disable nothing and would
// be invisible until an instrumented tree tripped vet again — which is the bug
// this constant exists to keep fixed.
func TestVetOffIsTheFlagTheGoCommandDefines(t *testing.T) {
	t.Parallel()

	if gocmd.VetOff != "-vet=off" {
		t.Errorf("VetOff = %q, want %q", gocmd.VetOff, "-vet=off")
	}
	if !strings.HasPrefix(gocmd.VetOff, "-") {
		t.Errorf("VetOff = %q, want a go command flag", gocmd.VetOff)
	}
	if gocmd.GoflagsKey != "GOFLAGS" {
		t.Errorf("GoflagsKey = %q, want %q", gocmd.GoflagsKey, "GOFLAGS")
	}
}
