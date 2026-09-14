// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/runner"
)

// TestAWorkspaceRunObeysTheWorkspaceFileTheSnapshotCarries is the one place
// GOWORK is decided for every `go` command this package issues, and a workspace
// run inverts exactly one half of the rule.
//
// GOWORK=off is what makes the package set built here the package set discovery
// type-checked: the go command searches every parent directory for a `go.work`
// and obeys $GOWORK, so a snapshot placed one level below somebody's workspace
// would otherwise resolve against a file the snapshot does not contain. A
// workspace run names a workspace file that *is* in the snapshot, because a
// module of a workspace resolves its siblings through it and a `go list` that
// ignored it would enumerate packages that do not build.
//
// Every other workspace file stays ignored either way, which is the half that
// does not change: nothing here ever consults the caller's own $GOWORK.
func TestAWorkspaceRunObeysTheWorkspaceFileTheSnapshotCarries(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/ws/first/pkg", "/snap/first/pkg", true, false),
			))}
		}
		return runner.Result{}
	}}
	opts, _ := buildOptions(t, f, 1)
	workFile := filepath.Join(opts.SnapshotRoot, "go.work")
	opts.WorkFile = workFile
	t.Setenv("GOWORK", filepath.Join("somebody", "elses", "go.work"))

	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}
	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("issued %d commands, want a listing and one compile", len(seen))
	}
	for _, c := range seen {
		if got := envValue(c.Env, "GOWORK"); got != workFile {
			t.Errorf("%v ran with GOWORK %q, want the snapshot's own %q", c.Argv[1], got, workFile)
		}
	}
}

// TestAMutantRunsWithNoOpinionAboutWorkspaces keeps the work file where it
// belongs, which is the build and not the measurement.
//
// A test binary is the user's program, already linked, and nothing it does
// depends on GOWORK. The toolchain settings this package pins for the build are
// deliberately not applied to it -- the fewer variables go-mutants invents
// around the user's program, the closer the measurement is to what `go test`
// would have produced -- and the work file is one of those settings.
func TestAMutantRunsWithNoOpinionAboutWorkspaces(t *testing.T) {
	env := execute.MutantEnv("some-mutant-id", t.TempDir())
	if got := envValue(env, "GOWORK"); got != "" {
		t.Errorf("a mutant ran with GOWORK %q, want none", got)
	}
}
