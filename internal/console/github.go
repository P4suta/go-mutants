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

// GitHub Actions gives a job two places to say something a human will read
// without opening a log, and go-mutants writes to both.
//
// # The step summary
//
// `$GITHUB_STEP_SUMMARY` names a file whose Markdown is rendered at the top of
// the job's page. It is *appended* to, never truncated: several steps of one
// job write to the same file, and a mutation run that wiped out the build step's
// summary would be a bad neighbour. What goes in it is the score, the counts,
// and the survivors worth acting on — the same three things the closing console
// block leads with, because somebody reading the job page and somebody reading
// the log should not have to reconcile two different summaries.
//
// # The annotations
//
// A `::warning file=,line=,col=::` line on standard output makes GitHub draw a
// marker in the pull request's diff, at the exact line. That is the one place a
// mutation report can reach a reviewer who was never going to open an artefact,
// so one annotation is emitted per *unexpected* survivor: an expected one is a
// declared equivalent mutant, and annotating it would train everybody to ignore
// the markers.
//
// # Why the report and not the event stream
//
// Both are composed from [report.Report] rather than from the closing summary
// event. The document is what was published, it carries the expectations ledger
// the "unexpected" in "unexpected survivor" is decided from, and building the
// output from it means these functions can be tested with a report and a
// temporary file instead of a whole engine run.

// GitHubSummaryEnv is the environment variable naming the step summary file.
const GitHubSummaryEnv = "GITHUB_STEP_SUMMARY"

// maxAnnotatedSurvivors caps how many survivors are listed in the step summary.
//
// A run over a large module can produce hundreds, and a job page with a
// four-hundred-row table in it is a job page nobody scrolls. The cap is on the
// *table* only: every unexpected survivor still gets its own annotation, since
// those are attached to the lines they belong to and a reviewer only ever sees
// the ones in the file they are looking at.
const maxAnnotatedSurvivors = 10

// GitHubStepSummary renders the Markdown block for the job page.
//
// It ends with a newline and is safe to append to a file another step has
// already written to; it begins with a heading, so it cannot run into whatever
// is above it.
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

// GitHubAnnotations renders one workflow command per unexpected survivor.
//
// The empty string is a run with nothing to annotate, and printing it writes
// nothing at all — rather than a blank line into a log that a job page turns
// into an empty annotation.
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

// EmitGitHub writes both halves: the annotations to w, and the step summary
// appended to the file named by summaryPath.
//
// summaryPath empty means the variable was not set, so there is no job page to
// write to and only the annotations are emitted. Both are the caller's to
// suppress under `--json`, where standard output belongs to the document; see
// the package documentation.
//
// The annotations go first and the file second, so that a job whose runner
// cannot write the summary file still gets the markers in its diff — which are
// the half a reviewer actually sees.
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
	// Appended, and created when it is not there: the runner makes the file,
	// but a `docker run` of the same workflow locally may not have, and failing
	// a run over a missing scratch file would be a poor trade.
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

// scoreLine states the score, or says plainly that there is none.
func scoreLine(r *report.Report) string {
	if r.Summary.ScorePercent == nil {
		return "**Score N/A** — no mutant counted towards a score."
	}
	return fmt.Sprintf("**Score %.2f%%** — %d of %d detected.",
		*r.Summary.ScorePercent,
		r.Summary.Killed+r.Summary.TimedOut,
		r.Summary.Killed+r.Summary.TimedOut+r.Summary.Survived)
}

// countsTable is the breakdown, as a two-row table so that the job page shows
// it as a table rather than as a paragraph of numbers.
func countsTable(r *report.Report) string {
	s := r.Summary
	return "| Mutants | Killed | Survived | Timeout | Inconclusive | Errored | Not run |\n" +
		"| ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n" +
		fmt.Sprintf("| %d | %d | %d | %d | %d | %d | %d |\n",
			s.Total, s.Killed, s.Survived, s.TimedOut, s.Inconclusive, s.Errored, s.NotRun)
}

// unexpectedSurvivors are the survivors the `[[mutation.expect]]` ledger did
// not predict, in document order.
//
// A fulfilled expectation is a mutant somebody has written down as equivalent,
// with a reason, and it is exactly the mutant that must not produce a warning:
// an annotation on a declared equivalent is noise a reviewer learns to scroll
// past, and once they have learned that they scroll past the real ones too.
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

// cell makes one value safe inside a Markdown table.
//
// A pipe would start a new column and a newline would end the row, and both are
// perfectly ordinary things to find in a mutant's original text — `a || b` is a
// boolean-connective mutation and a statement deletion can span lines. The
// escape for a pipe is a backslash even inside a code span, which is the one
// place GitHub's Markdown makes an exception to "code spans are literal".
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "`", "'")
	return strings.Join(strings.Fields(s), " ")
}

// escapeData escapes the message half of a workflow command, as GitHub's own
// toolkit does: a literal '%' first, so that the escapes introduced after it
// are not escaped again, then the two line endings that would otherwise end the
// command early.
func escapeData(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return strings.ReplaceAll(s, "\n", "%0A")
}

// escapeProperty escapes a property value, which needs the two separators of
// the property list as well: a comma would start another property and a colon
// would end the list.
func escapeProperty(s string) string {
	s = escapeData(s)
	s = strings.ReplaceAll(s, ":", "%3A")
	return strings.ReplaceAll(s, ",", "%2C")
}
