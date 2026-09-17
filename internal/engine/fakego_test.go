// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

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

func TestScopePatternResolutionFailureIsReported(t *testing.T) {
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

			for _, call := range f.Calls() {
				if call.Argv[0] == "build" || call.Argv[0] == "test" {
					t.Errorf("the run reached %v before the scope was resolved", call)
				}
			}
		})
	}
}

func TestBaselineFailureStopsTheRunWithTheOutputTail(t *testing.T) {
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

	var issued []string
	for _, call := range f.Calls() {
		issued = append(issued, call.Argv[0])
	}
	want := []string{"version", "list", "build", "test"}
	if strings.Join(issued, " ") != strings.Join(want, " ") {
		t.Errorf("the run issued %q, want %q", issued, want)
	}
	if got := outcome.ResolvedTestCommand; len(got) == 0 || got[0] != f.Bin() {
		t.Errorf("ResolvedTestCommand = %q, want it to start with the located toolchain %q",
			got, f.Bin())
	}
}

func TestABaselineTheToolchainOnlyLookedUpSaysSo(t *testing.T) {
	f, opts := fakeRun(t)
	opts.Config.Test.BaselineRuns = 3

	f.On("list", "-e").Stdout(scopeMarker + filepath.Join("snap", "only") + "\n")
	f.On("build")
	f.On("test").Stdout("ok  \tfixture.example/faked/only\t(cached)\n")

	outcome, _ := Run(t.Context(), opts)

	if len(outcome.BaselineRuns) != 3 {
		t.Fatalf("measured %d baseline runs, want 3", len(outcome.BaselineRuns))
	}
	warning, found := warningOf(outcome, CodeBaselineFromTestCache)
	if !found {
		t.Fatalf("a baseline of three cache lookups published %v, want a %s",
			outcome.Warnings, CodeBaselineFromTestCache)
	}
	for _, want := range []string{"3 baseline runs", "cache", "-count=1"} {
		if !strings.Contains(warning.Message, want) {
			t.Errorf("the warning does not say %q:\n%s", want, warning.Message)
		}
	}
}

func warningOf(outcome RunOutcome, code Code) (Warning, bool) {
	for _, warning := range outcome.Warnings {
		if warning.Code == string(code) {
			return warning, true
		}
	}
	return Warning{}, false
}

func TestEveryBaselineRunAfterTheFirstMeasuresTheSuite(t *testing.T) {
	f, opts := fakeRun(t)
	opts.Config.Test.BaselineRuns = 3

	f.On("list", "-e").Stdout(scopeMarker + filepath.Join("snap", "only") + "\n")
	f.On("build")
	f.On("test").Stdout("ok  \tfixture.example/faked/only\t0.004s\n")

	_, _ = Run(t.Context(), opts)

	var suites []mutantkit.Call
	for _, call := range f.Calls() {
		if len(call.Argv) > 0 && call.Argv[0] == "test" {
			suites = append(suites, call)
		}
	}
	if len(suites) != 3 {
		t.Fatalf("the baseline started %d suites, want 3: %v", len(suites), f.Calls())
	}
	if got := suites[0].Env["GOFLAGS"]; strings.Contains(got, gocmd.CountOnce) {
		t.Errorf("the first baseline run was given GOFLAGS=%q; it is the user's command as written", got)
	}
	for i, call := range suites[1:] {
		if got := call.Env["GOFLAGS"]; !strings.Contains(got, gocmd.CountOnce) {
			t.Errorf("baseline run %d was given GOFLAGS=%q, want it to carry %s so that it measures the suite",
				i+2, got, gocmd.CountOnce)
		}
	}
}
