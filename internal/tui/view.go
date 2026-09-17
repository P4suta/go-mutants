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

const (
	narrowWidth    = 60
	minWidth       = 24
	minHeight      = 6
	minBarWidth    = 8
	feedReserve    = 3
	displayIDWidth = 8
	diffIndent     = "    "
	ellipsis       = "..."
)

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
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	lines = append(lines, m.helpLine(width))
	return strings.Join(lines, "\n")
}

func (m model) frame() (width, height int) {
	return max(m.width, minWidth), max(m.height, minHeight)
}

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

func (m model) sections(width, height int) (head, workers []string, feedTitle string, feedHeight int) {
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

func (m model) headLines(width, budget int) []string {
	type candidate struct {
		text string
		rank int
	}
	candidates := []candidate{
		{m.headerLine(width), 0},
		{m.phaseLine(width), 3},
		{m.baseline, 4},
		{m.discovery, 4},
		{m.selection, 4},
		{m.coverage, 4},
		{m.scoreLine(width), 0},
		{m.countersLine(width), 1},
		{"", 5},
	}
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

func (m model) headerLine(width int) string {
	t := m.opts.theme
	left := "go-mutants " + m.opts.version
	if m.runID != "" && width >= narrowWidth {
		left += "  run " + m.runID
	}
	return spread(t.header.Render(left), t.detail.Render("elapsed "+formatClock(m.elapsed())), width)
}

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
		barWidth := width - printWidth(score) - printWidth(tail) - 5
		if barWidth >= minBarWidth {
			bar = "[" + gauge(percent/100, barWidth, t.gaugeOn, t.gaugeOff) + "] "
		}
	}
	return score + "  " + bar + tail
}

func (m model) progressText() string {
	t := m.opts.theme
	if m.total <= 0 {
		return t.detail.Render(strconv.Itoa(m.decided) + " done")
	}
	return t.detail.Render(strconv.Itoa(m.decided) + "/" + strconv.Itoa(m.total))
}

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
	return truncate(strings.Join(parts, "  "), width)
}

func (m model) workerLines(width, budget int) []string {
	if len(m.slots) == 0 || budget < 2 {
		return nil
	}
	t := m.opts.theme
	lines := make([]string, 0, len(m.slots)+2)
	lines = append(lines, t.label.Render("workers"))
	rows := min(len(m.slots), budget-2)
	if rows < len(m.slots) && rows > 0 {
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

const workerIndent = " "

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
	room := width - len(prefix) - displayIDWidth - 2 - printWidth(rule) - printWidth(right)
	location := pad(truncatePath(s.path+":"+strconv.Itoa(s.line), room), room)
	return prefix + shortID(s.displayID) + "  " + location + rule + right
}

func (m model) feedTitleLine(width int) string {
	t := m.opts.theme
	title := "survivors " + strconv.Itoa(len(m.survivors))
	if len(m.survivors) == 0 {
		return t.label.Render(truncate("survivors none yet", width))
	}
	return t.failed.Render(truncate(title, width))
}

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
		b.WriteString(truncate(head+truncatePath(location, room)+rule, width) + "\n")
		b.WriteString(diffIndent + t.removed.Render(truncate("- "+console.FormatText(s.Original), width-len(diffIndent))) + "\n")
		b.WriteString(diffIndent + t.added.Render(truncate("+ "+console.FormatText(s.Replacement), width-len(diffIndent))) + "\n")
	}
	return b.String()
}

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

func narrowedLine(e engine.SelectionNarrowed) string {
	var reasons []string
	if e.ChangedRef != "" {
		reasons = append(reasons, "lines changed since "+e.ChangedRef)
	}
	if e.Shards > 0 {
		reasons = append(reasons, fmt.Sprintf("shard %d of %d", e.Shard, e.Shards))
	}
	return fmt.Sprintf("selection: %d of %s selected by %s",
		e.Selected, countNoun(e.Of, "mutant"), strings.Join(reasons, " and "))
}

func coverageLine(e engine.CoverageMapped) string {
	scope := fmt.Sprintf("%d test %s", e.Binaries, plural(e.Binaries, "binary", "binaries"))
	if e.Tests > 0 {
		scope = fmt.Sprintf("%d %s of %s", e.Tests, plural(e.Tests, "test", "tests"), scope)
	}
	line := fmt.Sprintf("coverage: %s, %d of %d mutants covered, %d uncovered",
		scope, e.Covered, e.Covered+e.Uncovered, e.Uncovered)
	if e.Widened > 0 {
		line += fmt.Sprintf(", %d widened to whole binaries", e.Widened)
	}
	return line
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}

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

func pad(s string, width int) string {
	gap := width - printWidth(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

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

func truncatePath(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= len(ellipsis)+2 {
		return truncateHead(s, width)
	}
	keep := width - len(ellipsis)
	tail := keep * 2 / 3
	head := keep - tail
	return string(runes[:head]) + ellipsis + string(runes[len(runes)-tail:])
}

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

func shortID(displayID string) string {
	if len(displayID) <= displayIDWidth {
		return displayID
	}
	return displayID[:displayIDWidth]
}

func printWidth(s string) int { return lipgloss.Width(s) }

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
