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
// They differ in one row and say so. A listing has the whole discovery pass in
// memory and prints the coordinates of every suppressed site; a run is
// rendering a report, which keeps the count per file, so it prints the file and
// names the command that has the rest.
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
//
// The sites come from the discovery pass rather than from the document, and
// deliberately so: the catalogue document carries the aggregate, exactly as the
// run report does, and a listing is the one place with a whole pass still in
// memory to ask for the coordinates.
func explainListing(w io.Writer, color bool, skips []catalogSkip, sites []discover.SkipSite) error {
	e := newExplainer(w, color)
	e.skipSites(catalogSkipRows(skips), sites)
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

// skips writes the per-reason breakdown with one row per file, which is as
// much as a run can say.
//
// A run report carries the aggregate and nothing finer. That is a decision
// about a document rather than about the walk: the coordinates exist — every
// suppression is recorded with them, and `list --explain` prints them — but a
// document other tools read and diff should not grow forty positions per file
// for a phase whose output nobody consumes per site. So this names the file and
// points at the command that names the line, which is a workspace away rather
// than a re-run away.
func (e *explainer) skips(rows []skipRow) {
	e.skipSection(rows,
		"the report keeps the count per file; `go-mutants list --explain` prints the line and column of each one",
		func(reason string) {
			for _, row := range rows {
				if row.reason == reason {
					e.printf("  %s  %s\n", row.path, e.paint(styleExplainDetail, countNoun(row.count, "site")))
				}
			}
		})
}

// skipSites writes the same breakdown with one row per suppressed site.
//
// Coordinates are what makes the section answer the question it is read with —
// which of the forty expressions in this file was passed over, and where do I
// go to look at it. They cost one position lookup at a place discovery is
// already holding the token position, which is why the earlier argument against
// them ("carrying a list the length of the file's expressions") was an argument
// about a cost that is not paid: the list is the suppressions, not the
// expressions, and it is already being counted one at a time.
//
// One row is one suppressed *candidate*, so a position that two rules both
// proposed an edit at is printed twice. That is what keeps the rows summing to
// the count above them, and it is true: two edits really were declined there.
func (e *explainer) skipSites(rows []skipRow, sites []discover.SkipSite) {
	e.skipSection(rows, "", func(reason string) {
		for _, site := range sites {
			if string(site.Reason) == reason {
				e.printf("  %s\n", siteLocation(site))
			}
		}
	})
}

// siteLocation renders one site as `path:line:col`, or as the bare path for a
// whole-file reason.
//
// `path:0:0` would be a position, and there is none: a generated, cgo or
// excluded file is never opened, so nothing here knows where in it a candidate
// would have been. The bare path says exactly that.
func siteLocation(site discover.SkipSite) string {
	if site.Line == 0 {
		return site.Path
	}
	return site.Path + ":" + strconv.Itoa(site.Line) + ":" + strconv.Itoa(site.Column)
}

// skipSection writes the heading and one block per reason, with the rows of a
// block written by the caller.
//
// Reasons are the outer grouping because a reason is the actionable half: "this
// tree has forty constant expressions in it" is one decision to understand,
// while forty locations are the evidence for it. Within a reason the rows keep
// the order their source is already sorted in — (path, reason) for a document's
// skips, (path, line, column) for a pass's sites — so two runs over one
// workspace produce the same block and the two can be diffed.
//
// The preamble is the one line that differs between the two commands, and it is
// written under the heading rather than at the end of the section: it says what
// kind of rows are about to be read, which is of no use after they have been.
func (e *explainer) skipSection(rows []skipRow, preamble string, body func(reason string)) {
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
	if preamble != "" {
		e.printf("%s\n", e.paint(styleExplainDetail, preamble))
	}

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
		body(reason)
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
