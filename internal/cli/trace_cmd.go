// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bufio"
	"errors"
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

const traceCleanLong = `Delete the recordings and the diagnostics bundles in this workspace.

It is the collector a run already runs, invoked by hand: --keep N leaves the
newest N and removes the rest, and the default removes them all.

Both roots are swept, because both are the exhaust of the same runs and a second
command would be a second thing to remember and a second place to find a full
disk. ` + "`report.directory/trace/`" + ` holds a recording per traced run and
` + "`report.directory/diagnostics/`" + ` holds a bundle per failed one, and the rules
below apply to each of them. ` + "`trace list`" + ` is unchanged: a listing answers
"what recordings are there", and a bundle is not a recording.

Two things are never removed. A file or a directory somebody else keeps in
either root is left exactly as it was found — only a directory named by a run id
and holding go-mutants' own files is ever deleted. And one nothing finished
writing is left alone as well: a recording whose stream does not end with its
run-end event, or a bundle with no preserved-paths.txt, is a run still in
progress or one that died, and the second is the account you most want to keep.
--all says you have read them and removes those too; it takes no --keep, being
the answer to "remove everything".

An empty directory goes with the last thing in it, so a workspace that has been
cleaned looks like one that was never traced.

Deleting a recording loses a diagnostic and never a measurement. The reports and
the outcome cache are untouched; those are ` + "`report clean`" + `'s and ` + "`cache clean`" + `'s.`

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

func newTraceListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the recordings in this workspace, newest first",
		Long:  traceListLong,
		Args:  cobra.NoArgs,
		RunE:  runTraceList,
	}
}

const (
	recordedWidth = len("2026-08-18T10:15:00Z")
	eventsWidth   = len("EVENTS")
	statusWidth   = len("incomplete")
)

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

func newTraceSummaryCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "summary [RUN-ID|PATH]",
		Short: "Summarise one recording: where the run went, and what it ran",
		Long:  traceSummaryLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  runTraceSummary,
	}
}

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

func newTraceDiffCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diff BEFORE AFTER",
		Short: "Compare two recordings and print what moved between them",
		Long:  traceDiffLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  runTraceDiff,
	}
}

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

func newTraceValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate FILE",
		Short: "Check a recording against the schema go-mutants publishes",
		Long:  traceValidateLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  runTraceValidate,
	}
}

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

type cleanOptions struct {
	keep int
	all  bool
}

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

func (o *cleanOptions) execute(cmd *cobra.Command, _ []string) error {
	if o.keep < 0 {
		return usagef("--keep takes a number of recordings to keep, and %d is not one", o.keep)
	}
	if err := checkCleanScope(cmd.Flags().Changed("keep"), o.all); err != nil {
		return err
	}
	roots, err := workspaceRoots()
	if err != nil {
		return err
	}

	keep := retention{keep: o.keep, unfinished: o.all}
	var b strings.Builder
	var failure error
	for i, r := range roots {
		if sweepErr := sweepRoot(&b, r, keep, i == 0); sweepErr != nil && failure == nil {
			failure = sweepErr
		}
	}
	if err = emit(cmd.OutOrStdout(), b.String()); err != nil && failure == nil {
		return err
	}
	return failure
}

func sweepRoot(b *strings.Builder, r retentionRoot, keep retention, always bool) error {
	found, err := planSweep(r, keep)
	if err != nil {
		return &Error{
			Code:    CodeUnreadableTrace,
			Message: "the " + r.label + " directory " + r.path + " cannot be read",
			Err:     err,
		}
	}
	if !always && found.held == 0 {
		return nil
	}
	sizes := make(map[string]int64, len(found.stale))
	for _, name := range found.stale {
		sizes[name] = directorySize(filepath.Join(r.path, name))
	}
	removed, removeErr := prune(r, found)

	fmt.Fprintf(b, "%s root: %s\n", r.label, r.path)
	switch {
	case len(removed) > 0:
		var bytes int64
		for _, name := range removed {
			bytes += sizes[name]
		}
		fmt.Fprintf(b, "removed %s (%s)\n", countNoun(len(removed), r.noun), formatBytes(bytes))
	case found.held == 0:
		fmt.Fprintf(b, "nothing to remove: no %s in %s\n", r.noun, r.path)
	case found.candidates == 0:
		fmt.Fprintf(b, "nothing to remove: no %s in %s %s; --all removes those too\n",
			r.noun, r.path, r.unfinished)
	default:
		fmt.Fprintf(b, "nothing to remove: every %s in %s is kept\n", r.noun, r.path)
	}
	if removeErr != nil {
		return &Error{
			Code:    CodeTraceNotRemoved,
			Message: "a " + r.noun + " in " + r.path + " could not be removed",
			Err:     removeErr,
		}
	}
	switch err := removeDirectory(r.path); {
	case err == nil:
		fmt.Fprintf(b, "removed the empty %s directory %s\n", r.label, r.path)
	case errors.Is(err, errNotDirectory), errors.Is(err, errIsALink):
		return &Error{
			Code:    CodeUnreadableTrace,
			Message: "the " + r.label + " directory " + r.path + " cannot be read",
			Err:     describePath(r.path, err),
		}
	}
	return nil
}

func workspaceTraceRoot() (string, error) {
	dir, cfg, err := workspaceConfig()
	if err != nil {
		return "", err
	}
	return traceRoot(dir, cfg.Report.Directory, traceDefaultDirectory)
}

func workspaceRoots() ([]retentionRoot, error) {
	dir, cfg, err := workspaceConfig()
	if err != nil {
		return nil, err
	}
	recordings, err := traceRoot(dir, cfg.Report.Directory, traceDefaultDirectory)
	if err != nil {
		return nil, err
	}
	bundles, err := diagnosticsRoot(dir, cfg.Report.Directory)
	if err != nil {
		return nil, err
	}
	return []retentionRoot{traceRootAt(recordings), diagnosticsRootAt(bundles)}, nil
}

func workspaceConfig() (string, config.Config, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", config.Config{}, &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no workspace to find recordings in",
			Err:     err,
		}
	}
	cfg, err := config.Load(filepath.Join(dir, config.FileName), config.Overlay{})
	if err != nil {
		return "", config.Config{}, err
	}
	return dir, cfg, nil
}

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

func missingRecording(path string) error {
	return &Error{
		Code:    CodeUnreadableTrace,
		Message: strconv.Quote(path) + " is not a recording: there is no " + trace.FileName + " to read",
		Hint:    "name the run directory or the stream inside it, or `go-mutants trace list` to see what is recorded",
	}
}

func streamPath(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return filepath.Join(path, trace.FileName)
	}
	return path
}

func argAt(args []string, i int) string {
	if i >= len(args) {
		return ""
	}
	return args[i]
}

func orUnknown(verdict string) string {
	if verdict == "" {
		return "unknown"
	}
	return verdict
}

func signed(n int64) string {
	if n >= 0 {
		return "+" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

func writeDurations(b *strings.Builder, heading string, totals map[string]int64) {
	if len(totals) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, key := range sortedKeys(totals) {
		fmt.Fprintf(b, "  %s  %dms\n", key, totals[key])
	}
}

func writeCounts(b *strings.Builder, heading string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, key := range sortedKeys(counts) {
		fmt.Fprintf(b, "  %s  %d\n", key, counts[key])
	}
}

func writeExecTallies(b *strings.Builder, tallies map[string]trace.ExecTally) {
	if len(tallies) == 0 {
		return
	}
	b.WriteString("exec:\n")
	for _, kind := range sortedKeys(tallies) {
		fmt.Fprintf(b, "  %s  %d  %dms\n", kind, tallies[kind].Count, tallies[kind].DurationMS)
	}
}

func writeCountDeltas(b *strings.Builder, heading string, deltas map[string]int) {
	rows := make([]string, 0, len(deltas))
	for _, key := range sortedKeys(deltas) {
		if deltas[key] != 0 {
			rows = append(rows, fmt.Sprintf("  %s  %s\n", key, signed(int64(deltas[key]))))
		}
	}
	writeSection(b, heading, rows)
}

func writeDurationDeltas(b *strings.Builder, heading string, deltas map[string]int64) {
	rows := make([]string, 0, len(deltas))
	for _, key := range sortedKeys(deltas) {
		if deltas[key] != 0 {
			rows = append(rows, fmt.Sprintf("  %s  %sms\n", key, signed(deltas[key])))
		}
	}
	writeSection(b, heading, rows)
}

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

func writeSection(b *strings.Builder, heading string, rows []string) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", heading)
	for _, row := range rows {
		b.WriteString(row)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
