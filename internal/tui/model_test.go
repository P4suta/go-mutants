// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// A clock is a settable time source, so that a test can assert on an elapsed
// duration without waiting for one.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// harness is a model, its clock, and how many times Ctrl-C cancelled the run.
type harness struct {
	model     model
	clock     *clock
	cancelled int
}

// newHarness builds a model with a stopped clock and no styling at all, which
// is what an assertion about the layout wants.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newThemedHarness(t, asciiTheme())
}

// newThemedHarness builds a model on a given theme, so that a test can draw the
// frame the way production draws it: with the escape sequences in it.
func newThemedHarness(t *testing.T, th theme) *harness {
	t.Helper()
	h := &harness{clock: &clock{t: time.Date(2026, 8, 19, 10, 11, 12, 0, time.UTC)}}
	h.model = newModel(options{
		version: "0.1.0-dev",
		cancel:  func() { h.cancelled++ },
		now:     h.clock.now,
		theme:   th,
	})
	return h
}

// send folds messages into the model and returns the command the last one
// produced, so that a test can assert on what bubbletea was asked to do.
func (h *harness) send(t *testing.T, msgs ...tea.Msg) tea.Cmd {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		updated, next := h.model.Update(msg)
		m, ok := updated.(model)
		if !ok {
			t.Fatalf("Update returned %T, want tui.model", updated)
		}
		h.model, cmd = m, next
	}
	return cmd
}

// events folds engine events, which is what most of these tests send.
func (h *harness) events(t *testing.T, list ...engine.Event) tea.Cmd {
	t.Helper()
	msgs := make([]tea.Msg, 0, len(list))
	for _, e := range list {
		msgs = append(msgs, eventMsg{event: e})
	}
	return h.send(t, msgs...)
}

// isQuit reports whether a command is [tea.Quit].
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// The mutants the synthetic run measures. The display ids are longer than the
// eight characters the dashboard shows, which is the point: the truncation is
// part of the format.
var (
	killedResult = engine.MutantResult{
		ID:          strings.Repeat("1a2b3c4d", 8),
		DisplayID:   "1a2b3c4d5e6f7a8b9c0d",
		Path:        "internal/engine/engine.go",
		Line:        412,
		Column:      9,
		Rule:        "lt-to-le",
		Original:    "<",
		Replacement: "<=",
		Outcome:     mutation.OutcomeKilled,
		Duration:    181 * time.Millisecond,
		Worker:      0,
	}
	survivorResult = engine.MutantResult{
		ID:          strings.Repeat("9f8e7d6c", 8),
		DisplayID:   "9f8e7d6c5b4a3928170f",
		Path:        "internal/report/untested.go",
		Line:        9,
		Column:      12,
		Rule:        "neq-to-eq",
		Original:    "!=",
		Replacement: "==",
		Outcome:     mutation.OutcomeSurvived,
		Duration:    176 * time.Millisecond,
		Worker:      1,
	}
	uncoveredResult = engine.MutantResult{
		ID:          strings.Repeat("5c5c5c5c", 8),
		DisplayID:   "5c5c5c5c1d1d1d1d",
		Path:        "internal/glob/glob.go",
		Line:        88,
		Column:      3,
		Rule:        "true-to-false",
		Original:    "true",
		Replacement: "false",
		// No test binary reaches this one, so it was never executed. There is
		// no "uncovered" outcome in the engine's vocabulary and no uncovered
		// counter: the engine reports it as a survivor with the qualifier set —
		// nothing ran it, so nothing could have caught it — which is the only
		// shape engine.MutantResult.Uncovered is ever seen in. Duration is zero
		// because there were no child processes to time.
		Outcome:   mutation.OutcomeSurvived,
		Uncovered: true,
		Worker:    2,
	}
)

// planned is the opening of every synthetic run.
func planned(workers int) []engine.Event {
	return []engine.Event{
		engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: workers},
		engine.PhaseChanged{Phase: engine.PhaseDiscover, Detail: "locating the Go toolchain and copying the workspace"},
		engine.PhaseChanged{Phase: engine.PhaseBaseline, Detail: "building the snapshot, then 3 timed runs of go test ./..."},
		engine.BaselineProgress{Run: 1, Of: 3, Duration: 152 * time.Millisecond},
		engine.BaselineCompleted{
			Average: 170 * time.Millisecond, Slowest: 210 * time.Millisecond,
			Timeout: 10 * time.Second, TimeoutSource: engine.TimeoutDerived,
		},
		engine.PhaseChanged{Phase: engine.PhaseMutate, Detail: "discovering candidates, validating them, then executing the mutants"},
		engine.Discovered{Candidates: 6, Skips: 12},
		engine.Validated{Accepted: 4, Rejected: 1},
	}
}

// started is the event that fills a worker slot for a result.
func started(r engine.MutantResult) engine.MutantStarted {
	return engine.MutantStarted{
		ID: r.ID, DisplayID: r.DisplayID, Path: r.Path, Line: r.Line, Rule: r.Rule, Worker: r.Worker,
	}
}

func TestTheStreamMovesTheCountersAndTheSlots(t *testing.T) {
	tests := []struct {
		name          string
		events        []engine.Event
		wantTally     mutation.Tally
		wantDecided   int
		wantSurvivors int
		wantBusy      []int
	}{
		{
			name:     "nothing has settled while two workers are busy",
			events:   append(planned(3), started(killedResult), started(survivorResult)),
			wantBusy: []int{0, 1},
		},
		{
			name: "a kill and a survivor free their slots",
			events: append(planned(3),
				started(killedResult), started(survivorResult),
				engine.MutantFinished{Result: killedResult},
				engine.MutantFinished{Result: survivorResult},
			),
			wantTally:     mutation.Tally{Killed: 1, UnexpectedSurvivors: 1},
			wantDecided:   2,
			wantSurvivors: 1,
		},
		{
			// An uncovered mutant is a survivor the run never executed, and it
			// is counted with the survivors it cannot be told apart from by
			// outcome alone. The label in the feed is what tells them apart.
			name: "an uncovered mutant is counted as a survivor",
			events: append(planned(3),
				started(uncoveredResult),
				engine.MutantFinished{Result: uncoveredResult},
			),
			wantTally:     mutation.Tally{UnexpectedSurvivors: 1},
			wantDecided:   1,
			wantSurvivors: 1,
		},
		{
			name: "a timeout and a harness error land in their own counters",
			events: append(planned(2),
				engine.MutantFinished{Result: withOutcome(killedResult, mutation.OutcomeTimedOut)},
				engine.MutantFinished{Result: withOutcome(survivorResult, mutation.OutcomeErrored)},
				engine.MutantFinished{Result: withOutcome(uncoveredResult, mutation.OutcomeInconclusive)},
			),
			wantTally:   mutation.Tally{TimedOut: 1, Errored: 1, Inconclusive: 1},
			wantDecided: 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.events(t, tc.events...)

			if got := h.model.tally; got != tc.wantTally {
				t.Errorf("tally = %+v, want %+v", got, tc.wantTally)
			}
			if got := h.model.decided; got != tc.wantDecided {
				t.Errorf("decided = %d, want %d", got, tc.wantDecided)
			}
			if got := len(h.model.survivors); got != tc.wantSurvivors {
				t.Errorf("survivors = %d, want %d", got, tc.wantSurvivors)
			}
			var busy []int
			for i, s := range h.model.slots {
				if s.busy {
					busy = append(busy, i)
				}
			}
			if len(busy) != len(tc.wantBusy) {
				t.Fatalf("busy slots = %v, want %v", busy, tc.wantBusy)
			}
			for i := range busy {
				if busy[i] != tc.wantBusy[i] {
					t.Fatalf("busy slots = %v, want %v", busy, tc.wantBusy)
				}
			}
		})
	}
}

// withOutcome copies a result with a different verdict.
//
// The uncovered qualifier does not survive the change: the engine only ever
// sets it alongside a survivor, because an uncovered mutant is one nothing ran,
// and a copy carrying it onto a timeout would be a shape no run can produce.
func withOutcome(r engine.MutantResult, o mutation.Outcome) engine.MutantResult {
	r.Outcome = o
	r.Uncovered = r.Uncovered && o == mutation.OutcomeSurvived
	return r
}

func TestTheWorkerTableIsSizedByThePlanAndNeverGrows(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(3)...)
	if got := len(h.model.slots); got != 3 {
		t.Fatalf("slots = %d, want 3", got)
	}
	// A worker index the plan did not promise is ignored rather than growing
	// the table under the reader.
	h.events(t, engine.MutantStarted{DisplayID: "deadbeef", Path: "a.go", Line: 1, Rule: "r", Worker: 9})
	if got := len(h.model.slots); got != 3 {
		t.Fatalf("slots = %d after an out-of-range worker, want 3", got)
	}
	for i, s := range h.model.slots {
		if s.busy {
			t.Fatalf("slot %d is busy, want every slot idle", i)
		}
	}
}

func TestARetriedMutantReleasesTheSlotItWasClaimedIn(t *testing.T) {
	// A timeout is retried serially on worker 0, so the settled result reports
	// a different worker from the one that first claimed the mutant. Releasing
	// by worker index would leave worker 1 looking busy for the rest of the run.
	h := newHarness(t)
	h.events(t, planned(3)...)
	h.events(t, started(survivorResult)) // worker 1
	retry := survivorResult
	retry.Worker = 0
	h.events(t, started(retry), engine.MutantFinished{Result: withOutcome(retry, mutation.OutcomeTimedOut)})

	for i, s := range h.model.slots {
		if s.busy {
			t.Errorf("slot %d is still busy after the retry settled", i)
		}
	}
}

func TestTheLiveScoreIsUndefinedUntilSomethingIsMeasured(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)
	if got := h.model.score().String(); got != "n/a" {
		t.Errorf("score = %q before anything settled, want %q", got, "n/a")
	}
	h.events(t, engine.MutantFinished{Result: killedResult})
	if got := h.model.score().String(); got != "100.00%" {
		t.Errorf("score = %q after one kill, want %q", got, "100.00%")
	}
	h.events(t, engine.MutantFinished{Result: survivorResult})
	if got := h.model.score().String(); got != "50.00%" {
		t.Errorf("score = %q after one kill and one survivor, want %q", got, "50.00%")
	}
	// An uncovered mutant is a survivor and moves the score exactly as one: the
	// engine counts it in the denominator, because a line no test reaches is
	// precisely what a mutation score is meant to report.
	h.events(t, engine.MutantFinished{Result: uncoveredResult})
	if got := h.model.score().String(); got != "33.33%" {
		t.Errorf("score = %q after an uncovered survivor, want %q", got, "33.33%")
	}
	// A not-run mutant is outside the denominator, so it cannot move the score.
	h.events(t, engine.MutantFinished{Result: withOutcome(killedResult, mutation.OutcomeNotRun)})
	if got := h.model.score().String(); got != "33.33%" {
		t.Errorf("score = %q after a not-run mutant, want it unchanged at %q", got, "33.33%")
	}
}

func TestRunCompletedAdoptsTheEnginesCountsAndQuits(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)
	h.events(t, engine.MutantFinished{Result: killedResult})

	cmd := h.events(t, engine.RunCompleted{
		Status: engine.StatusOK,
		Run: &engine.RunSummary{
			RunID:  "20260819T101112Z-a1b2",
			Counts: engine.Counts{Total: 4, Killed: 3, Survived: 1, NotRun: 0},
			Score:  mutation.Score{Detected: 3, Denominator: 4},
		},
	})
	if !isQuit(cmd) {
		t.Fatal("RunCompleted did not quit the dashboard")
	}
	if got := h.model.tally.Killed; got != 3 {
		t.Errorf("killed = %d after the closing block, want the engine's 3", got)
	}
	if got := h.model.total; got != 4 {
		t.Errorf("total = %d after the closing block, want 4", got)
	}
	if got := h.model.score().String(); got != "75.00%" {
		t.Errorf("score = %q, want the engine's %q", got, "75.00%")
	}
	for i, s := range h.model.slots {
		if s.busy {
			t.Errorf("slot %d is busy after the run completed", i)
		}
	}
}

func TestCtrlCCancelsTheRunAndWaitsForIt(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)
	h.events(t, started(killedResult))

	cmd := h.send(t, tea.KeyMsg{Type: tea.KeyCtrlC})
	if h.cancelled != 1 {
		t.Fatalf("the run was cancelled %d times, want 1", h.cancelled)
	}
	if isQuit(cmd) {
		t.Fatal("the first ctrl+c quit the dashboard; it must wait for the partial report")
	}
	if !h.model.stopping {
		t.Fatal("the model did not enter its stopping state")
	}

	// Events keep arriving while the engine unwinds, and none of them may end
	// the program before the run says it is over.
	cmd = h.events(t, engine.MutantFinished{Result: killedResult}, engine.Warning{Code: "GOM7520", Message: "interrupted"})
	if isQuit(cmd) {
		t.Fatal("the dashboard quit while the run was still unwinding")
	}

	cmd = h.events(t, engine.RunCompleted{Status: engine.StatusInterrupted, Summary: "interrupted"})
	if !isQuit(cmd) {
		t.Fatal("the dashboard did not quit when the run completed")
	}
}

func TestASecondCtrlCQuitsImmediately(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)

	h.send(t, tea.KeyMsg{Type: tea.KeyCtrlC})
	cmd := h.send(t, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuit(cmd) {
		t.Fatal("the second ctrl+c did not quit")
	}
	// The run is cancelled once. The second press is about the terminal, not
	// about the run, and cancelling an already-cancelled context twice would be
	// harmless but would say the wrong thing about what the key means.
	if h.cancelled != 1 {
		t.Errorf("the run was cancelled %d times, want 1", h.cancelled)
	}
}

func TestAClosedStreamQuitsEvenWithoutRunCompleted(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)
	if cmd := h.send(t, streamClosedMsg{}); !isQuit(cmd) {
		t.Fatal("a closed stream left the dashboard running")
	}
}

func TestTheClockTicksUntilTheRunEnds(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)

	h.clock.advance(90 * time.Second)
	if cmd := h.send(t, tickMsg(h.clock.now())); cmd == nil {
		t.Fatal("a tick during the run did not schedule the next one")
	}
	if got := h.model.elapsed(); got != 90*time.Second {
		t.Errorf("elapsed = %s, want 1m30s", got)
	}
	h.events(t, engine.RunCompleted{Status: engine.StatusOK})
	if cmd := h.send(t, tickMsg(h.clock.now())); cmd != nil {
		t.Error("a tick after the run ended scheduled another; the clock must stop")
	}
}

// TestTheCoveragePassIsFoldedRatherThanDropped is the event that was added to
// the stream after this package was written, which is exactly the case
// [model.fold]'s default arm quietly swallows. How much of a run coverage is
// about to skip is the single most useful number a coverage-guided run has, and
// the plain renderer prints it.
func TestTheCoveragePassIsFoldedRatherThanDropped(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)
	if h.model.coverage != "" {
		t.Fatalf("a run with no coverage pass has a coverage line %q", h.model.coverage)
	}

	h.events(t, engine.Validated{Accepted: 47, Rejected: 2})
	h.events(t, engine.CoverageMapped{Binaries: 3, Covered: 40, Uncovered: 7})

	want := "coverage: 3 test binaries, 40 of 47 mutants covered, 7 uncovered"
	if got := h.model.coverage; got != want {
		t.Errorf("coverage line = %q, want %q", got, want)
	}
	// It is a fact about a different thing from the validated line, arrives
	// after it, and does not replace it.
	if got := h.model.discovery; got != "validated 47 mutants, 2 rejections" {
		t.Errorf("the coverage pass overwrote the validated line, leaving %q", got)
	}

	// One binary is one binary, not one binaries.
	h.events(t, engine.CoverageMapped{Binaries: 1, Covered: 1, Uncovered: 0})
	if got, want := h.model.coverage, "coverage: 1 test binary, 1 of 1 mutants covered, 0 uncovered"; got != want {
		t.Errorf("coverage line = %q, want %q", got, want)
	}
}

func TestWarningsAreCountedRatherThanDrawn(t *testing.T) {
	h := newHarness(t)
	h.events(t, planned(2)...)
	h.events(t, engine.Warning{Code: "GOM7301", Message: "a package was skipped"})
	if got := h.model.warnings; got != 1 {
		t.Fatalf("warnings = %d, want 1", got)
	}
	if got := len(h.model.survivors); got != 0 {
		t.Errorf("a warning put %d entries in the survivor feed, want 0", got)
	}
}
