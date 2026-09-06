// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// This file is the engine API's own contract test: the invariants a consumer is
// entitled to assume about every value the API hands out, checked against real
// prepared sessions rather than against hand-built structs.
//
// It is separate from api_integration_test.go because the two ask different
// questions. That file asks whether the engine measured the right thing; this
// one asks whether the answer is well formed whatever it says — every ID a full
// digest, every index its own position, every rejected mutant unaccepted, every
// probed mutant accepted. A consumer that reads a catalogue without checking
// any of it is not being careless: it is relying on this file.
//
// Every test here reuses the sessions api_integration_test.go prepares once for
// the whole package. Preparing another would cost a minute to re-establish
// facts the shared ones already carry.

// assertCatalogInvariants checks everything [gomutants.Catalog] promises,
// against the workspace that produced it.
//
// The workspace is a parameter rather than a field of the catalogue because one
// of the claims spans the two: a catalogue names the toolchain it was prepared
// with, and it has to be the toolchain the workspace located. A consumer that
// keys a cache on Catalog.Toolchain is entitled to that.
func assertCatalogInvariants(t *testing.T, c gomutants.Catalog, w *gomutants.Workspace) {
	t.Helper()

	if c.Digest == "" || c.WorkspaceDigest == "" || c.ModulePath == "" || c.Toolchain == "" {
		t.Errorf("catalogue identity is incomplete: digest=%q workspace=%q module=%q toolchain=%q",
			c.Digest, c.WorkspaceDigest, c.ModulePath, c.Toolchain)
	}
	if got, want := c.Toolchain, w.ToolchainVersion(); got != want {
		t.Errorf("catalogue toolchain = %q, want the workspace's %q", got, want)
	}
	if len(c.TestPackages) == 0 {
		t.Error("the catalogue names no test packages; a session with no binary can measure nothing")
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

// isDigest64 reports whether s is what every identity in this API is: 64
// lowercase hex characters. Uppercase is refused deliberately — a consumer
// comparing digests as strings has to be able to.
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

// moduleRelativeSlashPath reports whether path is what the API promises a
// source path is: relative, '/'-separated, and inside the module.
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

// TestCatalogInvariants checks the catalogue of all three shared sessions.
//
// The probe option is the one preparation flag that changes what a catalogue
// says about a mutant, so a claim that held only for the session that happens
// to build a probe tree would be a claim about that option rather than about
// the type. The third row is there for a different reason: probeable's three
// mutants all compile, so every clause about a rejection is *vacuous* over it —
// rejection IDs are unique because there are none, and "not accepted implies
// rejected" holds because everything is accepted. fixtures/rejectable holds
// three candidates whose mutated copy is not a program beside sixteen that
// compile, which is what makes those clauses bite.
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
			// Otherwise the row that exists to make the rejection clauses bite
			// could quietly stop doing so — a rule the fixture depends on being
			// selected, a compiler that started accepting a trap — and the
			// clauses would go on passing over nothing.
			if test.wantRejections && len(prepared.catalog.Rejections) == 0 {
				t.Error("the fixture chosen for its rejections produced none, so every" +
					" clause about one held vacuously")
			}
		})
	}
}

// TestProbeResultInvariants checks the shape of a measured pass.
//
// The content and the shape are different claims and both are load-bearing.
// TestProbeReportsTheMutantsATestInfected says the right mutants are named;
// this one says the set is one a consumer can index the catalogue with directly
// — ascending, distinct, in range, and never naming a mutant nothing could have
// recorded, or one no execution will ever run — which is what lets a caller
// skip a bounds check it would otherwise have to write and would otherwise get
// wrong.
//
// The whole package is probed rather than one test, because "strictly
// ascending" is a claim about a pair and a one-element set cannot break it. The
// fixture's two probed mutants are infected by two different tests, so a
// package-wide pass is the cheapest set with two indices in it. probeOf has
// already established that the pass was measured and that Infected is a set
// rather than nil.
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

// TestMutantResultInvariants checks the shape of the kill
// TestEveryKillIsPrecededByAnInfection establishes the meaning of, and of a
// survivor beside it.
//
// KilledBy is the field the pair exists for. It is a promise with two
// directions — a killed or timed-out result names the package that decided it,
// and every other outcome names nothing — and a consumer rendering "killed by"
// from an empty string, or ignoring the one it was given, needs both halves.
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
			// Before the expected-outcome check rather than after it: a result
			// carrying something outside the vocabulary is exactly the case the
			// expectation would fail on, and reporting it as "wanted killed" would
			// hide the more interesting half of what went wrong.
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
		})
	}
}

// TestVocabulariesArePinned writes out every string constant a consumer may
// have serialized, so that changing one is a decision rather than an accident.
//
// A renamed constant is a compile error for a consumer; a changed *value* is
// not. It is a run that reads back a cache written yesterday, sees an outcome
// it has no case for, and reports something else — which is why the literals
// are spelled here rather than compared to the constants they came from.
func TestVocabulariesArePinned(t *testing.T) {
	t.Parallel()

	// The API's outcome vocabulary is snake_case. run-report-v1 spells the same
	// two multi-word outcomes not-run and timed-out, in kebab-case, and a
	// consumer moving a value between the live API and a published report has
	// to translate rather than assume. The difference is deliberate and both
	// spellings are frozen.
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

// TestKnownPreparePhasesMatchWhatPrepareEmits keeps the list and the engine in
// step.
//
// [gomutants.KnownPreparePhases] is a promise about this build, and the only
// way it can be wrong is by drifting from the code that emits the events. A
// phase added to Prepare and not to the list would leave every consumer that
// pinned the list — which is what the doc tells them to do — refusing a phase
// the engine really emits, and it would do so at their users rather than here.
//
// Both shared preparations are checked, because the list is documented as what
// a consumer sees for *every* preparation and not only for a fully configured
// one. The session prepared without a probe tree skips five of the nine, and a
// skip is emitted as a start immediately followed by a finish — so if a skipped
// phase were ever silently dropped instead, that row would catch it while the
// probe row could not.
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

// phaseProgress is how far one phase has got through its two events.
type phaseProgress int

const (
	phaseUnseen phaseProgress = iota
	phaseOpen
	phaseClosed
)

// phaseEventProblems walks one preparation's events and reports the order the
// phases started in, together with everything wrong with the sequence.
//
// It tracks a state per phase rather than the position of the latest event of
// each kind, and the difference is the whole of what it checks. Positions
// overwrite: a phase that started, finished, and started again would leave a
// start position and a finish position both recorded, and read as a complete
// phase — while its live start has no finish and any consumer timing the phase
// from these events is left holding a stopwatch that never stops. Counting
// transitions instead makes each of the three malformed shapes — a second
// start, a second finish, a start after a finish — a state it has no edge for.
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
	// Over order rather than over the map, so the problems a caller prints come
	// out in the same sequence on every run.
	for _, phase := range order {
		if progress[phase] != phaseClosed {
			note("phase %s started and never finished", phase)
		}
	}
	return order, problems
}

// TestPhaseEventsAreOneStartAndOneFinishEachPhase pins what
// TestKnownPreparePhasesMatchWhatPrepareEmits reads a real preparation with.
//
// The claim in the doc is that a phase starts once and finishes once, and the
// sequences that break it are the ones no fixture will produce on demand: a
// doubled start, a doubled finish, a start after a finish. A checker that
// merely remembered the latest position of each would accept all three — the
// second start would overwrite the first and the already-recorded finish would
// still be there — so the phase would read as complete while its last start had
// no finish at all. That is the shape a consumer's own timers would break on,
// and it is why the walk is a separate function: a synthetic slice can state
// the case that a fixture cannot.
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
			// Overlap is legal and deliberate: the binary build starts before
			// the probe phases and finishes after them.
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
