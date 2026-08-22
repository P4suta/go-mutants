// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/report"
)

// `--explain` is the answer to "why is this listing smaller than I expected".
//
// Both commands print the same two kinds of detail, in the same shape, and the
// shape is the point: a run and a listing over one workspace suppress the same
// sites for the same reasons, so a user who has learned to read one has learned
// to read the other.
//
// Everything printed here is already in the JSON documents, which is why
// `--explain` and `--json` are refused together rather than combined: the
// document is the machine-readable form and this is the human one, and a
// command that wrote both would be writing the same facts twice in two
// languages on one stream.
//
// Nothing here is padded to a width computed from the data. A column that grows
// because one path is long is a column that shifts every other line the day that
// file is renamed, and this output is meant to be diffed between two runs.

// A skipRow is one recorded suppression, as either document spells it.
//
// It exists so that this file has one renderer rather than two: `list` carries
// [catalogSkip] and `run` carries [report.Skip], the two are the same three
// fields, and a second implementation would be a second chance for the two
// commands to disagree about something they observed together.
type skipRow struct {
	path   string
	reason string
	count  int
}

// catalogSkipRows adapts a listing's skips.
func catalogSkipRows(skips []catalogSkip) []skipRow {
	rows := make([]skipRow, 0, len(skips))
	for _, skip := range skips {
		rows = append(rows, skipRow{path: skip.Path, reason: skip.Reason, count: skip.Count})
	}
	return rows
}

// reportSkipRows adapts a report's skips.
func reportSkipRows(skips []report.Skip) []skipRow {
	rows := make([]skipRow, 0, len(skips))
	for _, skip := range skips {
		rows = append(rows, skipRow{path: skip.Path, reason: skip.Reason, count: skip.Count})
	}
	return rows
}

// The explanation styles, which are internal/console's eight ANSI colours
// rather than a palette of their own.
var (
	styleExplainHeader = lipgloss.NewStyle().Bold(true)
	styleExplainDetail = lipgloss.NewStyle().Faint(true)
)

// An explainer writes the detail sections.
type explainer struct {
	out   *bufio.Writer
	color bool
}

// newExplainer wraps a writer.
func newExplainer(w io.Writer, color bool) *explainer {
	return &explainer{out: bufio.NewWriter(w), color: color}
}

// explainListing writes the skip detail underneath a listing.
func explainListing(w io.Writer, color bool, skips []catalogSkip) error {
	e := newExplainer(w, color)
	e.skips(catalogSkipRows(skips))
	return e.out.Flush()
}

// explainRun writes the rejection and skip detail underneath a run's summary.
//
// The rejections come first because they are the ones a user can act on: a
// rejected mutant is a mutant go-mutants wanted to make and the compiler
// refused, so the diagnostic underneath it is either a limit of the guard forms
// or a mutant that could never have meant anything — and which of the two it is
// is only visible in the compiler's own words.
func explainRun(w io.Writer, color bool, r *report.Report) error {
	e := newExplainer(w, color)
	e.rejections(r.Rejected)
	e.skips(reportSkipRows(r.Skips))
	return e.out.Flush()
}

// rejections writes one block per mutant validation refused.
//
// The diagnostic is printed whole and indented rather than folded onto one
// line. A compiler complaint is often two lines — the mismatch, then the type
// it could not be — and the second is usually the one that says whether the
// rewrite could ever have worked. This is the one place in the tool that has
// room for it.
func (e *explainer) rejections(rejected []report.Rejected) {
	if len(rejected) == 0 {
		return
	}
	e.printf("\n%s\n", e.paint(styleExplainHeader,
		"rejected mutants ("+strconv.Itoa(len(rejected))+")"))
	e.printf("%s\n", e.paint(styleExplainDetail,
		"the instrumented snapshot would not compile with these spliced in, so they were never executed"))
	for _, r := range rejected {
		e.printf("\n%s  %s:%d:%d  %s\n",
			shortID(r.DisplayID), r.Path, r.Line, r.Column, e.paint(styleListRule, r.Rule))
		e.printf("%s\n", e.paint(styleExplainDetail, indent(r.Diagnostic)))
	}
}

// skips writes the per-reason breakdown and the files underneath each reason.
//
// Reasons are the outer grouping because a reason is the actionable half: "this
// tree has forty constant expressions in it" is one decision to understand,
// while forty file names are the evidence for it. Within a reason the files
// keep the report's order, which is (path, reason) — already sorted, already
// diffable — so two runs over one workspace produce the same block.
//
// Line numbers are deliberately absent. Discovery aggregates its suppressions
// per file and per reason as it walks, which is what keeps a skip cheap enough
// to record for every site; keeping the coordinates of each one would mean
// carrying a list the length of the file's expressions through a phase whose
// output nobody reads per site. The file and the reason are enough to find
// them, and inventing precision the input does not have would be worse than
// leaving it out.
func (e *explainer) skips(rows []skipRow) {
	if len(rows) == 0 {
		return
	}
	total := 0
	for _, row := range rows {
		total += row.count
	}
	e.printf("\n%s\n", e.paint(styleExplainHeader,
		"suppressed sites ("+strconv.Itoa(total)+")"))
	e.printf("%s\n", e.paint(styleExplainDetail,
		"discovery passed these over; they are never candidates, so they are in no score"))

	for _, reason := range reasonsOf(rows) {
		count := 0
		for _, row := range rows {
			if row.reason == reason {
				count += row.count
			}
		}
		e.printf("\n%s %s\n", e.paint(styleListRule, reason), countNoun(count, "site"))
		if explanation := discover.SkipReason(reason).Explanation(); explanation != "" {
			e.printf("%s\n", e.paint(styleExplainDetail, "  "+explanation))
		}
		for _, row := range rows {
			if row.reason == reason {
				e.printf("  %s  %s\n", row.path, e.paint(styleExplainDetail, countNoun(row.count, "site")))
			}
		}
	}
}

// reasonsOf returns the distinct reasons present, sorted, so that the blocks
// come out in one order whatever order the rows arrived in.
func reasonsOf(rows []skipRow) []string {
	reasons := make([]string, 0, len(rows))
	for _, row := range rows {
		if !slices.Contains(reasons, row.reason) {
			reasons = append(reasons, row.reason)
		}
	}
	slices.Sort(reasons)
	return reasons
}

// indent puts two spaces in front of every line of a block, so that a
// multi-line diagnostic reads as one thing attached to the line above it.
func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

// countNoun renders "1 site" or "3 sites".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + plural(noun)
}

// plural is the one English rule the nouns here need beyond adding an "s":
// "directory" pluralises as "directories", and a tool that writes "2
// directory" with an "s" on the end looks careless in the one place it is
// asking to be trusted with deleting files. Every other noun in these messages
// — outcome, mutant, run, check, key, site, day — takes a plain "s", and a noun
// that needs a third rule should be spelled out by its caller rather than turn
// this into a dictionary.
func plural(noun string) string {
	if rest, ok := strings.CutSuffix(noun, "y"); ok && !strings.HasSuffix(rest, "a") &&
		!strings.HasSuffix(rest, "e") && !strings.HasSuffix(rest, "o") && !strings.HasSuffix(rest, "u") {
		return rest + "ies"
	}
	return noun + "s"
}

// printf appends to the buffer. The write error is deliberately dropped: a
// bufio.Writer remembers the first failure and returns it from Flush, which is
// the one place this reports one.
func (e *explainer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.out, format, args...)
}

// paint applies a style, or does not. The guard is at the string level, exactly
// as in internal/console and in the listing renderer: with colour off no
// styling code runs at all, so the bytes cannot depend on what lipgloss decided
// about the terminal it thinks it is attached to.
func (e *explainer) paint(style lipgloss.Style, s string) string {
	if !e.color {
		return s
	}
	return style.Render(s)
}
