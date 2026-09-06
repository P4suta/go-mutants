// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/trace"
)

const traceLong = `Read the recordings ` + "`run --trace`" + ` wrote.

A trace is the diagnostic account of one run: the phases it passed through, the
steps inside them, every subprocess it started, how coverage placed each mutant,
what became of every mutant execution, and what it wrote or could not write.

A trace is never evidence. It takes no part in a verdict, in a mutant identity,
or in the key a cached outcome is stored under, and a recording that could not
be written costs a note rather than the run. The run report is the durable
claim; the trace is the account of how the claim was arrived at.

Recordings live in ` + "`<report.directory>/trace/<run-id>/`" + `, one directory per run,
named by the same id the report carries — so a recording and the document it
explains can always be paired. A run keeps the newest 10 and collects the rest,
and ` + "`trace clean`" + ` is the same collector run by hand.

These subcommands read; none of them measures anything. See docs/trace-v1.md
for the event contract itself.`

const traceListLong = `List the recordings in this workspace, newest first.

One row per recording: the run id, when the run started, how many events the
recording holds, whether it holds all of them, and what it takes up on disk.

A recording is complete when it ends with its ` + "`run-end`" + ` event and lost nothing
on the way. One that was interrupted has no ` + "`run-end`" + ` and is listed as
incomplete; one that dropped events — a bounded ring that overflowed, a disk
that would not take a write — is listed as lossy, which is worth seeing before
you read a count out of it.

A workspace that has never traced a run is not a failure. It exits 0 with an
empty listing, because that is a true answer to the question.`

const traceSummaryLong = `Summarise one recording: where the run went, and what it ran.

With no argument this reads the newest recording in this workspace, which is the
run you have just made. A run id names one of the others, and a path names a
recording anywhere — a colleague's bug report, a CI artefact — as either the run
directory or the stream inside it.

The phases and the stages say where the time went; the commands, tallied by
kind, say what the run actually started and for how long; the mutant outcomes
say what the executions came to. The header says whether the recording is
complete, which is the first thing to check before reading a number out of it.`

const traceDiffLong = `Compare two recordings and print what moved between them.

It is the shape of a performance question: two runs of one workspace, and the
difference in how many commands of each kind they started and how long those
took. Each side is a run id in this workspace or a path to a recording.

The comparison is diagnostic and never a verdict. A trace takes no part in a
score, and two recordings differing is a statement about how two runs went, not
about whether either was right — ` + "`go-mutants report`" + ` is where run-to-run claims
are compared.`

const traceValidateLong = `Check a recording against the schema go-mutants publishes.

Every line is checked, not the first: a recording is a stream, a consumer reads
it to the end, and a run that wrote nine hundred good events and one nobody can
decode has published a file that breaks halfway through.

The schema is embedded in this binary, so validation needs no network and no
files beyond the one named. It is the same check the tests run against every
event go-mutants records.

Exits 0 when every line is valid, and 2 with the first violation and its JSON
pointer when one is not.`

const traceCleanLong = `Delete the recordings in this workspace.

It is the collector a traced run already runs, invoked by hand: --keep N leaves
the newest N and removes the rest, and the default removes them all.

Two things are never removed. A file or a directory somebody else keeps in the
trace root is not a recording and is left exactly as it was found — only a
directory named by a run id and holding a stream is ever deleted. And a
recording whose stream does not end with its run-end event is left alone as
well: that is a run still in progress, or one that died, and the second is the
recording you most want to keep. --all says you have read them and removes those
too; it takes no --keep, being the answer to "remove everything".

An empty trace directory goes with the last recording in it, so a workspace that
has been cleaned looks like one that was never traced.

Deleting a recording loses a diagnostic and never a measurement. The reports and
the outcome cache are untouched; those are ` + "`report clean`" + `'s and ` + "`cache clean`" + `'s.`

// newTraceCommand builds the `trace` command tree.
//
// The parent prints help and succeeds, exactly as `report` and `cache` do:
// somebody typing `go-mutants trace` to find out what it can do has done
// nothing wrong.
func newTraceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Read the recordings `run --trace` wrote",
		Long:  traceLong,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newTraceListCommand())
	cmd.AddCommand(newTraceSummaryCommand())
	cmd.AddCommand(newTraceDiffCommand())
	cmd.AddCommand(newTraceValidateCommand())
	cmd.AddCommand(newTraceCleanCommand())
	return cmd
}

// newTraceListCommand builds `trace list`.
func newTraceListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the recordings in this workspace, newest first",
		Long:  traceListLong,
		Args:  cobra.NoArgs,
		RunE:  runTraceList,
	}
}

// The column widths of the `trace list` table. They are constants rather than
// measurements of the data, for the reason `report list`'s are: every value has
// a fixed shape, so a listing of one recording and a listing of a hundred line
// up with each other and two listings a week apart can be diffed.
const (
	recordedWidth = len("2026-08-18T10:15:00Z")
	eventsWidth   = len("EVENTS")
	statusWidth   = len("incomplete")
)

// runTraceList is `trace list`'s body.
func runTraceList(cmd *cobra.Command, _ []string) error {
	root, err := workspaceTraceRoot()
	if err != nil {
		return err
	}
	names, err := recordingsIn(root)
	if err != nil {
		return &Error{
			Code:    CodeUnreadableTrace,
			Message: "the trace directory " + root + " cannot be read",
			Err:     err,
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "trace root: %s\n", root)
	if len(names) == 0 {
		b.WriteString("no recording here yet\n")
		return emit(cmd.OutOrStdout(), b.String())
	}
	fmt.Fprintf(&b, "%-*s  %-*s  %*s  %-*s  %s\n",
		runIDWidth, "RUN", recordedWidth, "RECORDED", eventsWidth, "EVENTS", statusWidth, "STATUS", "SIZE")
	var total int64
	// Newest first, which is the order `report list` uses and the order
	// somebody who has just made a run wants.
	for _, name := range slices.Backward(names) {
		directory := filepath.Join(root, name)
		size := directorySize(directory)
		total += size
		summary, readErr := trace.ReadSummary(directory)
		events, status := "?", "unreadable"
		if readErr == nil {
			events, status = strconv.Itoa(summary.Events), completeness(summary)
		}
		fmt.Fprintf(&b, "%-*s  %-*s  %*s  %-*s  %s\n",
			runIDWidth, name, recordedWidth, recordedAt(name), eventsWidth, events,
			statusWidth, status, formatBytes(size))
	}
	fmt.Fprintf(&b, "%s, %s\n", countNoun(len(names), "recording"), formatBytes(total))
	return emit(cmd.OutOrStdout(), b.String())
}

// completeness is the one word a reader has to see before trusting a count.
//
// Two different things can be wrong with a recording and they are worth telling
// apart: one that was interrupted stops early and its last events are simply
// not there, while a lossy one is missing an unknown number from the middle. A
// question like "how many times did this run compile" has no answer in either,
// and the second is the one that looks like it does.
func completeness(summary trace.Summary) string {
	switch {
	case !summary.HasRunEnd:
		return "incomplete"
	case summary.EventsDropped > 0 || summary.MissingSequences > 0:
		return "lossy"
	default:
		return "complete"
	}
}

// recordedAt reads the moment out of a run id, which is what it was minted
// from. A name that does not carry one is reported as unknown rather than as a
// zero time nobody can tell from a real one.
func recordedAt(runID string) string {
	stamp, _, ok := strings.Cut(runID, "-")
	if !ok {
		return "unknown"
	}
	at, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		return "unknown"
	}
	return formatMoment(at)
}

// newTraceSummaryCommand builds `trace summary`.
func newTraceSummaryCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "summary [RUN-ID|PATH]",
		Short: "Summarise one recording: where the run went, and what it ran",
		Long:  traceSummaryLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  runTraceSummary,
	}
}

// runTraceSummary is `trace summary`'s body.
func runTraceSummary(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return usagef("trace summary takes at most one recording, as in `go-mutants trace summary 20260907T120000Z-a1b2` (got %d)", len(args))
	}
	summary, name, err := readRecording(argAt(args, 0))
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "trace %s  %s\n", name, completeness(summary))
	fmt.Fprintf(&b, "stream: %s\n", summary.Path)
	fmt.Fprintf(&b, "events %d  dropped %d  missing %d\n",
		summary.Events, summary.EventsDropped, summary.MissingSequences)
	if summary.Verdict != "" || summary.HasRunEnd {
		fmt.Fprintf(&b, "verdict %s  exit %d\n", orUnknown(summary.Verdict), summary.ExitCode)
	}
	if summary.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", summary.Error)
	}
	writeDurations(&b, "phases", summary.PhaseDurationMS)
	writeDurations(&b, "stages", summary.StageDurationMS)
	writeDurations(&b, "prepare", summary.PrepareDurationMS)
	writeExecTallies(&b, summary.ExecByKind)
	writeCounts(&b, "mutants", summary.MutantOutcomes)
	writeCounts(&b, "events by type", summary.Counts)
	return emit(cmd.OutOrStdout(), b.String())
}

// newTraceDiffCommand builds `trace diff`.
func newTraceDiffCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diff BEFORE AFTER",
		Short: "Compare two recordings and print what moved between them",
		Long:  traceDiffLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  runTraceDiff,
	}
}

// runTraceDiff is `trace diff`'s body.
func runTraceDiff(cmd *cobra.Command, args []string) error {
	if len(args) != 2 {
		return usagef("trace diff takes two recordings, as in `go-mutants trace diff 20260901T120000Z-a1b2 20260907T120000Z-c3d4` (got %d)", len(args))
	}
	before, _, err := readRecording(args[0])
	if err != nil {
		return err
	}
	after, _, err := readRecording(args[1])
	if err != nil {
		return err
	}
	delta := trace.Diff(before, after)

	var b strings.Builder
	b.WriteString("trace diff\n")
	fmt.Fprintf(&b, "  before: %s\n", before.Path)
	fmt.Fprintf(&b, "  after:  %s\n", after.Path)
	fmt.Fprintf(&b, "events %s  dropped %s  missing %s\n",
		signed(int64(delta.EventsDelta)), signed(delta.EventsDroppedDelta), signed(delta.MissingSequencesDelta))
	fmt.Fprintf(&b, "verdict %s -> %s\n", orUnknown(delta.BeforeVerdict), orUnknown(delta.AfterVerdict))
	writeCountDeltas(&b, "events by type", delta.CountDelta)
	writeDurationDeltas(&b, "phases", delta.PhaseDurationDeltaMS)
	writeDurationDeltas(&b, "stages", delta.StageDurationDeltaMS)
	writeDurationDeltas(&b, "prepare", delta.PrepareDurationDeltaMS)
	writeExecDeltas(&b, delta.ExecDelta)
	writeCountDeltas(&b, "mutants", delta.MutantOutcomeDelta)
	return emit(cmd.OutOrStdout(), b.String())
}

// newTraceValidateCommand builds `trace validate`.
func newTraceValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate FILE",
		Short: "Check a recording against the schema go-mutants publishes",
		Long:  traceValidateLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  runTraceValidate,
	}
}

// runTraceValidate is `trace validate`'s body.
//
// Both checks are made, for the reason `report validate` makes both: the schema
// is what a consumer relies on, and this build's own reader is what `trace
// summary` will use on the same file. A recording that satisfies one and not the
// other is worth knowing about now rather than at the summary — and the reader
// enforces what a schema cannot, since it validates one line at a time and where
// a line sits in a stream is the reader's to judge.
func runTraceValidate(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usagef("trace validate takes exactly one file, as in `go-mutants trace validate reports/mutation/trace/20260907T120000Z-a1b2/trace.jsonl` (got %d)", len(args))
	}
	path := args[0]
	if err := validateTraceLines(path); err != nil {
		return err
	}
	summary, err := trace.ReadSummary(path)
	if err != nil {
		return &Error{
			Code:    CodeUnreadableTrace,
			Message: strconv.Quote(path) + " is not a recording this build can read",
			Err:     err,
		}
	}
	if summary.Missing {
		return missingRecording(path)
	}
	return emit(cmd.OutOrStdout(), fmt.Sprintf("%s: valid %s, %s, %s\n",
		summary.Path, trace.SchemaV1, countNoun(summary.Events, "event"), completeness(summary)))
}

// validateTraceLines checks every line of a recording against the published
// schema, naming the line a violation was on.
func validateTraceLines(path string) error {
	stream := streamPath(path)
	file, err := os.Open(stream)
	if err != nil {
		return &Error{
			Code:    CodeUnreadableTrace,
			Message: strconv.Quote(stream) + " cannot be read",
			Err:     err,
		}
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if err := schemas.Validate(schemas.TraceEventV1, []byte(text)); err != nil {
			return &Error{
				Code:    CodeUnreadableTrace,
				Message: stream + ":" + strconv.Itoa(line) + " is not a trace event this build can read",
				Err:     err,
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return &Error{
			Code:    CodeUnreadableTrace,
			Message: strconv.Quote(stream) + " cannot be read to the end",
			Err:     err,
		}
	}
	return nil
}

// cleanOptions holds the flag destinations for one `trace clean`.
type cleanOptions struct {
	keep int
	all  bool
}

// newTraceCleanCommand builds `trace clean`.
func newTraceCleanCommand() *cobra.Command {
	o := &cleanOptions{}
	cmd := &cobra.Command{
		Use:   "clean [flags]",
		Short: "Delete the recordings in this workspace",
		Long:  traceCleanLong,
		Args:  cobra.NoArgs,
		RunE:  o.execute,
	}
	cmd.Flags().IntVar(&o.keep, "keep", 0,
		"keep the newest `N` recordings and remove the rest")
	cmd.Flags().BoolVar(&o.all, "all", false,
		"remove the recordings no run finished as well: a run in progress, or one that died")
	return cmd
}

// checkCleanScope refuses `--all` alongside `--keep`.
//
// Each is a complete answer and they contradict each other: `--keep` says leave
// some behind, `--all` says spare nothing. Resolving that silently would make
// the meaning of a command line that deletes depend on a rule nobody wrote
// down. It is a worded refusal rather than cobra's flag-group message for the
// reason `--json` with `--quiet` is one: neither flag is wrong on its own, so
// the remedy is to drop one rather than to fix a value.
func checkCleanScope(keep, all bool) error {
	if !keep || !all {
		return nil
	}
	return &Error{
		Code: CodeConflictingFlags,
		Message: "--all and --keep cannot be combined: --all removes every recording including the ones no run " +
			"finished, and --keep asks for some of them to survive",
		Hint: "drop --keep to remove everything, or drop --all to keep the newest N of the finished recordings",
	}
}

// execute is `trace clean`'s body.
//
// What was deleted is reported even when the sweep stopped part way through,
// and then the failure is returned — the shape `report clean` and `cache gc`
// use, for the same reason: deleting is the whole of what this command does, so
// one that could not delete must not exit 0, and one that removed two
// recordings before hitting a locked third should still say so.
func (o *cleanOptions) execute(cmd *cobra.Command, _ []string) error {
	if o.keep < 0 {
		return usagef("--keep takes a number of recordings to keep, and %d is not one", o.keep)
	}
	if err := checkCleanScope(cmd.Flags().Changed("keep"), o.all); err != nil {
		return err
	}
	root, err := workspaceTraceRoot()
	if err != nil {
		return err
	}
	keep := retention{keep: o.keep, unfinished: o.all}
	found, err := planSweep(root, keep)
	if err != nil {
		return &Error{
			Code:    CodeUnreadableTrace,
			Message: "the trace directory " + root + " cannot be read",
			Err:     err,
		}
	}
	// Measured before the sweep, because a directory that has been removed
	// cannot be sized, and totalled afterwards over what actually went — so a
	// sweep that stopped part way through reports the bytes it really took back
	// rather than the bytes it had meant to.
	sizes := make(map[string]int64, len(found.stale))
	for _, name := range found.stale {
		sizes[name] = directorySize(filepath.Join(root, name))
	}
	// The plan that was just measured, rather than a fresh one: every count in
	// the report below — what was held, what was a candidate, what went, and
	// what it took up — then describes one look at the directory. See
	// [pruneTraceRoot].
	removed, removeErr := pruneTraceRoot(root, found)

	var b strings.Builder
	fmt.Fprintf(&b, "trace root: %s\n", root)
	switch {
	case len(removed) > 0:
		var bytes int64
		for _, name := range removed {
			bytes += sizes[name]
		}
		fmt.Fprintf(&b, "removed %s (%s)\n", countNoun(len(removed), "recording"), formatBytes(bytes))
	case found.held == 0:
		fmt.Fprintf(&b, "nothing to remove: no recording in %s\n", root)
	case found.candidates == 0:
		// The retention rule doing its job, which is worth a sentence of its
		// own: a plain `trace clean` over a root of interrupted runs has removed
		// nothing on purpose, and without the reason that is indistinguishable
		// from a command that did not work.
		fmt.Fprintf(&b, "nothing to remove: no recording in %s ended with its run-end; --all removes those too\n", root)
	default:
		// Kept by --keep, which is the only other way to collect nothing from a
		// root that holds something. Saying "no recording" here would tell
		// somebody their recordings are gone while they are still on the disk.
		fmt.Fprintf(&b, "nothing to remove: every recording in %s is kept\n", root)
	}
	// And the directory itself, once the last recording in it has gone, so that
	// a workspace somebody has cleaned looks like one that was never traced.
	// os.Remove is the whole of the test: it refuses a directory with anything
	// left in it, which is exactly the directory that has to stay.
	if os.Remove(root) == nil {
		fmt.Fprintf(&b, "removed the empty trace directory %s\n", root)
	}
	if err = emit(cmd.OutOrStdout(), b.String()); err != nil && removeErr == nil {
		return err
	}
	if removeErr != nil {
		return &Error{
			Code:    CodeTraceNotRemoved,
			Message: "a recording in " + root + " could not be removed",
			Err:     removeErr,
		}
	}
	return nil
}

// workspaceTraceRoot is where a run started in this directory would record.
//
// It reads `report.directory` from the workspace's own configuration, so that
// these commands look exactly where a run here would have written.
func workspaceTraceRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no workspace to find recordings in",
			Err:     err,
		}
	}
	cfg, err := config.Load(filepath.Join(dir, config.FileName), config.Overlay{})
	if err != nil {
		return "", err
	}
	return traceRoot(dir, cfg.Report.Directory, traceDefaultDirectory)
}

// readRecording resolves what the user named and reads it.
//
// Three things can be named and each has its own answer. Nothing at all is the
// newest recording in this workspace, which is the run somebody has just made. A
// run id is one of this workspace's own, and is reported as missing rather than
// as an unreadable path when there is none. Anything else is a path — a
// colleague's bug report, a CI artefact — and may be the run directory or the
// stream inside it.
func readRecording(name string) (trace.Summary, string, error) {
	path, title, err := resolveRecording(name)
	if err != nil {
		return trace.Summary{}, "", err
	}
	summary, err := trace.ReadSummary(path)
	if err != nil {
		return trace.Summary{}, "", &Error{
			Code:    CodeUnreadableTrace,
			Message: strconv.Quote(path) + " is not a recording this build can read",
			Err:     err,
		}
	}
	if summary.Missing {
		if recordingName.MatchString(name) || name == "" {
			return trace.Summary{}, "", &Error{
				Code:    CodeNoTraceRecorded,
				Message: "no recording is filed under " + title,
				Hint:    "run `go-mutants run --trace` here, or `go-mutants trace list` to see what is recorded",
			}
		}
		return trace.Summary{}, "", missingRecording(path)
	}
	return summary, title, nil
}

// resolveRecording turns what the user named into a path and the name to print
// it under.
func resolveRecording(name string) (path, title string, err error) {
	if name == "" || recordingName.MatchString(name) {
		root, rootErr := workspaceTraceRoot()
		if rootErr != nil {
			return "", "", rootErr
		}
		if name != "" {
			return filepath.Join(root, name), name, nil
		}
		names, listErr := recordingsIn(root)
		if listErr != nil {
			return "", "", &Error{
				Code:    CodeUnreadableTrace,
				Message: "the trace directory " + root + " cannot be read",
				Err:     listErr,
			}
		}
		if len(names) == 0 {
			return "", "", &Error{
				Code:    CodeNoTraceRecorded,
				Message: "no recording is filed in " + root,
				Hint:    "run `go-mutants run --trace` here, or name a recording by its path",
			}
		}
		newest := names[len(names)-1]
		return filepath.Join(root, newest), newest, nil
	}
	return name, name, nil
}

// missingRecording is the refusal for a path that names no recording.
func missingRecording(path string) error {
	return &Error{
		Code:    CodeUnreadableTrace,
		Message: strconv.Quote(path) + " is not a recording: there is no " + trace.FileName + " to read",
		Hint:    "name the run directory or the stream inside it, or `go-mutants trace list` to see what is recorded",
	}
}

// streamPath is the file inside a recording, whether the run directory or the
// stream itself was named. It is [trace.Read]'s own rule, applied here so that
// a violation can be reported against a line of a named file.
func streamPath(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return filepath.Join(path, trace.FileName)
	}
	return path
}

// argAt is the argument at i, or the empty string when there is none.
func argAt(args []string, i int) string {
	if i >= len(args) {
		return ""
	}
	return args[i]
}

// orUnknown is what a recording with no verdict in it says instead.
func orUnknown(verdict string) string {
	if verdict == "" {
		return "unknown"
	}
	return verdict
}

// signed renders a delta with its sign, so that a column of them reads as
// changes rather than as counts.
func signed(n int64) string {
	if n >= 0 {
		return "+" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// writeDurations prints one section of a summary's totals, in name order, and
// nothing at all when there are none.
func writeDurations(b *strings.Builder, heading string, totals map[string]int64) {
	if len(totals) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, key := range sortedKeys(totals) {
		fmt.Fprintf(b, "  %s  %dms\n", key, totals[key])
	}
}

// writeCounts prints one section of a summary's tallies, in name order.
func writeCounts(b *strings.Builder, heading string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, key := range sortedKeys(counts) {
		fmt.Fprintf(b, "  %s  %d\n", key, counts[key])
	}
}

// writeExecTallies prints the commands a recording holds, by kind. It is the
// section a performance question is asked of: where a run went is answered by
// what it started, not by how many events that produced.
func writeExecTallies(b *strings.Builder, tallies map[string]trace.ExecTally) {
	if len(tallies) == 0 {
		return
	}
	b.WriteString("exec:\n")
	for _, kind := range sortedKeys(tallies) {
		fmt.Fprintf(b, "  %s  %d  %dms\n", kind, tallies[kind].Count, tallies[kind].DurationMS)
	}
}

// writeCountDeltas prints one section of a diff, skipping what did not move.
func writeCountDeltas(b *strings.Builder, heading string, deltas map[string]int) {
	rows := make([]string, 0, len(deltas))
	for _, key := range sortedKeys(deltas) {
		if deltas[key] != 0 {
			rows = append(rows, fmt.Sprintf("  %s  %s\n", key, signed(int64(deltas[key]))))
		}
	}
	writeSection(b, heading, rows)
}

// writeDurationDeltas prints one section of a diff's timings, skipping what did
// not move.
func writeDurationDeltas(b *strings.Builder, heading string, deltas map[string]int64) {
	rows := make([]string, 0, len(deltas))
	for _, key := range sortedKeys(deltas) {
		if deltas[key] != 0 {
			rows = append(rows, fmt.Sprintf("  %s  %sms\n", key, signed(deltas[key])))
		}
	}
	writeSection(b, heading, rows)
}

// writeExecDeltas prints the commands that moved between two recordings.
func writeExecDeltas(b *strings.Builder, deltas map[string]trace.ExecTally) {
	rows := make([]string, 0, len(deltas))
	for _, kind := range sortedKeys(deltas) {
		delta := deltas[kind]
		if delta.Count == 0 && delta.DurationMS == 0 {
			continue
		}
		rows = append(rows, fmt.Sprintf("  %s  %s  %sms\n", kind, signed(int64(delta.Count)), signed(delta.DurationMS)))
	}
	writeSection(b, "exec", rows)
}

// writeSection prints a heading and its rows, and nothing when there are none:
// an empty heading in a diff says "this changed" of something that did not.
func writeSection(b *strings.Builder, heading string, rows []string) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, row := range rows {
		b.WriteString(row)
	}
}

// sortedKeys is the keys of a map in name order, so that two summaries of two
// runs are comparable line by line.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
