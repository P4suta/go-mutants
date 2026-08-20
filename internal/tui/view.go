// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/engine"
)

// The frame's limits.
const (
	// narrowWidth is where the dashboard switches to its compact spelling:
	// abbreviated counter labels, no gauge bar, no rule column. Sixty columns
	// is where the full spelling of the counters stops fitting, and a line that
	// wraps in an alternate screen destroys the layout of every line under it.
	narrowWidth = 60
	// minWidth and minHeight are the smallest frame that is drawn at all.
	// Below them the reported size is treated as these, and the terminal clips:
	// there is no useful dashboard in six columns, and arithmetic that has to
	// stay correct there costs more than it is worth.
	minWidth  = 24
	minHeight = 6
	// minBarWidth is the narrowest gauge worth drawing. Below eight cells one
	// cell is more than twelve per cent of the score, and a bar that coarse
	// misleads where the number beside it does not.
	minBarWidth = 8
	// feedReserve is how many lines the survivor feed is kept from the worker
	// table when the frame is too short for both: a title and two entries.
	feedReserve = 3
	// displayIDWidth is how many hex characters of a display id is shown. It is
	// internal/console's result-line width, so an id read off the dashboard is
	// the id the summary underneath prints for the same mutant.
	displayIDWidth = 8
	// diffIndent is what the two-line survivor diff is indented by, matching
	// internal/console for the same reason.
	diffIndent = "    "
	// ellipsis marks a truncation. Three dots rather than the character,
	// because the dashboard is ASCII; see the package documentation.
	ellipsis = "..."
)

// View draws the frame.
//
// The result is exactly as many lines as the terminal is tall, with the help
// line last, so that the bottom of the screen does not move when a section
// above it grows. Every line is at most as wide as the terminal: an alternate
// screen has no reflow, and one over-long line would shift every line beneath
// it for the rest of the run.
func (m model) View() string {
	width, height := m.frame()
	head, workers, feedTitle, feedHeight := m.sections(width, height)

	lines := make([]string, 0, height)
	lines = append(lines, head...)
	lines = append(lines, workers...)
	if feedTitle != "" {
		lines = append(lines, feedTitle)
	}
	if feedHeight > 0 {
		lines = append(lines, strings.Split(m.feed.View(), "\n")...)
	}
	// Pad, then trim: the padding is what pins the help line to the bottom of
	// a frame the sections did not fill, and the trim is the guarantee that a
	// section which grew unexpectedly cannot push it off the screen.
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	lines = append(lines, m.helpLine(width))
	return strings.Join(lines, "\n")
}

// frame is the size to draw at, floored so that no arithmetic below divides by
// a zero the terminal reported.
func (m model) frame() (width, height int) {
	return max(m.width, minWidth), max(m.height, minHeight)
}

// relayout resizes the feed to the space the current frame leaves it and
// re-renders its contents.
//
// It runs after every message that changes either the state or the size,
// because both move the boundary: a run that has just learnt its mutant total
// gains a line in the head, and the feed is what gives that line up. Rendering
// the entries here rather than in View is what makes a resize re-truncate the
// paths in entries that were already on screen.
func (m *model) relayout() {
	width, height := m.frame()
	_, _, _, feedHeight := m.sections(width, height)
	m.feed.Width = width
	m.feed.Height = max(feedHeight, 1)
	m.feed.SetContent(m.feedContent(width))
	if m.follow {
		m.feed.GotoBottom()
	}
}

// sections lays the frame out: the head lines, the worker table, the feed's
// title, and how many lines are left for the feed itself.
//
// The order things are given up in is the order they stop being useful. The
// feed shrinks first, because a survivor that scrolled out of it is still in
// the report and is printed again when the run ends. The worker table goes
// next, oldest rows first with a count of what was dropped, because knowing
// that eight workers are busy is most of what the table says. The head is last
// and is trimmed by rank; see [model.headLines].
func (m model) sections(width, height int) (head, workers []string, feedTitle string, feedHeight int) {
	// One line is spoken for whatever happens: the help line at the bottom.
	budget := height - 1
	head = m.headLines(width, budget)
	budget -= len(head)

	workers = m.workerLines(width, max(budget-feedReserve, 0))
	budget -= len(workers)

	if budget >= 2 {
		feedTitle = m.feedTitleLine(width)
		feedHeight = budget - 1
	}
	return head, workers, feedTitle, feedHeight
}

// headLines renders the block above the worker table, trimmed to a budget.
//
// Each candidate carries a rank, and the ranks are what a short frame drops:
// the identity of the run and its score survive to the last, and the blank
// separator and the phase detail go first. A frame that dropped whichever line
// happened to be at the bottom would lose the counters — the one row a user
// glances at — the moment a baseline line appeared above them.
func (m model) headLines(width, budget int) []string {
	type candidate struct {
		text string
		rank int
	}
	// In reading order. The rank is what a short frame drops, not where a line
	// goes: the phase is above the score on screen and is given up before it.
	candidates := []candidate{
		{m.headerLine(width), 0},
		{m.phaseLine(width), 3},
		{m.baseline, 4},
		{m.discovery, 4},
		{m.coverage, 4},
		{m.scoreLine(width), 0},
		{m.countersLine(width), 1},
		{"", 5},
	}
	// The baseline, discovery, and coverage lines arrive as unstyled prose and
	// are the only candidates that can be over-long.
	for i := range candidates {
		if candidates[i].rank == 4 && candidates[i].text != "" {
			candidates[i].text = m.opts.theme.detail.Render(truncate(candidates[i].text, width))
		}
	}

	kept := make([]string, 0, len(candidates))
	for rank := 0; rank <= 5; rank++ {
		remaining := 0
		for _, c := range candidates {
			if c.rank <= rank && (c.text != "" || c.rank == 5) {
				remaining++
			}
		}
		if remaining > budget {
			break
		}
		kept = kept[:0]
		for _, c := range candidates {
			if c.rank <= rank && (c.text != "" || c.rank == 5) {
				kept = append(kept, c.text)
			}
		}
	}
	return kept
}

// headerLine is the tool, the run, and the clock.
func (m model) headerLine(width int) string {
	t := m.opts.theme
	left := "go-mutants " + m.opts.version
	if m.runID != "" && width >= narrowWidth {
		left += "  run " + m.runID
	}
	return spread(t.header.Render(left), t.detail.Render("elapsed "+formatClock(m.elapsed())), width)
}

// phaseLine is what the engine says it is doing. The detail is the engine's
// prose, printed verbatim for the reason internal/console prints it verbatim:
// only the engine knows how many baseline runs were configured.
func (m model) phaseLine(width int) string {
	if m.phase == "" {
		return ""
	}
	t := m.opts.theme
	head := t.phase.Render("phase " + m.phase.String() + ":")
	if m.detail == "" {
		return head
	}
	return head + " " + truncate(m.detail, max(width-len(m.phase)-8, 0))
}

// scoreLine is the gauge: the score so far, a bar, how many mutants have
// settled, and what is left.
//
// The score is [mutation.Score]'s own rendering, undefined case included, so
// that a run with nothing measured yet says "n/a" here and "N/A" in the summary
// rather than claiming a zero per cent nobody measured.
func (m model) scoreLine(width int) string {
	t := m.opts.theme
	score := t.score.Render("score " + m.score().String())
	progressText := m.progressText()
	tail := progressText
	if eta := m.etaText(); eta != "" {
		tail += "  " + eta
	}

	if width < narrowWidth {
		return truncate(score+"  "+tail, width)
	}
	bar := ""
	if percent, ok := m.score().Percent(); ok {
		t := m.opts.theme
		// Five columns are not the bar: the two that separate it from the
		// score, the two brackets, and the one that separates it from the tail.
		barWidth := width - printWidth(score) - printWidth(tail) - 5
		if barWidth >= minBarWidth {
			bar = "[" + gauge(percent/100, barWidth, t.gaugeOn, t.gaugeOff) + "] "
		}
	}
	return score + "  " + bar + tail
}

// progressText is how much of the catalogue has settled.
//
// Before validation there is no denominator — the catalogue does not exist yet
// — so it counts up rather than inventing a total to count towards.
func (m model) progressText() string {
	t := m.opts.theme
	if m.total <= 0 {
		return t.detail.Render(strconv.Itoa(m.decided) + " done")
	}
	return t.detail.Render(strconv.Itoa(m.decided) + "/" + strconv.Itoa(m.total))
}

// etaText is the estimate, or a word about why there is not one.
func (m model) etaText() string {
	t := m.opts.theme
	if m.done {
		return t.detail.Render("finished")
	}
	if m.stopping {
		return t.warning.Render("stopping")
	}
	eta, ok := m.eta.estimate(m.remaining(), m.workers)
	if !ok {
		return ""
	}
	return t.detail.Render("eta " + formatClock(eta))
}

// countersLine is the outcome breakdown so far.
//
// The labels are the vocabulary of the plain output — killed, survived,
// timeout, inconclusive, errored, not-run — abbreviated only where the frame is
// too narrow for them. There is deliberately no "uncovered" counter: no such
// outcome exists in [mutation.Outcome]. A mutant coverage showed no test
// reaches is reported as a survivor — nothing ran it, so nothing could have
// caught it — and is counted under "survived" here, exactly as the engine
// counts it. How many of them there are is the coverage line's job, and the
// qualifier on the label is what tells one survivor from the other in the feed.
func (m model) countersLine(width int) string {
	t := m.opts.theme
	type counter struct {
		long, short string
		value       int
		style       lipgloss.Style
	}
	counters := []counter{
		{"killed", "k", m.tally.Killed, t.ok},
		{"survived", "s", m.tally.Survived(), t.failed},
		{"timeout", "t", m.tally.TimedOut, t.ok},
		{"inconcl", "i", m.tally.Inconclusive, t.warning},
		{"errored", "e", m.tally.Errored, t.warning},
		{"not-run", "n", m.tally.NotRun, t.text},
	}
	if m.warnings > 0 {
		counters = append(counters, counter{"warnings", "w", m.warnings, t.warning})
	}
	parts := make([]string, 0, len(counters))
	for _, c := range counters {
		label := c.long
		if width < narrowWidth {
			label = c.short
		}
		parts = append(parts, c.style.Render(label+" "+strconv.Itoa(c.value)))
	}
	// Cut rather than dropped counters. Even the abbreviated spelling is
	// twenty-eight columns before a warnings counter joins it, which is wider
	// than [minWidth], and the counters are listed in the order they are worth
	// reading: how many were killed and how many got away come first, so a
	// frame that can only show the front of the line shows the front worth
	// having.
	return truncate(strings.Join(parts, "  "), width)
}

// workerLines renders the worker table, trimmed to a budget.
//
// The table is drawn from the moment the run is planned, before there is
// anything in it, so that the frame does not rearrange itself when the first
// mutant starts. Rows beyond the budget are replaced by a count: which
// particular workers were dropped is arbitrary, and how many is not.
func (m model) workerLines(width, budget int) []string {
	if len(m.slots) == 0 || budget < 2 {
		return nil
	}
	t := m.opts.theme
	lines := make([]string, 0, len(m.slots)+2)
	lines = append(lines, t.label.Render("workers"))
	// The budget also has to cover the title and the blank separator.
	rows := min(len(m.slots), budget-2)
	if rows < len(m.slots) && rows > 0 {
		// One row is given up so that the count of the dropped ones fits.
		rows--
	}
	for i := 0; i < rows; i++ {
		lines = append(lines, m.workerLine(i, m.slots[i], width))
	}
	if rows < len(m.slots) {
		lines = append(lines, t.detail.Render(fmt.Sprintf("%s+%d more", workerIndent, len(m.slots)-rows)))
	}
	return append(lines, "")
}

// workerIndent is what a worker row is indented by, so the table reads as
// belonging to its title.
const workerIndent = " "

// workerLine renders one worker's row.
func (m model) workerLine(index int, s slot, width int) string {
	t := m.opts.theme
	prefix := fmt.Sprintf("%s%2d  ", workerIndent, index)
	if !s.busy {
		return prefix + t.idle.Render("idle")
	}

	elapsed := console.FormatDuration(m.clock.Sub(s.since))
	right := "  " + t.detail.Render(elapsed)
	rule := ""
	if width >= narrowWidth {
		rule = "  " + t.rule.Render(s.rule)
	}
	// What is left after the fixed columns belongs to the location, which is
	// the only column that can be arbitrarily long. It is padded as well as
	// truncated, so that the rule and the elapsed time line up down the table
	// however long the paths above and below happen to be.
	room := width - len(prefix) - displayIDWidth - 2 - printWidth(rule) - printWidth(right)
	location := pad(truncatePath(s.path+":"+strconv.Itoa(s.line), room), room)
	return prefix + shortID(s.displayID) + "  " + location + rule + right
}

// feedTitleLine names the feed and says how many entries it holds.
func (m model) feedTitleLine(width int) string {
	t := m.opts.theme
	title := "survivors " + strconv.Itoa(len(m.survivors))
	if len(m.survivors) == 0 {
		return t.label.Render(truncate("survivors none yet", width))
	}
	return t.failed.Render(truncate(title, width))
}

// feedContent renders every survivor, oldest first.
//
// Each entry is internal/console's result line without its duration, and the
// same two-line diff underneath — the diff is the whole point of the feed,
// because `- <original>` / `+ <replacement>` is what a user pastes into the
// test they are about to write. The duration is dropped because a survivor's
// duration is the one number about it that never matters.
//
// The label is [console.ResultLabel] rather than [console.OutcomeLabel], so
// that a survivor no test binary reaches reads "SURVIVED (uncovered)" here and
// in the summary printed underneath the dashboard alike. The two say different
// things — a test to sharpen against a test to write — and a feed that dropped
// the qualifier would disagree with the block replayed two lines below it.
func (m model) feedContent(width int) string {
	t := m.opts.theme
	var b strings.Builder
	for i, s := range m.survivors {
		if i > 0 {
			b.WriteByte('\n')
		}
		label := console.ResultLabel(s.Outcome, s.Uncovered)
		head := t.outcome(s.Outcome).Render(fmt.Sprintf("%-*s", console.OutcomeWidth, label)) + "  " +
			shortID(s.DisplayID) + "  "
		location := s.Path + ":" + strconv.Itoa(s.Line) + ":" + strconv.Itoa(s.Column)
		rule := ""
		if width >= narrowWidth {
			rule = "  " + t.rule.Render(s.Rule)
		}
		room := width - printWidth(head) - printWidth(rule)
		// Clamped as a whole, and not only through the room left for the path.
		// The qualifier a label can carry is wider than the outcome column, so
		// the fixed part of this line is not fixed after all, and on a narrow
		// frame it can be wider than the terminal on its own — which is the one
		// thing [model.View] promises cannot happen.
		b.WriteString(truncate(head+truncatePath(location, room)+rule, width) + "\n")
		b.WriteString(diffIndent + t.removed.Render(truncate("- "+console.FormatText(s.Original), width-len(diffIndent))) + "\n")
		b.WriteString(diffIndent + t.added.Render(truncate("+ "+console.FormatText(s.Replacement), width-len(diffIndent))) + "\n")
	}
	return b.String()
}

// helpLine is the bottom line: what the one key that does something does.
func (m model) helpLine(width int) string {
	t := m.opts.theme
	switch {
	case m.done:
		return t.detail.Render(truncate("run "+string(m.status)+": writing the summary", width))
	case m.stopping:
		return t.warning.Render(truncate("stopping: publishing the partial report; ctrl+c again to leave now", width))
	default:
		return t.detail.Render(truncate("ctrl+c: stop, publish partial report", width))
	}
}

// The lines the engine's early phases contribute. Their wording deliberately
// matches internal/console's, so that a user who has seen one renderer reads
// the other without translating.
func baselineProgressLine(e engine.BaselineProgress) string {
	return fmt.Sprintf("baseline run %d/%d: %s", e.Run, e.Of, console.FormatDuration(e.Duration))
}

func baselineCompletedLine(e engine.BaselineCompleted) string {
	return fmt.Sprintf("baseline ok: avg %s, slowest %s, timeout %s (%s)",
		console.FormatDuration(e.Average), console.FormatDuration(e.Slowest),
		console.FormatDuration(e.Timeout), e.TimeoutSource)
}

func discoveredLine(e engine.Discovered) string {
	return fmt.Sprintf("discovered %s, %s", countNoun(e.Candidates, "candidate"), countNoun(e.Skips, "skip"))
}

func validatedLine(e engine.Validated) string {
	return fmt.Sprintf("validated %s, %s", countNoun(e.Accepted, "mutant"), countNoun(e.Rejected, "rejection"))
}

// coverageLine is what the coverage pass established.
//
// It states the uncovered count outright rather than leaving it as the
// remainder, for internal/console's reason: "3 of 4 covered" makes the reader
// do the subtraction that is the whole point of the phase. How much of the run
// is about to be skipped is the single most useful number a coverage-guided run
// has, and a dashboard that dropped it would be the one renderer that did.
func coverageLine(e engine.CoverageMapped) string {
	return fmt.Sprintf("coverage: %d test %s, %d of %d mutants covered, %d uncovered",
		e.Binaries, plural(e.Binaries, "binary", "binaries"),
		e.Covered, e.Covered+e.Uncovered, e.Uncovered)
}

// countNoun renders "1 candidate" or "3 candidates".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// plural picks the spelling a count takes.
func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}

// spread puts left at the start of a line and right at its end.
//
// When the two do not fit, the right-hand side wins: it is the clock, and a
// header whose title is truncated still says what the frame is, while a header
// with no clock has lost the one thing on that line that changes.
func spread(left, right string, width int) string {
	gap := width - printWidth(left) - printWidth(right)
	if gap < 1 {
		room := width - printWidth(right) - 1
		if room < 1 {
			return truncate(right, width)
		}
		return truncate(left, room) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// pad widens a string to a column width with spaces, and leaves a wider one
// alone: padding is for alignment, and truncation is [truncate]'s job.
func pad(s string, width int) string {
	gap := width - printWidth(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// truncate cuts a string to a width, marking that it did.
//
// The cut is made by ansi.Truncate rather than by slicing runes, because most
// of what this package truncates has already been through a [lipgloss.Style]:
// [spread] cuts a rendered header, [scoreLine] cuts a rendered score, and the
// feed cuts a rendered result line. An escape sequence is zero columns wide but
// several runes long, so a rune-indexed cut lands inside one — leaving a bare
// ESC that swallows the bytes after it, or an SGR that was opened and never
// reset, which leaks the colour into every line drawn under it for the rest of
// the alternate screen. ansi.Truncate counts columns for the width and copies
// the escape sequences through whole, the closing reset included.
//
// The ellipsis is part of the width, so the result never exceeds it. Below the
// ellipsis's own width there is no room to mark the cut at all, and the string
// is simply cut: a frame four columns wide has nothing to say anyway, and
// returning "" there — which is what asking ansi.Truncate for a tail wider than
// the whole allowance does — would drop the last legible characters instead.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if printWidth(s) <= width {
		return s
	}
	if width <= len(ellipsis) {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, width, ellipsis)
}

// truncatePath cuts a path from the middle, keeping both ends.
//
// The tail is what identifies the site — the file name and the line — and the
// head is what says which package it is in. What a long path has in the middle
// is directories the reader can infer, so that is what is given up.
func truncatePath(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= len(ellipsis)+2 {
		// Too narrow to keep both ends: keep the tail, which is the half that
		// names the file.
		return truncateHead(s, width)
	}
	keep := width - len(ellipsis)
	tail := keep * 2 / 3
	head := keep - tail
	return string(runes[:head]) + ellipsis + string(runes[len(runes)-tail:])
}

// truncateHead drops the front of a string, keeping its last width characters.
func truncateHead(s string, width int) string {
	runes := []rune(s)
	if width <= 0 {
		return ""
	}
	if len(runes) <= width {
		return s
	}
	return string(runes[len(runes)-width:])
}

// shortID truncates a display id to the width a result line prints, and leaves
// a shorter one alone rather than slicing past its end.
func shortID(displayID string) string {
	if len(displayID) <= displayIDWidth {
		return displayID
	}
	return displayID[:displayIDWidth]
}

// printWidth is how wide a string is on screen, ignoring the escape sequences
// a style put in it. Nothing in this package measures a rendered string with
// len: a coloured label is a dozen bytes wider than it looks.
func printWidth(s string) int { return lipgloss.Width(s) }

// formatClock renders a duration as a wall clock: mm:ss, or h:mm:ss once there
// is an hour of it.
//
// Durations on this screen are read as "how long is this taking", which is a
// question a clock answers better than "1h2m3.004s" does. Per-mutant durations
// are not clocks and keep internal/console's rendering.
func formatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second).Seconds())
	hours, minutes, seconds := total/3600, total/60%60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
