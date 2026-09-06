// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/trace"
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
		name     string
		jobs     int
		packages []string
		want     []string
	}{
		{"no parallelism chosen", 0, nil, []string{"build", "-o", os.DevNull, "./..."}},
		{"a negative parallelism", -3, nil, []string{"build", "-o", os.DevNull, "./..."}},
		{"one job", 1, nil, []string{"build", "-o", os.DevNull, "-p", "1", "./..."}},
		{"eight jobs", 8, nil, []string{"build", "-o", os.DevNull, "-p", "8", "./..."}},
		{"selected packages", 8, []string{"./a", "./b"}, []string{"build", "-o", os.DevNull, "-p", "8", "./a", "./b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := buildArgs(c.jobs, c.packages)
			if !slices.Equal(got, c.want) {
				t.Fatalf("buildArgs(%d, %q) = %s, want %s",
					c.jobs, c.packages, strings.Join(got, " "), strings.Join(c.want, " "))
			}
		})
	}
}

// TestBuildSnapshotLabelsTheExecAsValidateBuild names the compiles this phase
// spends.
//
// A validation of a large catalogue that has to bisect can spend dozens of
// builds, and in a recording they are otherwise the same `go build ./...` line
// repeated with no indication of what asked for it. The label is what separates
// them from the baseline's build and from every compile the execution phase
// issues, and the sequence is what lets a step point at the compile it ran.
//
// The toolchain here is a path with nothing behind it, so no process is
// started: what is being asserted is the label on the execution, and
// internal/runner records a command that could not be started exactly as it
// records one that ran — which is the case where a reader needs the record
// most.
func TestBuildSnapshotLabelsTheExecAsValidateBuild(t *testing.T) {
	t.Parallel()

	recorder, sink := recording(t)
	v := &validator{
		root:      t.TempDir(),
		toolchain: gocmd.Toolchain{GoBin: filepath.Join(t.TempDir(), "not-a-toolchain")},
		timeout:   time.Minute,
		recorder:  recorder,
	}

	got, err := v.buildSnapshot(t.Context())
	if CodeOf(err) != CodeBuildFailed {
		t.Fatalf("buildSnapshot failed with %q, want %q: %v", CodeOf(err), CodeBuildFailed, err)
	}

	var execs []trace.Event
	for _, event := range sink.Events() {
		if event.Type == trace.TypeExec {
			execs = append(execs, event)
		}
	}
	if len(execs) != 1 {
		t.Fatalf("the recording holds %d exec events, want one per build", len(execs))
	}
	if got := execs[0].Exec.Kind; got != trace.ExecKindValidateBuild {
		t.Errorf("the build is labelled %q, want %q", got, trace.ExecKindValidateBuild)
	}
	if argv := execs[0].Exec.Argv; len(argv) == 0 || argv[0] != v.toolchain.GoBin {
		t.Errorf("the recorded argv is %q, want the toolchain that could not be started", argv)
	}
	// The verdict points at its own compile, so a step that spent this build
	// can say which execution it was without reaching past the build seam.
	if got.execSeq != execs[0].Seq {
		t.Errorf("the verdict points at exec %d, want the %d this build was recorded at",
			got.execSeq, execs[0].Seq)
	}
}
