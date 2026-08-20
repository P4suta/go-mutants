// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// refreshInterval is how often the frame is repainted while nothing arrives.
//
// The interval exists for the two numbers no event carries: the elapsed clock
// and how long each worker has been on its mutant. Four frames a second is
// enough for a seconds-resolution clock to look alive and cheap enough that a
// run of tiny mutants is not competing with its own dashboard for the CPU.
const refreshInterval = 250 * time.Millisecond

// The default frame, used until the terminal says how big it is. bubbletea
// sends a [tea.WindowSizeMsg] at startup and on every resize, so these numbers
// are on screen for one frame at most — but a model that starts with a zero
// width would divide by it, and a test that never sends a size still deserves a
// picture.
const (
	defaultWidth  = 80
	defaultHeight = 24
)

// An eventMsg carries one engine event into the model. The renderer's
// forwarding goroutine wraps every event in one of these; see [Renderer.Run].
type eventMsg struct{ event engine.Event }

// A streamClosedMsg says the event channel closed. It is a safety net rather
// than the normal path: [engine.RunCompleted] is the last event of every run
// and is what quits the dashboard, and this message quits it anyway if a stream
// ever ends without one — a dashboard that outlived its engine would hold the
// terminal hostage.
type streamClosedMsg struct{}

// A tickMsg is the repaint clock.
type tickMsg time.Time

// A slot is one execution worker's row in the table.
//
// The table is fixed at [engine.RunPlanned.Workers] rows for the whole run, so
// a row that empties leaves a gap rather than closing it up: rows that
// reordered themselves as mutants settled would make the table impossible to
// read at exactly the moment it is worth reading, which is when one worker is
// stuck on something slow and the others are not.
type slot struct {
	busy      bool
	displayID string
	path      string
	line      int
	rule      string
	// since is when the current attempt started, for the elapsed column.
	since time.Time
}

// options are what a model needs that it cannot discover for itself.
type options struct {
	// version is the go-mutants version in the header. It is passed in for the
	// same reason internal/console takes it: internal/cli owns the string.
	version string
	// cancel cancels the run's context. Ctrl-C calls it; see the package
	// documentation for why that is all Ctrl-C does. It may be nil in a test.
	cancel func()
	// now is the clock, injectable so that a test can assert on an elapsed
	// time without waiting for one.
	now func() time.Time
	// theme is the palette; see [theme].
	theme theme
}

// A model is the dashboard's whole state.
//
// Everything in it is either something an event stated or something derived
// from time. Nothing is derived from another event: the live score is folded
// from outcomes through [mutation.Tally], which is the same type the engine
// scores the run with, rather than through a second formula that would
// eventually disagree with the summary printed underneath the dashboard.
type model struct {
	opts options

	// width and height are the terminal's, from [tea.WindowSizeMsg].
	width  int
	height int

	// started is when the dashboard opened, which is within a few milliseconds
	// of when the run did: the renderer is started before the engine.
	started time.Time
	// clock is the last time the frame was painted, and is what the elapsed
	// columns are measured against so that every duration on one frame is
	// measured from the same instant.
	clock time.Time

	runID   string
	workers int
	phase   engine.Phase
	detail  string
	// baseline, discovery, and coverage are the last line each of those phases
	// published, kept so that a run that has moved on still shows what it
	// established. coverage is empty for a run that did no coverage pass, which
	// publishes a [engine.Warning] saying why instead.
	baseline  string
	discovery string
	coverage  string

	// total is how many mutants will be executed, known from
	// [engine.Validated]. It is zero before validation, which is why the
	// counters line says "12 done" rather than "12/0" until it is not.
	total int
	// decided is how many mutants have settled.
	decided int
	// tally is the live breakdown. Survivors are recorded as unexpected,
	// because [engine.MutantFinished] does not say whether the expectations
	// ledger predicted one — the ledger is reconciled when the run ends. The
	// live score is therefore a lower bound that the closing block corrects,
	// which is stated on screen by the summary being printed again afterwards.
	tally mutation.Tally

	slots     []slot
	survivors []engine.MutantResult
	warnings  int
	eta       estimator

	feed viewport.Model
	// follow keeps the feed pinned to its newest entry until the user scrolls
	// away from the bottom, at which point it stays where they put it.
	follow bool

	// stopping is set by the first Ctrl-C: the run has been cancelled and is
	// unwinding, and the dashboard is waiting for it.
	stopping bool
	// done is set by [engine.RunCompleted].
	done   bool
	status engine.Status
}

// newModel builds the initial model.
func newModel(o options) model {
	if o.now == nil {
		o.now = time.Now
	}
	now := o.now()
	m := model{
		opts:    o,
		width:   defaultWidth,
		height:  defaultHeight,
		started: now,
		clock:   now,
		feed:    viewport.New(defaultWidth, 1),
		follow:  true,
	}
	m.relayout()
	return m
}

// Init starts the repaint clock. There is nothing to fetch: every fact the
// dashboard shows arrives as a message.
func (m model) Init() tea.Cmd {
	return tick(refreshInterval)
}

// tick schedules the next repaint.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update folds one message into the model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.key(msg)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		return m, nil

	case tickMsg:
		m.clock = time.Time(msg)
		// The clock stops when the run does. A dashboard that is quitting has
		// no more frames to paint, and a timer that outlived its program is a
		// goroutine bubbletea would have to wait for.
		if m.done {
			return m, nil
		}
		return m, tick(refreshInterval)

	case eventMsg:
		return m.fold(msg.event)

	case streamClosedMsg:
		m.done = true
		return m, tea.Quit

	default:
		return m, nil
	}
}

// key handles a keystroke.
//
// Ctrl-C is the only binding with a policy behind it; everything else is the
// feed's, so that page-up and the arrows scroll the survivors. Ordinary letters
// are deliberately not bound: a dashboard where "q" quit would lose a report to
// somebody typing into the wrong window.
func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		if m.stopping {
			// The escape hatch. See the package documentation: the run keeps
			// unwinding, and the renderer keeps draining it; what this gives
			// back is the terminal.
			return m, tea.Quit
		}
		m.stopping = true
		if m.opts.cancel != nil {
			m.opts.cancel()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.feed, cmd = m.feed.Update(msg)
	m.follow = m.feed.AtBottom()
	return m, cmd
}

// fold folds one engine event into the model.
//
// Every event the sealed interface defines is handled explicitly, so that one
// added later shows up here as the default case and is ignored rather than
// misread.
func (m model) fold(event engine.Event) (tea.Model, tea.Cmd) {
	// The clock advances on an event as well as on a tick, so that a worker
	// slot filled between two repaints is timed from when the mutant actually
	// started rather than from the frame that noticed it.
	m.clock = m.opts.now()
	switch e := event.(type) {
	case engine.RunPlanned:
		m.runID = e.RunID
		m.workers = e.Workers
		m.slots = make([]slot, max(e.Workers, 0))

	case engine.PhaseChanged:
		m.phase = e.Phase
		m.detail = e.Detail

	case engine.BaselineProgress:
		m.baseline = baselineProgressLine(e)

	case engine.BaselineCompleted:
		m.baseline = baselineCompletedLine(e)

	case engine.Discovered:
		m.discovery = discoveredLine(e)

	case engine.Validated:
		m.discovery = validatedLine(e)
		m.total = e.Accepted

	case engine.CoverageMapped:
		// Kept on its own line rather than folded into the discovery one: it is
		// the answer to a different question — how much of the catalogue is
		// about to be skipped — and it is published after validation, so
		// overwriting the discovery line would trade one fact for another.
		m.coverage = coverageLine(e)

	case engine.MutantStarted:
		m.claim(e)

	case engine.MutantFinished:
		m.settle(e.Result)

	case engine.Warning:
		// Counted here and printed by internal/cli once the alternate screen
		// is gone. A warning drawn into a frame that is about to be torn down
		// is a warning nobody can act on, so the dashboard shows that there
		// are some and the scrollback gets the text.
		m.warnings++

	case engine.ReportPublished:
		// Nothing to draw: the paths are printed after the dashboard exits,
		// where they can be selected and opened. That the report exists is
		// what the phase line already says.

	case engine.RunCompleted:
		m.done = true
		m.status = e.Status
		if e.Run != nil {
			// The engine's own counts replace the ones folded live. They are
			// the same numbers when nothing went wrong and the authoritative
			// ones when something did — a run cut short has mutants that were
			// never started, which no live counter could have known about.
			m.total = e.Run.Counts.Total
			m.tally = tallyOf(e.Run.Counts)
			m.warnings = e.Run.Warnings
		}
		m.releaseAll()
		m.relayout()
		return m, tea.Quit
	}
	m.relayout()
	return m, nil
}

// claim fills the worker slot a mutant just started in.
//
// A worker index outside the planned table is ignored rather than growing it.
// The table's width is a promise [engine.RunPlanned] made, and a row that
// appeared halfway through a run would move every row under it.
func (m *model) claim(e engine.MutantStarted) {
	if e.Worker < 0 || e.Worker >= len(m.slots) {
		return
	}
	m.slots[e.Worker] = slot{
		busy:      true,
		displayID: e.DisplayID,
		path:      e.Path,
		line:      e.Line,
		rule:      e.Rule,
		since:     m.clock,
	}
}

// settle records one settled mutant.
//
// The slot is released by identity rather than by [engine.MutantResult.Worker],
// because a mutant that timed out is retried serially on worker 0 and would
// otherwise leave the worker that first claimed it looking busy for the rest of
// the run.
func (m *model) settle(r engine.MutantResult) {
	for i := range m.slots {
		if m.slots[i].busy && m.slots[i].displayID == r.DisplayID {
			m.slots[i] = slot{}
		}
	}
	m.decided++
	// An unknown outcome cannot come from the engine — the events are typed —
	// and if one ever did, dropping it from the tally is better than refusing
	// to draw the frame.
	_ = m.tally.Record(mutation.Result{Outcome: r.Outcome})
	m.eta.observe(r.Duration)
	if r.Outcome == mutation.OutcomeSurvived {
		m.survivors = append(m.survivors, r)
	}
}

// releaseAll empties the worker table, which is what has happened by the time
// the run reports itself completed.
func (m *model) releaseAll() {
	for i := range m.slots {
		m.slots[i] = slot{}
	}
}

// tallyOf turns the engine's closing counts back into a tally.
//
// The one thing it cannot recover is the split between expected and unexpected
// survivors, which the counts deliberately do not carry — see [engine.Counts].
// Survivors are therefore restated as unexpected here, exactly as they were
// counted live, which leaves the dashboard's last score a lower bound on the
// one the summary prints. The summary is printed immediately afterwards, by
// internal/cli, from [engine.RunSummary.Score], so the authoritative number is
// never more than a line away from the estimate.
func tallyOf(c engine.Counts) mutation.Tally {
	return mutation.Tally{
		Killed:              c.Killed,
		TimedOut:            c.TimedOut,
		UnexpectedSurvivors: c.Survived,
		Inconclusive:        c.Inconclusive,
		Errored:             c.Errored,
		NotRun:              c.NotRun,
	}
}

// score is the live mutation score, derived the way the engine derives the
// final one.
func (m model) score() mutation.Score { return mutation.ScoreOf(m.tally) }

// remaining is how many mutants are still to settle, or zero when the total is
// not known yet.
func (m model) remaining() int {
	if m.total <= m.decided {
		return 0
	}
	return m.total - m.decided
}

// elapsed is how long the run has been going, as of the last repaint.
func (m model) elapsed() time.Duration { return m.clock.Sub(m.started) }
