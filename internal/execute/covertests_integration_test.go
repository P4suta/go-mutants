// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package execute_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// perTestModule is a module whose one test binary holds tests with different
// reach, which is the situation test-level narrowing exists for and the one
// the corpus fixtures cannot show: each of theirs has one test per package, so
// "the tests that reach it" and "the binaries that reach it" are the same
// answer there.
const perTestModule = "fixture.example/pertest"

// perTestSource is the program. Each function holds one comparison on a line
// of its own, and each comparison's text is unique in the file, which is how
// the test names a line without counting them: the discovery pass says where
// each comparison is.
const perTestSource = `package pertest

// Positive is reached by TestPositive alone.
func Positive(v int) bool {
	return v > 0
}

// Negative is reached by TestNegative alone.
func Negative(v int) bool {
	return v < 0
}

// Unreached is called by nothing.
func Unreached(v int) bool {
	return v != 0
}
`

// perTestSuite is the test file: two tests with different reach, a benchmark
// that `-test.list` names and no run can select, and a pair of tests where
// the second passes only after the first ran — the order dependence a
// per-test pass has to notice, because a test that fails alone cannot be
// the sole witness of anything.
const perTestSuite = `package pertest

import "testing"

func TestPositive(t *testing.T) {
	if !Positive(1) || Positive(0) {
		t.Fatal("Positive is wrong")
	}
}

func TestNegative(t *testing.T) {
	if !Negative(-1) || Negative(0) {
		t.Fatal("Negative is wrong")
	}
}

func BenchmarkPositive(b *testing.B) {
	for range b.N {
		Positive(1)
	}
}

var first bool

func TestFirst(t *testing.T) {
	first = true
}

func TestSecond(t *testing.T) {
	if !first {
		t.Fatal("TestSecond ran alone")
	}
}
`

// TestCollectTestCoverageTellsTheTestsOfOneBinaryApart is the pass against a
// real toolchain: the binary really lists its tests, each test really runs
// alone and leaves a profile, and the profiles really tell the tests apart —
// a mutant reached by one test of the binary is mapped to that test and not
// to the binary's other tests. It is also where the order-dependent test is
// shown to be reported rather than believed.
func TestCollectTestCoverageTellsTheTestsOfOneBinaryApart(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	source := testkit.NewModule(t).Module(perTestModule).
		Source("pertest.go", perTestSource).
		Source("pertest_test.go", perTestSuite).
		Root()
	snap := mutantkit.SnapshotOf(t, source)
	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	catalog := mutantkit.Catalog(t, found)
	if _, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
	}); err != nil {
		t.Fatalf("instrumenting the snapshot: %v", err)
	}

	work := t.TempDir()
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(work, "bin"),
		ScratchDir:   filepath.Join(work, "workers"),
		CoverPkg:     perTestModule + "/...",
		Jobs:         1,
		Timeout:      buildTimeout,
		Env:          env,
	}
	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the test binary: %v\n%s", err, execute.OutputOf(err))
	}
	if len(bins) != 1 {
		t.Fatalf("built %d binaries, want the one package's", len(bins))
	}

	collected, err := execute.CollectTestCoverage(t.Context(), opts, bins, filepath.Join(work, "coverage"))
	if err != nil {
		t.Fatalf("the per-test coverage pass: %v\n%s", err, execute.OutputOf(err))
	}

	// What the binary listed, in source order, without the benchmark.
	var names []string
	for _, data := range collected {
		names = append(names, data.Name)
	}
	if want := []string{"TestPositive", "TestNegative", "TestFirst", "TestSecond"}; !slices.Equal(names, want) {
		t.Fatalf("the pass ran %v, want %v", names, want)
	}
	for _, data := range collected {
		if want := data.Name != "TestSecond"; data.Passed != want {
			t.Errorf("%s alone: passed=%t, want %t (%s)", data.Name, data.Passed, want, data.Output)
		}
		if data.TimedOut {
			t.Errorf("%s alone is reported as timed out", data.Name)
		}
		if data.Name == "TestSecond" && !strings.Contains(data.Output, "TestSecond ran alone") {
			t.Errorf("the output retained for TestSecond does not say why it failed: %q", data.Output)
		}
		if data.Name != "TestSecond" && data.Output != "" {
			t.Errorf("output was retained for the passing %s: %q", data.Name, data.Output)
		}
	}

	// The profiles of the tests that passed alone, rendered the way the engine
	// renders them.
	profiles := make(map[coverage.TestKey]coverage.Profile)
	for i, data := range collected {
		if !data.Passed {
			continue
		}
		path := filepath.Join(work, "profile-"+strconv.Itoa(i)+".txt")
		result := mutantkit.RunGo(t, toolchain, snap.Root, env, "tool", "covdata", "textfmt", "-i="+data.Dir, "-o="+path)
		mutantkit.RequireExit(t, result, 0, "rendering the profile of "+data.Name)
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("opening the rendered profile of %s: %v", data.Name, err)
		}
		profile, err := coverage.ParseTextfmt(file)
		file.Close()
		if err != nil {
			t.Fatalf("parsing the rendered profile of %s: %v", data.Name, err)
		}
		profiles[coverage.TestKey{ImportPath: data.ImportPath, Name: data.Name}] = profile
	}

	mutants, onLine := spans(t, catalog, found)
	mapped := coverage.MapTests(coverage.TestOptions{
		ModulePath: found.ModulePath,
		Mutants:    mutants,
		Profiles:   profiles,
	})

	// Every mutant of a comparison is covered by exactly the test that
	// reaches it, and the unreached comparison's by nothing at all. The
	// comparisons are named by their text and located by discovery, so the
	// SPDX header the module builder prefixes does not enter into it.
	expect := map[string][]coverage.TestKey{
		"v > 0":  {{ImportPath: perTestModule, Name: "TestPositive"}},
		"v < 0":  {{ImportPath: perTestModule, Name: "TestNegative"}},
		"v != 0": nil,
	}
	for original, want := range expect {
		line := lineOf(t, found, original)
		ids := onLine[line]
		if len(ids) == 0 {
			t.Fatalf("no mutant was catalogued on line %d (%q); the fixture proves nothing about it", line, original)
		}
		for _, id := range ids {
			got := mapped.CoveringOf(id)
			if !slices.Equal(got, want) {
				t.Errorf("mutant %s of %q (line %d) is covered by %v, want %v", id[:12], original, line, got, want)
			}
			if want == nil && !slices.Contains(mapped.Uncovered, id) {
				t.Errorf("mutant %s of %q (line %d) is not reported uncovered", id[:12], original, line)
			}
		}
	}
}

// lineOf is the line discovery found the candidate replacing original on.
func lineOf(t *testing.T, found discover.Result, original string) int {
	t.Helper()
	for _, c := range found.Candidates {
		if c.Original == original {
			return c.Line
		}
	}
	t.Fatalf("discovery proposed no edit of %q; the catalogue is\n\t%s", original,
		strings.Join(mutantkit.CatalogLines(mutantkit.Catalog(t, found)), "\n\t"))
	return 0
}

// spans turns the catalogue into the coverage package's spans and indexes
// the mutant ids by the line they start on.
func spans(t *testing.T, catalog *mutation.Catalog, found discover.Result) ([]coverage.Mutant, map[int][]string) {
	t.Helper()
	type key struct {
		path string
		span mutation.Span
		rule string
	}
	lines := make(map[key]int, len(found.Candidates))
	for _, c := range found.Candidates {
		lines[key{c.Path, c.Span, c.Rule.Name}] = c.Line
		t.Logf("candidate: %s:%d %s %q -> %q", c.Path, c.Line, c.Rule.Name, c.Original, c.Replacement)
	}
	var mutants []coverage.Mutant
	onLine := make(map[int][]string)
	for _, m := range catalog.Mutants() {
		line, ok := lines[key{m.Path, m.Span, m.Rule.Name}]
		if !ok {
			t.Fatalf("mutant %s has no located candidate", m.DisplayID)
		}
		mutants = append(mutants, coverage.Mutant{
			ID:        m.ID,
			Path:      m.Path,
			StartLine: line,
			EndLine:   coverage.EndLine(line, m.Original),
		})
		onLine[line] = append(onLine[line], m.ID)
	}
	return mutants, onLine
}
