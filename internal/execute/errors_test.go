// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestCodesAreWellFormed(t *testing.T) {
	t.Parallel()

	codes := execute.Codes()
	if len(codes) == 0 {
		t.Fatal("Codes() is empty")
	}
	if !slices.IsSorted(codes) {
		t.Errorf("Codes() = %v, want them in numeric order", codes)
	}
	seen := map[execute.Code]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Errorf("code %s appears twice", c)
		}
		seen[c] = true

		rest, ok := strings.CutPrefix(string(c), "GOM")
		if !ok || len(rest) != 4 {
			t.Errorf("code %q is not of the form GOM####", c)
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			t.Errorf("code %q does not end in a number", c)
			continue
		}
		if n < 7500 || n > 7599 {
			t.Errorf("code %q is outside the GOM75xx block this package owns", c)
		}
	}
}

func TestCodesAreReachable(t *testing.T) {
	produced := map[execute.Code]bool{}
	record := func(err error) {
		if code := execute.CodeOf(err); code != "" {
			produced[code] = true
		}
	}

	snapshot := t.TempDir()

	_, err := execute.BuildTestBinaries(t.Context(), execute.Options{})
	record(err)

	blocked := filepath.Join(t.TempDir(), "occupied")
	testkit.WriteFile(t, blocked, []byte("not a directory"))
	_, err = execute.BuildTestBinaries(t.Context(), execute.Options{
		Toolchain: gocmd.Toolchain{GoBin: "go"}, SnapshotRoot: snapshot,
		BinDir: filepath.Join(blocked, "bin"),
	})
	record(err)

	failing := &fake{respond: func(context.Context, call) runner.Result {
		return runner.Result{ExitCode: 1, Output: []byte("go: broken\n")}
	}}
	opts, _ := buildOptions(t, failing, 1)
	_, err = execute.BuildTestBinaries(t.Context(), opts)
	record(err)

	garbage := &fake{respond: func(context.Context, call) runner.Result {
		return runner.Result{Output: []byte("not json\n")}
	}}
	opts, _ = buildOptions(t, garbage, 1)
	_, err = execute.BuildTestBinaries(t.Context(), opts)
	record(err)

	broken := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(pkgJSON("example.com/m/pkg", "/snap/pkg", true, false)))}
		}
		return runner.Result{ExitCode: 2, Output: []byte("undefined: Missing\n")}
	}}
	opts, _ = buildOptions(t, broken, 1)
	_, err = execute.BuildTestBinaries(t.Context(), opts)
	record(err)

	_, err = execute.Schedule(t.Context(), execute.Options{},
		mutants(mutantTimeout, "a"), nil, execute.Hooks{})
	record(err)

	record(execute.RunOne(t.Context(), execute.Options{},
		execute.MutantRun{ID: "abc"}, testBins("example.com/a")).Err)

	blockedScratch := execute.WithRunner(execute.Options{ScratchDir: filepath.Join(blocked, "w0")}, (&fake{}).run)
	record(execute.RunOne(t.Context(), blockedScratch,
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	unstartableRunner := &fake{respond: func(context.Context, call) runner.Result { return unstartable() }}
	record(execute.RunOne(t.Context(), options(unstartableRunner, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	stale := &fake{respond: func(context.Context, call) runner.Result { return staleCatalog() }}
	record(execute.RunOne(t.Context(), options(stale, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	record(execute.RunProbe(t.Context(), execute.Options{},
		execute.ProbeRun{Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	record(execute.RunProbe(t.Context(), options(unstartableRunner, 1),
		execute.ProbeRun{Timeout: mutantTimeout, LogPath: filepath.Join(t.TempDir(), "infection.log")},
		testBins("example.com/a")).Err)

	damaged := filepath.Join(t.TempDir(), "infection.log")
	testkit.WriteFile(t, damaged, []byte("not an infection log\n"))
	passing := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	record(execute.RunProbe(t.Context(), options(passing, 1),
		execute.ProbeRun{Timeout: mutantTimeout, LogPath: damaged}, testBins("example.com/a")).Err)

	record(execute.RunControl(t.Context(), execute.Options{},
		execute.ControlRun{}, testBins("example.com/a")).Err)

	record(execute.RunControl(t.Context(), options(unstartableRunner, 1),
		execute.ControlRun{Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = execute.Schedule(ctx, options(&fake{}, 1),
		mutants(mutantTimeout, "a"), testBins("example.com/a"), execute.Hooks{})
	record(err)

	refusing := &fake{respond: func(context.Context, call) runner.Result {
		return runner.Result{
			ExitCode: 2,
			Output:   []byte("flag provided but not defined: -test.testlogfile\n"),
		}
	}}
	refusingOptions := options(refusing, 1)
	refusingOptions.ScratchDir = t.TempDir()
	record(execute.RunOne(t.Context(), refusingOptions,
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout, RecordTestLog: true},
		testBins("example.com/a")).Err)

	covering, _ := coverOptions(t, &fake{respond: func(context.Context, call) runner.Result { return passed() }})
	_, err = execute.CollectCoverage(t.Context(), covering,
		testBins("example.com/a"), filepath.Join(covering.SnapshotRoot, "coverage"))
	record(err)

	redSuite := &fake{respond: func(context.Context, call) runner.Result { return failed("--- FAIL\n") }}
	coverFailing, coverDir := coverOptions(t, redSuite)
	_, err = execute.CollectCoverage(t.Context(), coverFailing, testBins("example.com/a"), coverDir)
	record(err)

	for _, code := range execute.Codes() {
		if !produced[code] {
			t.Errorf("no path in these tests produces %s; it is either unreachable or untested", code)
		}
	}
	for code := range produced {
		if !slices.Contains(execute.Codes(), code) {
			t.Errorf("%s was produced but is not in Codes()", code)
		}
	}
}

func TestErrorRendersTheCodeAndKeepsTheOutputSeparate(t *testing.T) {
	t.Parallel()

	cause := errors.New("the underlying trouble")
	err := &execute.Error{
		Code:    execute.CodeTestBuildFailed,
		Message: "the test binary for example.com/m/pkg could not be built",
		Output:  "./a_test.go:9:2: undefined: Missing",
		Err:     cause,
	}

	want := "GOM7505: the test binary for example.com/m/pkg could not be built: the underlying trouble"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if strings.Contains(err.Error(), "undefined") {
		t.Error("the retained output leaked into the one-line message")
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if got := execute.CodeOf(err); got != execute.CodeTestBuildFailed {
		t.Errorf("CodeOf = %q, want %q", got, execute.CodeTestBuildFailed)
	}
	if got := execute.OutputOf(err); got != err.Output {
		t.Errorf("OutputOf = %q, want %q", got, err.Output)
	}

	bare := &execute.Error{Code: execute.CodeOptions, Message: "no snapshot root was given"}
	if got, want := bare.Error(), "GOM7501: no snapshot root was given"; got != want {
		t.Errorf("Error() without a cause = %q, want %q", got, want)
	}
}

func TestCodeOfAndOutputOfIgnoreForeignErrors(t *testing.T) {
	t.Parallel()

	foreign := errors.New("from somewhere else")
	if got := execute.CodeOf(foreign); got != "" {
		t.Errorf("CodeOf(foreign) = %q, want empty", got)
	}
	if got := execute.OutputOf(foreign); got != "" {
		t.Errorf("OutputOf(foreign) = %q, want empty", got)
	}
	if got := execute.CodeOf(nil); got != "" {
		t.Errorf("CodeOf(nil) = %q, want empty", got)
	}
}

func TestTailKeepsTheEndAndCleansTheLineEndings(t *testing.T) {
	t.Parallel()

	if got := execute.Tail(nil); got != "" {
		t.Errorf("Tail(nil) = %q, want empty", got)
	}
	if got := execute.Tail([]byte("   \r\n\r\n")); got != "" {
		t.Errorf("Tail(blank) = %q, want empty", got)
	}
	if got, want := execute.Tail([]byte("one\r\ntwo\r\n")), "one\ntwo"; got != want {
		t.Errorf("Tail = %q, want %q", got, want)
	}

	var many strings.Builder
	for i := range 200 {
		many.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	got := execute.Tail([]byte(many.String()))
	lines := strings.Split(got, "\n")
	if len(lines) != execute.OutputTailLines {
		t.Errorf("kept %d lines, want %d", len(lines), execute.OutputTailLines)
	}
	if lines[len(lines)-1] != "line 199" {
		t.Errorf("last kept line = %q, want the last line written", lines[len(lines)-1])
	}
}
