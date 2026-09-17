// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package validate_test

import (
	"context"
	"errors"
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
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/internal/validate"
)

const rejectableModule = "fixture.example/rejectable"

var wantCatalog = []string{
	"compare.go negate-condition v < lo -> !(v < lo)",
	"compare.go condition-to-true v < lo -> true",
	"compare.go condition-to-false v < lo -> false",
	"compare.go lt-to-le < -> <=",
	"compare.go false-to-true false -> true",
	"compare.go negate-condition v > hi -> !(v > hi)",
	"compare.go condition-to-true v > hi -> true",
	"compare.go condition-to-false v > hi -> false",
	"compare.go gt-to-ge > -> >=",
	"compare.go false-to-true false -> true",
	"compare.go true-to-false true -> false",
	"compare.go return-zero-numeric v*0 + 1 -> 0",
	"compare.go mul-to-div * -> /",
	"compare.go add-to-sub + -> -",
	"limits.go return-zero-numeric 200 - 100 -> 0",
	"limits.go sub-to-add - -> +",
	"limits.go mul-to-div * -> /",
	"limits.go return-zero-numeric scaled + 1 -> 0",
	"limits.go add-to-sub + -> -",
	"named.go return-true level >= 3 -> true",
	"named.go return-false level >= 3 -> false",
	"named.go ge-to-gt >= -> >",
	"named.go true-to-false true -> false",
	"named.go negate-condition f -> !(f)",
	"named.go condition-to-true f -> true",
	"named.go condition-to-false f -> false",
	"named.go return-zero-numeric level -> 0",
}

var namedBool = catalogPositions(
	"named.go return-true level >= 3 -> true",
	"named.go return-false level >= 3 -> false",
	"named.go ge-to-gt >= -> >",
	"named.go true-to-false true -> false",
	"named.go negate-condition f -> !(f)",
	"named.go condition-to-true f -> true",
	"named.go condition-to-false f -> false",
)

var trapped = map[int]string{
	catalogPosition("compare.go mul-to-div * -> /"): "division by zero",
	catalogPosition("limits.go sub-to-add - -> +"):  "overflows",
	catalogPosition("limits.go mul-to-div * -> /"):  "division by zero",
}

func catalogPosition(entry string) int {
	found := -1
	for i, candidate := range wantCatalog {
		if candidate != entry {
			continue
		}
		if found >= 0 {
			panic("validate: " + entry + " appears twice in wantCatalog, so its position is ambiguous")
		}
		found = i
	}
	if found < 0 {
		panic("validate: " + entry + " is not in wantCatalog")
	}
	return found
}

func catalogPositions(entries ...string) []int {
	out := make([]int, 0, len(entries))
	for _, entry := range entries {
		out = append(out, catalogPosition(entry))
	}
	return out
}

func TestValidateIsolatesTheTrappedCandidates(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "rejectable")
	found, catalog := catalogFixture(t, toolchain, env, snap)

	result, err := validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
		Modules:      []validate.Module{{Dir: ".", Path: rejectableModule}},
		Toolchain:    toolchain,
		Jobs:         2,
		BuildTimeout: mutantkit.StepTimeout,
		Env:          env,
		Trace:        mutantkit.Trace(t),
	})
	if err != nil {
		t.Fatalf("validating the rejectable fixture: %v\n%s", err, retainedOutput(t, err))
	}

	mutants := catalog.Mutants()
	t.Run("the trapped candidates are rejected and the healthy ones accepted", func(t *testing.T) {
		var wantRejected, wantAccepted []string
		for i, m := range mutants {
			if _, isTrap := trapped[i]; isTrap {
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
				strings.Join(mutantkit.Describe(catalog, gotRejected), "\n\t"),
				strings.Join(mutantkit.Describe(catalog, wantRejected), "\n\t"))
		}
		if !slices.Equal(result.AcceptedIDs, wantAccepted) {
			t.Errorf("accepted\n\t%s\nwant\n\t%s",
				strings.Join(mutantkit.Describe(catalog, result.AcceptedIDs), "\n\t"),
				strings.Join(mutantkit.Describe(catalog, wantAccepted), "\n\t"))
		}
	})

	t.Run("a rejection is at the coordinates discovery reported", func(t *testing.T) {
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
		position := make(map[string]int, len(mutants))
		for i, m := range mutants {
			position[m.ID] = i
		}
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
			want, known := trapped[position[r.ID]]
			if !known {
				t.Errorf("%s was rejected and is not one of the fixture's traps", what)
				continue
			}
			if !strings.Contains(r.Diagnostic, want) {
				t.Errorf("the diagnostic of %s does not say %q:\n%s", what, want, r.Diagnostic)
			}
		}
	})

	t.Run("the surviving guards are the ones that were accepted", func(t *testing.T) {
		want := map[string]int{"compare.go": 6, "limits.go": 2, "named.go": 4}
		if got := result.Instrumented.GuardsByFile; !maps.Equal(got, want) {
			t.Errorf("guards by file = %v, want %v", got, want)
		}
		if want := []string{"compare.go", "limits.go", "named.go"}; !slices.Equal(result.Instrumented.FilesInstrumented, want) {
			t.Errorf("instrumented files = %v, want %v", result.Instrumented.FilesInstrumented, want)
		}
		if result.Builds < 2 {
			t.Errorf("validation spent %d builds, want more than one for a catalogue it had to search",
				result.Builds)
		}
	})

	t.Run("the validated snapshot builds", func(t *testing.T) {
		build := mutantkit.RunGo(t, toolchain, snap.Root, env, "build", "./...")
		mutantkit.RequireExit(t, build, 0, "`go build ./...` in the validated snapshot")
	})

	t.Run("the instrumented baseline passes", func(t *testing.T) {
		baseline := mutantkit.RunSuite(t, toolchain, snap.Root, env)
		mutantkit.RequireExit(t, baseline, 0, "the instrumented baseline")
		mutantkit.RequireOutput(t, baseline, "the instrumented baseline",
			"--- PASS: TestInRange", "--- PASS: TestErased",
			"--- PASS: TestLevel", "--- PASS: TestRatio",
			"--- PASS: TestReady", "--- PASS: TestAlways")
	})

	t.Run("the named boolean type is instrumented rather than rejected", func(t *testing.T) {
		accepted := make(map[string]bool, len(result.AcceptedIDs))
		for _, id := range result.AcceptedIDs {
			accepted[id] = true
		}
		for _, position := range namedBool {
			mutant := mutants[position]
			what := mutant.DisplayID + " (" + mutant.Rule.Name + " in " + mutant.Path + ")"
			if !accepted[mutant.ID] {
				t.Errorf("%s was not accepted; the named boolean type is being rejected again", what)
				continue
			}
			red := mutantkit.RunSuite(t, toolchain, snap.Root, mutantkit.Activate(env, mutant.ID))
			mutantkit.RequireExit(t, red, 1, "the suite with "+what+" active")
		}
		if guards := result.Instrumented.GuardsByFile["named.go"]; guards == 0 {
			t.Error("named.go carries no guards, so its candidates were restored rather than instrumented")
		}
	})

	t.Run("an accepted mutant is still activatable", func(t *testing.T) {
		mutant := mutants[catalogPosition("limits.go add-to-sub + -> -")]
		red := mutantkit.RunSuite(t, toolchain, snap.Root, mutantkit.Activate(env, mutant.ID))
		what := "the suite with " + mutant.DisplayID + " (" + mutant.Rule.Name + " in " + mutant.Path + ") active"
		mutantkit.RequireExit(t, red, 1, what)
		mutantkit.RequireOutput(t, red, what, "Ratio(9) = -1, want 1", "--- FAIL: TestRatio")
		if got := strings.Count(string(red.Output), "--- FAIL:"); got != 1 {
			t.Errorf("%s reported %d failures, want exactly 1:\n%s", what, got, red.Output)
		}
	})

	t.Run("only the instrumented files drifted", func(t *testing.T) {
		drifts, err := snap.Redigest()
		if err != nil {
			t.Fatalf("re-digesting the snapshot: %v", err)
		}
		want := []string{
			"changed compare.go",
			"added gomutants_rt/gomutants_rt.go",
			"changed limits.go",
			"changed named.go",
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

func TestValidateIsDeterministic(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))

	type pass struct {
		rejected []string
		accepted []string
		bytes    map[string][]byte
	}
	run := func() pass {
		snap := mutantkit.Snapshot(t, "rejectable")
		found, catalog := catalogFixture(t, toolchain, env, snap)
		result, err := validate.Validate(t.Context(), validate.Options{
			Snap:         snap,
			Catalog:      catalog,
			Hints:        mutantkit.Hints(t, found),
			Modules:      []validate.Module{{Dir: ".", Path: rejectableModule}},
			Toolchain:    toolchain,
			BuildTimeout: mutantkit.StepTimeout,
			Env:          env,
			Trace:        mutantkit.Trace(t),
		})
		if err != nil {
			t.Fatalf("validating the rejectable fixture: %v\n%s", err, retainedOutput(t, err))
		}
		out := pass{accepted: result.AcceptedIDs, bytes: make(map[string][]byte)}
		for _, r := range result.Rejected {
			out.rejected = append(out.rejected, r.ID+" "+r.Path+":"+strconv.Itoa(r.Line)+" "+r.Rule)
		}
		for _, name := range []string{"compare.go", "limits.go", "named.go", "gomutants_rt/gomutants_rt.go"} {
			out.bytes[name] = testkit.ReadFile(t, filepath.Join(snap.Root, filepath.FromSlash(name)))
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

func TestValidateRefusesATreeItDidNotBreak(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "rejectable")
	found, catalog := catalogFixture(t, toolchain, env, snap)

	testkit.WriteSource(t, snap.Root, "broken.go", "package rejectable\n\n"+
		"func Broken() int { return undefinedHelper() }\n")

	result, err := validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
		Modules:      []validate.Module{{Dir: ".", Path: rejectableModule}},
		Toolchain:    toolchain,
		BuildTimeout: mutantkit.StepTimeout,
		Env:          env,
		Trace:        mutantkit.Trace(t),
	})
	if err == nil {
		t.Fatal("Validate accepted a snapshot that does not build, want a refusal")
	}
	if got := validate.CodeOf(err); got != validate.CodeNotMutantInduced {
		t.Fatalf("Validate failed with %s, want %s: %v", got, validate.CodeNotMutantInduced, err)
	}
	if !strings.Contains(retainedOutput(t, err), "undefinedHelper") {
		t.Errorf("the refusal does not carry the compiler's reason:\n%v\n%s", err, retainedOutput(t, err))
	}
	if len(result.Rejected) != 0 {
		t.Errorf("Validate rejected %d candidates for a failure none of them caused", len(result.Rejected))
	}
	if result.Builds != 2 {
		t.Errorf("Validate spent %d builds before refusing, want 2", result.Builds)
	}
}

func TestValidateLeavesNoBuildOutputInTheSnapshot(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))

	const modulePath = "fixture.example/singlemain"
	source := testkit.NewModule(t).Module(modulePath).Source("main.go", "package main\n\n"+
		"// Before is here to be mutated: `<` is a comparison candidate, and a\n"+
		"// guard around it is a guard in a `package main` file.\n"+
		"func Before(a, b int) bool { return a < b }\n\n"+
		"func main() {\n"+
		"\tif Before(1, 2) {\n"+
		"\t\tprintln(\"ordered\")\n"+
		"\t}\n"+
		"}\n").Root()

	snap := mutantkit.SnapshotOf(t, source)
	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	catalog := mutantkit.Catalog(t, found)
	if catalog.Len() == 0 {
		t.Fatal("the single-main module produced no candidates, so nothing would be instrumented or built")
	}

	result, err := validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
		Modules:      []validate.Module{{Dir: ".", Path: found.ModulePath}},
		Toolchain:    toolchain,
		BuildTimeout: mutantkit.StepTimeout,
		Env:          env,
		Trace:        mutantkit.Trace(t),
	})
	if err != nil {
		t.Fatalf("validating the single-main module: %v\n%s", err, retainedOutput(t, err))
	}
	if len(result.Rejected) != 0 {
		t.Fatalf("validation rejected %d of %d candidates in a module with no traps in it",
			len(result.Rejected), catalog.Len())
	}
	if want := []string{"main.go"}; !slices.Equal(result.Instrumented.FilesInstrumented, want) {
		t.Fatalf("instrumented files = %v, want %v", result.Instrumented.FilesInstrumented, want)
	}
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

func TestValidateDoesNotTouchTheUsersBuildCache(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	harness, err := testkit.BuildCache()
	if err != nil {
		t.Fatalf("resolving the test build cache: %v", err)
	}
	scratch := t.TempDir()
	env := testkit.Compose(t, scratch)
	private := filepath.Join(t.TempDir(), "gocache")
	buildEnv := append(slices.Clip(env), "GOCACHE="+private)

	users := usersBuildCache(t, harness)
	usersBefore := directoryState(t, users)

	const modulePath = "fixture.example/cached"
	source := testkit.NewModule(t).Module(modulePath).Source("before.go", "package cached\n\n"+
		"// Before is here to be mutated, so that validation has a guarded tree to\n"+
		"// build rather than a module it can accept without compiling anything.\n"+
		"func Before(a, b int) bool { return a < b }\n").Root()

	snap := mutantkit.SnapshotOf(t, source)
	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	catalog := mutantkit.Catalog(t, found)
	if catalog.Len() == 0 {
		t.Fatal("the module produced no candidates, so nothing would be instrumented or built")
	}
	if _, err := validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
		Modules:      []validate.Module{{Dir: ".", Path: found.ModulePath}},
		Toolchain:    toolchain,
		BuildTimeout: mutantkit.StepTimeout,
		Env:          buildEnv,
		Trace:        mutantkit.Trace(t),
	}); err != nil {
		t.Fatalf("validating the module: %v\n%s", err, retainedOutput(t, err))
	}

	answer := mutantkit.RunGo(t, toolchain, snap.Root, env, "env", "GOCACHE")
	mutantkit.RequireExit(t, answer, 0, "`go env GOCACHE` under the composed environment")
	if got := strings.TrimSpace(string(answer.Output)); !testkit.SamePath(got, harness) {
		t.Errorf("a child of this run reads GOCACHE=%s, want the harness's own %s", got, harness)
	}

	fallback := filepath.Join(scratch, "home", "cache", "go-build")
	if _, statErr := os.Stat(fallback); statErr == nil {
		t.Errorf("a build cache was created at %s, so the children resolved GOCACHE from the moved "+
			"HOME rather than from the harness's pin", fallback)
	}

	if testkit.BuildCacheEntries(t, private) == 0 {
		t.Errorf("the validation's builds wrote nothing into %s, the cache its environment named, "+
			"so they went to a cache this test cannot see", private)
	}

	t.Run("the developer's own build cache is untouched", func(t *testing.T) {
		if users == "" {
			t.Skip("this machine has no initialised build cache outside the harness's, so there is " +
				"nothing to compare: a fresh runner with GO_MUTANTS_TEST_GOCACHE pointed at the job's " +
				"temporary area is exactly that machine")
		}
		if got := directoryState(t, users); got != usersBefore {
			t.Errorf("the developer's build cache %s changed while this test ran:\n\tbefore %s\n\tafter  %s",
				users, usersBefore, got)
		}
	})
}

func usersBuildCache(t *testing.T, harness string) string {
	t.Helper()
	root, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(root, "go-build")
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		return ""
	}
	if testkit.SamePath(dir, harness) {
		return ""
	}
	if len(testkit.Entries(t, dir)) < 2 {
		return ""
	}
	return dir
}

func directoryState(t *testing.T, dir string) string {
	t.Helper()
	if dir == "" {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("reading the state of %s: %v", dir, err)
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano) + " " + strings.Join(testkit.Entries(t, dir), " ")
}

func catalogFixture(t *testing.T, toolchain gocmd.Toolchain, env []string, snap *snapshot.Snapshot) (discover.Result, *mutation.Catalog) {
	t.Helper()

	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	if found.ModulePath != rejectableModule {
		t.Fatalf("discovered module path = %q, want %q", found.ModulePath, rejectableModule)
	}
	catalog := mutantkit.Catalog(t, found)
	if got := mutantkit.CatalogLines(catalog); !slices.Equal(got, wantCatalog) {
		t.Fatalf("catalogue =\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(wantCatalog, "\n\t"))
	}
	return found, catalog
}

func retainedOutput(t *testing.T, err error) string {
	t.Helper()
	var failure *validate.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a *validate.Error", err)
	}
	return failure.RetainedOutput()
}

const forcedFailureTarget = "TestValidateRefusesATreeItDidNotBreak"

const helperTimeout = 5 * time.Minute

func TestValidateFailureShowsTheInstrumentedSource(t *testing.T) {
	t.Parallel()

	cache, err := testkit.BuildCache()
	if err != nil {
		t.Fatalf("resolving the test build cache for the child: %v", err)
	}
	env := append(testkit.Compose(t, testkit.Scratch(t)),
		testkit.ForceFailEnv+"="+forcedFailureTarget,
		testkit.KeepEnv+"=",
		testkit.BuildCacheEnv+"="+cache,
	)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), helperTimeout)
	defer cancel()
	result := testkit.ExecContext(ctx, t, testkit.Root(t), env, testkit.HelperArgv(forcedFailureTarget)...)

	if result.ExitCode == 0 {
		t.Fatalf("the child passed, so %s never reached the test it names:\n%s",
			testkit.ForceFailEnv, result.Output)
	}
	testkit.RequireOutput(t, result, "the forced failure",
		"forced failure by "+testkit.ForceFailEnv,
		"--- ",
		".go (",
		"Code generated by go-mutants",
	)
}
