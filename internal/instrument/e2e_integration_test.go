// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package instrument_test

import (
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const (
	killableModule = "fixture.example/killable"

	boundaryCase = "TestClamp/at_the_high_bound"
)

type kill struct {
	name       string
	path, rule string
	evidence   string
	failing    string
	failures   int
	intact     string
}

func TestVerticalSliceKillsTheCoveredMutantsAndSparesTheUncoveredOne(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "killable")

	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	if found.ModulePath != killableModule {
		t.Fatalf("discovered module path = %q, want %q", found.ModulePath, killableModule)
	}

	catalog := mutantkit.Catalog(t, found)
	wantCatalog := []string{
		"clamp.go negate-condition v < hi -> !(v < hi)",
		"clamp.go condition-to-true v < hi -> true",
		"clamp.go condition-to-false v < hi -> false",
		"clamp.go lt-to-le < -> <=",
		"clamp.go negate-condition v > lo -> !(v > lo)",
		"clamp.go condition-to-true v > lo -> true",
		"clamp.go condition-to-false v > lo -> false",
		"clamp.go gt-to-ge > -> >=",
		"clamp.go return-zero-numeric v -> 0",
		"clamp.go return-zero-numeric lo + 1 -> 0",
		"clamp.go add-to-sub + -> -",
		"clamp.go return-zero-numeric hi - 1 -> 0",
		"clamp.go sub-to-add - -> +",
		"ready.go true-to-false true -> false",
		"untested.go return-true a != b -> true",
		"untested.go return-false a != b -> false",
		"untested.go neq-to-eq != -> ==",
	}
	if got := mutantkit.CatalogLines(catalog); !slices.Equal(got, wantCatalog) {
		t.Fatalf("catalogue =\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(wantCatalog, "\n\t"))
	}

	instrumented, instrumentErr := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
	})
	if instrumentErr != nil {
		t.Fatalf("instrumenting the snapshot: %v", instrumentErr)
	}
	if want := []string{"clamp.go", "ready.go", "untested.go"}; !slices.Equal(instrumented.FilesInstrumented, want) {
		t.Errorf("instrumented %q, want %q", instrumented.FilesInstrumented, want)
	}
	if want := map[string]int{"clamp.go": 5, "ready.go": 1, "untested.go": 1}; !maps.Equal(instrumented.GuardsByFile, want) {
		t.Errorf("guards by file = %v, want %v", instrumented.GuardsByFile, want)
	}
	if want := killableModule + "/gomutants_rt"; instrumented.RuntimeImport != want {
		t.Errorf("runtime import = %q, want %q", instrumented.RuntimeImport, want)
	}

	kills := []kill{
		{
			name:     "the high-bound comparison",
			path:     "clamp.go",
			rule:     "lt-to-le",
			evidence: "Clamp(10, 0, 10) = 10, want 9",
			failing:  boundaryCase,
			failures: 2,
			intact:   "--- PASS: TestIsReady",
		},
		{
			name:     "the low-bound comparison",
			path:     "clamp.go",
			rule:     "gt-to-ge",
			evidence: "Clamp(0, 0, 10) = 0, want 1",
			failing:  "TestClamp/at_the_low_bound",
			failures: 2,
			intact:   "--- PASS: TestIsReady",
		},
		{
			name:     "the boolean literal",
			path:     "ready.go",
			rule:     "true-to-false",
			evidence: "IsReady() = false, want true",
			failing:  "TestIsReady",
			failures: 1,
			intact:   "--- PASS: " + boundaryCase,
		},
	}
	survivor := mutantkit.MutantAt(t, catalog, "untested.go", "neq-to-eq")

	t.Run("the instrumented tree builds", func(t *testing.T) {
		build := mutantkit.RunGo(t, toolchain, snap.Root, env, "build", "./...")
		mutantkit.RequireExit(t, build, 0, "`go build ./...` in the instrumented snapshot")
	})

	t.Run("the instrumented baseline passes", func(t *testing.T) {
		baseline := mutantkit.RunSuite(t, toolchain, snap.Root, env)
		mutantkit.RequireExit(t, baseline, 0, "the instrumented baseline")
		mutantkit.RequireOutput(t, baseline, "the instrumented baseline",
			"--- PASS: "+boundaryCase, "--- PASS: TestIsReady")
	})

	for _, k := range kills {
		t.Run("activating "+k.name+" kills the suite", func(t *testing.T) {
			mutant := mutantkit.MutantAt(t, catalog, k.path, k.rule)
			red := mutantkit.RunSuite(t, toolchain, snap.Root, mutantkit.Activate(env, mutant.ID))
			what := "the suite with " + mutant.DisplayID + " (" + k.rule + " in " + k.path + ") active"
			mutantkit.RequireExit(t, red, 1, what)

			mutantkit.RequireOutput(t, red, what, k.evidence, "--- FAIL: "+k.failing, k.intact)

			if got := strings.Count(string(red.Output), "--- FAIL:"); got != k.failures {
				t.Errorf("%s reported %d failures, want %d (%s and, for a table-driven test, its parent):\n%s",
					what, got, k.failures, k.failing, red.Output)
			}
		})
	}

	t.Run("activating an uncovered mutant leaves the suite green", func(t *testing.T) {
		survived := mutantkit.RunSuite(t, toolchain, snap.Root, mutantkit.Activate(env, survivor.ID))
		what := "the suite with " + survivor.DisplayID + " (" + survivor.Rule.Name + " in untested.go) active"
		mutantkit.RequireExit(t, survived, 0, what)
		mutantkit.RequireOutput(t, survived, what, "--- PASS: "+boundaryCase, "--- PASS: TestIsReady")
	})

	t.Run("an unknown mutant refuses to run the tests", func(t *testing.T) {
		binary := filepath.Join(testkit.Scratch(t), "killable.test")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		compile := mutantkit.RunGo(t, toolchain, snap.Root, env, "test", "-c", "-o", binary, ".")
		mutantkit.RequireExit(t, compile, 0, "compiling the fixture's test binary")

		unknown := strings.Repeat("0", len(survivor.ID))

		refusal := runner.Run(t.Context(), runner.Spec{
			Argv:    []string{binary},
			Dir:     snap.Root,
			Env:     mutantkit.Activate(env, unknown),
			Timeout: mutantkit.StepTimeout,
		})
		what := "the test binary with an unknown mutant active"
		mutantkit.RequireExit(t, refusal, instrument.UnknownMutantExit, what)
		mutantkit.RequireOutput(t, refusal, what, "go-mutants", unknown, "stale")

		for _, forbidden := range []string{"PASS", "=== RUN", "--- "} {
			if strings.Contains(string(refusal.Output), forbidden) {
				t.Errorf("%s got as far as %q, so it ran tests before refusing:\n%s",
					what, forbidden, refusal.Output)
			}
		}
	})

	t.Run("only the instrumented files drifted", func(t *testing.T) {
		drifts, redigestErr := snap.Redigest()
		if redigestErr != nil {
			t.Fatalf("re-digesting the snapshot: %v", redigestErr)
		}
		want := []string{
			"changed clamp.go",
			"added gomutants_rt/gomutants_rt.go",
			"changed ready.go",
			"changed untested.go",
		}
		if got := driftLines(drifts); !slices.Equal(got, want) {
			t.Errorf("the snapshot drifted as\n\t%s\nwant\n\t%s",
				strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
		}
	})
}

func driftLines(drifts []snapshot.Drift) []string {
	out := make([]string, 0, len(drifts))
	for _, drift := range drifts {
		out = append(out, drift.Kind.String()+" "+drift.RelPath)
	}
	return out
}

const refusedModule = "fixture.example/refused"

const refusedSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package refused holds the declarations Form D has to decline, written the way
// a user writes them rather than the way a fixture would.
package refused

import "fmt"

// Widen declares a variable whose type is spelled across three lines.
//
// Turning the declaration into an assignment means cutting that type out in
// place, and the bytes cut hold a line break — so the guard could not keep the
// file's line numbering. There is nothing wrong with the source; it is gofmt's
// own output.
func Widen(n int, mk func(int) func(int) int) int {
	var scale func(
		v int,
	) int = mk(n + 1)
	return scale(n)
}

// Shadow reads, in the initialiser of a declaration, the variable that
// declaration shadows. Go resolves the inner "total * 2" outward, because a
// declared name's scope starts at the end of its own specification.
func Shadow(n int) int {
	total := n + 1
	{
		total := total * 2
		n = total
	}
	return n
}

// Wrap is the same shape in the form Go programs are full of: an error wrapped
// into a new one of the same name. It is the dangerous case, because hoisting
// the declaration produces a program that still compiles and still passes — it
// simply wraps a nil error instead of the one that was there.
func Wrap(n int, err error) error {
	if err != nil {
		err := fmt.Errorf("step %d: %w", n+1, err)
		return err
	}
	return nil
}

// Kept is the positive side of Widen's refusal, and the only place a spec with
// no initialiser is rewritten rather than declined. It is cut out whole, which
// is legal exactly because it fits on one line; the guard declares the name it
// took away, and the spec beside it becomes the assignment.
func Kept(n int) int {
	var (
		total int
		start = n + 1
	)
	total = start * 2
	return total
}
`

const refusedTest = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package refused

import (
	"errors"
	"fmt"
	"testing"
)

func double(k int) func(int) int { return func(v int) int { return v * k } }

func TestWiden(t *testing.T) {
	if got := Widen(3, double); got != 12 {
		t.Errorf("Widen(3, double) = %d, want 12", got)
	}
}

func TestShadow(t *testing.T) {
	if got := Shadow(2); got != 6 {
		t.Errorf("Shadow(2) = %d, want 6", got)
	}
}

func TestWrap(t *testing.T) {
	inner := errors.New("boom")
	got := Wrap(1, inner)
	if !errors.Is(got, inner) {
		t.Errorf("Wrap(1, inner) = %v, want an error wrapping %v", got, inner)
	}
	if want := fmt.Sprintf("step 2: %v", inner); got.Error() != want {
		t.Errorf("Wrap(1, inner) = %q, want %q", got, want)
	}
	if Wrap(1, nil) != nil {
		t.Error("Wrap(1, nil) is not nil")
	}
}

func TestKept(t *testing.T) {
	if got := Kept(3); got != 8 {
		t.Errorf("Kept(3) = %d, want 8", got)
	}
}
`

func TestDeclarationsNoFormCanRewriteAreSkippedRatherThanFatal(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))

	source := testkit.NewModule(t).Module(refusedModule).
		Source("refused.go", refusedSource).
		Source("refused_test.go", refusedTest).
		Root()
	snap := mutantkit.SnapshotOf(t, source)
	found := mutantkit.DiscoverWith(t, toolchain, snap, env)

	if got := len(found.Skips); got != 0 {
		lines := make([]string, 0, len(found.Skips))
		for _, skip := range found.Skips {
			lines = append(lines, skip.Path+" "+string(skip.Reason)+" "+fmt.Sprint(skip.Count))
		}
		t.Errorf("skips =\n\t%s\nwant none", strings.Join(lines, "\n\t"))
	}

	forms := map[discover.GuardForm]int{}
	for _, candidate := range found.Candidates {
		forms[candidate.Guard.Form]++
	}
	if got, want := forms[discover.GuardFormD], 2; got != want {
		t.Errorf("%d candidates use Form D, want %d: the declarations this file is about "+
			"are the ones it must not rewrite", got, want)
	}
	if got, want := forms[discover.GuardFormE], 3; got != want {
		t.Errorf("%d candidates use Form E, want %d: one per declaration Form D declines",
			got, want)
	}

	catalog := mutantkit.Catalog(t, found)
	wantCatalog := []string{
		"refused.go add-to-sub + -> -",
		"refused.go return-zero-numeric scale(n) -> 0",
		"refused.go add-to-sub + -> -",
		"refused.go mul-to-div * -> /",
		"refused.go delete-assignment n = total -> ",
		"refused.go return-zero-numeric n -> 0",
		"refused.go negate-condition err != nil -> !(err != nil)",
		"refused.go nil-error-branch err != nil -> false",
		"refused.go condition-to-true err != nil -> true",
		"refused.go neq-to-eq != -> ==",
		"refused.go add-to-sub + -> -",
		"refused.go return-err-to-nil err -> nil",
		"refused.go add-to-sub + -> -",
		"refused.go delete-assignment total = start * 2 -> ",
		"refused.go mul-to-div * -> /",
		"refused.go return-zero-numeric total -> 0",
	}
	if lines := mutantkit.CatalogLines(catalog); !slices.Equal(lines, wantCatalog) {
		t.Fatalf("catalogue =\n\t%s\nwant\n\t%s",
			strings.Join(lines, "\n\t"), strings.Join(wantCatalog, "\n\t"))
	}

	if _, instrumentErr := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
	}); instrumentErr != nil {
		t.Fatalf("instrumenting the snapshot: %v", instrumentErr)
	}

	build := mutantkit.RunGo(t, toolchain, snap.Root, env, "build", "./...")
	mutantkit.RequireExit(t, build, 0, "`go build ./...` in the instrumented snapshot")

	baseline := mutantkit.RunSuite(t, toolchain, snap.Root, env)
	mutantkit.RequireExit(t, baseline, 0, "the instrumented baseline")
	mutantkit.RequireOutput(t, baseline, "the instrumented baseline",
		"--- PASS: TestWiden", "--- PASS: TestShadow", "--- PASS: TestWrap", "--- PASS: TestKept")
}
