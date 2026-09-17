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

const refreshInterval = 250 * time.Millisecond

const (
	defaultWidth  = 80
	defaultHeight = 24
)

type eventMsg struct{ event engine.Event }

type streamClosedMsg struct{}

type tickMsg time.Time

type slot struct {
	busy      bool
	displayID string
	path      string
	line      int
	rule      string
	since     time.Time
}

type options struct {
	version string
	cancel  func()
	now     func() time.Time
	theme   theme
}

type model struct {
	opts options

	width  int
	height int

	started time.Time
	clock   time.Time

	runID     string
	workers   int
	phase     engine.Phase
	detail    string
	baseline  string
	discovery string
	selection string
	coverage  string

	total   int
	decided int
	tally   mutation.Tally

	slots     []slot
	survivors []engine.MutantResult
	warnings  int
	eta       estimator

	feed   viewport.Model
	follow bool

	stopping bool
	done     bool
	status   engine.Status
}

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

func (m model) Init() tea.Cmd {
	return tick(refreshInterval)
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

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

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		if m.stopping {
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

func (m model) fold(event engine.Event) (tea.Model, tea.Cmd) {
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

	case engine.SelectionNarrowed:
		m.selection = narrowedLine(e)
		m.total = e.Selected

	case engine.CoverageMapped:
		m.coverage = coverageLine(e)

	case engine.MutantStarted:
		m.claim(e)

	case engine.MutantFinished:
		m.settle(e.Result)

	case engine.Warning:
		m.warnings++

	case engine.MemoryDerived:

	case engine.ReportPublished:

	case engine.RunCompleted:
		m.done = true
		m.status = e.Status
		if e.Run != nil {
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

func (m *model) claim(e engine.MutantStarted) {
	if e.Worker < 0 || e.Worker >= len(m.slots) {
		return
	}
	m.slots[e.Worker] = slot{
		busy:      true,
		displayID: e.DisplayID,
		path:      engine.WorkspaceLocation(e.ModuleDir, e.Path),
		line:      e.Line,
		rule:      e.Rule,
		since:     m.clock,
	}
}

func (m *model) settle(r engine.MutantResult) {
	for i := range m.slots {
		if m.slots[i].busy && m.slots[i].displayID == r.DisplayID {
			m.slots[i] = slot{}
		}
	}
	m.decided++
	_ = m.tally.Record(mutation.Result{Outcome: r.Outcome})
	m.eta.observe(r.Duration)
	if r.Outcome == mutation.OutcomeSurvived {
		m.survivors = append(m.survivors, r)
	}
}

func (m *model) releaseAll() {
	for i := range m.slots {
		m.slots[i] = slot{}
	}
}

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

func (m model) score() mutation.Score { return mutation.ScoreOf(m.tally) }

func (m model) remaining() int {
	if m.total <= m.decided {
		return 0
	}
	return m.total - m.decided
}

func (m model) elapsed() time.Duration { return m.clock.Sub(m.started) }
