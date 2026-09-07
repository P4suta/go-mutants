// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The v0 vertical slice: the whole schemata mechanism end to end, against a
// real toolchain and a real test suite.
//
// Every other test in this package proves one layer in isolation — the
// flattener against its goldens, the splicer against its invariants, the guard
// forms against a compiler. None of them can answer the question this file
// exists for, which is the only question that matters about a mutation tester:
// does activating one mutant in an instrumented tree turn a passing suite red,
// while activating another leaves it green? Answering it needs a snapshot, a
// discovery pass, a catalogue, an instrumentation pass, a build, and four child
// processes, so it lives behind the `integration` tag rather than in the suite
// that has to stay fast.
//
// The steps run in order and share one instrumented snapshot, because the
// snapshot is what they are all statements about. Each one asserts an exit code
// and quotes the child's output when it disagrees: a mutation tester that
// misreads an exit status reports a fiction, and the only defence is that every
// status this pipeline depends on is pinned somewhere.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/instrument/...
package instrument_test

import (
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const (
	// killableModule is the module path of the fixture this file drives.
	killableModule = "fixture.example/killable"

	// boundaryCase names the fixture's high-bound row. It is the input at which
	// `v < hi` and `v <= hi` disagree, so it is both the case that dies when
	// that mutant is live and the case whose passing says no mutant is.
	boundaryCase = "TestClamp/at_the_high_bound"
)

// A kill is one mutant the fixture's tests are supposed to detect, and the
// evidence that it was that mutant which did it.
//
// Naming the evidence is the point. A red suite alone proves nothing: a tree
// that stopped compiling exits 1 and prints FAIL, and so does an unrelated
// broken test. Every mutant here diverges from the original at exactly one
// input, so the message that input produces is a signature no other cause can
// forge — and the test that has to keep passing alongside it is what separates
// a detection from a tree that broke everywhere at once.
type kill struct {
	// name says which edit this is, for the subtest's own name.
	name string
	// path and rule name the mutant. Together they select exactly one, which is
	// what the fixture's one-function-per-file layout is for.
	path, rule string
	// evidence is the assertion message only this mutant can produce.
	evidence string
	// failing is the test the evidence comes from, as `go test -v` names it.
	failing string
	// failures is how many "--- FAIL:" lines the suite should print: two for a
	// table-driven test, where the parent fails with its case, and one for a
	// test that has no subtests.
	failures int
	// intact is a test that has to keep passing while this mutant is live.
	intact string
}

// TestVerticalSliceKillsTheCoveredMutantsAndSparesTheUncoveredOne runs the
// whole pipeline over the killable fixture and watches its mutants meet the
// fates the fixture was built to give them.
//
// The kills and the survival are halves of one claim and neither is worth much
// alone. A suite that goes red when a mutant is activated could be a tree that
// stopped compiling; a suite that stays green could be activation that never
// happened. Together — same tree, same command, same environment, one changed
// variable — they say the mechanism dispatches, and the steps in between say it
// does so without disturbing anything else.
func TestVerticalSliceKillsTheCoveredMutantsAndSparesTheUncoveredOne(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "killable")

	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	if found.ModulePath != killableModule {
		t.Fatalf("discovered module path = %q, want %q", found.ModulePath, killableModule)
	}

	// The catalogue, pinned whole. Thirteen mutants is still small enough to
	// write down, and writing it down is what earns the lookups below: one
	// mutant per (path, rule) is a property of how the fixture is laid out —
	// one function per file and no repeated operator — not a coincidence, and
	// if it ever stops holding this line says so before a step silently
	// activates the wrong one.
	catalog := mutantkit.Catalog(t, found)
	wantCatalog := []string{
		"clamp.go negate-condition v < hi -> !(v < hi)",
		"clamp.go lt-to-le < -> <=",
		"clamp.go negate-condition v > lo -> !(v > lo)",
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

	// The hints discovery just computed, carried across the phase boundary:
	// which rewrite form each edit takes is a question about types, and this is
	// the only pass that had a type checker.
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
	// Nine mutants in clamp.go are five guards, not nine: a guard is a rewrite
	// site, and the mutants of one expression or one statement share it. Both
	// halves matter — the two conditions are two guards because they are two
	// expressions, and `return lo + 1` is one guard for both the addition and
	// the whole returned value — which is the distinction this line is here to
	// keep visible.
	if want := map[string]int{"clamp.go": 5, "ready.go": 1, "untested.go": 1}; !maps.Equal(instrumented.GuardsByFile, want) {
		t.Errorf("guards by file = %v, want %v", instrumented.GuardsByFile, want)
	}
	if want := killableModule + "/gomutants_rt"; instrumented.RuntimeImport != want {
		t.Errorf("runtime import = %q, want %q", instrumented.RuntimeImport, want)
	}

	// The fixture's own claim about itself, made machine-checkable: these three
	// die and the fourth lives. Each is named by its file and its rule rather
	// than by its identity — an identity is a digest over the fixture's bytes,
	// so a hard-coded one would turn every edit to a comment in the fixture into
	// a failure here.
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
		// Semantic preservation, stated as the user would meet it. With nothing
		// in the environment every guard takes the branch holding the original
		// bytes, so the suite has to pass exactly as it does in the fixture —
		// and it has to actually run: `go test` over a tree with no tests left
		// in it also exits 0, which is why the passing subtests are named.
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

			// A red suite is not yet evidence, and neither is a FAIL line. The
			// mutant's own signature is the wrong answer it produces at the one
			// input where it differs from the original, so that is what gets
			// asserted — together with the test that has to still be passing
			// beside it.
			mutantkit.RequireOutput(t, red, what, k.evidence, "--- FAIL: "+k.failing, k.intact)

			// And nothing else went red. A mutant that broke the whole suite
			// would be a broken tree wearing a detection's clothes.
			if got := strings.Count(string(red.Output), "--- FAIL:"); got != k.failures {
				t.Errorf("%s reported %d failures, want %d (%s and, for a table-driven test, its parent):\n%s",
					what, got, k.failures, k.failing, red.Output)
			}
		})
	}

	t.Run("activating an uncovered mutant leaves the suite green", func(t *testing.T) {
		// Same tree, same command, same environment, one different ID. That is
		// the whole difference from the steps above, and it is deliberate: exit
		// 0 only means "survived" because those steps proved this exact plumbing
		// kills. Written as a separate, independently spelled invocation, a typo
		// in the variable name would produce this same green.
		//
		// Exit 0 also proves this mutant is really in the tree and was really
		// activated: an ID the generated runtime does not know exits 97 from
		// init, which `go test` would report as a failed package. A survivor is
		// a mutant that ran and changed nothing, not one that was never live.
		survived := mutantkit.RunSuite(t, toolchain, snap.Root, mutantkit.Activate(env, survivor.ID))
		what := "the suite with " + survivor.DisplayID + " (" + survivor.Rule.Name + " in untested.go) active"
		mutantkit.RequireExit(t, survived, 0, what)
		mutantkit.RequireOutput(t, survived, what, "--- PASS: "+boundaryCase, "--- PASS: TestIsReady")
	})

	t.Run("an unknown mutant refuses to run the tests", func(t *testing.T) {
		binary := filepath.Join(t.TempDir(), "killable.test")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		// Compiled to a temporary directory outside the snapshot. A test binary
		// written into the tree would be drift in the last step, and would be
		// indistinguishable there from a test that wrote into its own package.
		compile := mutantkit.RunGo(t, toolchain, snap.Root, env, "test", "-c", "-o", binary, ".")
		mutantkit.RequireExit(t, compile, 0, "compiling the fixture's test binary")

		// An identity of the right shape — 64 hex characters — and a value no
		// digest produces.
		unknown := strings.Repeat("0", len(survivor.ID))

		// Straight to the binary rather than through `go test`, which reports a
		// child's refusal as its own exit 1 and loses the status the runner has
		// to recognise as an infrastructure error.
		refusal := runner.Run(t.Context(), runner.Spec{
			Argv:    []string{binary},
			Dir:     snap.Root,
			Env:     mutantkit.Activate(env, unknown),
			Timeout: mutantkit.StepTimeout,
		})
		what := "the test binary with an unknown mutant active"
		mutantkit.RequireExit(t, refusal, instrument.UnknownMutantExit, what)
		mutantkit.RequireOutput(t, refusal, what, "go-mutants", unknown, "stale")

		// "Quickly" means the process refused before running anything, which is
		// the property that matters and the only one that does not become a
		// flake on a loaded machine: the exit happens in the generated package's
		// init, before the testing framework has started.
		for _, forbidden := range []string{"PASS", "=== RUN", "--- "} {
			if strings.Contains(string(refusal.Output), forbidden) {
				t.Errorf("%s got as far as %q, so it ran tests before refusing:\n%s",
					what, forbidden, refusal.Output)
			}
		}
	})

	t.Run("only the instrumented files drifted", func(t *testing.T) {
		// Last, so that it covers the builds and every suite run as well as the
		// rewrite. This is the gate that catches a test writing into the
		// tree every later mutant is measured against; here it also pins the
		// rewrite itself, so the exact set is written out rather than filtered
		// down to "the ones we did not expect".
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

// driftLines renders what Redigest found, in the path order it returns.
func driftLines(drifts []snapshot.Drift) []string {
	out := make([]string, 0, len(drifts))
	for _, drift := range drifts {
		out = append(out, drift.Kind.String()+" "+drift.RelPath)
	}
	return out
}

// refusedModule is the module path of the throwaway module the test below
// writes, discovers, and instruments.
const refusedModule = "fixture.example/refused"

// refusedSource is ordinary, gofmt-clean Go holding the three declaration
// shapes v1's guard forms cannot rewrite, each beside code in the same function
// that they must not take down with them.
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

// refusedTest pins what the package computes, so that the instrumented baseline
// is checked against the pristine program's answers rather than against itself.
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

// TestDeclarationsNoFormCanRewriteAreSkippedRatherThanFatal drives the whole
// pipeline over a module whose declarations Form D has to refuse.
//
// Both halves of the refusal are asserted, and each was a real defect. A `var`
// whose declared type is spelled across lines used to reach the instrumenter as
// a hint it could only answer with a line-drift error — a hard failure out of
// Instrument, so the user's whole run ended with an internal error and no
// results, over source gofmt had just produced. And a declaration whose
// initialiser reads the name it declares used to be hoisted in front of that
// initialiser, which rebinds the reference to a freshly zeroed variable: the
// tree still compiles, the instrumented baseline still passes, and every mutant
// measured in the rewritten function is measured against a program the user did
// not write. That one is why the baseline here is checked against a test suite
// that pins the pristine answers, rather than against exit 0 alone.
//
// What the refusals must not do is take anything else with them. The catalogue
// is pinned whole: the mutants outside those three statements are all still
// there, in a file three of whose statements produced none. The last function
// is the other side of the same line — a `var` block Form D does rewrite, whose
// initialiser-less spec is cut out whole because it fits on one line, which is
// the one place in the suite that cut is exercised end to end.
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

	// Every edit inside one of the three declarations is a recorded skip, and
	// the reason is the one the frozen contract reserves for a site no guard
	// form can express.
	want := []string{"refused.go unnameable-decl-type 3"}
	got := make([]string, 0, len(found.Skips))
	for _, skip := range found.Skips {
		got = append(got, skip.Path+" "+string(skip.Reason)+" "+fmt.Sprint(skip.Count))
	}
	if !slices.Equal(got, want) {
		t.Errorf("skips =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	catalog := mutantkit.Catalog(t, found)
	wantCatalog := []string{
		"refused.go return-zero-numeric scale(n) -> 0",
		"refused.go add-to-sub + -> -",
		"refused.go delete-assignment n = total -> ",
		"refused.go return-zero-numeric n -> 0",
		"refused.go negate-condition err != nil -> !(err != nil)",
		"refused.go nil-error-branch err != nil -> false",
		"refused.go neq-to-eq != -> ==",
		"refused.go return-err-to-nil err -> nil",
		// Kept, whose `var` block holds the one spec-with-no-initialiser this
		// suite rewrites rather than refuses.
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
