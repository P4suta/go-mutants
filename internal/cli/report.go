// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"fmt"
	"os"
	"strconv"
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

` + "`list`" + `, ` + "`latest`" + ` and ` + "`clean`" + ` — the history store's own commands — land in a
later release.`

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
	cmd.AddCommand(newReportMergeCommand())
	cmd.AddCommand(newReportValidateCommand())
	return cmd
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
