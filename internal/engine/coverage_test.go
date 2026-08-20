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
)

// TestCoverageIsOnOnlyForTheBuiltInTestCommand pins the rule the whole feature
// switches on.
//
// It is not a preference and there is nothing to configure: the mapping is
// between a test binary and the lines it reached, and it is sound exactly when
// go-mutants compiled the binaries itself and knows what each one is. Anything
// else — a wrapper, an extra flag, a different program — is a command whose
// coverage cannot be attributed, and a wrong attribution costs a kill rather
// than costing time.
func TestCoverageIsOnOnlyForTheBuiltInTestCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command []string
		want    bool
	}{
		{name: "the built-in default", command: config.DefaultTestCommand(), want: true},
		{
			// The same argv written out by hand, which is what a project that
			// pins `test.command` in its configuration file most often has.
			name: "the default spelled out", command: []string{"go", "test", "./..."}, want: true,
		},
		{name: "one extra flag", command: []string{"go", "test", "-count=1", "./..."}, want: false},
		{name: "a narrower pattern", command: []string{"go", "test", "./internal/..."}, want: false},
		{name: "another program", command: []string{"gotestsum", "--", "./..."}, want: false},
		{name: "a shell script", command: []string{"./scripts/test.sh"}, want: false},
		{name: "the flags reordered", command: []string{"go", "test", "./...", ""}, want: false},
		{name: "nothing at all", command: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := coverageEnabled(test.command); got != test.want {
				t.Errorf("coverageEnabled(%q) = %t, want %t", test.command, got, test.want)
			}
		})
	}
}

// TestCustomTestCommandWarningNamesBothCommands is what makes the rule above
// diagnosable.
//
// A user who has just set `test.command` and noticed the run got slower needs
// three things in one line: which command they wrote, which one would have
// enabled the optimisation, and what the run is doing instead.
func TestCustomTestCommandWarningNamesBothCommands(t *testing.T) {
	t.Parallel()

	message := customTestCommand([]string{"go", "test", "-count=1", "./..."})
	for _, needle := range []string{
		`"go test -count=1 ./..."`,
		`"go test ./..."`,
		"every mutant will be measured against every one of them",
	} {
		if !strings.Contains(message, needle) {
			t.Errorf("the warning does not mention %q:\n%s", needle, message)
		}
	}
	if strings.ContainsAny(message, "\n\r") {
		t.Errorf("the warning is not one line: %q", message)
	}
}

// TestUnavailableWarningSaysWhatTheRunWillDoInstead covers the fail-open
// message, whose second half is the load-bearing one: a warning saying only
// that coverage failed leaves a reader wondering whether the results can be
// trusted, and they can.
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
	// One line, with no doubled full stop where the cause's own punctuation met
	// the clause after it.
	if strings.ContainsAny(w.Message, "\n\r") {
		t.Errorf("the warning is not one line: %q", w.Message)
	}
	if strings.Contains(w.Message, ".;") {
		t.Errorf("the warning has the cause's full stop in the middle of it: %q", w.Message)
	}
}

// TestCoverageMutantsDerivesTheLineIntervalFromTheOriginal is the join between
// the catalogue and the mapping.
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

// TestCoverageMutantsLeavesOutAMutantItCannotLocate is the fail-open rule
// applied to one mutant rather than to the run.
//
// A catalogued mutant with no coordinates is documented as impossible. If it
// ever happened, including it here would give it no covering binary and turn it
// into an uncovered survivor — a mutant silently never executed. Leaving it out
// of the mapping instead leaves it with a nil binary list, which is every
// binary.
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

// TestCoveragePhaseKeepsAMutantItCouldNotAskAbout is the other half of the
// same rule, and the reason the phase tracks which mutants it submitted.
//
// A mutant left out of the mapping has no answer, and an absent answer must not
// read as "nothing covers it": that would turn the impossible case — a
// catalogued mutant with no coordinates — into a mutant silently never
// executed, reported as an uncovered survivor. It keeps a nil binary list
// instead, which internal/execute reads as every binary.
func TestCoveragePhaseKeepsAMutantItCouldNotAskAbout(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 8)
	s := &session{events: events}
	st := &state{
		results: map[string]report.MutantResult{},
		display: map[string]MutantResult{
			// No coordinates, so coverageMutants leaves it out.
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

// TestIndicesOfTranslatesCoveringPathsIntoBinaryPositions covers the last hop
// before internal/execute, including the guard that turns an impossible mismatch
// into an unnarrowed mutant rather than a failed one.
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
	// Nil rather than an empty slice, because internal/execute reads nil as
	// "every binary" and an empty subset as a caller bug worth refusing.
	if got := indicesOf([]string{"example.com/m/z"}, index); got != nil {
		t.Errorf("indicesOf of an unknown binary = %v, want nil", got)
	}
	if got := indicesOf(nil, index); got != nil {
		t.Errorf("indicesOf(nil) = %v, want nil", got)
	}
}

// TestRecordUncoveredFilesASurvivorNobodyExecuted pins the three things the
// engine states about a mutant no binary reaches.
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

// TestNotableGroupsUncoveredSurvivorsAfterCoveredOnes is the sub-order inside
// the survivor rank.
//
// Both are survivors and neither outranks the other as a finding, but they call
// for different work — sharpen a test, or write one — so a reader gets the two
// kinds in two runs. It stays a sub-order rather than a rank of its own: an
// uncovered survivor still comes before every timeout.
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
	for _, m := range notable(st, rep) {
		got = append(got, m.ID)
	}
	want := []string{"c-a", "c-z", "u-a", "u-z", "t-a"}
	if !slices.Equal(got, want) {
		t.Errorf("notable = %v, want %v", got, want)
	}
	// And the flag travels, or the renderer cannot say why the mutant survived.
	for _, m := range notable(st, rep) {
		if want := strings.HasPrefix(m.ID, "u-"); m.Uncovered != want {
			t.Errorf("%s: Uncovered = %t, want %t", m.ID, m.Uncovered, want)
		}
	}
}

// TestUncoveredOfCountsTheDocumentRatherThanTheRun keeps the closing summary's
// number a reading of the published report, as every other number in that block
// is.
func TestUncoveredOfCountsTheDocumentRatherThanTheRun(t *testing.T) {
	t.Parallel()

	rep := &report.Report{Mutants: []report.Mutant{
		{ID: "a", Uncovered: true},
		{ID: "b"},
		{ID: "c", Uncovered: true},
	}}
	if got := uncoveredOf(rep); got != 2 {
		t.Errorf("uncoveredOf = %d, want 2", got)
	}
	if got := uncoveredOf(&report.Report{}); got != 0 {
		t.Errorf("uncoveredOf of an empty report = %d, want 0", got)
	}
}

// TestReportCoverageModeMapsBothSpellings holds the engine's enum and the
// document's together, as [TestReportTimeoutSourceMapsBothSpellings] does for
// the other pair.
func TestReportCoverageModeMapsBothSpellings(t *testing.T) {
	t.Parallel()

	if got := reportCoverageMode(CoveragePackage); got != report.CoveragePackage {
		t.Errorf("reportCoverageMode(%s) = %q, want %q", CoveragePackage, got, report.CoveragePackage)
	}
	if got := reportCoverageMode(CoverageOff); got != report.CoverageOff {
		t.Errorf("reportCoverageMode(%s) = %q, want %q", CoverageOff, got, report.CoverageOff)
	}
	// The zero value is a run that never reached the coverage phase, and it has
	// to name a mode rather than the empty string the document would refuse.
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

// TestWarnCodeCarriesAnotherPackagesBlock is why the coverage warnings do not
// need a GOM40xx code of their own.
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
	// The GOM76xx codes stay out of this package's own table: one condition,
	// one identifier, defined next to the rule it is about.
	if slices.Contains(Codes(), Code(coverage.CodeCustomTestCommand)) {
		t.Error("a coverage code is listed in the engine's own block")
	}
}

// TestCoveragePassSkipsARunWithNothingToNarrow is the one shortcut worth
// stating: the pass costs a full run of every test binary, and paying that to
// decide the fate of no mutants is pure loss.
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
			// The options are deliberately unusable: reaching internal/execute
			// at all would fail, so a pass that returns cleanly is a pass that
			// never started.
			runs, result, err := s.coveragePhase(t.Context(), execute.Options{}, "", "example.com/m",
				test.bins, test.runs, st)
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

// TestBuildFallsBackToAPlainBuildWhenCoverageWillNotCompile is the fail-open
// rule applied at the earliest point it can bite.
//
// Coverage is on by default and was never asked for, and a
// `-cover -coverpkg=<module>/...` build reaches packages an ordinary
// `go test -c` of one package does not — so it can fail where the plain build
// would have succeeded. Letting that fail the run would turn a green workspace
// red for the sake of an optimisation, and would report it as
// [execute.CodeTestBuildFailed], whose own documentation reads it as a
// go-mutants bug in the instrumented rewrite.
//
// The options here are deliberately unusable, so both builds fail: what is
// being asserted is that the *second* one was attempted at all, without
// coverage, and that the run said why.
func TestBuildFallsBackToAPlainBuildWhenCoverageWillNotCompile(t *testing.T) {
	t.Parallel()

	s := &session{}
	opts := execute.Options{CoverPkg: "example.com/m/..."}

	_, err := s.buildTestBinaries(t.Context(), &opts)
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
}

// TestPlainBuildFailureIsNotACoverageWarning keeps the fallback from
// misdescribing an ordinary build failure as a coverage problem.
func TestPlainBuildFailureIsNotACoverageWarning(t *testing.T) {
	t.Parallel()

	s := &session{}
	opts := execute.Options{}

	if _, err := s.buildTestBinaries(t.Context(), &opts); err == nil {
		t.Fatal("buildTestBinaries succeeded against unusable options")
	}
	if len(s.warnings) != 0 {
		t.Errorf("a run that never asked for coverage published %+v", s.warnings)
	}
}

// TestInterruptedBuildIsNotRetried is the last fail-open boundary: a cancelled
// run is not a coverage failure, and retrying would spend a second build on a
// run that is already unwinding.
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

	_, err := s.buildTestBinaries(ctx, &opts)
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
}

// TestUsableProfilesRefusesASetThatSaysNothing pins the last fail-open trigger:
// every document parsed, and not one block between them.
//
// A workspace with no statements at all has no mutants either, and this phase
// is only reached when there are some — so an empty profile set is coverage
// collection having silently produced nothing, which is the failure that would
// otherwise report a perfectly tested workspace as entirely uncovered.
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
			// One binary that covered nothing is ordinary; what matters is that
			// something, somewhere, has blocks.
			name: "one profile with blocks is enough",
			profiles: map[string]coverage.Profile{
				"example.com/m/a": {Mode: "set"},
				"example.com/m/b": {Mode: "set", Blocks: []coverage.Block{block}},
			},
			wantErr: false,
		},
		{
			// Blocks with zero counts are the point of the whole mapping: they
			// are what makes a mutant honestly uncovered rather than unknown.
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
