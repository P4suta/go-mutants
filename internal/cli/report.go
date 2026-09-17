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

	"github.com/P4suta/go-mutants/internal/discover"
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

func newReportListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the runs this module has recorded, newest first",
		Long:  reportListLong,
		Args:  cobra.NoArgs,
		RunE:  runReportList,
	}
}

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

type latestOptions struct {
	json bool
}

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

func newReportCleanCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "Delete this module's run history",
		Long:  reportCleanLong,
		Args:  cobra.NoArgs,
		RunE:  runReportClean,
	}
}

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
		fmt.Fprintf(&b, "removed %s (%s) of %s from %s\n",
			countNoun(documents, "stored document"), formatBytes(removedBytes), found.module,
			countNoun(directories, "workspace directory"))
	}
	writeDamaged(&b, found.orphaned, leftAloneNote)
	writeHistorySkipped(&b, found.skipped)
	if err = emit(cmd.OutOrStdout(), b.String()); err != nil && sweepErr == nil {
		return err
	}
	return sweepErr
}

const (
	runIDWidth    = len("20260818T101500Z-3f2a")
	finishedWidth = len("2026-08-18T10:15:00Z")
	scoreWidth    = len("100.0%")
)

type history struct {
	root       string
	module     string
	workspaces []report.StoredWorkspace
	runs       []report.StoredRun
	damaged    []report.Damaged
	orphaned   []report.Damaged
	skipped    []report.Skipped
}

func readHistory() (history, error) {
	dir, err := os.Getwd()
	if err != nil {
		return history{}, &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no module to find history for",
			Err:     err,
		}
	}
	project, err := projectAt(dir)
	if err != nil {
		return history{}, err
	}
	module := project.name()
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
			if project.holds(run) {
				mine = append(mine, run)
			}
		}
		if len(mine) == 0 {
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
	slices.SortFunc(found.runs, report.NewestFirst)
	return found, nil
}

type project struct {
	module  string
	modules []string
}

func projectAt(dir string) (project, error) {
	workspace, err := discover.DetectWorkspace(dir)
	if err != nil {
		return project{}, err
	}
	if workspace != nil {
		modules := make([]string, 0, len(workspace.Modules))
		for _, module := range workspace.Modules {
			modules = append(modules, module.Path)
		}
		return project{modules: modules}, nil
	}
	module, err := moduleAt(dir)
	if err != nil {
		return project{}, err
	}
	return project{module: module}, nil
}

func (p project) name() string {
	if p.module != "" {
		return p.module
	}
	return "the workspace of " + strings.Join(p.modules, ", ")
}

func (p project) holds(run report.StoredRun) bool {
	if p.module != "" {
		return run.ModulePath == p.module
	}
	return slices.Equal(run.Modules, p.modules)
}

func formatScore(run report.StoredRun) string {
	score, ok := run.Score()
	if !ok {
		return "n/a"
	}
	return strconv.FormatFloat(score, 'f', 1, 64) + "%"
}

func writeDamaged(b *strings.Builder, damaged []report.Damaged, note string) {
	if len(damaged) == 0 {
		return
	}
	fmt.Fprintf(b, "%s go-mutants could not read%s:\n", countNoun(len(damaged), "stored document"), note)
	for _, row := range damaged {
		fmt.Fprintf(b, "  %s: %s\n", row.Path, row.Reason)
	}
}

const leftAloneNote = ", so the directories holding them were left alone"

func writeHistorySkipped(b *strings.Builder, skipped []report.Skipped) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(b, "skipped %s go-mutants will not touch:\n", countNoun(len(skipped), "directory"))
	for _, row := range skipped {
		fmt.Fprintf(b, "  %s: %s\n", row.Name, row.Reason)
	}
}

type mergeOptions struct {
	output string
}

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

func (o *mergeOptions) execute(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return usagef("report merge takes the shard reports to merge, as in `go-mutants report merge shard-1.json shard-2.json`")
	}
	runID := engine.NewRunID(time.Now())
	merged, documentType, err := mergeDocuments(args, runID)
	if err != nil {
		return err
	}
	data, err := merged.Marshal()
	if err != nil {
		return err
	}
	if err = schemas.Validate(documentType, data); err != nil {
		return err
	}

	if o.output == "" {
		_, err = cmd.OutOrStdout().Write(data)
		return err
	}
	if err = writeMergedFile(o.output, data); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "merged %s into %s\n",
		countNoun(len(args), "shard report"), o.output)
	return nil
}

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

func runReportValidate(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usagef("report validate takes exactly one file, as in `go-mutants report validate reports/mutation/mutation.json` (got %d)", len(args))
	}
	path := args[0]
	data, err := readFile(path)
	if err != nil {
		return err
	}
	documentType, err := report.DocumentTypeOf(data)
	if err != nil {
		return err
	}
	if err = schemas.Validate(documentType, data); err != nil {
		return err
	}
	summary, err := validatedSummary(documentType, data)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", path, summary)
	return nil
}

func validatedSummary(documentType string, data []byte) (string, error) {
	if documentType == report.WorkspaceDocumentType {
		w, err := report.ParseWorkspace(data)
		if err != nil {
			return "", err
		}
		total := 0
		for _, module := range w.Modules {
			total += len(module.Report.Mutants)
		}
		return fmt.Sprintf("valid %s v%d, run %s, %s across %s",
			w.DocumentType, w.SchemaVersion, w.RunID,
			countNoun(total, "mutant"), countNoun(len(w.Modules), "module")), nil
	}
	r, err := report.Parse(data)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("valid %s v%d, run %s, %s",
		r.DocumentType, r.SchemaVersion, r.RunID, countNoun(len(r.Mutants), "mutant")), nil
}

type marshalable interface{ Marshal() ([]byte, error) }

func mergeDocuments(paths []string, runID string) (marshalable, string, error) {
	documentType, err := documentTypeAt(paths[0])
	if err != nil {
		return nil, "", err
	}
	if documentType == report.WorkspaceDocumentType {
		shards := make([]*report.WorkspaceReport, 0, len(paths))
		for _, path := range paths {
			shard, readErr := readWorkspaceReport(path)
			if readErr != nil {
				return nil, "", readErr
			}
			shards = append(shards, shard)
		}
		merged, mergeErr := report.MergeWorkspaces(report.MergeWorkspaceOptions{
			RunID:  runID,
			Shards: shards,
		})
		return merged, schemas.WorkspaceReportV1, mergeErr
	}
	shards := make([]*report.Report, 0, len(paths))
	for _, path := range paths {
		shard, readErr := readReport(path)
		if readErr != nil {
			return nil, "", readErr
		}
		shards = append(shards, shard)
	}
	merged, mergeErr := report.MergeShards(report.MergeOptions{RunID: runID, Shards: shards})
	return merged, schemas.RunReportV1, mergeErr
}

func documentTypeAt(path string) (string, error) {
	data, err := readFile(path)
	if err != nil {
		return "", err
	}
	documentType, err := report.DocumentTypeOf(data)
	if err != nil {
		return "", notAReport(path, "merge", err)
	}
	return documentType, nil
}

func readWorkspaceReport(path string) (*report.WorkspaceReport, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if err = schemas.Validate(schemas.WorkspaceReportV1, data); err != nil {
		return nil, notAReport(path, "merge", err)
	}
	w, err := report.ParseWorkspace(data)
	if err != nil {
		return nil, notAReport(path, "merge", err)
	}
	return w, nil
}

func writeMergedFile(path string, data []byte) error {
	return report.WriteBytes(path, data)
}

func readReport(path string) (*report.Report, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if err = schemas.Validate(schemas.RunReportV1, data); err != nil {
		return nil, notAReport(path, "merge", err)
	}
	r, err := report.Parse(data)
	if err != nil {
		return nil, notAReport(path, "merge", err)
	}
	return r, nil
}

func notAReport(path, action string, cause error) error {
	return &Error{
		Code:    CodeInvalidReportDocument,
		Message: strconv.Quote(path) + " is not a run report this build can " + action,
		Err:     cause,
	}
}

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
