// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The corpus modules that are about the edges of a workspace rather than about
// the operators: a `go.work`, a build tag, CRLF sources, a package with no
// tests, a suite that writes into its own directory, and a declaration whose
// type cannot be named.
//
// Every one of them was a paragraph in fixtures/README.md promising a fixture
// before it was a fixture, and the promise is the reason they belong together:
// each is a thing the run has to get right that no amount of exercising `simple/`
// or `families/` would ever reach. They are driven from here rather than from
// integration_test.go so that the file a reader opens after `git log
// fixtures/` is the file that says what those modules are for.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
//
// The comment above is deliberately not a package doc — integration_test.go
// carries this package's — which is what the blank line below is for.

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestRunInsideAGoWorkspaceSeesOnlyTheModuleItWasPointedAt is the scope of a
// run stated against a tree that offers it more.
//
// The fixture is a `go.work` over two modules. The run is pointed at `app/`,
// and everything it does has to be about `app/` alone: the snapshot holds that
// module's three files and not the workspace file one directory above it, the
// catalogue holds that module's six mutants, and one test binary is built.
//
// The sibling module's fate is what makes the absence checkable rather than
// merely plausible. All three of `lib`'s mutants survive on purpose — its test
// exercises the function and asserts nothing about the answer — so a run that
// reached across the workspace would still be green and would differ from this
// one in exactly the assertion below: every mutant killed, and six of them.
func TestRunInsideAGoWorkspaceSeesOnlyTheModuleItWasPointedAt(t *testing.T) {
	t.Parallel()

	workspace := testkit.Copy(t, "workspace")
	outcome, events, err := collect(t, t.Context(), optionsAt(t, filepath.Join(workspace, "app")))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	// go.mod, app.go, app_test.go. The workspace file and the whole of `lib`
	// are one directory above the root the run was given, so a snapshot that
	// held four files would be a snapshot of something else.
	if outcome.SnapshotFiles != 3 {
		t.Errorf("snapshotted %d files, want 3 (go.mod, app.go, app_test.go)", outcome.SnapshotFiles)
	}
	want := []string{
		"killed app.go:28 gt-to-ge",
		"killed app.go:28 negate-condition",
		"killed app.go:29 return-zero-numeric",
		"killed app.go:29 sub-to-add",
		"killed app.go:31 add-to-sub",
		"killed app.go:31 return-zero-numeric",
	}
	got := slices.Clone(results(events))
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("results =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	// Said again as a property rather than as a list, because it is the half
	// that would notice `lib` arriving: all three of its mutants are survivors,
	// so a leak across the workspace shows up here as an outcome that is not a
	// kill even if the six lines above somehow still matched.
	for _, m := range outcome.Report.Mutants {
		if m.Outcome != report.OutcomeKilled {
			t.Errorf("mutant %s in %s settled as %s; every mutant of `app` is killed, so a survivor "+
				"is the sibling module having been discovered", m.DisplayID, m.Path, m.Outcome)
		}
	}
	if binaries := outcome.Report.Coverage.Binaries; binaries == nil || *binaries != 1 {
		t.Errorf("coverage.binaries = %v, want the one test binary `app` has", binaries)
	}
}

// TestRunAtTheWorkspaceRootIsRefused is the other direction, and the one that
// used to give the wrong answer.
//
// A workspace has no single module path, no single set of module-relative
// identities and no single baseline, so v1 refuses it. Discovery has always
// said so — GOM4102, with the workspace file named — but discovery runs after
// the copy, the scope resolution and a full baseline, and the scope resolution
// got there first with a different story: `go list ./...` in a workspace
// directory places no package, so the run reported the *user's test command* as
// matching nothing. That is a true sentence about the wrong subject, and it
// sends a reader to their `test.command`.
//
// So the question is asked before anything is copied, and the two halves of
// this assertion are what that buys: the code is the one a user can search for,
// and the path in the message is the `go.work` in their own tree rather than
// one in a temporary directory that no longer exists.
func TestRunAtTheWorkspaceRootIsRefused(t *testing.T) {
	t.Parallel()

	workspace := testkit.Copy(t, "workspace")
	_, _, err := collect(t, t.Context(), optionsAt(t, workspace))
	if err == nil {
		t.Fatal("a run at the root of a go.work workspace completed")
	}
	if code := discover.CodeOf(err); code != discover.CodeWorkspace {
		t.Errorf("code = %q, want %q: %v", code, discover.CodeWorkspace, err)
	}
	for _, phrase := range []string{
		"multi-module workspaces are not yet supported",
		"makes this a workspace",
		"run go-mutants inside one of its modules instead",
		filepath.Join(workspace, discover.WorkspaceFile),
	} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("the refusal does not say %q:\n%v", phrase, err)
		}
	}
	// Nothing was copied and nothing was published, so the engine left the
	// workspace exactly as it found it. (internal/cli files a diagnostics
	// bundle for a failed run, which is its own decision and its own tests'
	// business; this is the engine, and it wrote nothing at all.)
	if _, statErr := os.Stat(filepath.Join(workspace, "reports")); statErr == nil {
		t.Error("the refused run wrote a report directory into the workspace it refused")
	}
}

// TestBuildTagsNarrowTheCatalogueThroughGOFLAGS is the environment as an input
// to the catalogue, and to the key a cached outcome is filed under.
//
// GOFLAGS reaches every `go` command a run issues, so `-tags=special` changes
// which files the module even has — the catalogue is two mutants with the tag
// and one without. That much is a fact about the go command. The half that is
// go-mutants' own is the second one: an outcome measured with the tag must not
// be adopted by a run without it, because the two ran different programs. The
// three runs below are what separates that claim from "the cache was cold" —
// the second run proves the store can produce a hit at all, and only then does
// the third one's zero mean anything.
//
// It does not take t.Parallel(): GOFLAGS is a process-wide global and the
// engine reads the environment it is running in. That is the suite's standing
// rule — no redirection, no serialisation — and this is one of the few tests
// whose subject *is* a variable.
func TestBuildTagsNarrowTheCatalogueThroughGOFLAGS(t *testing.T) {
	root := testkit.Copy(t, "tagged")
	cacheRoot := t.TempDir()

	t.Setenv("GOFLAGS", "-tags=special")
	tagged := runCached(t, cacheOptions(t, root, cacheRoot))
	assertTaggedCatalog(t, tagged, []string{"plain.go true-to-false", "special.go false-to-true"})
	if tagged.Cache.Hits != 0 {
		t.Errorf("the first run had %d cache hits against an empty store", tagged.Cache.Hits)
	}
	if tagged.Cache.Writes == 0 {
		t.Fatal("the first run stored nothing, so a later miss would prove nothing")
	}

	// The same command in the same environment: this is what a hit looks like,
	// and it is the control the assertion below needs.
	warm := runCached(t, cacheOptions(t, root, cacheRoot))
	if warm.Cache.Hits != len(tagged.Mutants) {
		t.Fatalf("a repeat of the tagged run adopted %d of %d outcomes, want all of them",
			warm.Cache.Hits, len(tagged.Mutants))
	}

	// And now the tag goes away. t.Setenv's cleanup restores whatever the
	// developer had, which is what makes unsetting it here safe: the variable
	// has to be *absent* rather than empty, because `GOFLAGS=` and an unset
	// GOFLAGS are different values to the go command and hash differently.
	if err := os.Unsetenv("GOFLAGS"); err != nil {
		t.Fatalf("unsetting GOFLAGS: %v", err)
	}
	plain := runCached(t, cacheOptions(t, root, cacheRoot))
	assertTaggedCatalog(t, plain, []string{"plain.go true-to-false"})
	if plain.Cache.Hits != 0 {
		t.Errorf("the untagged run adopted %d outcome(s) measured with `-tags=special`, "+
			"which were measured against a different program", plain.Cache.Hits)
	}
	if plain.Cache.Misses != len(plain.Mutants) {
		t.Errorf("the untagged run reports %d misses over %d mutants, want a miss for each",
			plain.Cache.Misses, len(plain.Mutants))
	}
}

// assertTaggedCatalog names every mutant of a `tagged/` run by file and rule.
//
// By name rather than by count, because the count alone cannot tell "the tagged
// file was compiled" from "some other file grew a second candidate".
func assertTaggedCatalog(t *testing.T, r *report.Report, want []string) {
	t.Helper()
	got := make([]string, 0, len(r.Mutants))
	for _, m := range r.Mutants {
		if m.Outcome != report.OutcomeKilled {
			t.Errorf("mutant %s (%s %s) settled as %s, want killed", m.DisplayID, m.Path, m.Rule, m.Outcome)
		}
		got = append(got, m.Path+" "+m.Rule)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("catalogue = %q, want %q", got, want)
	}
}

// TestCRLFSourcesAreInstrumentedByteForByteAndKilled runs a whole workspace
// whose every line ends CRLF.
//
// The module is synthesized rather than checked in, and fixtures/README.md says
// why: `.gitattributes` pins `* -text`, so a CRLF fixture would be CRLF on
// every platform and would change every mutant identity that covers it.
//
// Two claims, and they are different claims. The verdicts have to be the LF
// tree's verdicts — a rewriter that lost a carriage return would still compile,
// and would still kill the same mutants, so this alone would not notice — and
// the instrumented tree the run built has to still be CRLF, which is what a
// byte rewriter promises and what the first claim rests on. The identities are
// deliberately not compared: a mutant id is the digest of the file's bytes, and
// the bytes really are different.
func TestCRLFSourcesAreInstrumentedByteForByteAndKilled(t *testing.T) {
	t.Parallel()

	lf, _, err := collect(t, t.Context(), options(t, "simple"))
	if err != nil {
		t.Fatalf("Run over the LF fixture: %v", err)
	}

	crlfRoot := testkit.NewModule(t).From("simple").CRLF().Root()
	opts := optionsAt(t, crlfRoot)
	// The snapshot is kept so that what the run actually built can be read.
	// It lives under the test's own scratch directory, so keeping it costs a
	// directory the harness removes with the rest.
	opts.KeepTemp = KeepTempAlways
	crlf, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run over the CRLF copy: %v", err)
	}
	if crlf.Status != StatusOK {
		t.Fatalf("status = %s, want %s", crlf.Status, StatusOK)
	}

	if got, want := fatesOf(crlf.Report), fatesOf(lf.Report); !slices.Equal(got, want) {
		t.Errorf("the CRLF copy settled\n\t%s\nand the LF fixture settled\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	// The instrumented tree, as it was when the test binaries were built. Both
	// halves are asserted: the guards are there, so this is an instrumented file
	// rather than a pristine one, and every line break in it is still a CRLF.
	for _, name := range []string{"simple.go", "simple_test.go"} {
		source := testkit.ReadFile(t, filepath.Join(crlf.SnapshotRoot, name))
		if name == "simple.go" && !strings.Contains(string(source), "__gm") {
			t.Fatalf("%s in the kept snapshot carries no guard, so this is not the tree the run built",
				name)
		}
		if bare := strings.Count(string(source), "\n") - strings.Count(string(source), "\r\n"); bare != 0 {
			t.Errorf("the instrumented %s holds %d line break(s) that are not CRLF", name, bare)
		}
	}
}

// fatesOf renders every mutant of a report as "outcome path:line rule", sorted,
// so that two runs of the same program in different line endings can be
// compared without comparing their identities.
func fatesOf(r *report.Report) []string {
	out := make([]string, 0, len(r.Mutants))
	for _, m := range r.Mutants {
		out = append(out, fmt.Sprintf("%s %s:%d %s", m.Outcome, m.Path, m.Line, m.Rule))
	}
	slices.Sort(out)
	return out
}

// TestAPackageWithoutTestsReportsItsMutantsAsUncoveredSurvivors is the coverage
// pass answering for a package no binary reaches.
//
// The fixture is a package with tests beside one without. The two mutants in
// the tested package are killed; the two in the orphan are survivors that were
// never started, carry no attempts and no duration, and are marked uncovered —
// which is the difference between "nothing killed this" and "nothing ran this",
// and the only honest thing a report can say about the second.
func TestAPackageWithoutTestsReportsItsMutantsAsUncoveredSurvivors(t *testing.T) {
	t.Parallel()

	outcome, events, err := collect(t, t.Context(), options(t, "untested"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	want := []string{
		"killed lib/lib.go:15 mul-to-div",
		"killed lib/lib.go:15 return-zero-numeric",
		"survived orphan/orphan.go:24 div-to-mul",
		"survived orphan/orphan.go:24 return-zero-numeric",
	}
	got := slices.Clone(results(events))
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("results =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	started := make(map[string]bool, len(outcome.Report.Mutants))
	for _, e := range events {
		if begun, ok := e.(MutantStarted); ok {
			started[begun.ID] = true
		}
	}
	uncovered := 0
	for _, m := range outcome.Report.Mutants {
		if !m.Uncovered {
			continue
		}
		uncovered++
		if !strings.HasPrefix(m.Path, "orphan/") {
			t.Errorf("the uncovered mutant %s is in %s, want it in the package with no test file",
				m.DisplayID, m.Path)
		}
		if m.Attempts != 0 || m.DurationMS != 0 || started[m.ID] {
			t.Errorf("the uncovered mutant %s reports %d attempts in %dms (started=%v), want none of it",
				m.DisplayID, m.Attempts, m.DurationMS, started[m.ID])
		}
	}
	if uncovered != 2 {
		t.Errorf("the report holds %d uncovered mutants, want the orphan package's 2", uncovered)
	}
	if binaries := outcome.Report.Coverage.Binaries; binaries == nil || *binaries != 1 {
		t.Errorf("coverage.binaries = %v, want the one binary `lib` has", binaries)
	}
}

// TestScopingTheTestCommandToTheUntestedPackageIsRefused is the one shape the
// pattern check cannot catch.
//
// `./orphan/...` names a package that really is there, so the resolution before
// the baseline is satisfied — and then not one of the packages it named has a
// test file, so no test binary is built. Nothing downstream would notice: the
// coverage pass skips a run with no binaries, the scheduler walks an empty
// list, every mutant comes back survived, and the run publishes a score of zero
// as though it had looked. It is refused by name instead.
func TestScopingTheTestCommandToTheUntestedPackageIsRefused(t *testing.T) {
	t.Parallel()

	opts := options(t, "untested")
	opts.TestArgv = []string{"go", "test", "./orphan/..."}
	_, _, err := collect(t, t.Context(), opts)
	if err == nil {
		t.Fatal("a test command whose scope holds no test file was accepted")
	}
	if code := CodeOf(err); code != CodeTestScope {
		t.Errorf("code = %q, want %q: %v", code, CodeTestScope, err)
	}
	for _, phrase := range []string{"./orphan/...", "no package with a test file", "survived a suite that was never run"} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("the refusal does not say %q:\n%v", phrase, err)
		}
	}
}

// TestATestThatWritesIntoItsOwnDirectoryStopsTheRunAtTheDriftGate is the gate
// that keeps a score from being a mixture of two programs.
//
// The fixture's suite passes and writes a file into the package directory it
// runs in, which is an entirely ordinary thing for a test to do and fatal here:
// every mutant is measured against the tree the baseline was measured against,
// so the second mutant would be measuring something the first never saw. The
// run stops, and the message names both the file and the cause — the second is
// what turns "1 file changed" into something a reader can act on.
func TestATestThatWritesIntoItsOwnDirectoryStopsTheRunAtTheDriftGate(t *testing.T) {
	t.Parallel()

	_, _, err := collect(t, t.Context(), options(t, "selfwriting"))
	if err == nil {
		t.Fatal("a run whose suite wrote into the snapshot completed")
	}
	if code := CodeOf(err); code != CodeWorkspaceDrift {
		t.Fatalf("code = %q, want %q: %v", code, CodeWorkspaceDrift, err)
	}
	var engineErr *Error
	if !errors.As(err, &engineErr) {
		t.Fatalf("err = %v, want an *engine.Error carrying the drifting paths", err)
	}
	for _, phrase := range []string{
		"changed while the tests ran",
		"the tests write into the package directory they run in",
	} {
		if !strings.Contains(engineErr.Message, phrase) {
			t.Errorf("the refusal does not say %q:\n%s", phrase, engineErr.Message)
		}
	}
	if !strings.Contains(engineErr.Output, "witness.txt") {
		t.Errorf("the refusal does not name the file the suite wrote:\n%s", engineErr.Output)
	}
}

// TestAnUnnameableDeclarationIsSkippedWithItsReasonAndTheRunStaysGreen is a
// refusal that is not a failure.
//
// A guard at the addition inside `hidden.New(a + b)` would have to declare a
// temporary of the type `c` is given, and that type is `*hidden.counter` — not
// exported, so there is no source form of the declaration and discovery does
// not invent one by adding an import. The site is recorded with the reserved
// reason and the pass carries on: the ordinary candidate on the next line is
// catalogued, executed and killed, and the run completes.
//
// The coordinates are asserted through internal/discover rather than through
// the report, and the split is the one the two views were designed around: the
// document carries the aggregate — how much of a file was passed over, and why
// — because forty coordinates per file is a document nobody would read, and the
// listing carries the sites for the person who asked which. This is the one
// fixture whose whole subject is a single site, so both are checked.
func TestAnUnnameableDeclarationIsSkippedWithItsReasonAndTheRunStaysGreen(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "unnameable")
	outcome, events, err := collect(t, t.Context(), optionsAt(t, root))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	wantSkips := []report.Skip{{Path: "unnameable.go", Reason: string(discover.SkipUnnameableDeclType), Count: 1}}
	if !slices.Equal(outcome.Report.Skips, wantSkips) {
		t.Errorf("skips = %+v, want %+v", outcome.Report.Skips, wantSkips)
	}
	if found := discoveredOf(t, events); found.Skips != 1 || found.Candidates != 3 {
		t.Errorf("Discovered = %+v, want 3 candidates and 1 skip", found)
	}
	want := []string{
		"killed hidden/hidden.go:17 return-nil",
		"killed hidden/hidden.go:20 return-zero-numeric",
		"killed unnameable.go:21 return-zero-numeric",
	}
	got := slices.Clone(results(events))
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("results =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	// And the site itself, with the coordinates the report does not carry.
	// The discovery pass is run again over a snapshot of the same tree rather
	// than reaching into the one the run took, which is already gone.
	sites := mutantkit.Discover(t, mutantkit.Toolchain(t), mutantkit.SnapshotOf(t, root)).SkipSites
	wantSites := []discover.SkipSite{{
		Path:   "unnameable.go",
		Reason: discover.SkipUnnameableDeclType,
		Line:   20,
		Column: 20,
	}}
	if !slices.Equal(sites, wantSites) {
		t.Errorf("skip sites = %+v, want %+v", sites, wantSites)
	}
}

// discoveredOf returns the one Discovered event of a run.
func discoveredOf(t *testing.T, events []Event) Discovered {
	t.Helper()
	for _, e := range events {
		if got, ok := e.(Discovered); ok {
			return got
		}
	}
	t.Fatal("the run published no Discovered event")
	return Discovered{}
}
