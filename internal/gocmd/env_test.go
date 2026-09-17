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
			name: "GOFLAGS set to nothing gains the flag without a leading space",
			env:  []string{"GOFLAGS=", "PATH=/usr/bin"},
			want: []string{"GOFLAGS=-vet=off", "PATH=/usr/bin"},
		},
		{
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

func TestAppendGoflagsDoesNotAliasItsInput(t *testing.T) {
	t.Parallel()

	env := []string{"PATH=/usr/bin", "GOFLAGS=-mod=readonly"}
	got := gocmd.AppendGoflags(env, gocmd.VetOff)
	got[0] = "PATH=/tampered"
	if env[0] != "PATH=/usr/bin" {
		t.Errorf("writing to the result changed the input: env[0] = %q", env[0])
	}
}

func TestAppendGoflagsAddsNothingForAnEmptyFlag(t *testing.T) {
	t.Parallel()

	env := []string{"PATH=/usr/bin"}
	for _, flag := range []string{"", "   "} {
		if got := gocmd.AppendGoflags(env, flag); !slices.Equal(got, env) {
			t.Errorf("AppendGoflags(env, %q) = %q, want the environment unchanged", flag, got)
		}
	}
}

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

func TestSameEnvKeyIsDecidedByThePlatformItIsGiven(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		goos string
		a, b string
		want bool
	}{
		{"windows folds the case of a name", "windows", "GOFLAGS", "Goflags", true},
		{"windows still tells two names apart", "windows", "GOFLAGS", "GOPATH", false},
		{"windows matches an identical spelling", "windows", "GOFLAGS", "GOFLAGS", true},
		{"elsewhere the spelling is the name", "linux", "GOFLAGS", "Goflags", false},
		{"elsewhere an identical spelling still matches", "linux", "GOFLAGS", "GOFLAGS", true},
		{"darwin is not windows either", "darwin", "GOFLAGS", "goflags", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := gocmd.SameEnvKeyOn(c.goos, c.a, c.b); got != c.want {
				t.Errorf("SameEnvKeyOn(%q, %q, %q) = %v, want %v", c.goos, c.a, c.b, got, c.want)
			}
		})
	}
}

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
