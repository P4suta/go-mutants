// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestBuildArgsSendTheOutputToTheNullDevice pins the whole command one
// validation build runs, because the flag that matters most in it is the one
// whose absence is invisible in a passing run.
//
// A `go build` with no `-o` writes a linked executable into its working
// directory whenever the pattern resolves to a single `main` package, and this
// phase's working directory is the snapshot root — a tree that is re-digested
// afterwards, with no exclusions, to catch tests that write into the package
// directory they run in. The file would be reported as workspace drift and the
// user would be told their tests did it. No fixture in the corpus can catch
// that (they are libraries, and the generated runtime makes `./...` more than
// one package in any case), so the vector itself is the thing to assert.
//
// The rest of the vector is pinned in the same breath, since the whole point of
// asserting it is that a build is otherwise only ever observed through its
// exit status: `-p` appears only when a parallelism was chosen, and `./...`
// stays last, because the go command takes everything after the flags as
// packages.
func TestBuildArgsSendTheOutputToTheNullDevice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		jobs int
		want []string
	}{
		{"no parallelism chosen", 0, []string{"build", "-o", os.DevNull, "./..."}},
		{"a negative parallelism", -3, []string{"build", "-o", os.DevNull, "./..."}},
		{"one job", 1, []string{"build", "-o", os.DevNull, "-p", "1", "./..."}},
		{"eight jobs", 8, []string{"build", "-o", os.DevNull, "-p", "8", "./..."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := buildArgs(c.jobs)
			if !slices.Equal(got, c.want) {
				t.Fatalf("buildArgs(%d) = %s, want %s",
					c.jobs, strings.Join(got, " "), strings.Join(c.want, " "))
			}
		})
	}
}
