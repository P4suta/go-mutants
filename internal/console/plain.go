// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
)

const OutcomeWidth = 9

const uncoveredSuffix = " (uncovered)"

const resultIDWidth = 8

const diffIndent = "    "

type Renderer interface {
	Run(ctx context.Context, events <-chan engine.Event) error
}

type PlainRenderer struct {
	Out       io.Writer
	Version   string
	Color     bool
	Quiet     bool
	Verbosity int
}

func NewPlain(out io.Writer, version string, color, quiet bool) *PlainRenderer {
	return &PlainRenderer{Out: out, Version: version, Color: color, Quiet: quiet}
}

func (r *PlainRenderer) Run(ctx context.Context, events <-chan engine.Event) error {
	_ = ctx
	w := bufio.NewWriter(r.Out)
	var failure error
	for event := range events {
		if failure != nil {
			continue
		}
		line, ok := r.line(event)
		if !ok {
			continue
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			failure = err
			continue
		}
		if err := w.Flush(); err != nil {
			failure = err
		}
	}
	return failure
}

func (r *PlainRenderer) line(event engine.Event) (string, bool) {
	switch e := event.(type) {
	case engine.RunPlanned:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleHeader, fmt.Sprintf("go-mutants %s (run %s)", r.Version, e.RunID)), true

	case engine.PhaseChanged:
		if r.Quiet {
			return "", false
		}
		return r.paint(stylePhase, "phase "+e.Phase.String()+":") + " " + e.Detail, true

	case engine.BaselineProgress:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleDetail, fmt.Sprintf("baseline run %d/%d: %s",
			e.Run, e.Of, FormatDuration(e.Duration))), true

	case engine.BaselineCompleted:
		return r.paint(styleOK, "baseline ok:") + fmt.Sprintf(" avg %s, slowest %s, timeout %s (%s)",
			FormatDuration(e.Average), FormatDuration(e.Slowest),
			FormatDuration(e.Timeout), e.TimeoutSource), true

	case engine.MemoryDerived:
		if r.Quiet || r.Verbosity < VerbosityDetail {
			return "", false
		}
		return r.paint(styleDetail, memoryDerivedLine(e)), true

	case engine.Discovered:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleDetail, fmt.Sprintf("discovered %s, %s",
			countNoun(e.Candidates, "candidate"), countNoun(e.Skips, "skip"))), true

	case engine.Validated:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleDetail, fmt.Sprintf("validated %s, %s",
			countNoun(e.Accepted, "mutant"), countNoun(e.Rejected, "rejection"))), true

	case engine.SelectionNarrowed:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleDetail, narrowedLine(e)), true

	case engine.CoverageMapped:
		if r.Quiet {
			return "", false
		}
		return r.paint(styleDetail, coverageMappedLine(e)), true

	case engine.MutantStarted:
		return "", false

	case engine.CacheHit:
		return "", false

	case engine.PhaseCompleted:
		return r.phaseCompleted(e)

	case engine.Traced:
		return r.traced(e.Event)

	case engine.DirectoryKept:
		return "kept " + e.Kind + ": " + e.Path, true

	case engine.MutantFinished:
		if r.Quiet {
			return "", false
		}
		return r.result(e.Result)

	case engine.Warning:
		return r.paint(styleWarning, "warning "+e.Code+":") + " " + e.Message + r.warningDetail(e), true

	case engine.ReportPublished:
		return publishedPaths(e), true

	case engine.RunCompleted:
		return r.completed(e), true

	default:
		return "", false
	}
}

func publishedPaths(e engine.ReportPublished) string {
	lines := []string{
		"report run: " + e.RunPath,
		"report latest: " + e.LatestPath,
	}
	if e.ProjectionPath != "" {
		lines = append(lines, "report json: "+e.ProjectionPath)
	}
	if e.HTMLPath != "" {
		lines = append(lines, "report html: "+e.HTMLPath)
	}
	if e.TracePath != "" {
		lines = append(lines, "trace: "+e.TracePath)
	}
	return strings.Join(lines, "\n")
}

func (r *PlainRenderer) result(m engine.MutantResult) (string, bool) {
	label := ResultLabel(m.Outcome, m.Uncovered)
	if label == "" {
		return "", false
	}
	line := r.paint(outcomeStyle(m.Outcome), fmt.Sprintf("%-*s", OutcomeWidth, label)) + "  " +
		shortID(m.DisplayID) + "  " +
		engine.WorkspaceLocation(m.ModuleDir, m.Path) + ":" +
		strconv.Itoa(m.Line) + ":" + strconv.Itoa(m.Column) + "  " +
		r.paint(styleRule, m.Rule) + "  " +
		FormatText(m.Original) + " -> " + FormatText(m.Replacement) + "  " +
		r.paint(styleDetail, "("+FormatDuration(m.Duration)+cachedSuffix(m)+")") +
		r.attribution(m)
	if m.Outcome != mutation.OutcomeSurvived {
		return line, true
	}
	return line + "\n" +
		diffIndent + r.paint(styleRemoved, "- "+FormatText(m.Original)) + "\n" +
		diffIndent + r.paint(styleAdded, "+ "+FormatText(m.Replacement)) +
		r.covering(m), true
}

func cachedSuffix(m engine.MutantResult) string {
	if !m.Cached {
		return ""
	}
	return " cached"
}

func (r *PlainRenderer) completed(e engine.RunCompleted) string {
	if e.Run == nil {
		style := styleOK
		if e.Status != engine.StatusOK {
			style = styleFailed
		}
		line := r.paint(style, "run "+e.Status.String()+":")
		if e.Summary == "" {
			return line
		}
		return line + " " + e.Summary
	}
	return r.summary(*e.Run, e.Status)
}

func (r *PlainRenderer) summary(s engine.RunSummary, status engine.Status) string {
	var b strings.Builder
	for _, m := range s.Notable {
		if line, ok := r.result(m); ok {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	c := s.Counts
	fmt.Fprintf(&b, "mutants %d  killed %d  survived %d  timeout %d  inconclusive %d  errored %d  not-run %d  rejected %d",
		c.Total, c.Killed, c.Survived, c.TimedOut, c.Inconclusive, c.Errored, c.NotRun, c.Rejected)
	if s.Coverage.Narrowed() {
		fmt.Fprintf(&b, "  uncovered %d", c.Uncovered)
	}
	if s.Cache == engine.CacheOn {
		fmt.Fprintf(&b, "  cached %d", c.Cached)
	}
	b.WriteByte('\n')

	if _, defined := s.Score.Percent(); defined {
		b.WriteString(r.paint(styleScore, "score "+s.Score.String()) + "\n")
	} else {
		b.WriteString(r.paint(styleScore, "score N/A") + " (0 valid mutants)\n")
	}

	if e := s.Expectations; e.Total() > 0 {
		fmt.Fprintf(&b, "expectations %d fulfilled  %d unfulfilled  %d stale\n",
			e.Fulfilled, e.Unfulfilled, e.Stale)
	}
	if s.Warnings > 0 {
		fmt.Fprintf(&b, "%s\n", r.paint(styleWarning, "warnings "+strconv.Itoa(s.Warnings)))
	}
	for _, skip := range s.Skips {
		b.WriteString(r.paint(styleDetail, fmt.Sprintf("skip %s %d", skip.Reason, skip.Count)) + "\n")
	}

	if status == engine.StatusInterrupted {
		b.WriteString(r.paint(styleFailed, "run "+s.RunID+"  interrupted"))
		return b.String()
	}

	if s.Failure.Reason != "" {
		b.WriteString(r.paint(styleFailed, "failed "+s.Failure.Reason.String()) + ": " + s.Failure.Detail + "\n")
	}
	style := styleOK
	if s.ExitCode != mutation.ExitOK {
		style = styleFailed
	}
	b.WriteString(r.paint(style, "run "+s.RunID+"  exit "+s.ExitCode.String()))
	return b.String()
}

func ResultLabel(o mutation.Outcome, uncovered bool) string {
	label := OutcomeLabel(o)
	if uncovered && o == mutation.OutcomeSurvived {
		return label + uncoveredSuffix
	}
	return label
}

func OutcomeLabel(o mutation.Outcome) string {
	switch o {
	case mutation.OutcomeKilled:
		return "KILLED"
	case mutation.OutcomeSurvived:
		return "SURVIVED"
	case mutation.OutcomeTimedOut:
		return "TIMEOUT"
	case mutation.OutcomeInconclusive:
		return "INCONCL"
	case mutation.OutcomeErrored:
		return "ERROR"
	case mutation.OutcomeNotRun:
		return ""
	}
	return ""
}

func FormatText(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "\n\r\t") || strings.TrimSpace(s) != s {
		return strconv.Quote(s)
	}
	return s
}

func shortID(displayID string) string {
	if len(displayID) <= resultIDWidth {
		return displayID
	}
	return displayID[:resultIDWidth]
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

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func coverageMappedLine(e engine.CoverageMapped) string {
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

func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}

func (r *PlainRenderer) paint(style lipgloss.Style, s string) string {
	if !r.Color {
		return s
	}
	return style.Render(s)
}

func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Millisecond).String()
}
