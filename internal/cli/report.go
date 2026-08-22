// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

const reportLong = `Work with the documents a run wrote.

A run report is the source of truth for everything go-mutants says: the console
summary, the exit status, and every later rendering are derived from it. These
subcommands read those documents rather than producing new measurements.

` + "`list`" + `, ` + "`latest`" + ` and ` + "`clean`" + ` work on the history go-mutants keeps for itself in
the operating system's cache directory, one document per run. ` + "`merge`" + ` and
` + "`validate`" + ` work on files you name.`

const reportListLong = `List the runs this module has recorded, newest first.

Every run files a copy of its report under the operating system's cache
directory, and this is that history: the run id, when it finished, the score,
and how the run ended.

The history is filed per workspace, and a workspace is identified by a digest of
its contents — so two runs with an edit between them are stored apart. The runs
of one module are gathered back together here by the module path in each
document, which is why this is run from a module root and lists that module's
runs rather than everything on the machine.

A directory in the store that carries no go-mutants marker is listed as skipped
rather than read, and a document that cannot be read is named rather than
dropped: "nothing here" and "something here I could not read" are different
answers.

A module with no runs yet is not a failure. It exits 0 with an empty listing,
because that is a true answer to the question.`

const reportLatestLong = `Print the newest run this module recorded, and where it is filed.

It is the run ` + "`report list`" + ` puts at the top, summarised: how it ended, what it
scored, the breakdown, and the path of the document itself — which is what to
hand to ` + "`report validate`" + `, to jq, or to a diff against yesterday's.

--json prints the stored document instead, byte for byte as it was filed. It is
not re-encoded: a document written by an earlier release is what that release
wrote, and reshaping an archive on its way to standard output is the one thing
an archive must never do.

A module with no runs recorded is an error here, unlike an empty ` + "`report list`" + `:
this command's whole output is one document, and there is none.`

const reportCleanLong = `Delete this module's run history.

It removes the stored run documents and the pointer to the newest, in every
workspace directory holding runs of this module, and nothing else. The
ownership marker stays, so the directory keeps the identity a concurrent run may
be relying on, and the outcomes filed beside the runs stay too: those are
` + "`cache clean`" + `'s.

A directory that carries no go-mutants marker, or one whose marker this build
did not write, is refused rather than deleted — the cache root is shared with
every other tool on the machine — and so is a directory whose documents cannot
be read, since nothing there can prove which module they belong to.

Deleting run history loses measurements. It never changes a verdict: the next
run measures everything it would have measured anyway.`

const mergeLong = `Combine the reports of a sharded run into the whole run's report.

Each ` + "`--shard K/N`" + ` run discovers, validates and reports the entire catalogue and
executes only its own share, so the shards are directly comparable documents.
This proves they describe one run — one tool version, one workspace digest, one
catalogue, one changed ref, one shard total, and every index from 1 to N exactly
once with no mutant measured twice — and then writes the document that run would
have produced unsharded.

Any mismatch is a refusal naming the first discrepancy. A merge of the wrong
documents would publish a score describing a run that never happened, and the
whole point of merging is that somebody is going to trust the result.

The merged document reports no shard of its own and carries merge.shards
instead. Its counts, its score and its expectations are recomputed from the
merged rows rather than added up from the shards, so the numbers in it are the
numbers the unsharded run would have reached.

With no --output the document goes to standard output, so it can be piped into
a validator or into jq.`

const validateLong = `Check a run report against the schema go-mutants publishes.

The schema is embedded in this binary, so validation needs no network and no
files beyond the one named. It is the same check the tests run against every
document go-mutants writes: a report that passes here is one any consumer of
run-report v1 can rely on.

Exits 0 when the document is valid, and 2 with the first violation and its JSON
pointer when it is not.`

// newReportCommand builds the `report` command tree.
//
// The parent has no behaviour of its own beyond printing help, exactly as the
// root command does: somebody typing `go-mutants report` to find out what it
// can do has done nothing wrong, and a non-zero status there breaks a shell
// script that tests the tool is present.
func newReportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Work with the documents a run wrote",
		Long:  reportLong,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newReportListCommand())
	cmd.AddCommand(newReportLatestCommand())
	cmd.AddCommand(newReportCleanCommand())
	cmd.AddCommand(newReportMergeCommand())
	cmd.AddCommand(newReportValidateCommand())
	return cmd
}

// newReportListCommand builds `report list`.
func newReportListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the runs this module has recorded, newest first",
		Long:  reportListLong,
		Args:  cobra.NoArgs,
		RunE:  runReportList,
	}
}

// runReportList is `report list`'s body.
func runReportList(cmd *cobra.Command, _ []string) error {
	found, err := readHistory()
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "history for %s\n", found.module)
	fmt.Fprintf(&b, "store: %s\n", found.root)
	if len(found.runs) == 0 {
		b.WriteString("no run is recorded for this module yet\n")
	} else {
		fmt.Fprintf(&b, "%-*s  %-*s  %*s  %s\n",
			runIDWidth, "RUN", finishedWidth, "FINISHED", scoreWidth, "SCORE", "STATUS")
		for _, run := range found.runs {
			fmt.Fprintf(&b, "%-*s  %-*s  %*s  %s\n",
				runIDWidth, run.RunID,
				finishedWidth, formatMoment(run.FinishedAt),
				scoreWidth, formatScore(run),
				run.Status)
		}
		fmt.Fprintf(&b, "%s in %s\n",
			countNoun(len(found.runs), "run"),
			countNoun(len(found.workspaces), "workspace directory"))
	}
	writeDamaged(&b, append(slices.Clone(found.damaged), found.orphaned...), "")
	writeHistorySkipped(&b, found.skipped)
	return emit(cmd.OutOrStdout(), b.String())
}

// latestOptions holds the flag destinations for one `report latest`.
type latestOptions struct {
	json bool
}

// newReportLatestCommand builds `report latest`.
func newReportLatestCommand() *cobra.Command {
	o := &latestOptions{}
	cmd := &cobra.Command{
		Use:   "latest [flags]",
		Short: "Print the newest run this module recorded, and where it is filed",
		Long:  reportLatestLong,
		Args:  cobra.NoArgs,
		RunE:  o.execute,
	}
	cmd.Flags().BoolVar(&o.json, "json", false, "print the stored document instead of the summary")
	return cmd
}

// execute is `report latest`'s body.
func (o *latestOptions) execute(cmd *cobra.Command, _ []string) error {
	found, err := readHistory()
	if err != nil {
		return err
	}
	if len(found.runs) == 0 {
		return &Error{
			Code:    CodeNoStoredRun,
			Message: "no run is recorded for " + found.module + " in " + found.root,
			Hint:    "run `go-mutants run` here first, or `go-mutants report list` to see what is stored",
		}
	}
	run := found.runs[0]

	if o.json {
		data, readErr := report.ReadStored(run.Path)
		if readErr != nil {
			return readErr
		}
		_, writeErr := cmd.OutOrStdout().Write(data)
		return writeErr
	}

	var b strings.Builder
	fmt.Fprintf(&b, "run %s  %s  %s\n", run.RunID, run.Status, formatMoment(run.FinishedAt))
	fmt.Fprintf(&b, "score %s  killed %d  survived %d  timed out %d  inconclusive %d  errored %d  not run %d\n",
		formatScore(run), run.Summary.Killed, run.Summary.Survived, run.Summary.TimedOut,
		run.Summary.Inconclusive, run.Summary.Errored, run.Summary.NotRun)
	if failure := run.Summary.Policy.Failure; failure != nil {
		fmt.Fprintf(&b, "policy: %s\n", *failure)
	}
	fmt.Fprintf(&b, "%s\n", run.Path)
	return emit(cmd.OutOrStdout(), b.String())
}

// newReportCleanCommand builds `report clean`.
func newReportCleanCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "Delete this module's run history",
		Long:  reportCleanLong,
		Args:  cobra.NoArgs,
		RunE:  runReportClean,
	}
}

// runReportClean is `report clean`'s body.
//
// What was deleted is reported even when the sweep stopped part way through,
// and then the failure is returned — the shape `cache gc` uses, for the same
// reason: deleting is the whole of what this command does, so one that could
// not delete must not exit 0, and one that removed two histories before hitting
// a locked third should still say so.
func runReportClean(cmd *cobra.Command, _ []string) error {
	found, err := readHistory()
	if err != nil {
		return err
	}

	store := report.History{}
	var documents, directories int
	var removedBytes int64
	var sweepErr error
	for _, workspace := range found.workspaces {
		removed, removeErr := store.RemoveRuns(workspace.Digest)
		documents += removed.Runs
		removedBytes += removed.Bytes
		if removed.Runs > 0 {
			directories++
		}
		if removeErr != nil {
			sweepErr = removeErr
			break
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "store: %s\n", found.root)
	if documents == 0 {
		fmt.Fprintf(&b, "nothing to remove: no run is recorded for %s\n", found.module)
	} else {
		// The count is of documents removed rather than of runs listed, and the
		// two differ by exactly the unreadable ones: a truncated file in a
		// directory this module owns is go-mutants' own leftover, and leaving it
		// behind would mean `clean` never finishing the job.
		fmt.Fprintf(&b, "removed %s (%s) of %s from %s\n",
			countNoun(documents, "stored document"), formatBytes(removedBytes), found.module,
			countNoun(directories, "workspace directory"))
	}
	// The documents that were *not* deleted, and why. A clean that says
	// "nothing to remove" while history sits on the disk would be the worst
	// answer this command can give, so the directories it could not attribute
	// are named here rather than only by `report list`.
	writeDamaged(&b, found.orphaned, leftAloneNote)
	writeHistorySkipped(&b, found.skipped)
	if err = emit(cmd.OutOrStdout(), b.String()); err != nil && sweepErr == nil {
		return err
	}
	return sweepErr
}

// The column widths of the `report list` table.
//
// They are constants rather than measurements of the data, exactly as the run
// console and the mutant listing are: every value in the first three columns
// has a fixed shape — a run id is a stamp and four hex digits, a moment is RFC
// 3339 to the second, a score is at most "100.0%" — so a listing of one run and
// a listing of a hundred line up with each other, and two listings a week apart
// can be diffed.
const (
	runIDWidth    = len("20260818T101500Z-3f2a")
	finishedWidth = len("2026-08-18T10:15:00Z")
	scoreWidth    = len("100.0%")
)

// A history is one module's run history, gathered out of the store.
type history struct {
	// root is the store that was read.
	root string
	// module is the module path the runs were matched by.
	module string
	// workspaces are the workspace directories holding this module's runs.
	workspaces []report.StoredWorkspace
	// runs are every run of this module, newest first, across all of them.
	runs []report.StoredRun
	// damaged are the unreadable documents in this module's own directories.
	// They are go-mutants' own leftovers — a run killed half way through a
	// write — and `report clean` removes them with the runs beside them.
	damaged []report.Damaged
	// orphaned are the unreadable documents in directories that hold no run
	// anybody could attribute. Nothing there can prove whose they are, so they
	// are reported and never deleted.
	orphaned []report.Damaged
	// skipped are the directories in the store that are not go-mutants'.
	skipped []report.Skipped
}

// readHistory reads the store and keeps what belongs to the module in this
// directory.
//
// The module path is what joins one project's runs back together. A history
// directory is named after a digest of the workspace's contents — so an edit
// between two runs files them apart, by design, since a workspace digest is
// what makes a mutant id mean something — and the module path in each document
// is the only thing in the store that says which project a run was about.
//
// A document that cannot be read is attributed to nobody, so a directory whose
// documents are all unreadable is reported and never cleaned: nothing in it can
// prove whose it is. An unreadable file in a directory that also holds this
// module's runs is a different thing — go-mutants' own leftover from an
// interrupted write — and goes with them.
func readHistory() (history, error) {
	dir, err := os.Getwd()
	if err != nil {
		return history{}, &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no module to find history for",
			Err:     err,
		}
	}
	module, err := moduleAt(dir)
	if err != nil {
		return history{}, err
	}
	listing, err := report.History{}.List()
	if err != nil {
		return history{}, err
	}

	found := history{
		root:       listing.Root,
		module:     module,
		workspaces: []report.StoredWorkspace{},
		runs:       []report.StoredRun{},
		damaged:    []report.Damaged{},
		orphaned:   []report.Damaged{},
		skipped:    listing.Skipped,
	}
	for _, workspace := range listing.Workspaces {
		mine := make([]report.StoredRun, 0, len(workspace.Runs))
		for _, run := range workspace.Runs {
			if run.ModulePath == module {
				mine = append(mine, run)
			}
		}
		if len(mine) == 0 {
			// A directory whose documents could not be read at all belongs to
			// nobody this command can name. Its unreadable files are still
			// reported — an unreadable history is worth knowing about wherever
			// it is, and silence would make it indistinguishable from an empty
			// store — but nothing in it is ever deleted.
			if len(workspace.Runs) == 0 {
				found.orphaned = append(found.orphaned, workspace.Damaged...)
			}
			continue
		}
		found.damaged = append(found.damaged, workspace.Damaged...)
		workspace.Runs = mine
		found.workspaces = append(found.workspaces, workspace)
		found.runs = append(found.runs, mine...)
	}
	// The store's own comparator, not a copy of it: the runs being ordered here
	// come from several workspace directories rather than one, and that is
	// exactly the case where a second implementation of "newest" could disagree
	// with the one `report list` inside a directory used. See [report.NewestFirst].
	slices.SortFunc(found.runs, report.NewestFirst)
	return found, nil
}

// formatScore renders a run's score for a column, and says so when there is
// none. A run that measured nothing has no percentage, and both plausible
// sentinels are lies; see [report.Summary].
func formatScore(run report.StoredRun) string {
	score, ok := run.Score()
	if !ok {
		return "n/a"
	}
	return strconv.FormatFloat(score, 'f', 1, 64) + "%"
}

// writeDamaged names the stored documents that could not be read, and says
// nothing when they all could.
//
// The note is what the caller adds to the headline: `report clean` says that
// the directories holding these were left alone, because "nothing to remove"
// while history sits on the disk would be the worst answer that command can
// give. `report list` has nothing to add, since a listing removes nothing. The
// rows themselves are the same either way, and they are the part that must not
// drift between the two commands: a user comparing them is comparing paths.
func writeDamaged(b *strings.Builder, damaged []report.Damaged, note string) {
	if len(damaged) == 0 {
		return
	}
	fmt.Fprintf(b, "%s go-mutants could not read%s:\n", countNoun(len(damaged), "stored document"), note)
	for _, row := range damaged {
		fmt.Fprintf(b, "  %s: %s\n", row.Path, row.Reason)
	}
}

// leftAloneNote is `report clean`'s addition to the headline [writeDamaged]
// prints. See there.
const leftAloneNote = ", so the directories holding them were left alone"

// writeHistorySkipped lists the directories in the store the walk would not
// look inside, and says nothing when there were none.
func writeHistorySkipped(b *strings.Builder, skipped []report.Skipped) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(b, "skipped %s go-mutants will not touch:\n", countNoun(len(skipped), "directory"))
	for _, row := range skipped {
		fmt.Fprintf(b, "  %s: %s\n", row.Name, row.Reason)
	}
}

// mergeOptions holds the flag destinations for one `report merge`.
type mergeOptions struct {
	output string
}

// newReportMergeCommand builds `report merge`.
func newReportMergeCommand() *cobra.Command {
	o := &mergeOptions{}
	cmd := &cobra.Command{
		Use:   "merge FILE... [flags]",
		Short: "Combine the reports of a sharded run into the whole run's report",
		Long:  mergeLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  o.execute,
	}
	cmd.Flags().StringVar(&o.output, "output", "",
		"write the merged document to `PATH` instead of standard output")
	return cmd
}

// execute is `report merge`'s body.
func (o *mergeOptions) execute(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return usagef("report merge takes the shard reports to merge, as in `go-mutants report merge shard-1.json shard-2.json`")
	}
	shards := make([]*report.Report, 0, len(args))
	for _, path := range args {
		shard, err := readReport(path)
		if err != nil {
			return err
		}
		shards = append(shards, shard)
	}

	merged, err := report.MergeShards(report.MergeOptions{
		// Minted here rather than inside internal/report: a run id is a run's
		// identity, and that package files documents rather than starting
		// anything. The merged document is a new artefact and deserves its own.
		RunID:  engine.NewRunID(time.Now()),
		Shards: shards,
	})
	if err != nil {
		return err
	}
	data, err := merged.Marshal()
	if err != nil {
		return err
	}
	// Checked before it is written, not after. A merged document is what a CI
	// job publishes, and publishing one that does not satisfy the schema
	// go-mutants itself defines would be worse than refusing to publish at all.
	if err = schemas.Validate(schemas.RunReportV1, data); err != nil {
		return err
	}

	if o.output == "" {
		_, err = cmd.OutOrStdout().Write(data)
		return err
	}
	if err = report.WriteFile(o.output, merged); err != nil {
		return err
	}
	// To standard error, so that the path is visible when somebody is watching
	// and out of the way of anything reading standard output.
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "merged %s into %s\n",
		countNoun(len(shards), "shard report"), o.output)
	return nil
}

// newReportValidateCommand builds `report validate`.
func newReportValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate FILE",
		Short: "Check a run report against the schema go-mutants publishes",
		Long:  validateLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  runReportValidate,
	}
	return cmd
}

// runReportValidate is `report validate`'s body.
func runReportValidate(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usagef("report validate takes exactly one file, as in `go-mutants report validate reports/mutation/mutation.json` (got %d)", len(args))
	}
	path := args[0]
	data, err := readFile(path)
	if err != nil {
		return err
	}
	if err = schemas.Validate(schemas.RunReportV1, data); err != nil {
		return err
	}
	// Decoded as well as validated, because the two catch different things: the
	// schema is what a consumer relies on, and this build's own reader is what
	// `report merge` will use on the same file. A document that satisfies one
	// and not the other is worth knowing about now rather than at the merge.
	r, err := report.Parse(data)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: valid %s v%d, run %s, %s\n",
		path, r.DocumentType, r.SchemaVersion, r.RunID, countNoun(len(r.Mutants), "mutant"))
	return nil
}

// readReport reads one document and decodes it, validating it against the
// published schema on the way through.
//
// The schema check comes first, and it is what makes the decoder's job small: a
// document that has been proven to have the right shape can be read into this
// build's own types without every field needing a second opinion about whether
// it is plausible.
func readReport(path string) (*report.Report, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if err = schemas.Validate(schemas.RunReportV1, data); err != nil {
		return nil, notAReport(path, err)
	}
	r, err := report.Parse(data)
	if err != nil {
		return nil, notAReport(path, err)
	}
	return r, nil
}

// notAReport names the file a failure was about.
//
// The cause keeps its own code — this package does not re-code the failures of
// the packages it drives — and gains one of its own in front, because `report
// merge` is handed several files and "which one" is the first thing its user
// needs to know. `report validate` is given exactly one and reports the failure
// unwrapped, since the path is the command line the user has just typed.
func notAReport(path string, cause error) error {
	return &Error{
		Code:    CodeInvalidReportDocument,
		Message: strconv.Quote(path) + " is not a run report this build can merge",
		Err:     cause,
	}
}

// readFile reads a document a user named on the command line.
//
// A missing or unreadable file is a usage error rather than an infrastructure
// one: the mistake is in the command line, and the remedy is to name a
// different path.
func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{
			Code:    CodeUnreadableReport,
			Message: strconv.Quote(path) + " cannot be read",
			Err:     err,
		}
	}
	return data, nil
}
