// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The first three stages of a run — locate, scope, baseline — driven by a
// scripted `go`.
//
// integration_test.go runs the whole pipeline against the corpus with a real
// toolchain, which is the only way to prove that a mutant really is killed by a
// real suite. It is also tens of seconds per test, and it cannot produce the two
// failures below on demand: a `go list` that refuses a pattern, and a baseline
// suite that goes red. A fixture whose tests fail is a fixture every other test
// has to route around, and the first cannot be written at all.
//
// So they are scripted here. The pipeline is the real one — the snapshot is
// copied, the scope is resolved, the baseline is built and measured — and only
// the toolchain is a stand-in, which is what makes these unit-tier tests that
// run in a hundredth of the time and on a machine with no Go at all.

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestMain turns this binary into the scripted `go` when it is started as one.
// The tests below put it on PATH, so without the dispatch every `go` command a
// run issues would start the whole package again.
func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

// fakeRun builds a workspace and the options for a run over it, with a scripted
// toolchain published into this process.
//
// The publication is what makes this a test of the *pipeline* rather than of a
// seam: internal/engine locates the toolchain with gocmd.Options{} — no explicit
// path, no environment — and composes every child's environment from this
// process's own, so PATH is the only way in and there is nothing to inject. The
// price is t.Setenv, so no test here may be parallel.
//
// The three directories are all named because every default is a directory of
// the developer's own: the history store and the outcome cache would otherwise
// be filed under os.UserCacheDir, and the snapshot would be made in the
// machine's temporary area rather than in one this test can prove it emptied.
func fakeRun(t *testing.T) (*mutantkit.Fake, Options) {
	t.Helper()

	module := testkit.NewModule(t).Module("fixture.example/faked")
	module.Source("only/only.go", "package only\n\nfunc Only(n int) int {\n\tif n > 1 {\n\t\treturn n\n\t}\n\treturn 1\n}\n")

	f := mutantkit.FakeGo(t)
	f.Version("1.99.0")
	f.Export()

	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1
	cfg.Execution.Jobs = 1

	private := testkit.Scratch(t)
	temp := filepath.Join(private, "temp")
	if err := os.MkdirAll(temp, 0o755); err != nil {
		t.Fatalf("creating the run's temporary directory: %v", err)
	}
	return f, Options{
		Config:        cfg,
		WorkspaceRoot: module.Root(),
		ToolVersion:   "0.0.0-test",
		TempDirectory: temp,
		HistoryRoot:   filepath.Join(private, "history"),
		CacheRoot:     filepath.Join(private, "cache"),
	}
}

// TestScopePatternResolutionFailureIsReported is the check that costs a second
// and saves several minutes: every pattern in a recognised test command is
// resolved before anything is built or measured.
//
// Both ways a pattern can fail are here, and they are different diagnoses. A
// `go list` that exits non-zero is the go command refusing to work in this
// snapshot at all, and its own words are the explanation; a listing that
// succeeds and places nothing is a pattern that names no package, which is a
// typo in the user's own `test.command` and has to say so with the pattern
// quoted.
//
// Neither can be produced by a real toolchain over a fixture that builds, which
// is why this test is scripted: `go list -e` is deliberately forgiving, and the
// only way to see the refusal branch is a `go` that refuses.
func TestScopePatternResolutionFailureIsReported(t *testing.T) {
	// No t.Parallel: the scripted toolchain is published with t.Setenv.
	for _, test := range []struct {
		name    string
		script  func(*mutantkit.Fake)
		wantErr []string
	}{
		{
			name: "the go command refuses the listing",
			script: func(f *mutantkit.Fake) {
				f.On("list", "-e").
					Stderr("go: updates to go.mod needed; to update it:\n\tgo mod tidy\n").
					Exit(1)
			},
			wantErr: []string{`"./..."`, "could not be resolved", "exited with status 1"},
		},
		{
			name: "the pattern places no package",
			script: func(f *mutantkit.Fake) {
				// What `go list -e` really answers for a pattern that matches
				// nothing: a warning on the stream the capture merges, an exit
				// status of zero, and not one placed row.
				f.On("list", "-e").
					Stderr("go: warning: \"./...\" matched no packages\n").
					Exit(0)
			},
			wantErr: []string{`"./..."`, "matches no package in the workspace"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, opts := fakeRun(t)
			test.script(f)

			outcome, err := Run(t.Context(), opts)
			if err == nil {
				t.Fatalf("Run with an unresolvable scope = %+v, want an error", outcome)
			}
			if code := CodeOf(err); code != CodeTestScope {
				t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, CodeTestScope)
			}
			for _, want := range test.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Error() = %q, want it to contain %q", err, want)
				}
			}
			if outcome.Status != StatusFailed {
				t.Errorf("Status = %s, want %s", outcome.Status, StatusFailed)
			}

			// Resolved before anything was built or measured, which is the
			// whole reason the check exists: a typo must not cost a build,
			// three timed runs, a discovery pass and an instrumentation.
			for _, call := range f.Calls() {
				if call.Argv[0] == "build" || call.Argv[0] == "test" {
					t.Errorf("the run reached %v before the scope was resolved", call)
				}
			}
		})
	}
}

// TestBaselineFailureStopsTheRunWithTheOutputTail is the refusal that keeps a
// score honest: a project whose tests are already red cannot be
// mutation-tested, because every mutant would be "killed" by a failure that was
// there before go-mutants arrived.
//
// The output is what makes it actionable, and it is the part that used to stop
// at the process boundary. `go test` explains a red suite in its own words, on a
// stream the runner merges, and a refusal that reported only "exited with status
// 1" would send the user to run their own suite again by hand to find out which
// test it was.
func TestBaselineFailureStopsTheRunWithTheOutputTail(t *testing.T) {
	// No t.Parallel: the scripted toolchain is published with t.Setenv.
	f, opts := fakeRun(t)

	f.On("list", "-e").Stdout(scopeMarker + filepath.Join("snap", "only") + "\n")
	f.On("build")
	const failure = "--- FAIL: TestOnly (0.00s)\n" +
		"    only_test.go:11: Only(2) = 1, want 2\n" +
		"FAIL\n" +
		"FAIL\tfixture.example/faked/only\t0.004s\n"
	f.On("test").Stdout(failure).Exit(1)

	outcome, err := Run(t.Context(), opts)
	if err == nil {
		t.Fatalf("Run over a workspace whose suite is red = %+v, want an error", outcome)
	}
	if code := CodeOf(err); code != CodeBaselineTestFailed {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, CodeBaselineTestFailed)
	}
	if want := "baseline run 1 of 1 failed"; !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to say %q", err, want)
	}
	for _, line := range strings.Split(strings.TrimSpace(failure), "\n") {
		if !strings.Contains(OutputOf(err), line) {
			t.Errorf("the retained output does not quote %q:\n%s", line, OutputOf(err))
		}
	}

	// The order the pipeline promises, read off the toolchain rather than off
	// the code: the version probe, the scope, the build, and only then the
	// suite. A baseline measured before the snapshot built would be measuring a
	// tree the run had not proven.
	var issued []string
	for _, call := range f.Calls() {
		issued = append(issued, call.Argv[0])
	}
	want := []string{"version", "list", "build", "test"}
	if strings.Join(issued, " ") != strings.Join(want, " ") {
		t.Errorf("the run issued %q, want %q", issued, want)
	}
	// And the suite ran the toolchain the run reported rather than whatever
	// `go` a child's PATH would have resolved: a run under a toolchain manager
	// must not measure a different `go` from the one it names.
	if got := outcome.ResolvedTestCommand; len(got) == 0 || got[0] != f.Bin() {
		t.Errorf("ResolvedTestCommand = %q, want it to start with the located toolchain %q",
			got, f.Bin())
	}
}
