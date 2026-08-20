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
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

const (
	// killableModule is the module path of the fixture this file drives.
	killableModule = "fixture.example/killable"

	// stepTimeout bounds every child process. Each step is a build or a run of a
	// suite that takes well under a second once warm, so a minute is not a
	// budget — it is the point past which something has hung rather than been
	// slow, and a hung child is what the supervisor exists to end.
	stepTimeout = 60 * time.Second

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
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "killable")

	found, discoverErr := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
	})
	if discoverErr != nil {
		t.Fatalf("discovering the killable fixture: %v", discoverErr)
	}
	if found.ModulePath != killableModule {
		t.Fatalf("discovered module path = %q, want %q", found.ModulePath, killableModule)
	}

	// The catalogue, pinned whole. Four mutants is small enough to write down,
	// and writing it down is what earns the lookups below: one mutant per
	// (path, rule) is a property of how the fixture is laid out, not a
	// coincidence, and if it ever stops holding this line says so before a step
	// silently activates the wrong one.
	catalog := catalogFrom(t, found)
	wantCatalog := []string{
		"clamp.go lt-to-le < -> <=",
		"clamp.go gt-to-ge > -> >=",
		"ready.go true-to-false true -> false",
		"untested.go neq-to-eq != -> ==",
	}
	if got := catalogLines(catalog); !slices.Equal(got, wantCatalog) {
		t.Fatalf("catalogue =\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(wantCatalog, "\n\t"))
	}

	instrumented, instrumentErr := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
	})
	if instrumentErr != nil {
		t.Fatalf("instrumenting the snapshot: %v", instrumentErr)
	}
	if want := []string{"clamp.go", "ready.go", "untested.go"}; !slices.Equal(instrumented.FilesInstrumented, want) {
		t.Errorf("instrumented %q, want %q", instrumented.FilesInstrumented, want)
	}
	// Two mutants in clamp.go are two guards, not one: they sit on different
	// expressions. Mutants of a single expression would share a guard, which is
	// the distinction this line is here to keep visible.
	if want := map[string]int{"clamp.go": 2, "ready.go": 1, "untested.go": 1}; !maps.Equal(instrumented.GuardsByFile, want) {
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
	survivor := mutantAt(t, catalog, "untested.go", "neq-to-eq")

	t.Run("the instrumented tree builds", func(t *testing.T) {
		build := goInSnapshot(t, toolchain, snap.Root, "", "build", "./...")
		requireExit(t, build, 0, "`go build ./...` in the instrumented snapshot")
	})

	t.Run("the instrumented baseline passes", func(t *testing.T) {
		// Semantic preservation, stated as the user would meet it. With nothing
		// in the environment every guard takes the branch holding the original
		// bytes, so the suite has to pass exactly as it does in the fixture —
		// and it has to actually run: `go test` over a tree with no tests left
		// in it also exits 0, which is why the passing subtests are named.
		baseline := runSuite(t, toolchain, snap.Root, "")
		requireExit(t, baseline, 0, "the instrumented baseline")
		requireOutput(t, baseline, "the instrumented baseline",
			"--- PASS: "+boundaryCase, "--- PASS: TestIsReady")
	})

	for _, k := range kills {
		t.Run("activating "+k.name+" kills the suite", func(t *testing.T) {
			mutant := mutantAt(t, catalog, k.path, k.rule)
			red := runSuite(t, toolchain, snap.Root, mutant.ID)
			what := "the suite with " + mutant.DisplayID + " (" + k.rule + " in " + k.path + ") active"
			requireExit(t, red, 1, what)

			// A red suite is not yet evidence, and neither is a FAIL line. The
			// mutant's own signature is the wrong answer it produces at the one
			// input where it differs from the original, so that is what gets
			// asserted — together with the test that has to still be passing
			// beside it.
			requireOutput(t, red, what, k.evidence, "--- FAIL: "+k.failing, k.intact)

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
		survived := runSuite(t, toolchain, snap.Root, survivor.ID)
		what := "the suite with " + survivor.DisplayID + " (" + survivor.Rule.Name + " in untested.go) active"
		requireExit(t, survived, 0, what)
		requireOutput(t, survived, what, "--- PASS: "+boundaryCase, "--- PASS: TestIsReady")
	})

	t.Run("an unknown mutant refuses to run the tests", func(t *testing.T) {
		binary := filepath.Join(t.TempDir(), "killable.test")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		// Compiled to a temporary directory outside the snapshot. A test binary
		// written into the tree would be drift in the last step, and would be
		// indistinguishable there from a test that wrote into its own package.
		compile := goInSnapshot(t, toolchain, snap.Root, "", "test", "-c", "-o", binary, ".")
		requireExit(t, compile, 0, "compiling the fixture's test binary")

		// An identity of the right shape — 64 hex characters — and a value no
		// digest produces.
		unknown := strings.Repeat("0", len(survivor.ID))

		// Straight to the binary rather than through `go test`, which reports a
		// child's refusal as its own exit 1 and loses the status the runner has
		// to recognise as an infrastructure error.
		refusal := runner.Run(t.Context(), runner.Spec{
			Argv:    []string{binary},
			Dir:     snap.Root,
			Env:     fixtureEnv(unknown),
			Timeout: stepTimeout,
		})
		what := "the test binary with an unknown mutant active"
		requireExit(t, refusal, instrument.UnknownMutantExit, what)
		requireOutput(t, refusal, what, "go-mutants", unknown, "stale")

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

// snapshotFixture copies a corpus module into a disposable directory and
// registers its removal.
//
// The cleanup is registered the moment the snapshot exists, before the caller
// can do anything that fails: every step after this one is entitled to call
// t.Fatalf, and a snapshot that outlives the test is a copy of a tree left in
// the temporary directory with nobody to remove it.
func snapshotFixture(t *testing.T, name string) *snapshot.Snapshot {
	t.Helper()
	root, absErr := filepath.Abs(filepath.Join("..", "..", "fixtures", name))
	if absErr != nil {
		t.Fatalf("resolving the %s fixture: %v", name, absErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Fatalf("fixture %s is not a module: %v", name, statErr)
	}
	// DestParent keeps the copy inside the test's own temporary directory, so a
	// failure that skips the cleanup still leaves nothing behind for long.
	snap, createErr := snapshot.Create(root, snapshot.Options{DestParent: t.TempDir()})
	if createErr != nil {
		t.Fatalf("snapshotting the %s fixture: %v", name, createErr)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("cleaning up the snapshot at %s: %v", snap.Root, cleanupErr)
		}
	})
	return snap
}

// catalogFrom turns a discovery result into the catalogue everything after it
// is indexed by: the builder validates and identifies each candidate, and Build
// settles the canonical order the generated runtime's dense indices come from.
func catalogFrom(t *testing.T, found discover.Result) *mutation.Catalog {
	t.Helper()
	builder := mutation.NewBuilder()
	for _, located := range found.Candidates {
		if err := builder.Add(located.Candidate); err != nil {
			t.Fatalf("adding the candidate at %s:%d:%d: %v", located.Path, located.Line, located.Column, err)
		}
	}
	catalog, buildErr := builder.Build()
	if buildErr != nil {
		t.Fatalf("building the catalogue: %v", buildErr)
	}
	return catalog
}

// catalogLines renders the catalogue in its own canonical order, one mutant per
// line, in the terms this test is about: where it is, which rule proposed it,
// and what it rewrites.
//
// Identities are left out deliberately. They are digests over the fixture's
// bytes, so pinning them here would make every edit to a comment in the fixture
// a failure in this file, while saying nothing this rendering does not.
func catalogLines(catalog *mutation.Catalog) []string {
	out := make([]string, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		out = append(out, fmt.Sprintf("%s %s %s -> %s", m.Path, m.Rule.Name, m.Original, m.Replacement))
	}
	return out
}

// mutantAt returns the one catalogued mutant of a rule in a file.
//
// Uniqueness is asserted rather than assumed, and the assertion is what turns
// the fixture's layout into a contract: one function per file and no repeated
// operator means a rule in a file names exactly one mutant, so a test can say
// which mutant it means without knowing an identity or a catalogue position. A
// second match would mean the fixture drifted and the steps below had been
// activating whichever one happened to come first.
func mutantAt(t *testing.T, catalog *mutation.Catalog, path, rule string) mutation.Mutant {
	t.Helper()
	var found []mutation.Mutant
	for _, m := range catalog.Mutants() {
		if m.Path == path && m.Rule.Name == rule {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the catalogue holds %d mutants of %s in %s, want exactly 1", len(found), rule, path)
	}
	return found[0]
}

// fixtureEnv builds the environment every child in this test receives, with one
// mutant activated when active is not empty.
//
// It is composed rather than inherited, for the reason internal/engine composes
// its own: a developer with GO_MUTANTS_ACTIVE exported in their shell would
// otherwise have the instrumented baseline running a mutant, and the step that
// proves semantic preservation would be proving nothing at all. The three go
// settings are pinned for the neighbouring reason — a fixture with no
// dependencies must never reach the network to build, a `go.work` above the
// temporary directory must not join itself to the snapshot, and a GOFLAGS from
// the developer's shell must not decide what any of this resolves against.
func fixtureEnv(active string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+4)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GO_MUTANTS_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GOWORK=off", "GOFLAGS=-mod=readonly", "GOPROXY=off")
	if active != "" {
		env = append(env, instrument.ActiveEnv+"="+active)
	}
	return env
}

// goInSnapshot runs one go command inside the snapshot, supervised by the same
// package that will supervise thousands of them in a real run.
func goInSnapshot(t *testing.T, toolchain gocmd.Toolchain, dir, active string, args ...string) runner.Result {
	t.Helper()
	spec := toolchain.Command(args...)
	spec.Dir = dir
	spec.Env = fixtureEnv(active)
	spec.Timeout = stepTimeout
	return runner.Run(t.Context(), spec)
}

// runSuite runs the fixture's whole test suite in the instrumented snapshot,
// with one mutant activated or with none.
//
// The baseline, the kill, and the survival all go through this one function on
// purpose. Written as three invocations they could differ in the single detail
// that decides what they mean — a flag, a field, the spelling of the variable —
// and then "the suite passed" would be evidence of a typo rather than of a
// survivor.
//
// -count=1 defeats the go test result cache. The cache keys on the environment
// a test binary reads, so it would very probably do the right thing here;
// "very probably" is not a foundation for the one test that exists to prove
// activation works. -v is what lets a step name the subtest that passed or
// failed, rather than inferring it from an exit code.
func runSuite(t *testing.T, toolchain gocmd.Toolchain, root, active string) runner.Result {
	t.Helper()
	return goInSnapshot(t, toolchain, root, active, "test", "-count=1", "-v", "./...")
}

// requireExit ends the step unless the child ran to completion with the status
// the step expects, and quotes the child's output whenever it did not.
//
// The three cases are kept apart because they mean different things. Err is
// go-mutants failing to run a process at all and is never a statement about the
// tests; a timeout leaves no exit code to compare; only the third is the child
// having answered.
func requireExit(t *testing.T, result runner.Result, want int, what string) {
	t.Helper()
	switch {
	case result.Err != nil:
		t.Fatalf("%s could not be run: %v\n%s", what, result.Err, result.Output)
	case result.TimedOut:
		t.Fatalf("%s did not finish within %s:\n%s", what, stepTimeout, result.Output)
	case result.ExitCode != want:
		t.Fatalf("%s exited %d, want %d:\n%s", what, result.ExitCode, want, result.Output)
	}
}

// requireOutput fails the step for each needle the child did not print, quoting
// the whole output once per miss so a failure is readable without re-running.
func requireOutput(t *testing.T, result runner.Result, what string, needles ...string) {
	t.Helper()
	out := string(result.Output)
	for _, needle := range needles {
		if !strings.Contains(out, needle) {
			t.Errorf("%s did not print %q:\n%s", what, needle, out)
		}
	}
}

// driftLines renders what Redigest found, in the path order it returns.
func driftLines(drifts []snapshot.Drift) []string {
	out := make([]string, 0, len(drifts))
	for _, drift := range drifts {
		out = append(out, drift.Kind.String()+" "+drift.RelPath)
	}
	return out
}
