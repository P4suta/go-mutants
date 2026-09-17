// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

func TestParseTestListKeepsWhatARunWouldExecute(t *testing.T) {
	t.Parallel()

	got := execute.ParseTestList([]byte("TestA\nTestB\nExampleC\nFuzzD\nBenchmarkE\nok  \texample.com/m\t0.001s\n"))
	if want := []string{"TestA", "TestB", "ExampleC", "FuzzD"}; !slices.Equal(got, want) {
		t.Errorf("ParseTestList = %v, want %v", got, want)
	}
	if got := execute.ParseTestList(nil); len(got) != 0 {
		t.Errorf("ParseTestList(nil) = %v, want none", got)
	}
}

func isTestList(c call) bool {
	return slices.ContainsFunc(c.Argv, func(a string) bool { return strings.HasPrefix(a, "-test.list=") })
}

func selectedTest(c call) string {
	for _, a := range c.Argv {
		if selector, ok := strings.CutPrefix(a, "-test.run="); ok {
			name, anchored := strings.CutPrefix(selector, "^")
			name, terminated := strings.CutSuffix(name, "$")
			if !anchored || !terminated {
				return "unanchored selector " + selector
			}
			return name
		}
	}
	return ""
}

func TestCollectTestCoverageRunsEachTestAloneIntoItsOwnDirectory(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		switch {
		case isTestList(c):
			return runner.Result{Output: []byte("TestA\nTestB\n")}
		case selectedTest(c) == "TestB":
			return failed("--- FAIL: TestB\n")
		}
		return passed()
	}}
	opts, coverDir := coverOptions(t, f)
	bins := testBins("example.com/m/a")

	collected, err := execute.CollectTestCoverage(t.Context(), opts, bins, coverDir)
	if err != nil {
		t.Fatalf("CollectTestCoverage: %v", err)
	}
	if len(collected) != 2 {
		t.Fatalf("collected %d per-test profiles, want 2: %+v", len(collected), collected)
	}
	want := []execute.TestCoverageData{
		{ImportPath: "example.com/m/a", Name: "TestA", Passed: true},
		{ImportPath: "example.com/m/a", Name: "TestB", Passed: false},
	}
	for i, data := range collected {
		if data.ImportPath != want[i].ImportPath || data.Name != want[i].Name || data.Passed != want[i].Passed {
			t.Errorf("profile %d = %+v, want %+v (directory aside)", i, data, want[i])
		}
		if data.Path == "" || !strings.HasPrefix(data.Path, coverDir) {
			t.Errorf("profile %d is at %q, want a path under %s", i, data.Path, coverDir)
		}
		if ok, statErr := statDir(filepath.Dir(data.Path)); statErr != nil || !ok {
			t.Errorf("the directory holding %s was not created: %v", data.Path, statErr)
		}
	}
	if collected[0].Path == collected[1].Path {
		t.Errorf("two tests share the coverage profile %s", collected[0].Path)
	}

	seen := f.seen()
	if len(seen) != 3 {
		t.Fatalf("started %d processes, want a listing and one run per test: %v", len(seen), f.programs())
	}
	if !isTestList(seen[0]) || seen[0].program() != bins[0].BinPath {
		t.Errorf("the first process was %v, want the binary's -test.list", seen[0].Argv)
	}
	for i, name := range []string{"TestA", "TestB"} {
		c := seen[i+1]
		if got := selectedTest(c); got != name {
			t.Errorf("run %d selected %q, want %q via an anchored -test.run", i, got, name)
		}
		if !slices.Contains(c.Argv, "-test.coverprofile="+collected[i].Path) {
			t.Errorf("run %d argv = %v, want it to write into %s", i, c.Argv, collected[i].Path)
		}
		if c.Dir != bins[0].Dir {
			t.Errorf("run %d ran in %q, want the package directory %q", i, c.Dir, bins[0].Dir)
		}
		if active := c.active(); active != "" {
			t.Errorf("run %d activated the mutant %q", i, active)
		}
	}
}

func TestCollectTestCoverageLabelsEveryRun(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isTestList(c) {
			return runner.Result{Output: []byte("TestOne\n")}
		}
		return passed()
	}}
	built, coverDir := coverOptions(t, f)
	opts, sink := traced(t, f, built)
	bins := testBins("example.com/m/a")

	if _, err := execute.CollectTestCoverage(t.Context(), opts, bins, coverDir); err != nil {
		t.Fatalf("CollectTestCoverage: %v", err)
	}
	var got []string
	for _, event := range eventsOf(sink, trace.TypeExec) {
		got = append(got, event.Exec.Kind+" "+event.Exec.Subject)
	}
	want := []string{
		trace.ExecKindTestList + " example.com/m/a",
		trace.ExecKindCoverageRun + " example.com/m/a TestOne",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the runs were recorded as %q, want %q", got, want)
	}
}

func TestCollectTestCoverageReportsAListingThatFails(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isTestList(c) {
			return failed("flag provided but not defined: -test.list\n")
		}
		return passed()
	}}
	opts, coverDir := coverOptions(t, f)

	_, err := execute.CollectTestCoverage(t.Context(), opts, testBins("example.com/m/a"), coverDir)
	if got := execute.CodeOf(err); got != execute.CodeCoverageFailed {
		t.Fatalf("CollectTestCoverage = %v (code %q), want %s", err, got, execute.CodeCoverageFailed)
	}
	if !strings.Contains(err.Error(), "example.com/m/a") {
		t.Errorf("the failure does not name the binary: %v", err)
	}
}

func TestCollectTestCoverageProfilesTheTestsConcurrently(t *testing.T) {
	t.Parallel()

	const jobs = 4
	var arrived sync.WaitGroup
	arrived.Add(jobs)
	together := make(chan struct{})
	var once sync.Once

	f := &fake{respond: func(ctx context.Context, c call) runner.Result {
		if isTestList(c) {
			return runner.Result{Output: []byte("TestA\nTestB\nTestC\nTestD\n")}
		}
		arrived.Done()
		go once.Do(func() { arrived.Wait(); close(together) })
		select {
		case <-together:
			return passed()
		case <-ctx.Done():
			return runner.Result{Err: ctx.Err()}
		}
	}}
	opts, coverDir := coverOptions(t, f)
	opts.Jobs = jobs

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	collected, err := execute.CollectTestCoverage(ctx, opts, testBins("example.com/m/a"), coverDir)
	if err != nil {
		t.Fatalf("CollectTestCoverage: %v", err)
	}

	var names []string
	for _, data := range collected {
		names = append(names, data.Name)
	}
	if want := "TestA TestB TestC TestD"; strings.Join(names, " ") != want {
		t.Errorf("the profiles came out as %q, want %q", strings.Join(names, " "), want)
	}
	for i, data := range collected {
		if !data.Passed {
			t.Errorf("profile %d did not pass: %+v", i, data)
		}
	}
}
