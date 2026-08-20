// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Compile validation against a real toolchain, over a module built to need it.
//
// The unit tests in this package prove the search and the parser against fakes,
// which is the only way to cover them exhaustively — a table of "does this
// subset compile" answers costs microseconds where a real build costs seconds.
// What a fake cannot say is whether the real compiler agrees: whether a guarded
// comparison really does fail against a named boolean type, whether the message
// it prints really names the file the way this package normalizes paths,
// whether the line it reports really is the line the catalogue recorded. That
// is what this file is for, and it is why the fixture it drives is a module
// whose traps are ordinary-looking Go.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/validate/...
package validate_test

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/validate"
)

const (
	// rejectableModule is the module path of the fixture this file drives.
	rejectableModule = "fixture.example/rejectable"

	// stepTimeout bounds every child process. Each step is a build or a run of
	// a suite that takes well under a second once warm, so a minute is not a
	// budget — it is the point past which something has hung rather than been
	// slow.
	stepTimeout = 60 * time.Second
)

// wantCatalog is the fixture's whole catalogue, in catalogue order.
//
// It is written out rather than derived because every assertion below names a
// mutant by its position in it. Nine candidates is small enough to read, and
// pinning it means a change to the fixture that adds or moves a candidate fails
// here — where the answer is "update the fixture's expectations" — instead of
// silently shifting which mutant a later assertion is about.
var wantCatalog = []string{
	"compare.go lt-to-le < -> <=",
	"compare.go false-to-true false -> true",
	"compare.go gt-to-ge > -> >=",
	"compare.go false-to-true false -> true",
	"compare.go true-to-false true -> false",
	"compare.go eq-to-neq == -> !=",
	"flag.go gt-to-ge > -> >=",
	"flag.go true-to-false true -> false",
	"flag.go neq-to-eq != -> ==",
}

// trapped names the catalogue positions that cannot compile: the two shapes of
// the [Flag] trap in flag.go and the one in compare.go. Everything else must
// survive.
var trapped = []int{5, 6, 7}

// TestValidateIsolatesTheTrappedCandidates runs discovery, instrumentation and
// validation over the fixture and watches the three candidates that cannot
// compile come out as rejections while the six that can stay in the tree.
//
// Both halves are the claim, and neither is worth anything alone. Rejecting
// everything would produce a green build too — an empty tree compiles — and
// accepting everything would produce a tree that does not build at all. What
// makes this a statement about isolation is that the two files each lose
// exactly the candidates the compiler refused and keep the rest, in the same
// pass, with the accepted ones still activatable afterwards.
func TestValidateIsolatesTheTrappedCandidates(t *testing.T) {
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "rejectable")
	found, catalog := catalogFixture(t, toolchain, snap)

	result, err := validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		ModulePath:   rejectableModule,
		Toolchain:    toolchain,
		Jobs:         2,
		BuildTimeout: stepTimeout,
		Env:          fixtureEnv(""),
	})
	if err != nil {
		t.Fatalf("validating the rejectable fixture: %v", err)
	}

	mutants := catalog.Mutants()
	t.Run("the trapped candidates are rejected and the healthy ones accepted", func(t *testing.T) {
		var wantRejected, wantAccepted []string
		for i, m := range mutants {
			if slices.Contains(trapped, i) {
				wantRejected = append(wantRejected, m.ID)
				continue
			}
			wantAccepted = append(wantAccepted, m.ID)
		}
		var gotRejected []string
		for _, r := range result.Rejected {
			gotRejected = append(gotRejected, r.ID)
		}
		if !slices.Equal(gotRejected, wantRejected) {
			t.Errorf("rejected\n\t%s\nwant\n\t%s",
				strings.Join(describe(catalog, gotRejected), "\n\t"),
				strings.Join(describe(catalog, wantRejected), "\n\t"))
		}
		if !slices.Equal(result.AcceptedIDs, wantAccepted) {
			t.Errorf("accepted\n\t%s\nwant\n\t%s",
				strings.Join(describe(catalog, result.AcceptedIDs), "\n\t"),
				strings.Join(describe(catalog, wantAccepted), "\n\t"))
		}
	})

	t.Run("a rejection is at the coordinates discovery reported", func(t *testing.T) {
		// A rejected mutant and a live one have to be named the same way, and
		// nothing downstream would catch it if they were not: `list` prints
		// discovery's coordinates and the report prints these, from the same
		// catalogue, and a user comparing the two would be looking at one
		// mutant described twice in two places. Validation derives the pair
		// from the pristine bytes rather than from a token.FileSet, so the two
		// derivations agreeing is a real claim rather than a tautology.
		located := make(map[string]discover.Located, len(found.Candidates))
		for _, l := range found.Candidates {
			id, idErr := l.Candidate.ID()
			if idErr != nil {
				t.Fatalf("identifying the candidate at %s:%d:%d: %v", l.Path, l.Line, l.Column, idErr)
			}
			located[id] = l
		}
		for _, r := range result.Rejected {
			want, ok := located[r.ID]
			if !ok {
				t.Errorf("rejection %s names a mutant discovery never found", r.DisplayID)
				continue
			}
			if r.Line != want.Line || r.Column != want.Column {
				t.Errorf("%s is rejected at %s:%d:%d, discovery found it at %d:%d",
					r.DisplayID, r.Path, r.Line, r.Column, want.Line, want.Column)
			}
		}
	})

	t.Run("every rejection carries the compiler's own words", func(t *testing.T) {
		// The diagnostic is the whole reason a rejection is reported rather
		// than dropped, and by the time this phase returns the message no
		// longer exists anywhere: the tree compiles. Each one has to name the
		// file it is about and the line the catalogue recorded for the
		// candidate, which is also the end-to-end statement of line
		// preservation — the compiler and the catalogue agreeing about where
		// something is, with a guard spliced in between them.
		for _, r := range result.Rejected {
			what := r.DisplayID + " (" + r.Rule + " in " + r.Path + ")"
			if r.Line <= 0 || r.Column <= 0 {
				t.Errorf("%s is reported at %d:%d, want a real coordinate", what, r.Line, r.Column)
			}
			normalized := strings.ReplaceAll(r.Diagnostic, `\`, "/")
			if !strings.Contains(normalized, r.Path) {
				t.Errorf("the diagnostic of %s does not name its file:\n%s", what, r.Diagnostic)
			}
			if !strings.Contains(normalized, ":"+strconv.Itoa(r.Line)+":") {
				t.Errorf("the diagnostic of %s is not about line %d:\n%s", what, r.Line, r.Diagnostic)
			}
			// The fixture's traps all fail the same way, and saying so here is
			// what distinguishes "the compiler refused this guard" from "the
			// build failed for some other reason and this candidate was
			// standing nearby".
			if !strings.Contains(r.Diagnostic, "Flag") {
				t.Errorf("the diagnostic of %s does not mention the named boolean type:\n%s", what, r.Diagnostic)
			}
		}
	})

	t.Run("the surviving guards are the ones that were accepted", func(t *testing.T) {
		// Five of six in compare.go and one of three in flag.go: the counts of
		// what is left, per file, which is the tree's own version of the
		// accepted set. A file whose every candidate had been rejected would be
		// absent from both, since a pristine file carries no guards at all.
		want := map[string]int{"compare.go": 5, "flag.go": 1}
		if got := result.Instrumented.GuardsByFile; !maps.Equal(got, want) {
			t.Errorf("guards by file = %v, want %v", got, want)
		}
		if want := []string{"compare.go", "flag.go"}; !slices.Equal(result.Instrumented.FilesInstrumented, want) {
			t.Errorf("instrumented files = %v, want %v", result.Instrumented.FilesInstrumented, want)
		}
		if result.Builds < 2 {
			t.Errorf("validation spent %d builds, want more than one for a catalogue it had to search",
				result.Builds)
		}
	})

	t.Run("the validated snapshot builds", func(t *testing.T) {
		// Independently of the build validation ran itself: this one is the
		// user's own `go build ./...`, and it is the phase's postcondition.
		build := goInSnapshot(t, toolchain, snap.Root, "", "build", "./...")
		requireExit(t, build, 0, "`go build ./...` in the validated snapshot")
	})

	t.Run("the instrumented baseline passes", func(t *testing.T) {
		// `go build ./...` never compiles a _test.go file, so the build above
		// has only seen half the tree. This runs the other half, with no mutant
		// active: every guard takes the branch holding the original bytes, so
		// the suite has to pass exactly as it does in the fixture — and it has
		// to actually run, which is why the passing subtests are named.
		baseline := runSuite(t, toolchain, snap.Root, "")
		requireExit(t, baseline, 0, "the instrumented baseline")
		requireOutput(t, baseline, "the instrumented baseline",
			"--- PASS: TestInRange", "--- PASS: TestEnabled", "--- PASS: TestMatches")
	})

	t.Run("an accepted mutant is still activatable", func(t *testing.T) {
		// The failure this catches is the one the whole design of the phase is
		// arranged around. Isolation rewrites files but never the generated
		// runtime, because the runtime's dense indices are what every guard in
		// the tree spells; a phase that regenerated it from the accepted subset
		// would produce a tree that builds, a baseline that passes, and an
		// activation that turns on the wrong mutant — or none. So one accepted
		// mutant, in the file that lost two of its three candidates, is
		// activated and has to kill the test that covers it.
		mutant := mutants[8]
		red := runSuite(t, toolchain, snap.Root, mutant.ID)
		what := "the suite with " + mutant.DisplayID + " (" + mutant.Rule.Name + " in " + mutant.Path + ") active"
		requireExit(t, red, 1, what)
		requireOutput(t, red, what, "Enabled(1, 2) = false, want true", "--- FAIL: TestEnabled")
		if got := strings.Count(string(red.Output), "--- FAIL:"); got != 1 {
			t.Errorf("%s reported %d failures, want exactly 1:\n%s", what, got, red.Output)
		}
	})

	t.Run("only the instrumented files drifted", func(t *testing.T) {
		// Last, so that it covers the builds and both suite runs as well as the
		// rewriting. Every probe of the search wrote a file through a temporary
		// file and a rename, so this is also where a temporary left behind by
		// any of a dozen rewrites would show up as an addition nobody expected.
		drifts, err := snap.Redigest()
		if err != nil {
			t.Fatalf("re-digesting the snapshot: %v", err)
		}
		want := []string{
			"changed compare.go",
			"changed flag.go",
			"added gomutants_rt/gomutants_rt.go",
		}
		got := make([]string, 0, len(drifts))
		for _, drift := range drifts {
			got = append(got, drift.Kind.String()+" "+drift.RelPath)
		}
		if !slices.Equal(got, want) {
			t.Errorf("the snapshot drifted as\n\t%s\nwant\n\t%s",
				strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
		}
	})
}

// TestValidateIsDeterministic validates two fresh copies of one fixture and
// compares both answers and both trees.
//
// Two copies rather than two passes: validation rewrites the snapshot it is
// given, so a second pass over the first tree would be a different question.
// What is being asserted is the promise the phase makes to the outcome cache
// and to shard merging — the same workspace produces the same accepted set, the
// same rejections, and the same bytes — and the bytes are the half that no
// other test in this file would notice going wrong.
func TestValidateIsDeterministic(t *testing.T) {
	toolchain := locateToolchain(t)

	type pass struct {
		rejected []string
		accepted []string
		bytes    map[string][]byte
	}
	run := func() pass {
		snap := snapshotFixture(t, "rejectable")
		_, catalog := catalogFixture(t, toolchain, snap)
		result, err := validate.Validate(t.Context(), validate.Options{
			Snap:         snap,
			Catalog:      catalog,
			ModulePath:   rejectableModule,
			Toolchain:    toolchain,
			BuildTimeout: stepTimeout,
			Env:          fixtureEnv(""),
		})
		if err != nil {
			t.Fatalf("validating the rejectable fixture: %v", err)
		}
		out := pass{accepted: result.AcceptedIDs, bytes: make(map[string][]byte)}
		for _, r := range result.Rejected {
			out.rejected = append(out.rejected, r.ID+" "+r.Path+":"+strconv.Itoa(r.Line)+" "+r.Rule)
		}
		for _, name := range []string{"compare.go", "flag.go", "gomutants_rt/gomutants_rt.go"} {
			src, readErr := os.ReadFile(filepath.Join(snap.Root, filepath.FromSlash(name)))
			if readErr != nil {
				t.Fatalf("reading %s from the validated snapshot: %v", name, readErr)
			}
			out.bytes[name] = src
		}
		return out
	}

	first, second := run(), run()
	if !slices.Equal(first.rejected, second.rejected) {
		t.Errorf("the two passes rejected\n\t%s\nand\n\t%s",
			strings.Join(first.rejected, "\n\t"), strings.Join(second.rejected, "\n\t"))
	}
	if !slices.Equal(first.accepted, second.accepted) {
		t.Errorf("the two passes accepted %d and %d mutants", len(first.accepted), len(second.accepted))
	}
	for name, want := range first.bytes {
		if got := second.bytes[name]; string(got) != string(want) {
			t.Errorf("the two passes left different bytes in %s:\n%s\nand\n%s", name, want, got)
		}
	}
}

// TestValidateRefusesATreeItDidNotBreak points validation at a snapshot that
// does not compile on its own.
//
// This is the one refusal the phase owes a user an explanation for. Everything
// else it meets is a candidate it can drop; a tree that was already broken is
// not, and bisecting one would reject a whole file's mutants for somebody
// else's compile error and then report a mutation score computed from what was
// left. The failure carries the compiler's output for the same reason a
// rejection does: the user has to be told what is wrong, not that something is.
func TestValidateRefusesATreeItDidNotBreak(t *testing.T) {
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "rejectable")
	_, catalog := catalogFixture(t, toolchain, snap)

	// A second file in the same package, referring to something that does not
	// exist. It holds no candidates, so nothing this phase could reject would
	// make it compile — which is exactly the situation being tested.
	broken := filepath.Join(snap.Root, "broken.go")
	const source = "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
		"// SPDX-License-Identifier: MIT OR Apache-2.0\n\n" +
		"package rejectable\n\n" +
		"func Broken() int { return undefinedHelper() }\n"
	if err := os.WriteFile(broken, []byte(source), 0o644); err != nil {
		t.Fatalf("writing the broken file into the snapshot: %v", err)
	}

	result, err := validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		ModulePath:   rejectableModule,
		Toolchain:    toolchain,
		BuildTimeout: stepTimeout,
		Env:          fixtureEnv(""),
	})
	if err == nil {
		t.Fatal("Validate accepted a snapshot that does not build, want a refusal")
	}
	if got := validate.CodeOf(err); got != validate.CodeNotMutantInduced {
		t.Fatalf("Validate failed with %s, want %s: %v", got, validate.CodeNotMutantInduced, err)
	}
	if !strings.Contains(err.Error(), "undefinedHelper") {
		t.Errorf("the refusal does not carry the compiler's reason:\n%v", err)
	}
	if len(result.Rejected) != 0 {
		t.Errorf("Validate rejected %d candidates for a failure none of them caused", len(result.Rejected))
	}
	// Two builds and no more: the tree as instrumented, and the tree with every
	// guard removed. Searching would have been the wrong answer, and spending
	// builds on it would mean it had been attempted.
	if result.Builds != 2 {
		t.Errorf("Validate spent %d builds before refusing, want 2", result.Builds)
	}
}

// TestValidateLeavesNoBuildOutputInTheSnapshot validates a module of exactly
// one `package main` directory and then looks at the tree for anything the
// builds left behind.
//
// The module shape is the whole point, and it is the one shape the corpus has
// none of: every fixture here is a library. `go build` with no `-o` writes a
// linked executable into its working directory whenever the pattern it is given
// resolves to a single package and that package is `main`, and validation's
// working directory is the snapshot root — so this is where a build of the
// snapshot can add a file to the tree it is measuring, and the snapshot is
// re-digested with no exclusions precisely to catch files appearing in it. An
// executable at the root is neither a guarded file nor part of the generated
// runtime, so it would reach the user as workspace drift: a run stopped, and
// their own test suite blamed for a file go-mutants wrote.
//
// What is asserted is therefore the drift gate's own question, in the drift
// gate's terms. It is stated over the module shape that can fail it rather than
// over one that cannot, which is the difference between this and the last step
// of [TestValidateIsolatesTheTrappedCandidates] — the same assertion made where
// no executable was ever a possibility. Note that within this phase the
// generated runtime package already makes `./...` more than one package by the
// time anything is built; the flag that keeps this true is asserted directly,
// without a toolchain, by TestBuildArgsSendTheOutputToTheNullDevice.
func TestValidateLeavesNoBuildOutputInTheSnapshot(t *testing.T) {
	toolchain := locateToolchain(t)

	const modulePath = "fixture.example/singlemain"
	source := t.TempDir()
	writeModuleFile(t, source, "go.mod", "module "+modulePath+"\n\ngo 1.26\n")
	writeModuleFile(t, source, "main.go", "package main\n\n"+
		"// Before is here to be mutated: `<` is a comparison candidate, and a\n"+
		"// guard around it is a guard in a `package main` file.\n"+
		"func Before(a, b int) bool { return a < b }\n\n"+
		"func main() {\n"+
		"\tif Before(1, 2) {\n"+
		"\t\tprintln(\"ordered\")\n"+
		"\t}\n"+
		"}\n")

	snap, err := snapshot.Create(source, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatalf("snapshotting the single-main module: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("cleaning up the snapshot at %s: %v", snap.Root, cleanupErr)
		}
	})

	found, err := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
	})
	if err != nil {
		t.Fatalf("discovering the single-main module: %v", err)
	}
	catalog, err := discover.BuildCatalog(found)
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	if catalog.Len() == 0 {
		t.Fatal("the single-main module produced no candidates, so nothing would be instrumented or built")
	}

	result, err := validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		ModulePath:   found.ModulePath,
		Toolchain:    toolchain,
		BuildTimeout: stepTimeout,
		Env:          fixtureEnv(""),
	})
	if err != nil {
		t.Fatalf("validating the single-main module: %v", err)
	}
	// Nothing in this module can fail to compile when guarded, so a rejection
	// here means the phase is answering a different question than the one this
	// test is asking, and the drift assertion below would be about a tree that
	// had been searched rather than built once.
	if len(result.Rejected) != 0 {
		t.Fatalf("validation rejected %d of %d candidates in a module with no traps in it",
			len(result.Rejected), catalog.Len())
	}
	if want := []string{"main.go"}; !slices.Equal(result.Instrumented.FilesInstrumented, want) {
		t.Fatalf("instrumented files = %v, want %v", result.Instrumented.FilesInstrumented, want)
	}
	// Asserted rather than assumed: the generated runtime is a second package
	// under `./...`, and it is the reason the go command has no single main
	// package to name an executable after by the time this phase builds
	// anything. If it ever stopped being written unconditionally, this module
	// shape would depend on the build's own flag alone — and this is the line
	// that would say so, next to the comment explaining why it matters.
	if result.Instrumented.RuntimeDir == "" {
		t.Fatal("validation reported no generated runtime directory")
	}
	runtimeDir := filepath.Join(snap.Root, filepath.FromSlash(result.Instrumented.RuntimeDir))
	info, statErr := os.Stat(runtimeDir)
	if statErr != nil {
		t.Fatalf("the generated runtime is not in the snapshot: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("%s is in the snapshot but is not a directory, so `./...` need not match it",
			result.Instrumented.RuntimeDir)
	}

	drifts, err := snap.Redigest()
	if err != nil {
		t.Fatalf("re-digesting the snapshot: %v", err)
	}
	guarded := make(map[string]bool, len(result.Instrumented.FilesInstrumented))
	for _, path := range result.Instrumented.FilesInstrumented {
		guarded[path] = true
	}
	runtimePrefix := result.Instrumented.RuntimeDir + "/"

	var unexpected []string
	for _, drift := range drifts {
		switch {
		case drift.Kind == snapshot.DriftChanged && guarded[drift.RelPath]:
		case drift.Kind == snapshot.DriftAdded && strings.HasPrefix(drift.RelPath, runtimePrefix):
		default:
			unexpected = append(unexpected,
				drift.Kind.String()+" "+drift.RelPath+" ("+strconv.FormatInt(drift.GotSize, 10)+" bytes)")
		}
	}
	if len(unexpected) != 0 {
		t.Errorf("the validated snapshot drifted in %d way(s) that are neither a guarded file nor the "+
			"generated runtime, which a run would report as the user's tests writing into their own tree:\n\t%s",
			len(unexpected), strings.Join(unexpected, "\n\t"))
	}
}

// writeModuleFile writes one file of a module built for a single test.
//
// The SPDX header goes on every file this project writes into a tree it owns,
// including the ones that only ever exist inside a t.TempDir().
func writeModuleFile(t *testing.T, dir, name, content string) {
	t.Helper()
	const header = "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
		"// SPDX-License-Identifier: MIT OR Apache-2.0\n\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(header+content), 0o644); err != nil {
		t.Fatalf("writing %s into the module at %s: %v", name, dir, err)
	}
}

// snapshotFixture copies a corpus module into a disposable directory and
// registers its removal.
func snapshotFixture(t *testing.T, name string) *snapshot.Snapshot {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "fixtures", name))
	if err != nil {
		t.Fatalf("resolving the %s fixture: %v", name, err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("fixture %s is not a module: %v", name, err)
	}
	snap, err := snapshot.Create(root, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatalf("snapshotting the %s fixture: %v", name, err)
	}
	t.Cleanup(func() {
		if err := snap.Cleanup(); err != nil {
			t.Errorf("cleaning up the snapshot at %s: %v", snap.Root, err)
		}
	})
	return snap
}

// catalogFixture discovers the fixture's candidates and catalogues them, then
// pins the catalogue: every assertion in this file names a mutant by its
// position in [wantCatalog].
//
// The discovery result is returned alongside because it is the only place the
// coordinates a user is shown for a *live* mutant exist, and one step compares
// them with the coordinates validation reports for a rejected one.
func catalogFixture(t *testing.T, toolchain gocmd.Toolchain, snap *snapshot.Snapshot) (discover.Result, *mutation.Catalog) {
	t.Helper()

	found, err := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
	})
	if err != nil {
		t.Fatalf("discovering the rejectable fixture: %v", err)
	}
	if found.ModulePath != rejectableModule {
		t.Fatalf("discovered module path = %q, want %q", found.ModulePath, rejectableModule)
	}
	catalog, err := discover.BuildCatalog(found)
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}

	got := make([]string, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		got = append(got, fmt.Sprintf("%s %s %s -> %s", m.Path, m.Rule.Name, m.Original, m.Replacement))
	}
	if !slices.Equal(got, wantCatalog) {
		t.Fatalf("catalogue =\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(wantCatalog, "\n\t"))
	}
	return found, catalog
}

// describe renders a list of mutant IDs in the terms the fixture is written in,
// so that a failure reads as a list of candidates rather than of digests.
func describe(catalog *mutation.Catalog, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		m, ok := catalog.ByID(id)
		if !ok {
			out = append(out, id+" (not in the catalogue)")
			continue
		}
		out = append(out, fmt.Sprintf("[%d] %s %s %s -> %s", m.Index, m.Path, m.Rule.Name, m.Original, m.Replacement))
	}
	return out
}

// locateToolchain finds the Go toolchain this test's children run.
func locateToolchain(t *testing.T) gocmd.Toolchain {
	t.Helper()
	toolchain, err := gocmd.LocateContext(t.Context(), gocmd.Options{})
	if err != nil {
		t.Fatalf("locating a Go toolchain: %v", err)
	}
	return toolchain
}

// fixtureEnv builds the environment every child in this test receives, with one
// mutant activated when active is not empty.
//
// It is composed rather than inherited, for the reason internal/engine composes
// its own: a developer with GO_MUTANTS_ACTIVE exported in their shell would
// otherwise have the instrumented baseline running a mutant. The three go
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
// package that supervises them in a real run.
func goInSnapshot(t *testing.T, toolchain gocmd.Toolchain, dir, active string, args ...string) runner.Result {
	t.Helper()
	spec := toolchain.Command(args...)
	spec.Dir = dir
	spec.Env = fixtureEnv(active)
	spec.Timeout = stepTimeout
	return runner.Run(t.Context(), spec)
}

// runSuite runs the fixture's whole test suite in the snapshot, with one mutant
// activated or with none.
//
// -count=1 defeats the go test result cache, which keys on the environment a
// test binary reads and so would very probably do the right thing here; "very
// probably" is not a foundation for the step that tells a survivor from a
// cached green. -v is what lets a step name the subtest that passed or failed.
func runSuite(t *testing.T, toolchain gocmd.Toolchain, root, active string) runner.Result {
	t.Helper()
	return goInSnapshot(t, toolchain, root, active, "test", "-count=1", "-v", "./...")
}

// requireExit ends the step unless the child ran to completion with the status
// the step expects, and quotes the child's output whenever it did not.
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
