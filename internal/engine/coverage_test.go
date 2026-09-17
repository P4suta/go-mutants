// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

func TestUnavailableWarningSaysWhatTheRunWillDoInstead(t *testing.T) {
	t.Parallel()

	s := &session{}
	s.unavailable("`go tool covdata` is not in this toolchain.")

	if len(s.warnings) != 1 {
		t.Fatalf("published %d warnings, want 1", len(s.warnings))
	}
	w := s.warnings[0]
	if w.Code != string(coverage.CodeUnavailable) {
		t.Errorf("code = %q, want %q", w.Code, coverage.CodeUnavailable)
	}
	for _, needle := range []string{
		"covdata",
		"every mutant will be measured against every test binary",
		"slower and never wrong",
	} {
		if !strings.Contains(w.Message, needle) {
			t.Errorf("the warning does not mention %q:\n%s", needle, w.Message)
		}
	}
	if strings.ContainsAny(w.Message, "\n\r") {
		t.Errorf("the warning is not one line: %q", w.Message)
	}
	if strings.Contains(w.Message, ".;") {
		t.Errorf("the warning has the cause's full stop in the middle of it: %q", w.Message)
	}
}

func TestCoverageMutantsDerivesTheLineIntervalFromTheOriginal(t *testing.T) {
	t.Parallel()

	st := &state{display: map[string]MutantResult{
		"one-line": {ID: "one-line", Path: "a.go", Line: 12, Original: "!="},
		"two-line": {ID: "two-line", Path: "b.go", Line: 30, Original: "foo(\n\tbar)"},
	}}
	runs := []execute.MutantRun{{ID: "one-line"}, {ID: "two-line"}}

	got := coverageMutants(runs, st)
	want := []coverage.Mutant{
		{ID: "one-line", Path: "a.go", StartLine: 12, EndLine: 12},
		{ID: "two-line", Path: "b.go", StartLine: 30, EndLine: 31},
	}
	if !slices.Equal(got, want) {
		t.Errorf("coverageMutants = %+v, want %+v", got, want)
	}
}

func TestCoverageMutantsLeavesOutAMutantItCannotLocate(t *testing.T) {
	t.Parallel()

	st := &state{display: map[string]MutantResult{
		"located":   {ID: "located", Path: "a.go", Line: 4, Original: "<"},
		"no-line":   {ID: "no-line", Path: "a.go", Original: "<"},
		"no-path":   {ID: "no-path", Line: 4, Original: "<"},
		"not-shown": {},
	}}
	runs := []execute.MutantRun{{ID: "located"}, {ID: "no-line"}, {ID: "no-path"}, {ID: "missing"}}

	got := coverageMutants(runs, st)
	if len(got) != 1 || got[0].ID != "located" {
		t.Errorf("coverageMutants = %+v, want only the located mutant", got)
	}
}

func TestCoveragePhaseKeepsAMutantItCouldNotAskAbout(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 8)
	s := &session{events: events}
	st := &state{
		results: map[string]report.MutantResult{},
		display: map[string]MutantResult{
			"nowhere": {ID: "nowhere", DisplayID: "nowhere"},
		},
	}
	runs := []execute.MutantRun{{ID: "nowhere"}}
	bins := []execute.TestBinary{{ImportPath: "example.com/m/a"}}
	profiles := map[string]coverage.Profile{
		"example.com/m/a": {Mode: "set", Blocks: []coverage.Block{{
			File: "example.com/m/a.go", StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 1, NumStmt: 1, Count: 1,
		}}},
	}

	mapped := coverage.Map(coverage.Options{
		ModulePath: "example.com/m",
		Mutants:    coverageMutants(runs, st),
		Profiles:   profiles,
	})
	if mapped.Matched == 0 {
		t.Fatal("the fixture profile does not line up, so this checks the wrong path")
	}

	kept, _ := s.narrow(mapped, coverageMutants(runs, st), bins, runs, st)
	close(events)

	if len(kept) != 1 || kept[0].ID != "nowhere" {
		t.Fatalf("the phase kept %+v, want the unplaceable mutant", kept)
	}
	if kept[0].Binaries != nil {
		t.Errorf("Binaries = %v, want nil so that every binary is measured", kept[0].Binaries)
	}
	if _, recorded := st.results["nowhere"]; recorded {
		t.Error("the unplaceable mutant was filed as an uncovered survivor")
	}
	for e := range events {
		if finished, ok := e.(MutantFinished); ok {
			t.Errorf("the unplaceable mutant was settled without being executed: %+v", finished.Result)
		}
	}
}

func TestIndicesOfTranslatesCoveringPathsIntoBinaryPositions(t *testing.T) {
	t.Parallel()

	bins := []execute.TestBinary{
		{ImportPath: "example.com/m/a"},
		{ImportPath: "example.com/m/b"},
		{ImportPath: "example.com/m/c"},
	}
	index := binaryIndex(bins)

	if got := indicesOf([]string{"example.com/m/a", "example.com/m/c"}, index); !slices.Equal(got, []int{0, 2}) {
		t.Errorf("indicesOf = %v, want [0 2]", got)
	}
	if got := indicesOf([]string{"example.com/m/z"}, index); got != nil {
		t.Errorf("indicesOf of an unknown binary = %v, want nil", got)
	}
	if got := indicesOf(nil, index); got != nil {
		t.Errorf("indicesOf(nil) = %v, want nil", got)
	}
}

func TestRecordUncoveredFilesASurvivorNobodyExecuted(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 4)
	s := &session{events: events}
	st := &state{
		results: map[string]report.MutantResult{},
		display: map[string]MutantResult{
			"orphan": {ID: "orphan", DisplayID: "orphan", Path: "a.go", Line: 4, Rule: "lt-to-le"},
		},
	}

	s.recordUncovered("orphan", st)
	close(events)

	result := st.results["orphan"]
	if result.Outcome != mutation.OutcomeSurvived {
		t.Errorf("outcome = %s, want %s", result.Outcome, mutation.OutcomeSurvived)
	}
	if !result.Uncovered {
		t.Error("the result is not marked uncovered")
	}
	if result.Attempts != 0 || result.Duration != 0 || result.KilledBy != "" {
		t.Errorf("the result claims a measurement: %+v", result)
	}

	var published []Event
	for e := range events {
		published = append(published, e)
	}
	if len(published) != 1 {
		t.Fatalf("published %d events, want the one MutantFinished", len(published))
	}
	finished, ok := published[0].(MutantFinished)
	if !ok {
		t.Fatalf("published %T, want MutantFinished", published[0])
	}
	if !finished.Result.Uncovered || finished.Result.Outcome != mutation.OutcomeSurvived {
		t.Errorf("the event carries %+v", finished.Result)
	}
	if finished.Result.Duration != 0 {
		t.Errorf("the event claims %s of execution", finished.Result.Duration)
	}
}

func TestNotableGroupsUncoveredSurvivorsAfterCoveredOnes(t *testing.T) {
	t.Parallel()

	rows := []struct {
		id        string
		path      string
		outcome   mutation.Outcome
		uncovered bool
	}{
		{"u-a", "a.go", mutation.OutcomeSurvived, true},
		{"c-z", "z.go", mutation.OutcomeSurvived, false},
		{"t-a", "a.go", mutation.OutcomeTimedOut, false},
		{"u-z", "z.go", mutation.OutcomeSurvived, true},
		{"c-a", "a.go", mutation.OutcomeSurvived, false},
	}
	st := &state{display: make(map[string]MutantResult)}
	rep := &report.Report{}
	for i, row := range rows {
		st.display[row.id] = MutantResult{ID: row.id, DisplayID: row.id, Path: row.path, Line: i + 1}
		outcome, err := report.OutcomeOf(row.outcome)
		if err != nil {
			t.Fatalf("rendering %s: %v", row.outcome, err)
		}
		rep.Mutants = append(rep.Mutants, report.Mutant{ID: row.id, Outcome: outcome, Uncovered: row.uncovered})
	}

	got := make([]string, 0, len(rows))
	for _, m := range notable(st, rep.Mutants) {
		got = append(got, m.ID)
	}
	want := []string{"c-a", "c-z", "u-a", "u-z", "t-a"}
	if !slices.Equal(got, want) {
		t.Errorf("notable = %v, want %v", got, want)
	}
	for _, m := range notable(st, rep.Mutants) {
		if want := strings.HasPrefix(m.ID, "u-"); m.Uncovered != want {
			t.Errorf("%s: Uncovered = %t, want %t", m.ID, m.Uncovered, want)
		}
	}
}

func TestUncoveredOfCountsTheDocumentRatherThanTheRun(t *testing.T) {
	t.Parallel()

	rep := &report.Report{Mutants: []report.Mutant{
		{ID: "a", Uncovered: true},
		{ID: "b"},
		{ID: "c", Uncovered: true},
	}}
	if got := uncoveredOf(rep.Mutants); got != 2 {
		t.Errorf("uncoveredOf = %d, want 2", got)
	}
	if got := uncoveredOf(nil); got != 0 {
		t.Errorf("uncoveredOf of an empty report = %d, want 0", got)
	}
}

func TestReportCoverageModeMapsBothSpellings(t *testing.T) {
	t.Parallel()

	if got := reportCoverageMode(CoveragePackage); got != report.CoveragePackage {
		t.Errorf("reportCoverageMode(%s) = %q, want %q", CoveragePackage, got, report.CoveragePackage)
	}
	if got := reportCoverageMode(CoverageOff); got != report.CoverageOff {
		t.Errorf("reportCoverageMode(%s) = %q, want %q", CoverageOff, got, report.CoverageOff)
	}
	if got := reportCoverageMode(""); got != report.CoverageOff {
		t.Errorf("reportCoverageMode(zero) = %q, want %q", got, report.CoverageOff)
	}
	if got := (coverageResult{}).Mode(); got != CoverageOff {
		t.Errorf("the zero coverageResult reports mode %q, want %q", got, CoverageOff)
	}
	if got := (coverageResult{mode: CoveragePackage}).Mode(); got != CoveragePackage {
		t.Errorf("Mode() = %q, want %q", got, CoveragePackage)
	}
	if !report.CoveragePackage.Valid() || !report.CoverageOff.Valid() {
		t.Error("the document refuses one of the two modes it publishes")
	}
	if report.CoverageMode("line").Valid() {
		t.Error("the document accepts a mode it does not define")
	}
}

func TestWarnCodeCarriesAnotherPackagesBlock(t *testing.T) {
	t.Parallel()

	s := &session{}
	s.warnCode(string(coverage.CodeCustomTestCommand), "coverage is off")
	s.warn(CodeSnapshotNotRemoved, "the snapshot survived")

	if len(s.warnings) != 2 {
		t.Fatalf("published %d warnings, want 2", len(s.warnings))
	}
	if s.warnings[0].Code != "GOM7601" || s.warnings[1].Code != string(CodeSnapshotNotRemoved) {
		t.Errorf("warnings = %+v, want the coverage code first and this package's second", s.warnings)
	}
	if slices.Contains(Codes(), Code(coverage.CodeCustomTestCommand)) {
		t.Error("a coverage code is listed in the engine's own block")
	}
}

func TestCoveragePassSkipsARunWithNothingToNarrow(t *testing.T) {
	t.Parallel()

	s := &session{}
	st := &state{results: map[string]report.MutantResult{}, display: map[string]MutantResult{}}

	for _, test := range []struct {
		name string
		runs []execute.MutantRun
		bins []execute.TestBinary
	}{
		{name: "no mutants", bins: []execute.TestBinary{{ImportPath: "example.com/m/a"}}},
		{name: "no binaries", runs: []execute.MutantRun{{ID: "a"}}},
		{name: "neither"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runs, result, err := s.coveragePhase(t.Context(), execute.Options{}, "", "example.com/m",
				test.bins, test.runs, st, config.NarrowingPackage)
			if err != nil {
				t.Fatalf("coveragePhase: %v", err)
			}
			if result.Mode() != CoverageOff {
				t.Errorf("mode = %q, want %q", result.Mode(), CoverageOff)
			}
			if len(runs) != len(test.runs) {
				t.Errorf("the pass returned %d runs for %d", len(runs), len(test.runs))
			}
			if len(s.warnings) != 0 {
				t.Errorf("a skipped pass published %+v", s.warnings)
			}
		})
	}
}

func TestBuildFallsBackToAPlainBuildWhenCoverageWillNotCompile(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	s := &session{clock: tickingClock()}
	s.trace = trace.New(sink, s.now, trace.StartRecord{Kind: trace.StartKindRun})
	opts := execute.Options{CoverPkg: "example.com/m/..."}

	var cov coverageResult
	_, err := s.buildTestBinaries(t.Context(), &opts, &cov)
	if err == nil {
		t.Fatal("buildTestBinaries succeeded against unusable options")
	}
	if opts.CoverPkg != "" {
		t.Errorf("CoverPkg = %q after the fallback, want it cleared", opts.CoverPkg)
	}
	if len(s.warnings) != 1 {
		t.Fatalf("published %d warnings, want the one that says coverage was given up: %+v",
			len(s.warnings), s.warnings)
	}
	w := s.warnings[0]
	if w.Code != string(coverage.CodeUnavailable) {
		t.Errorf("code = %q, want %q", w.Code, coverage.CodeUnavailable)
	}
	if !strings.Contains(w.Message, "coverage instrumentation") {
		t.Errorf("the warning does not say what was given up:\n%s", w.Message)
	}

	var details []string
	for _, e := range sink.Events() {
		if e.Type == trace.TypeStage && e.Stage.Name == "build-binaries" && e.Stage.State == trace.StateStarted {
			details = append(details, e.Stage.Detail)
		}
	}
	if !slices.Equal(details, []string{"coverage", "plain"}) {
		t.Errorf("recorded the build stages %v, want the coverage one and then the plain one", details)
	}

	var notes []trace.NoteRecord
	for _, e := range sink.Events() {
		if e.Type == trace.TypeNote && e.Note.Kind == trace.NoteCoverageUnavailable {
			notes = append(notes, *e.Note)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("recorded %d coverage-unavailable notes, want one: %+v", len(notes), notes)
	}
	if notes[0].Detail != cov.coverageFallback {
		t.Errorf("the note carries\n%s\nwant the whole kept failure\n%s", notes[0].Detail, cov.coverageFallback)
	}
	if notes[0].Detail == w.Message {
		t.Error("the note is the warning again rather than the failure underneath it")
	}
}

func TestPlainBuildFailureIsNotACoverageWarning(t *testing.T) {
	t.Parallel()

	s := &session{}
	opts := execute.Options{}

	var cov coverageResult
	if _, err := s.buildTestBinaries(t.Context(), &opts, &cov); err == nil {
		t.Fatal("buildTestBinaries succeeded against unusable options")
	}
	if len(s.warnings) != 0 {
		t.Errorf("a run that never asked for coverage published %+v", s.warnings)
	}
	if cov.coverageFallback != "" {
		t.Errorf("a run that never asked for coverage kept a coverage failure:\n%s", cov.coverageFallback)
	}
}

func TestInterruptedBuildIsNotRetried(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	s := &session{}
	opts := execute.Options{
		Toolchain:    gocmd.Toolchain{GoBin: filepath.Join("tools", "bin", "go")},
		SnapshotRoot: t.TempDir(),
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		CoverPkg:     "example.com/m/...",
	}

	var cov coverageResult
	_, err := s.buildTestBinaries(ctx, &opts, &cov)
	if err == nil {
		t.Fatal("buildTestBinaries succeeded with a cancelled context")
	}
	if !interrupted(err) {
		t.Fatalf("error = %v, want a cancellation", err)
	}
	if len(s.warnings) != 0 {
		t.Errorf("a cancelled build published %+v", s.warnings)
	}
	if opts.CoverPkg == "" {
		t.Error("a cancelled build gave up coverage, which it has no reason to decide")
	}
	if cov.coverageFallback != "" {
		t.Errorf("a cancelled build kept a coverage failure that never happened:\n%s", cov.coverageFallback)
	}
}

func TestUsableProfilesRefusesASetThatSaysNothing(t *testing.T) {
	t.Parallel()

	block := coverage.Block{File: "example.com/m/a.go", StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 1, NumStmt: 1}
	tests := []struct {
		name     string
		profiles map[string]coverage.Profile
		wantErr  bool
	}{
		{name: "no profiles at all", profiles: nil, wantErr: true},
		{
			name:     "one empty profile",
			profiles: map[string]coverage.Profile{"example.com/m/a": {Mode: "set"}},
			wantErr:  true,
		},
		{
			name: "every profile empty",
			profiles: map[string]coverage.Profile{
				"example.com/m/a": {Mode: "set"},
				"example.com/m/b": {Mode: "set"},
			},
			wantErr: true,
		},
		{
			name: "one profile with blocks is enough",
			profiles: map[string]coverage.Profile{
				"example.com/m/a": {Mode: "set"},
				"example.com/m/b": {Mode: "set", Blocks: []coverage.Block{block}},
			},
			wantErr: false,
		},
		{
			name: "blocks that were never reached still count as data",
			profiles: map[string]coverage.Profile{
				"example.com/m/a": {Mode: "set", Blocks: []coverage.Block{block}},
			},
			wantErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := usable(test.profiles)
			if (err != nil) != test.wantErr {
				t.Fatalf("usable = %v, want an error: %t", err, test.wantErr)
			}
			if err != nil && coverage.CodeOf(err) != coverage.CodeUnavailable {
				t.Errorf("code = %q, want %q (%v)", coverage.CodeOf(err), coverage.CodeUnavailable, err)
			}
		})
	}
}

func TestTheCoverageWarningCarriesTheWholeReasonWhenThereIsMoreOfIt(t *testing.T) {
	t.Parallel()

	const whole = "the test binaries do not compile with coverage instrumentation\n" +
		"# example.com/m [example.com/m.test]\n" +
		"./m_test.go:9:2: undefined: helper"

	s := &session{}
	s.unavailableInFull("the test binaries do not compile with coverage instrumentation", whole)
	if len(s.warnings) != 1 {
		t.Fatalf("published %d warnings, want 1", len(s.warnings))
	}
	if got := s.warnings[0].Detail; got != whole {
		t.Errorf("Detail = %q, want the whole reason %q", got, whole)
	}
	if strings.ContainsAny(s.warnings[0].Message, "\n\r") {
		t.Errorf("the message grew the detail: %q", s.warnings[0].Message)
	}

	short := &session{}
	short.unavailable("`go tool covdata` is not in this toolchain.")
	if got := short.warnings[0].Detail; got != "" {
		t.Errorf("Detail = %q for a reason the message already states in full", got)
	}
}
