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
	if got.execSeq != execs[0].Seq {
		t.Errorf("the verdict points at exec %d, want the %d this build was recorded at",
			got.execSeq, execs[0].Seq)
	}
}
