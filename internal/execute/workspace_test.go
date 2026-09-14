// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
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
// workspace run *removes* it, so the go command finds the workspace file of the
// tree it is running in by walking up from its own working directory -- because
// a module of a workspace resolves its siblings through that file and a `go
// list` that ignored it would enumerate packages that do not build.
//
// Removed rather than named, and the difference is not cosmetic. A named path
// has a spelling and a working directory has another: a temporary directory
// reached through a symlink -- which is every one of them on macOS -- gives the
// go command a resolved cwd and an unresolved GOWORK, and it compares the two
// as text. "directory prefix app does not contain modules listed in go.work",
// about a module that is right there, is what that looks like.
//
// Removed rather than emptied, too: an empty value is a value, and the go
// command reads an empty GOWORK as "no workspace" rather than as "decide for
// yourself". The caller's own $GOWORK is gone either way, which is the half of
// the rule that does not change.
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
	opts.Workspace = true
	t.Setenv("GOWORK", filepath.Join("somebody", "elses", "go.work"))

	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}
	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("issued %d commands, want a listing and one compile", len(seen))
	}
	for _, c := range seen {
		if got := envValue(c.Env, "GOWORK"); got != "" {
			t.Errorf("%v ran with GOWORK %q, want none at all", c.Argv[1], got)
		}
		if slices.ContainsFunc(c.Env, func(entry string) bool {
			return strings.HasPrefix(entry, "GOWORK=")
		}) {
			t.Errorf("%v ran with a GOWORK entry; an empty value is a value", c.Argv[1])
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
