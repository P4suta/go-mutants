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

// OutcomeWidth is the column width of the outcome in a result line.
//
// It is nine because SURVIVED is the longest of the six outcome labels and one
// trailing space separates it from the two that follow, which keeps every id,
// path, and rule in the same column across those six. It is a constant rather
// than a width computed from the data on purpose: a column that grew because
// one outcome appeared would shift every other line the day it stopped
// appearing, and this output is meant to be diffed between two runs.
//
// One label is wider than the column and overflows it: "SURVIVED (uncovered)",
// which shifts the rest of *its own* line eleven characters right and leaves
// every other line alone. That is the deliberate trade. Widening the column to
// twenty would spend eleven characters of every line on a qualifier most lines
// do not carry, and putting the qualifier at the end of the line would bury the
// most actionable thing a run can say — "no test runs this line at all" —
// behind the duration. Uncovered survivors are also grouped together by
// [engine.RunSummary.Notable], so the overflow is a block that lines up with
// itself rather than a stutter through the list.
const OutcomeWidth = 9

// uncoveredSuffix marks a survivor no test binary reaches.
const uncoveredSuffix = " (uncovered)"

// resultIDWidth is how many hex characters of a mutant's display id a result
// line prints. It matches `list`, so the id under a survivor is the id a
// listing showed for the same mutant, and it is long enough to retype into
// `--mutant`, which resolves against the whole catalogue.
const resultIDWidth = 8

// diffIndent is what the two-line survivor diff is indented by, so that it
// reads as belonging to the line above it.
const diffIndent = "    "

// A Renderer turns one engine event stream into output.
//
// The contract is the mirror image of the engine's: Run must read events until
// the channel closes, because the engine's sends block, and it must not return
// early on error without draining. Both implementations satisfy it the same
// way, and [PlainRenderer.Run] is the reference.
type Renderer interface {
	// Run consumes events until the channel is closed and returns the first
	// output error, or nil. It is called once per run, from its own goroutine,
	// started before the engine is.
	Run(ctx context.Context, events <-chan engine.Event) error
}

// A PlainRenderer writes one block of lines per event.
//
// The zero value is not usable: Out must be set. Use [NewPlain], which also
// wraps the writer so that a run does not pay for a syscall per line.
type PlainRenderer struct {
	// Out is where lines go. It is written to from Run's goroutine only.
	Out io.Writer
	// Version is the go-mutants version printed in the header. It is passed in
	// rather than imported so that internal/cli remains the one place the
	// version string exists.
	Version string
	// Color enables ANSI styling. Decide it with [ColorEnabled].
	Color bool
	// Quiet suppresses the header, the phase banners, the baseline progress,
	// and the per-mutant result lines. What survives is what a user who asked
	// for quiet still needs: the baseline summary, every warning, where the
	// report was filed, and the closing summary block — which lists the
	// survivors again, so nothing actionable is lost. Errors are not this
	// type's to print and are unaffected.
	Quiet bool
}

// NewPlain returns a renderer writing buffered lines to out.
func NewPlain(out io.Writer, version string, color, quiet bool) *PlainRenderer {
	return &PlainRenderer{Out: out, Version: version, Color: color, Quiet: quiet}
}

// Run renders events until the channel closes.
//
// ctx is accepted for the [Renderer] contract and deliberately does not abort
// the loop. A cancelled run still has to finish arriving: the engine unwinds,
// cleans up, publishes its warnings, its partial report, and its
// [engine.RunCompleted], and closes. A renderer that stopped reading at the
// cancellation would deadlock the very shutdown it was reacting to, and would
// drop the last thing the user needs to see.
//
// A write failure stops the rendering but not the draining: the loop keeps
// consuming so the engine can finish, and the first error is returned at the
// end.
//
// Every block is flushed as it is written. The buffer is there so that a styled
// line reaches the terminal as one syscall rather than as four fragments, not
// to batch the run's output: a "phase discover" banner exists to say what is
// happening while a large workspace is copied, and a banner that arrives at the
// same moment as the closing line is a banner that was never worth printing.
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

// line renders one event, and reports whether anything should be printed at
// all. Every event this package knows is handled explicitly; an event added to
// the sealed interface later shows up here as the default case and prints
// nothing rather than a Go struct dump.
//
// Three events deliberately print nothing in this renderer.
// [engine.MutantStarted] exists so that a dashboard can show a worker slot
// filling; a plain log that printed it would say everything twice, once when a
// mutant began and once when it settled. [engine.CacheHit] is the same
// judgement: the [engine.MutantFinished] that follows it carries the outcome
// and marks it cached, and a line for each would print every reused mutant
// twice. A [engine.MutantFinished] whose outcome is not-run is the judgement in
// the other direction: the mutant was reached and abandoned when the run was
// cut short, which the closing counts state once rather than a line at a time.
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
		// Dropped under --quiet like the discovery and coverage lines above,
		// and for the same reason: it is progress rather than a finding, and
		// the selection it describes is written down in the report either way.
		return r.paint(styleDetail, narrowedLine(e)), true

	case engine.CoverageMapped:
		if r.Quiet {
			return "", false
		}
		// The number that matters is how many mutants are about to be skipped,
		// so it is stated as a count and not only as the remainder: "3 of 4
		// covered" leaves the reader doing the subtraction that is the whole
		// point of the phase.
		return r.paint(styleDetail, fmt.Sprintf("coverage: %d test %s, %d of %d mutants covered, %d uncovered",
			e.Binaries, plural(e.Binaries, "binary", "binaries"),
			e.Covered, e.Covered+e.Uncovered, e.Uncovered)), true

	case engine.MutantStarted:
		return "", false

	case engine.CacheHit:
		return "", false

	case engine.MutantFinished:
		if r.Quiet {
			return "", false
		}
		return r.result(e.Result)

	case engine.Warning:
		return r.paint(styleWarning, "warning "+e.Code+":") + " " + e.Message, true

	case engine.ReportPublished:
		return "report run: " + e.RunPath + "\nreport latest: " + e.LatestPath, true

	case engine.RunCompleted:
		return r.completed(e), true

	default:
		return "", false
	}
}

// result renders one settled mutant as
// "OUTCOME  ID8  path:line:col  rule  original -> replacement  (duration)",
// with the two-line diff underneath a survivor.
//
// The diff is attached to the line wherever the line appears — as the mutant
// settles, and again in the closing block — rather than only to the first of
// them. A rule that depended on where the line was printed would leave a quiet
// run, which prints no live results, showing survivors with nothing to act on.
func (r *PlainRenderer) result(m engine.MutantResult) (string, bool) {
	label := ResultLabel(m.Outcome, m.Uncovered)
	if label == "" {
		return "", false
	}
	line := r.paint(outcomeStyle(m.Outcome), fmt.Sprintf("%-*s", OutcomeWidth, label)) + "  " +
		shortID(m.DisplayID) + "  " +
		m.Path + ":" + strconv.Itoa(m.Line) + ":" + strconv.Itoa(m.Column) + "  " +
		r.paint(styleRule, m.Rule) + "  " +
		FormatText(m.Original) + " -> " + FormatText(m.Replacement) + "  " +
		r.paint(styleDetail, "("+FormatDuration(m.Duration)+cachedSuffix(m)+")")
	if m.Outcome != mutation.OutcomeSurvived {
		return line, true
	}
	return line + "\n" +
		diffIndent + r.paint(styleRemoved, "- "+FormatText(m.Original)) + "\n" +
		diffIndent + r.paint(styleAdded, "+ "+FormatText(m.Replacement)), true
}

// cachedSuffix marks a result this run adopted rather than measured.
//
// It goes inside the duration's own parentheses because the two facts belong
// together: "(120ms cached)" says that the number is real and that it was
// measured by an earlier run, which is exactly what a reader wondering how a
// thousand mutants finished in four seconds needs to know. Putting it in a
// column of its own would have moved every result line for a feature that is
// off in most runs.
func cachedSuffix(m engine.MutantResult) string {
	if !m.Cached {
		return ""
	}
	return " cached"
}

// completed renders the last block of the run: the closing summary when there
// is one, and the one-line status when the run stopped before it had measured
// anything.
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

// summary renders the closing block: the mutants worth acting on, worst first,
// then the counts, the score, the expectations ledger, the warnings, the skip
// breakdown, and the run's own identity and exit status.
//
// Nothing here is computed. Every number arrives on the event, because a
// renderer that derived one would be a second implementation of a decision —
// what counts as a detection, which mutants are worth listing — and the two
// would eventually disagree in front of a user looking at both.
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
	// Appended rather than slotted in beside `survived`, because it is not a
	// seventh bucket: uncovered mutants are already inside `survived`, and a
	// reader adding the columns up has to still get `mutants`. It appears only
	// in a coverage-guided run — including as "uncovered 0", which is a
	// measurement worth stating — and never in one that did not ask, where the
	// number would be a zero nobody went looking for.
	if s.Coverage == engine.CoveragePackage {
		fmt.Fprintf(&b, "  uncovered %d", c.Uncovered)
	}
	// Appended on the same terms and for the same reason: a cached mutant is
	// already inside one of the buckets, so it is stated beside them rather than
	// among them. "cached 0" from a run whose cache was on is a real
	// measurement — the cache is cold, or the code has moved on — and a run with
	// the cache off says nothing rather than a zero nobody went looking for.
	if s.Cache == engine.CacheOn {
		fmt.Fprintf(&b, "  cached %d", c.Cached)
	}
	b.WriteByte('\n')

	// "n/a" rather than a percentage, and the reason beside it. Both plausible
	// sentinels are lies: 0 reads as "your tests caught nothing" and 100 as
	// "your tests caught everything", when the truth is that nothing was
	// measured.
	if _, defined := s.Score.Percent(); defined {
		b.WriteString(r.paint(styleScore, "score "+s.Score.String()) + "\n")
	} else {
		b.WriteString(r.paint(styleScore, "score N/A") + " (0 valid mutants)\n")
	}

	if e := s.Expectations; e.Total() > 0 {
		fmt.Fprintf(&b, "expectations %d fulfilled  %d unfulfilled  %d stale\n",
			e.Fulfilled, e.Unfulfilled, e.Stale)
	}
	// A count rather than the warnings themselves. Each one was published as it
	// happened and printed then, --quiet included; repeating the text here
	// would say everything twice, and saying nothing at all would let a warning
	// scroll out of sight in a long run with nothing at the end to point at it.
	if s.Warnings > 0 {
		fmt.Fprintf(&b, "%s\n", r.paint(styleWarning, "warnings "+strconv.Itoa(s.Warnings)))
	}
	for _, skip := range s.Skips {
		b.WriteString(r.paint(styleDetail, fmt.Sprintf("skip %s %d", skip.Reason, skip.Count)) + "\n")
	}

	// An interrupted run exits on its signal — 130 for Ctrl-C, 143 for
	// SIGTERM — and only the command line knows which arrived. Printing a
	// number the engine could not have known would be a guess in the one place
	// a reader is entitled to take the output literally. The gate verdict is
	// dropped with it: a run cut short did not finish measuring what the gates
	// are about.
	if status == engine.StatusInterrupted {
		b.WriteString(r.paint(styleFailed, "run "+s.RunID+"  interrupted"))
		return b.String()
	}

	// The gate that failed, named. Half of them are invisible in the numbers
	// above — an empty catalogue, a stale expectations ledger, a mutant the
	// harness could not run — and this is also the only place the reason
	// appears at all, since a policy failure is deliberately not printed to
	// standard error as an error.
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

// ResultLabel returns the label a result line prints for one settled mutant.
//
// It is [OutcomeLabel] plus the one qualifier a label can carry: a survivor no
// test binary reaches prints "SURVIVED (uncovered)", because the two say
// different things to the person reading them. A covered survivor means a test
// runs the line and did not notice the edit, which is a test to sharpen. An
// uncovered one means nothing runs the line at all, which is a test to write —
// and the run did not even execute the mutant to find out.
//
// The qualifier is only ever attached to a survivor, which is the only outcome
// an uncovered mutant can have: nothing ran, so nothing could have caught it.
// An unexpected combination prints the plain label rather than a contradiction.
func ResultLabel(o mutation.Outcome, uncovered bool) string {
	label := OutcomeLabel(o)
	if uncovered && o == mutation.OutcomeSurvived {
		return label + uncoveredSuffix
	}
	return label
}

// OutcomeLabel returns the fixed, uppercase name a result line prints for an
// outcome, or "" for one that gets no line of its own.
//
// The five labels are the vocabulary of the plain output and of the dashboard
// alike, and they are short enough to keep the column narrow.
// [mutation.OutcomeNotRun] deliberately has none: a mutant nothing was learned
// about is a number in the counts, not a line claiming a result.
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
	default:
		return ""
	}
}

// FormatText renders an original or a replacement for a human.
//
// One-line text with no leading or trailing space is printed as it stands,
// which is what makes `== -> !=` read like the edit it is. Anything else is
// quoted, so that a multi-line statement deletion cannot break the
// one-line-per-mutant shape the output promises, and a deletion — an empty
// replacement — prints as `""` rather than as nothing at all.
func FormatText(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "\n\r\t") || strings.TrimSpace(s) != s {
		return strconv.Quote(s)
	}
	return s
}

// shortID truncates a display id to the result-line width, and leaves anything
// shorter alone rather than slicing past its end.
func shortID(displayID string) string {
	if len(displayID) <= resultIDWidth {
		return displayID
	}
	return displayID[:resultIDWidth]
}

// narrowedLine words one [engine.SelectionNarrowed] for a human: what the run
// narrowed itself by, and how much of the catalogue is left.
//
// Both narrowings are named when both applied, in the order they were applied,
// because a shard of a changed run that executes three mutants has two reasons
// for the number and a reader who is told only one of them will go looking for
// the wrong mistake.
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

// countNoun renders "1 candidate" or "3 candidates".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// plural picks between two spellings of a noun, for the ones an "s" does not
// make plural.
func plural(n int, singular, many string) string {
	if n == 1 {
		return singular
	}
	return many
}

// paint applies a style, or does not.
//
// The guard is at the string level rather than inside a configured lipgloss
// renderer on purpose: with colour off, no styling code runs at all, so the
// output cannot depend on what lipgloss decided about the terminal it thinks it
// is attached to.
func (r *PlainRenderer) paint(style lipgloss.Style, s string) string {
	if !r.Color {
		return s
	}
	return style.Render(s)
}

// FormatDuration renders a duration the way every go-mutants line does:
// rounded to the millisecond.
//
// Rounding is not cosmetic. An unrounded Go duration prints all the precision
// the clock had — "151.9873ms" — which is noise in a line a human reads and, in
// a log two runs are diffed from, noise that hides the change that matters. A
// millisecond is finer than any measurement here is meaningful to: the runner's
// own supervision overhead is larger than that.
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Millisecond).String()
}
