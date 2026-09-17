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

func TestAMutantRunsWithNoOpinionAboutWorkspaces(t *testing.T) {
	env := execute.MutantEnv("some-mutant-id", t.TempDir())
	if got := envValue(env, "GOWORK"); got != "" {
		t.Errorf("a mutant ran with GOWORK %q, want none", got)
	}
}
