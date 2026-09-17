// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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
	"github.com/P4suta/go-mutants/trace"
)

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

func TestRunAtTheWorkspaceRootMeasuresEveryModuleAtOnce(t *testing.T) {
	t.Parallel()

	workspace := testkit.Copy(t, "workspace")
	outcome, _, err := collect(t, t.Context(), optionsAt(t, workspace))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}
	if outcome.Report != nil {
		t.Errorf("a workspace run published a run report; the module path it would need has no answer")
	}
	doc := outcome.WorkspaceReport
	if doc == nil {
		t.Fatal("a workspace run published no workspace report")
	}
	if doc.DocumentType != report.WorkspaceDocumentType {
		t.Errorf("document_type = %q, want %q", doc.DocumentType, report.WorkspaceDocumentType)
	}
	if doc.Summary.Survived != 0 || doc.Summary.Total != 11 {
		t.Errorf("summary = %d of %d killed; every mutant of this workspace is killed, and three of "+
			"them only by another module's tests", doc.Summary.Killed, doc.Summary.Total)
	}

	want := map[string]int{
		"fixture.example/workspace/app":   6,
		"fixture.example/workspace/cross": 2,
		"fixture.example/workspace/lib":   3,
	}
	if len(doc.Modules) != len(want) {
		t.Fatalf("the document holds %d modules, want the workspace's %d", len(doc.Modules), len(want))
	}
	for _, module := range doc.Modules {
		if module.Report.Workspace.ModulePath != module.ModulePath {
			t.Errorf("module %s carries a report of %s",
				module.ModulePath, module.Report.Workspace.ModulePath)
		}
		if got := module.Report.Summary.Killed; got != want[module.ModulePath] {
			t.Errorf("%s killed %d of its mutants, want %d",
				module.ModulePath, got, want[module.ModulePath])
		}
		for _, m := range module.Report.Mutants {
			if m.Outcome != report.OutcomeKilled {
				t.Errorf("mutant %s in %s %s settled as %s",
					m.DisplayID, module.ModulePath, m.Path, m.Outcome)
			}
		}
	}
}

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

	warm := runCached(t, cacheOptions(t, root, cacheRoot))
	if warm.Cache.Hits != len(tagged.Mutants) {
		t.Fatalf("a repeat of the tagged run adopted %d of %d outcomes, want all of them",
			warm.Cache.Hits, len(tagged.Mutants))
	}

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

func TestCRLFSourcesAreInstrumentedByteForByteAndKilled(t *testing.T) {
	t.Parallel()

	lf, _, err := collect(t, t.Context(), options(t, "simple"))
	if err != nil {
		t.Fatalf("Run over the LF fixture: %v", err)
	}

	crlfRoot := testkit.NewModule(t).From("simple").CRLF().Root()
	opts := optionsAt(t, crlfRoot)
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

func fatesOf(r *report.Report) []string {
	out := make([]string, 0, len(r.Mutants))
	for _, m := range r.Mutants {
		out = append(out, fmt.Sprintf("%s %s:%d %s", m.Outcome, m.Path, m.Line, m.Rule))
	}
	slices.Sort(out)
	return out
}

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
	if found := discoveredOf(t, events); found.Skips != 1 || found.Candidates != 15 {
		t.Errorf("Discovered = %+v, want 15 candidates and 1 skip", found)
	}
	want := []string{
		"killed hidden/hidden.go:17 return-nil",
		"killed hidden/hidden.go:20 return-zero-numeric",
		"killed hidden/hidden.go:35 return-zero-numeric",
		"killed hidden/hidden.go:39 return-zero-numeric",
		"killed reachable/reachable.go:18 return-zero-numeric",
		"killed reachable/reachable.go:21 return-zero-numeric",
		"killed split/sayable.go:19 return-zero-numeric",
		"killed split/sayable.go:23 add-to-sub",
		"killed split/sayable.go:23 return-zero-numeric",
		"killed split/unsayable.go:21 add-to-sub",
		"killed split/unsayable.go:23 return-zero-numeric",
		"killed split/unsayable.go:25 return-zero-numeric",
		"killed unnameable.go:25 add-to-sub",
		"killed unnameable.go:26 return-zero-numeric",
		"killed unnameable.go:46 return-zero-numeric",
	}
	got := slices.Clone(results(events))
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("results =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	sites := mutantkit.Discover(t, mutantkit.Toolchain(t), mutantkit.SnapshotOf(t, root)).SkipSites
	wantSites := []discover.SkipSite{{
		Path:   "unnameable.go",
		Reason: discover.SkipUnnameableDeclType,
		Line:   44,
		Column: 23,
		Rule:   "add-to-sub",
	}}
	if !slices.Equal(sites, wantSites) {
		t.Errorf("skip sites = %+v, want %+v", sites, wantSites)
	}
}

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

func TestIsolateGivesEveryWorkerATreeAndPutsItBackBetweenMutants(t *testing.T) {
	t.Parallel()

	opts, sink := tracedOptions(t, "selfwriting")
	opts.Config.Execution.Isolate = true
	opts.Config.Execution.Jobs = 1

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("an isolating run of a self-writing suite: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	survivors, kills := 0, 0
	for _, m := range outcome.Report.Mutants {
		switch m.Outcome {
		case report.OutcomeSurvived:
			survivors++
		case report.OutcomeKilled:
			kills++
		default:
			t.Errorf("mutant %s (%s) settled as %s", m.DisplayID, m.Rule, m.Outcome)
		}
	}
	if survivors == 0 {
		t.Errorf("no mutant survived, so either the worker's tree is not being put back between "+
			"mutants or the fixture stopped having a survivor:\n\t%s", strings.Join(results(events), "\n\t"))
	}
	if kills == 0 {
		t.Errorf("nothing was killed, so the suite is not measuring anything:\n\t%s",
			strings.Join(results(events), "\n\t"))
	}

	restored, copies := 0, 0
	for _, event := range sink.Events() {
		switch {
		case event.Type == trace.TypeNote && event.Note != nil &&
			event.Note.Kind == trace.NoteWorkerRestored:
			restored++
		case event.Type == trace.TypeSnapshot && event.Snapshot != nil &&
			event.Snapshot.Kind == trace.SnapshotKindWorker:
			copies++
		}
	}
	if copies != 1 {
		t.Errorf("the run recorded %d worker copies, want the one worker it was given", copies)
	}
	if restored == 0 {
		t.Error("the run restored no worker copy, so nothing was put back between mutants")
	}
}

func TestIsolateIsOffByDefault(t *testing.T) {
	t.Parallel()

	if opts := options(t, "selfwriting"); opts.Config.Execution.Isolate {
		t.Error("execution.isolate defaults to true, so the drift gate can never be reached")
	}
}
