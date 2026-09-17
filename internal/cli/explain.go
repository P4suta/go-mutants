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

type skipRow struct {
	path   string
	reason string
	count  int
}

func catalogSkipRows(skips []catalogSkip) []skipRow {
	rows := make([]skipRow, 0, len(skips))
	for _, skip := range skips {
		rows = append(rows, skipRow{path: skip.Path, reason: skip.Reason, count: skip.Count})
	}
	return rows
}

func reportSkipRows(skips []report.Skip) []skipRow {
	rows := make([]skipRow, 0, len(skips))
	for _, skip := range skips {
		rows = append(rows, skipRow{path: skip.Path, reason: skip.Reason, count: skip.Count})
	}
	return rows
}

var (
	styleExplainHeader = lipgloss.NewStyle().Bold(true)
	styleExplainDetail = lipgloss.NewStyle().Faint(true)
)

type explainer struct {
	out   *bufio.Writer
	color bool
}

func newExplainer(w io.Writer, color bool) *explainer {
	return &explainer{out: bufio.NewWriter(w), color: color}
}

func explainListing(w io.Writer, color bool, skips []catalogSkip, sites []discover.SkipSite) error {
	e := newExplainer(w, color)
	e.skipSites(catalogSkipRows(skips), sites)
	return e.out.Flush()
}

func explainRun(w io.Writer, color bool, r *report.Report) error {
	e := newExplainer(w, color)
	e.rejections(r.Rejected)
	e.skips(reportSkipRows(r.Skips))
	return e.out.Flush()
}

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

func (e *explainer) skipSites(rows []skipRow, sites []discover.SkipSite) {
	e.skipSection(rows, "", func(reason string) {
		for _, site := range sites {
			if string(site.Reason) == reason {
				e.printf("  %s\n", siteLocation(site))
			}
		}
	})
}

func siteLocation(site discover.SkipSite) string {
	if site.Line == 0 {
		return site.Path
	}
	where := site.Path + ":" + strconv.Itoa(site.Line) + ":" + strconv.Itoa(site.Column)
	if site.Rule == "" {
		return where
	}
	return where + " " + site.Rule
}

func siteLocationOf(site accountSkipSite) string {
	if site.Line == nil {
		return site.Path
	}
	where := site.Path + ":" + strconv.Itoa(*site.Line) + ":" + strconv.Itoa(*site.Column)
	if site.Rule == nil {
		return where
	}
	return where + " " + *site.Rule
}

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

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + plural(noun)
}

func plural(noun string) string {
	if rest, ok := strings.CutSuffix(noun, "y"); ok && !strings.HasSuffix(rest, "a") &&
		!strings.HasSuffix(rest, "e") && !strings.HasSuffix(rest, "o") && !strings.HasSuffix(rest, "u") {
		return rest + "ies"
	}
	return noun + "s"
}

func (e *explainer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.out, format, args...)
}

func (e *explainer) paint(style lipgloss.Style, s string) string {
	if !e.color {
		return s
	}
	return style.Render(s)
}
