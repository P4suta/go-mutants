// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func assertCatalogInvariants(t *testing.T, c gomutants.Catalog, w *gomutants.Workspace) {
	t.Helper()

	if c.Digest == "" || c.WorkspaceDigest == "" || c.ModulePath == "" || c.Toolchain == "" {
		t.Errorf("catalogue identity is incomplete: digest=%q workspace=%q module=%q toolchain=%q",
			c.Digest, c.WorkspaceDigest, c.ModulePath, c.Toolchain)
	}
	if !isDigest64(c.PreparedDigest) {
		t.Errorf("catalogue PreparedDigest = %q, want 64 lowercase hex characters; it is what a"+
			" consumer keys prepared evidence on, and a half-built key is worse than none",
			c.PreparedDigest)
	}
	if got, want := c.Toolchain, w.ToolchainVersion(); got != want {
		t.Errorf("catalogue toolchain = %q, want the workspace's %q", got, want)
	}
	if len(c.TestPackages) == 0 {
		t.Error("the catalogue names no test packages; a session with no binary can measure nothing")
	}
	if c.Selection != nil {
		t.Errorf("the catalogue carries Selection %+v for a preparation that asked for none;"+
			" nil is what says every mutant is selected", c.Selection)
	}

	seenPackage := make(map[string]bool, len(c.TestPackages))
	for _, pkg := range c.TestPackages {
		if pkg == "" {
			t.Error("the catalogue names an empty test package")
		}
		if seenPackage[pkg] {
			t.Errorf("test package %q is listed twice", pkg)
		}
		seenPackage[pkg] = true
	}

	accepted := make(map[string]bool, len(c.Mutants))
	seenID := make(map[string]bool, len(c.Mutants))
	seenDisplay := make(map[string]bool, len(c.Mutants))
	for i, m := range c.Mutants {
		if !isDigest64(m.ID) {
			t.Errorf("mutant %d has ID %q, want 64 lowercase hex characters", i, m.ID)
		}
		if seenID[m.ID] {
			t.Errorf("mutant ID %s appears twice", m.ID)
		}
		seenID[m.ID] = true
		if m.DisplayID == "" || !strings.HasPrefix(m.ID, m.DisplayID) {
			t.Errorf("mutant %s has DisplayID %q, want a non-empty prefix of its ID", m.ID, m.DisplayID)
		}
		if seenDisplay[m.DisplayID] {
			t.Errorf("display ID %s appears twice; it is what a user types", m.DisplayID)
		}
		seenDisplay[m.DisplayID] = true
		if m.Index != uint32(i) {
			t.Errorf("mutant %s is at position %d and carries Index %d; the probe log's"+
				" indices address this slice", m.DisplayID, i, m.Index)
		}
		if !moduleRelativeSlashPath(m.Path) {
			t.Errorf("mutant %s has Path %q, want a module-relative '/'-separated path", m.DisplayID, m.Path)
		}
		if m.Package == "" {
			t.Errorf("mutant %s names no package", m.DisplayID)
		}
		if m.Line < 1 || m.Column < 1 {
			t.Errorf("mutant %s is at %d:%d, want 1-based coordinates", m.DisplayID, m.Line, m.Column)
		}
		if want := m.Line + strings.Count(m.Original, "\n"); m.EndLine != want {
			t.Errorf("mutant %s has EndLine %d and covers %q from line %d, which ends on line %d;"+
				" a consumer selecting by line range applies exactly this rule and would miss it",
				m.DisplayID, m.EndLine, m.Original, m.Line, want)
		}
		if m.StartByte > m.EndByte {
			t.Errorf("mutant %s spans [%d,%d), which is empty backwards", m.DisplayID, m.StartByte, m.EndByte)
		}
		if m.Original == m.Replacement {
			t.Errorf("mutant %s replaces %q with itself, which mutates nothing", m.DisplayID, m.Original)
		}
		if m.Rule == "" || m.RuleVersion < 1 {
			t.Errorf("mutant %s was produced by rule %q version %d, want a named rule of version 1 or later",
				m.DisplayID, m.Rule, m.RuleVersion)
		}
		if !isDigest64(m.SourceDigest) {
			t.Errorf("mutant %s has SourceDigest %q, want 64 lowercase hex characters", m.DisplayID, m.SourceDigest)
		}
		if !m.Selected {
			t.Errorf("mutant %s is not Selected in a session prepared with no selection;"+
				" a consumer that never narrows would skip it", m.DisplayID)
		}
		if m.Probed && !m.Accepted {
			t.Errorf("mutant %s is Probed and not Accepted; a mutant that is never executed"+
				" must not carry a probe status a consumer would read as a fact", m.DisplayID)
		}
		accepted[m.ID] = m.Accepted
	}

	rejected := make(map[string]bool, len(c.Rejections))
	for _, r := range c.Rejections {
		if rejected[r.ID] {
			t.Errorf("rejection %s appears twice", r.ID)
		}
		rejected[r.ID] = true
		if _, catalogued := accepted[r.ID]; !catalogued {
			t.Errorf("rejection %s names no catalogued mutant; a mutant that vanished into a"+
				" rejection nothing can look up is the silence rejections exist to prevent", r.ID)
		}
		if accepted[r.ID] {
			t.Errorf("mutant %s is rejected and Accepted", r.ID)
		}
	}
	for id, ok := range accepted {
		if !ok && !rejected[id] {
			t.Errorf("mutant %s is not Accepted and carries no rejection saying why", id)
		}
	}
}

func isDigest64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func moduleRelativeSlashPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return false
	}
	for element := range strings.SplitSeq(path, "/") {
		if element == "" || element == "." || element == ".." {
			return false
		}
	}
	return true
}

func TestModuleInvariants(t *testing.T) {
	t.Parallel()

	prepared := probeable(t)
	module, err := prepared.workspace.Module(t.Context(), gomutants.ModuleQuery{})
	if err != nil {
		t.Fatalf("listing a prepared workspace: %v", err)
	}

	if module.Path != prepared.catalog.ModulePath {
		t.Errorf("Module.Path = %q, want the catalogue's %q", module.Path, prepared.catalog.ModulePath)
	}
	if got, want := module.Toolchain, prepared.workspace.ToolchainVersion(); got != want {
		t.Errorf("Module.Toolchain = %q, want the workspace's %q", got, want)
	}
	if len(module.Packages) == 0 {
		t.Fatal("the listing names no packages, so every claim below holds vacuously")
	}

	paths := make([]string, 0, len(module.Packages))
	for _, pkg := range module.Packages {
		paths = append(paths, pkg.ImportPath)
	}
	if !slices.IsSorted(paths) {
		t.Errorf("Packages are not sorted by ImportPath: %v", paths)
	}
	if len(slices.Compact(slices.Clone(paths))) != len(paths) {
		t.Errorf("Packages name the same import path twice: %v", paths)
	}

	for _, pkg := range module.Packages {
		if pkg.ImportPath == "" || pkg.Name == "" {
			t.Errorf("a package is unnamed: %+v", pkg)
		}
		if !filepath.IsAbs(pkg.Dir) {
			t.Errorf("%s has Dir %q, which is not absolute", pkg.ImportPath, pkg.Dir)
		}
		if !underRoot(t, prepared.parent, pkg.Dir) {
			t.Errorf("%s has Dir %q, which is not inside the workspace's temporary parent %q",
				pkg.ImportPath, pkg.Dir, prepared.parent)
		}
		if info, statErr := os.Stat(pkg.Dir); statErr != nil || !info.IsDir() {
			t.Errorf("%s has Dir %q, which is not a directory on disk: %v", pkg.ImportPath, pkg.Dir, statErr)
		}
		if want := len(pkg.TestGoFiles)+len(pkg.XTestGoFiles) != 0; pkg.HasTests != want {
			t.Errorf("%s HasTests = %v, want %v for TestGoFiles=%v XTestGoFiles=%v",
				pkg.ImportPath, pkg.HasTests, want, pkg.TestGoFiles, pkg.XTestGoFiles)
		}
	}

	root := ""
	for _, pkg := range module.Packages {
		if pkg.ImportPath == module.Path {
			root = pkg.Dir
		}
	}
	if root == "" {
		t.Fatalf("the listing holds no package at the module root %q, only %v", module.Path, paths)
	}
	declared, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading the frozen go.mod: %v", err)
	}
	if !strings.Contains(string(declared), "\ngo "+module.GoVersion+"\n") {
		t.Errorf("GoVersion = %q, which the frozen go.mod does not declare:\n%s", module.GoVersion, declared)
	}
}

func TestCatalogInvariants(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		fixture        func(*testing.T) *preparedFixture
		wantRejections bool
	}{
		{name: "with a probe tree", fixture: probeable},
		{name: "without a probe tree", fixture: unprobeable},
		{name: "with rejections", fixture: rejectable, wantRejections: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			prepared := test.fixture(t)
			assertCatalogInvariants(t, prepared.catalog, prepared.workspace)
			if test.wantRejections && len(prepared.catalog.Rejections) == 0 {
				t.Error("the fixture chosen for its rejections produced none, so every" +
					" clause about one held vacuously")
			}
		})
	}
}

func TestProbeResultInvariants(t *testing.T) {
	t.Parallel()
	prepared := probeable(t)
	result := probeOf(t, prepared.session, gomutants.ProbeRequest{Package: probeableModule})

	if len(result.Infected) < 2 {
		t.Errorf("infected = %v; the fixture has two probed mutants that different tests infect,"+
			" and one index cannot witness an ordering", result.Infected)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0: a measured pass is one every binary exited zero for", result.ExitCode)
	}
	if result.Duration < 0 {
		t.Errorf("duration = %s, want a non-negative wall-clock time", result.Duration)
	}
	for i := 1; i < len(result.Infected); i++ {
		if result.Infected[i] <= result.Infected[i-1] {
			t.Errorf("infected = %v, want strictly ascending catalogue indices", result.Infected)
			break
		}
	}
	for _, index := range result.Infected {
		if uint64(index) >= uint64(len(prepared.catalog.Mutants)) {
			t.Errorf("infected names index %d, and the catalogue holds %d mutants",
				index, len(prepared.catalog.Mutants))
			continue
		}
		mutant := prepared.catalog.Mutants[index]
		if !mutant.Probed {
			t.Errorf("infected names %s, which is not Probed; only a probed mutant"+
				" has anything compiled that could record it", mutant.DisplayID)
		}
		if !mutant.Accepted {
			t.Errorf("infected names %s, which validation rejected; nothing will execute it,"+
				" so an infection fact about it licenses nothing", mutant.DisplayID)
		}
	}
}

func TestMutantResultInvariants(t *testing.T) {
	t.Parallel()
	prepared := probeable(t)
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)
	label := mutantkit.APIByRule(t, prepared.catalog, labelRule)

	for _, test := range []struct {
		name   string
		mutant gomutants.Mutant
		want   gomutants.Outcome
	}{
		{name: "a kill", mutant: width, want: gomutants.OutcomeKilled},
		{name: "a survivor", mutant: label, want: gomutants.OutcomeSurvived},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
				Mutant:  test.mutant.ID,
				Package: probeableModule,
				Args:    []string{"-test.run=^TestWidth$"},
			})
			if err != nil {
				t.Fatalf("executing %s: %v", test.mutant.DisplayID, err)
			}
			if !slices.Contains([]gomutants.Outcome{
				gomutants.OutcomeNotRun,
				gomutants.OutcomeKilled,
				gomutants.OutcomeSurvived,
				gomutants.OutcomeTimedOut,
				gomutants.OutcomeInconclusive,
				gomutants.OutcomeErrored,
			}, result.Outcome) {
				t.Errorf("outcome %q is outside the vocabulary", result.Outcome)
			}
			if result.Outcome != test.want {
				t.Fatalf("outcome = %s, want %s:\n%s", result.Outcome, test.want, result.OutputTail)
			}
			if result.Duration < 0 {
				t.Errorf("duration = %s, want a non-negative wall-clock time", result.Duration)
			}
			if result.ID != test.mutant.ID {
				t.Errorf("result ID = %q, want the full ID %q", result.ID, test.mutant.ID)
			}
			if result.DisplayID == "" || !strings.HasPrefix(result.ID, result.DisplayID) {
				t.Errorf("result DisplayID = %q, want a prefix of %q", result.DisplayID, result.ID)
			}
			decided := result.Outcome == gomutants.OutcomeKilled || result.Outcome == gomutants.OutcomeTimedOut
			if decided != (result.KilledBy != "") {
				t.Errorf("outcome %s carries KilledBy %q; a package is named exactly for a kill"+
					" and a timeout", result.Outcome, result.KilledBy)
			}
			if test.want == gomutants.OutcomeSurvived {
				if result.Output != nil {
					t.Errorf("a survivor carries %d bytes of Output, want none: holding it for every"+
						" mutant in a run is how a consumer runs out of memory", len(result.Output))
				}
				if result.Truncated || result.TotalBytes != 0 {
					t.Errorf("a survivor reports Truncated = %v and TotalBytes = %d, want false and 0",
						result.Truncated, result.TotalBytes)
				}
				return
			}
			if len(result.Output) == 0 {
				t.Fatal("a kill carries no Output, so the evidence for it cannot be shown")
			}
			if want := lastLines(result.Output, 50); result.OutputTail != want {
				t.Errorf("OutputTail = %q, want the last 50 lines of Output with the carriage"+
					" returns stripped: %q", result.OutputTail, want)
			}
			if result.TotalBytes < int64(len(result.Output)) {
				t.Errorf("TotalBytes = %d, want at least the %d bytes that were kept",
					result.TotalBytes, len(result.Output))
			}
		})
	}
}

func TestResultsCarryTraceSeqAndBinaries(t *testing.T) {
	t.Parallel()

	pinType[int64](gomutants.CommandResult{}.TraceSeq)
	pinType[[]string](gomutants.MutantResult{}.Binaries)
	pinType[int64](gomutants.MutantResult{}.TraceSeq)
	pinType[[]string](gomutants.ProbeResult{}.Binaries)
	pinType[int64](gomutants.ProbeResult{}.TraceSeq)
	pinType[[]string](gomutants.ControlResult{}.Binaries)
	pinType[[]int64](gomutants.ControlResult{}.ExecSeqs)
	pinType[int64](gomutants.ControlResult{}.TraceSeq)

	for name, seq := range map[string]int64{
		"CommandResult": gomutants.CommandResult{}.TraceSeq,
		"MutantResult":  gomutants.MutantResult{}.TraceSeq,
		"ProbeResult":   gomutants.ProbeResult{}.TraceSeq,
		"ControlResult": gomutants.ControlResult{}.TraceSeq,
	} {
		if seq != 0 {
			t.Errorf("the zero %s carries TraceSeq %d, want 0", name, seq)
		}
	}
	if (gomutants.MutantResult{}).Binaries != nil || (gomutants.ProbeResult{}).Binaries != nil ||
		(gomutants.ControlResult{}).Binaries != nil || (gomutants.ControlResult{}).ExecSeqs != nil {
		t.Error("a zero result names test binaries it never ran")
	}
}

func TestTestLogSurfaceIsPinned(t *testing.T) {
	t.Parallel()

	pinType[bool](gomutants.ExecRequest{}.RecordTestLog)
	pinType[bool](gomutants.ProbeRequest{}.RecordTestLog)
	pinType[bool](gomutants.ControlRequest{}.RecordTestLog)
	pinType[[]gomutants.TestLog](gomutants.MutantResult{}.TestLogs)
	pinType[[]gomutants.TestLog](gomutants.ProbeResult{}.TestLogs)
	pinType[[]gomutants.TestLog](gomutants.ControlResult{}.TestLogs)
	pinType[string](gomutants.TestLog{}.Package)
	pinType[string](gomutants.TestLog{}.Dir)
	pinType[[]gomutants.TestLogEntry](gomutants.TestLog{}.Entries)
	pinType[bool](gomutants.TestLog{}.Complete)
	pinType[string](gomutants.TestLog{}.Err)
	pinType[gomutants.TestLogOp](gomutants.TestLogEntry{}.Op)
	pinType[string](gomutants.TestLogEntry{}.Name)

	if (gomutants.MutantResult{}).TestLogs != nil || (gomutants.ProbeResult{}).TestLogs != nil ||
		(gomutants.ControlResult{}).TestLogs != nil {
		t.Error("a zero result carries test logs nothing recorded")
	}
	if (gomutants.ExecRequest{}).RecordTestLog || (gomutants.ProbeRequest{}).RecordTestLog ||
		(gomutants.ControlRequest{}).RecordTestLog {
		t.Error("the zero request records a test log; it is a file per binary per call, so it" +
			" is asked for rather than assumed")
	}
	if !errors.Is(fmt.Errorf("wrapped: %w", gomutants.ErrTestLogUnsupported),
		gomutants.ErrTestLogUnsupported) {
		t.Error("ErrTestLogUnsupported does not survive wrapping")
	}
}

func TestVocabulariesArePinned(t *testing.T) {
	t.Parallel()

	for name, pair := range map[string][2]string{
		"OutcomeNotRun":       {string(gomutants.OutcomeNotRun), "not_run"},
		"OutcomeKilled":       {string(gomutants.OutcomeKilled), "killed"},
		"OutcomeSurvived":     {string(gomutants.OutcomeSurvived), "survived"},
		"OutcomeTimedOut":     {string(gomutants.OutcomeTimedOut), "timed_out"},
		"OutcomeInconclusive": {string(gomutants.OutcomeInconclusive), "inconclusive"},
		"OutcomeErrored":      {string(gomutants.OutcomeErrored), "errored"},

		"ProbeMeasured":    {string(gomutants.ProbeMeasured), "measured"},
		"ProbeTestFailed":  {string(gomutants.ProbeTestFailed), "test-failed"},
		"ProbeTimedOut":    {string(gomutants.ProbeTimedOut), "timed-out"},
		"ProbeUnavailable": {string(gomutants.ProbeUnavailable), "unavailable"},

		"PrepareEventStarted":  {string(gomutants.PrepareEventStarted), "started"},
		"PrepareEventFinished": {string(gomutants.PrepareEventFinished), "finished"},

		"PreparePhaseSucceeded": {string(gomutants.PreparePhaseSucceeded), "succeeded"},
		"PreparePhaseFailed":    {string(gomutants.PreparePhaseFailed), "failed"},
		"PreparePhaseSkipped":   {string(gomutants.PreparePhaseSkipped), "skipped"},

		"ChangeAdded":    {string(gomutants.ChangeAdded), "added"},
		"ChangeRemoved":  {string(gomutants.ChangeRemoved), "removed"},
		"ChangeModified": {string(gomutants.ChangeModified), "modified"},

		"TestLogGetenv": {string(gomutants.TestLogGetenv), "getenv"},
		"TestLogOpen":   {string(gomutants.TestLogOpen), "open"},
		"TestLogStat":   {string(gomutants.TestLogStat), "stat"},
		"TestLogChdir":  {string(gomutants.TestLogChdir), "chdir"},

		"BranchDecreasing": {gomutants.BranchDecreasing, "decreasing"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}

	want := []gomutants.PreparePhase{
		"discovery",
		"probe_snapshot",
		"main_validation",
		"main_restoration",
		"verification",
		"binary_build",
		"probe_validation",
		"probe_coverage_build",
		"probe_restoration",
	}
	if got := gomutants.KnownPreparePhases(); !slices.Equal(got, want) {
		t.Errorf("KnownPreparePhases() = %v, want %v", got, want)
	}
	if first := gomutants.KnownPreparePhases(); len(first) > 0 {
		first[0] = "clobbered"
		if gomutants.KnownPreparePhases()[0] == "clobbered" {
			t.Error("KnownPreparePhases handed out a slice a caller can rewrite")
		}
	}
}

func TestKnownPreparePhasesMatchWhatPrepareEmits(t *testing.T) {
	t.Parallel()
	known := gomutants.KnownPreparePhases()
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) *preparedFixture
	}{
		{name: "with a probe tree", fixture: probeable},
		{name: "without a probe tree", fixture: unprobeable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			order, problems := phaseEventProblems(test.fixture(t).events)
			for _, problem := range problems {
				t.Error(problem)
			}
			if !slices.Equal(order, known) {
				t.Errorf("Prepare started the phases %v, and KnownPreparePhases lists %v", order, known)
			}
		})
	}
}

type phaseProgress int

const (
	phaseUnseen phaseProgress = iota
	phaseOpen
	phaseClosed
)

func phaseEventProblems(events []gomutants.PrepareEvent) ([]gomutants.PreparePhase, []string) {
	progress := make(map[gomutants.PreparePhase]phaseProgress, len(events))
	var order []gomutants.PreparePhase
	var problems []string
	note := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	for i, event := range events {
		switch event.State {
		case gomutants.PrepareEventStarted:
			switch progress[event.Phase] {
			case phaseUnseen:
				order = append(order, event.Phase)
				progress[event.Phase] = phaseOpen
			case phaseOpen:
				note("event %d starts %s again while it is still running", i, event.Phase)
			case phaseClosed:
				note("event %d starts %s again after it finished", i, event.Phase)
			}
		case gomutants.PrepareEventFinished:
			switch progress[event.Phase] {
			case phaseUnseen:
				note("event %d finishes %s, which never started", i, event.Phase)
			case phaseOpen:
				progress[event.Phase] = phaseClosed
			case phaseClosed:
				note("event %d finishes %s, which had already finished", i, event.Phase)
			}
		default:
			note("event %d carries state %q, which is neither started nor finished", i, event.State)
		}
	}
	for _, phase := range order {
		if progress[phase] != phaseClosed {
			note("phase %s started and never finished", phase)
		}
	}
	return order, problems
}

func TestPhaseEventsAreOneStartAndOneFinishEachPhase(t *testing.T) {
	t.Parallel()

	const one, two = gomutants.PreparePhaseDiscovery, gomutants.PreparePhaseVerification
	start := func(phase gomutants.PreparePhase) gomutants.PrepareEvent {
		return gomutants.PrepareEvent{Phase: phase, State: gomutants.PrepareEventStarted}
	}
	finish := func(phase gomutants.PreparePhase) gomutants.PrepareEvent {
		return gomutants.PrepareEvent{
			Phase: phase, State: gomutants.PrepareEventFinished, Result: gomutants.PreparePhaseSucceeded,
		}
	}

	for _, test := range []struct {
		name         string
		events       []gomutants.PrepareEvent
		wantOrder    []gomutants.PreparePhase
		wantProblems int
	}{
		{
			name:      "a well formed pair of phases",
			events:    []gomutants.PrepareEvent{start(one), finish(one), start(two), finish(two)},
			wantOrder: []gomutants.PreparePhase{one, two},
		},
		{
			name:      "two phases open at once",
			events:    []gomutants.PrepareEvent{start(one), start(two), finish(two), finish(one)},
			wantOrder: []gomutants.PreparePhase{one, two},
		},
		{
			name:         "a start after the phase already finished",
			events:       []gomutants.PrepareEvent{start(one), finish(one), start(one)},
			wantOrder:    []gomutants.PreparePhase{one},
			wantProblems: 1,
		},
		{
			name:         "a doubled start",
			events:       []gomutants.PrepareEvent{start(one), start(one), finish(one)},
			wantOrder:    []gomutants.PreparePhase{one},
			wantProblems: 1,
		},
		{
			name:         "a doubled finish",
			events:       []gomutants.PrepareEvent{start(one), finish(one), finish(one)},
			wantOrder:    []gomutants.PreparePhase{one},
			wantProblems: 1,
		},
		{
			name:         "a finish with no start",
			events:       []gomutants.PrepareEvent{finish(one)},
			wantProblems: 1,
		},
		{
			name:         "a start with no finish",
			events:       []gomutants.PrepareEvent{start(one)},
			wantOrder:    []gomutants.PreparePhase{one},
			wantProblems: 1,
		},
		{
			name:         "a state that is neither",
			events:       []gomutants.PrepareEvent{{Phase: one, State: "paused"}},
			wantProblems: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			order, problems := phaseEventProblems(test.events)
			if !slices.Equal(order, test.wantOrder) {
				t.Errorf("order = %v, want %v", order, test.wantOrder)
			}
			if len(problems) != test.wantProblems {
				t.Errorf("problems = %d (%v), want %d", len(problems), problems, test.wantProblems)
			}
		})
	}
}

func TestPreparedDigestDiffersWithProbe(t *testing.T) {
	t.Parallel()

	probed := probeable(t)
	unprobed := unprobeable(t)

	if probed.catalog.Digest != unprobed.catalog.Digest {
		t.Fatalf("the two sessions catalogued different mutant sets (%s and %s);"+
			" the claim below is about two preparations of one set",
			probed.catalog.Digest, unprobed.catalog.Digest)
	}
	differs := false
	for i, mutant := range probed.catalog.Mutants {
		if mutant.Probed != unprobed.catalog.Mutants[i].Probed {
			differs = true
			break
		}
	}
	if !differs {
		t.Fatal("no mutant is Probed in one session and not the other, so the probe tree" +
			" changed nothing this digest could have noticed")
	}

	if probed.catalog.PreparedDigest == unprobed.catalog.PreparedDigest {
		t.Errorf("both sessions carry PreparedDigest %s; a consumer keyed on it would reuse"+
			" evidence gathered where an absent mutant meant something else",
			probed.catalog.PreparedDigest)
	}
	for _, digest := range []string{probed.catalog.PreparedDigest, unprobed.catalog.PreparedDigest} {
		if !isDigest64(digest) {
			t.Errorf("PreparedDigest = %q, want 64 lowercase hex characters", digest)
		}
	}
}

func TestPreparedDigestIsStableAcrossTwoPreparationsOfOneTree(t *testing.T) {
	t.Parallel()

	first := unprobeable(t)
	second := prepareProbeable(false)
	if second.err != nil {
		t.Fatalf("preparing the probeable fixture a second time: %v", second.err)
	}

	if got, want := second.catalog.Digest, first.catalog.Digest; got != want {
		t.Fatalf("the second preparation catalogued %s and the first %s;"+
			" the two are meant to be the same tree", got, want)
	}
	if got, want := second.catalog.PreparedDigest, first.catalog.PreparedDigest; got != want {
		t.Errorf("PreparedDigest = %s on the second preparation and %s on the first;"+
			" two preparations of one tree must be interchangeable, and a key that"+
			" moves with the temporary directory never hits", got, want)
	}
}
