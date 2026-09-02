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
)

// TestCodesAreWellFormed keeps the diagnostic codes usable as the stable
// handles they are advertised to be: unique, sorted, and inside the block this
// package owns. A duplicated code makes two different failures
// indistinguishable to anyone searching for one.
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

// TestCodesAreReachable asserts the list is complete: every code the package
// documents can actually be produced. A code nobody can trigger is dead
// documentation, and one that is triggered but unlisted would be missing from
// `doctor`'s table.
func TestCodesAreReachable(t *testing.T) {
	produced := map[execute.Code]bool{}
	record := func(err error) {
		if code := execute.CodeOf(err); code != "" {
			produced[code] = true
		}
	}

	snapshot := t.TempDir()

	// GOM7501: options that cannot describe a build.
	_, err := execute.BuildTestBinaries(t.Context(), execute.Options{})
	record(err)

	// GOM7502: a binary directory that cannot be created, because a regular
	// file already sits where the directory would go.
	blocked := filepath.Join(t.TempDir(), "occupied")
	writeFile(t, blocked, "not a directory")
	_, err = execute.BuildTestBinaries(t.Context(), execute.Options{
		Toolchain: gocmd.Toolchain{GoBin: "go"}, SnapshotRoot: snapshot,
		BinDir: filepath.Join(blocked, "bin"),
	})
	record(err)

	// GOM7503 and GOM7504: a listing that fails, and one that cannot be read.
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

	// GOM7505: a package whose test binary does not compile.
	broken := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(pkgJSON("example.com/m/pkg", "/snap/pkg", true, false)))}
		}
		return runner.Result{ExitCode: 2, Output: []byte("undefined: Missing\n")}
	}}
	opts, _ = buildOptions(t, broken, 1)
	_, err = execute.BuildTestBinaries(t.Context(), opts)
	record(err)

	// GOM7510: nothing to measure against.
	_, err = execute.Schedule(t.Context(), execute.Options{},
		mutants(mutantTimeout, "a"), nil, execute.Hooks{})
	record(err)

	// GOM7511: a mutant with no timeout.
	record(execute.RunOne(t.Context(), execute.Options{},
		execute.MutantRun{ID: "abc"}, testBins("example.com/a")).Err)

	// GOM7512: a worker temporary directory that cannot be created.
	blockedScratch := execute.WithRunner(execute.Options{ScratchDir: filepath.Join(blocked, "w0")}, (&fake{}).run)
	record(execute.RunOne(t.Context(), blockedScratch,
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	// GOM7513: a test binary that could not be started.
	unstartableRunner := &fake{respond: func(context.Context, call) runner.Result { return unstartable() }}
	record(execute.RunOne(t.Context(), options(unstartableRunner, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	// GOM7514: the generated runtime refusing an unknown identity.
	stale := &fake{respond: func(context.Context, call) runner.Result { return staleCatalog() }}
	record(execute.RunOne(t.Context(), options(stale, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	// GOM7515: a probe pass with no log to record into, which could only ever
	// end as an empty set of indices produced by having recorded nothing.
	record(execute.RunProbe(t.Context(), execute.Options{},
		execute.ProbeRun{Timeout: mutantTimeout}, testBins("example.com/a")).Err)

	// GOM7516: a probe's test binary that could not be started.
	record(execute.RunProbe(t.Context(), options(unstartableRunner, 1),
		execute.ProbeRun{Timeout: mutantTimeout, LogPath: filepath.Join(t.TempDir(), "infection.log")},
		testBins("example.com/a")).Err)

	// GOM7517: an infection log that exists and cannot be read against the
	// catalogue it was supposed to have been written against.
	damaged := filepath.Join(t.TempDir(), "infection.log")
	writeFile(t, damaged, "not an infection log\n")
	passing := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	record(execute.RunProbe(t.Context(), options(passing, 1),
		execute.ProbeRun{Timeout: mutantTimeout, LogPath: damaged}, testBins("example.com/a")).Err)

	// GOM7520: a cancelled schedule.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = execute.Schedule(ctx, options(&fake{}, 1),
		mutants(mutantTimeout, "a"), testBins("example.com/a"), execute.Hooks{})
	record(err)

	// GOM7530: a coverage directory inside the snapshot, which the drift gate
	// would otherwise report as a test writing into the tree.
	covering, _ := coverOptions(t, &fake{respond: func(context.Context, call) runner.Result { return passed() }})
	_, err = execute.CollectCoverage(t.Context(), covering,
		testBins("example.com/a"), filepath.Join(covering.SnapshotRoot, "coverage"))
	record(err)

	// GOM7531: a test binary that does not pass during the coverage pass.
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

// TestErrorRendersTheCodeAndKeepsTheOutputSeparate pins the two-part shape every
// GOM error in this repository has: a one-line message a terminal can prefix
// and a grep can find, with the child's output beside it rather than inside it.
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

// TestCodeOfAndOutputOfIgnoreForeignErrors keeps the accessors honest about
// errors this package did not produce.
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

// TestTailKeepsTheEndAndCleansTheLineEndings pins the trimming every retained
// output goes through. The tail rather than the head, because the assertion
// that failed is at the end; the carriage returns removed here rather than by a
// renderer, because the same string goes into a report where a stray CR is
// invisible until it shows up in a diff.
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
