// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/report"
)

const GitHubSummaryEnv = "GITHUB_STEP_SUMMARY"

const maxAnnotatedSurvivors = 10

func GitHubStepSummary(r *report.Report) string {
	var b strings.Builder
	b.WriteString("## go-mutants\n\n")
	b.WriteString(scoreLine(r) + "\n\n")
	b.WriteString(countsTable(r))

	survivors := unexpectedSurvivors(r)
	if len(survivors) == 0 {
		b.WriteString("\nNo mutant survived unexpectedly.\n")
		return b.String()
	}
	b.WriteString("\n### Survivors\n\n")
	b.WriteString("| Mutant | Location | Mutation |\n| --- | --- | --- |\n")
	shown := min(len(survivors), maxAnnotatedSurvivors)
	for _, m := range survivors[:shown] {
		fmt.Fprintf(&b, "| `%s` | `%s:%d:%d` | `%s` |\n",
			cell(m.DisplayID), cell(m.Path), m.Line, m.Column,
			cell(m.Rule+" "+FormatText(m.Original)+" -> "+FormatText(m.Replacement)))
	}
	if rest := len(survivors) - shown; rest > 0 {
		fmt.Fprintf(&b, "\n%s more in the full report.\n", strconv.Itoa(rest))
	}
	return b.String()
}

func GitHubAnnotations(r *report.Report) string {
	var b strings.Builder
	for _, m := range unexpectedSurvivors(r) {
		b.WriteString("::warning file=" + escapeProperty(m.Path) +
			",line=" + strconv.Itoa(m.Line) +
			",col=" + strconv.Itoa(m.Column) +
			"::" + escapeData("mutant "+m.DisplayID+" survived ("+
			m.Rule+" "+FormatText(m.Original)+" -> "+FormatText(m.Replacement)+")") + "\n")
	}
	return b.String()
}

func EmitGitHub(w io.Writer, summaryPath string, r *report.Report) error {
	if r == nil {
		return nil
	}
	if annotations := GitHubAnnotations(r); annotations != "" {
		if _, err := io.WriteString(w, annotations); err != nil {
			return err
		}
	}
	if summaryPath == "" {
		return nil
	}
	file, err := os.OpenFile(summaryPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(GitHubStepSummary(r))
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

func scoreLine(r *report.Report) string {
	if r.Summary.ScorePercent == nil {
		return "**Score N/A** — no mutant counted towards a score."
	}
	return fmt.Sprintf("**Score %.2f%%** — %d of %d detected.",
		*r.Summary.ScorePercent,
		r.Summary.Killed+r.Summary.TimedOut,
		r.Summary.Killed+r.Summary.TimedOut+r.Summary.Survived)
}

func countsTable(r *report.Report) string {
	s := r.Summary
	return "| Mutants | Killed | Survived | Timeout | Inconclusive | Errored | Not run |\n" +
		"| ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n" +
		fmt.Sprintf("| %d | %d | %d | %d | %d | %d | %d |\n",
			s.Total, s.Killed, s.Survived, s.TimedOut, s.Inconclusive, s.Errored, s.NotRun)
}

func unexpectedSurvivors(r *report.Report) []report.Mutant {
	expected := make(map[string]bool, len(r.Expectations))
	for _, e := range r.Expectations {
		if e.State == report.StateFulfilled {
			expected[e.ID] = true
		}
	}
	out := make([]report.Mutant, 0, len(r.Mutants))
	for _, m := range r.Mutants {
		if m.Outcome == report.OutcomeSurvived && !expected[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "`", "'")
	return strings.Join(strings.Fields(s), " ")
}

func escapeData(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return strings.ReplaceAll(s, "\n", "%0A")
}

func escapeProperty(s string) string {
	s = escapeData(s)
	s = strings.ReplaceAll(s, ":", "%3A")
	return strings.ReplaceAll(s, ",", "%2C")
}
