// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
)

const cacheLong = `Work with the outcomes go-mutants has proven before.

A run may reuse a mutant's outcome when nothing that could change it has moved:
the go-mutants version and the executable itself, the Go toolchain's own
release, the workspace digest, the catalogue, the test command, the timeout, and
CGO_ENABLED, GOARCH, GODEBUG, GOEXPERIMENT, GOFLAGS and GOOS. All of those are
hashed into one key, and entries are filed under it — so editing a source file
does not invalidate anything, it simply means the next run looks somewhere else
and finds nothing.

Nothing here is ever needed for correctness. The cache holds only outcomes a
later run would have measured identically, and ` + "`cache clean`" + ` never changes a
verdict — only how long reaching it takes.

These commands read cache.directory from .go-mutants.toml, so they look where a
run in this directory would look. They refuse to touch a directory in the
operating system's cache that does not carry go-mutants' own ownership marker,
and they never touch the run history filed beside the outcomes: that is
` + "`report clean`" + `'s.`

const cacheStatusLong = `Print where the outcome cache lives and what is in it.

One line per workspace, ordered by key: how many outcomes are stored for it, what
they take up, and when the newest was written. A directory under the cache root
that carries no go-mutants marker, or one naming a format this build does not
know, is listed as skipped rather than counted — a directory go-mutants will not
delete is worth knowing about, and "nothing here" and "nothing I would touch"
are different answers.`

const cacheGCLong = `Delete stored outcomes written more than N days ago.

An entry is only ever read by a run whose tool version, toolchain, code,
catalogue, command, timeout and environment all still match the ones that wrote
it, so an entry a month old has almost certainly outlived the context that could
read it. Age is the modification time, and reading an entry does not refresh it:
this removes what is old, not what is unpopular.

Context directories left with nothing in them are removed too. The run history
beside them is never touched.`

const cacheCleanLong = `Delete every stored outcome.

The next run measures everything, which is the same set of verdicts more slowly.
The run history filed in the same directories is left exactly as it was; ` + "`report`" + `
owns that.`

// newCacheCommand builds the `cache` command tree.
//
// The parent prints help and succeeds, exactly as `report` and the root command
// do: somebody typing `go-mutants cache` to find out what it can do has done
// nothing wrong.
func newCacheCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Work with the outcomes go-mutants has proven before",
		Long:  cacheLong,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCacheStatusCommand())
	cmd.AddCommand(newCacheGCCommand())
	cmd.AddCommand(newCacheCleanCommand())
	return cmd
}

// newCacheStatusCommand builds `cache status`.
func newCacheStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print where the outcome cache lives and what is in it",
		Long:  cacheStatusLong,
		Args:  cobra.NoArgs,
		RunE:  runCacheStatus,
	}
}

// runCacheStatus is `cache status`'s body.
func runCacheStatus(cmd *cobra.Command, _ []string) error {
	root, err := cacheRoot()
	if err != nil {
		return err
	}
	survey, err := cache.Status(root)
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "cache root: %s\n", survey.Root)
	if len(survey.Workspaces) == 0 {
		b.WriteString("nothing cached yet\n")
	} else {
		fmt.Fprintf(&b, "%s, %s, %s\n",
			countNoun(len(survey.Workspaces), "workspace"),
			countNoun(survey.Entries(), "outcome"),
			formatBytes(survey.Bytes()))
		for _, workspace := range survey.Workspaces {
			fmt.Fprintf(&b, "  %s  %s  %s  newest %s\n",
				workspace.Key,
				countNoun(workspace.Entries, "outcome"),
				formatBytes(workspace.Bytes),
				formatMoment(workspace.Newest))
		}
	}
	writeSkipped(&b, survey.Skipped)
	return emit(cmd.OutOrStdout(), b.String())
}

// gcOptions holds the flag destinations for one `cache gc`.
type gcOptions struct {
	days int
}

// newCacheGCCommand builds `cache gc`.
func newCacheGCCommand() *cobra.Command {
	o := &gcOptions{}
	cmd := &cobra.Command{
		Use:   "gc [flags]",
		Short: "Delete stored outcomes written more than N days ago",
		Long:  cacheGCLong,
		Args:  cobra.NoArgs,
		RunE:  o.execute,
	}
	cmd.Flags().IntVar(&o.days, "days", cache.DefaultGCDays,
		"delete outcomes last written more than `N` days ago")
	return cmd
}

// execute is `cache gc`'s body.
//
// The sweep is reported even when it failed part way through, and then the
// failure is returned. Deleting is the whole of what this command does, so a
// `gc` that could not delete must not exit 0 — and a `gc` that removed nine
// entries and then hit a locked tenth should still say so, because the next run
// of it has nine fewer to do.
func (o *gcOptions) execute(cmd *cobra.Command, _ []string) error {
	if o.days < 0 {
		return usagef("--days %d is not an age: pass the number of days an outcome may sit on disk, as in `go-mutants cache gc --days 7`", o.days)
	}
	root, err := cacheRoot()
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -o.days)
	sweep, sweepErr := cache.GC(root, cutoff)

	var b strings.Builder
	fmt.Fprintf(&b, "cache root: %s\n", sweep.Root)
	if sweep.Entries == 0 {
		fmt.Fprintf(&b, "nothing to remove: no stored outcome is older than %s\n", countNoun(o.days, "day"))
	} else {
		fmt.Fprintf(&b, "removed %s (%s) older than %s from %s, and %s\n",
			countNoun(sweep.Entries, "outcome"), formatBytes(sweep.Bytes),
			countNoun(o.days, "day"), countNoun(sweep.Workspaces, "workspace"),
			countNoun(sweep.Contexts, "emptied directory"))
	}
	writeSkipped(&b, sweep.Skipped)
	if err = emit(cmd.OutOrStdout(), b.String()); err != nil && sweepErr == nil {
		return err
	}
	return sweepErr
}

// newCacheCleanCommand builds `cache clean`.
func newCacheCleanCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "Delete every stored outcome",
		Long:  cacheCleanLong,
		Args:  cobra.NoArgs,
		RunE:  runCacheClean,
	}
}

// runCacheClean is `cache clean`'s body.
func runCacheClean(cmd *cobra.Command, _ []string) error {
	root, err := cacheRoot()
	if err != nil {
		return err
	}
	sweep, sweepErr := cache.Clean(root)

	var b strings.Builder
	fmt.Fprintf(&b, "cache root: %s\n", sweep.Root)
	if sweep.Entries == 0 && sweep.Workspaces == 0 {
		b.WriteString("nothing to remove: no outcome is stored\n")
	} else {
		fmt.Fprintf(&b, "removed %s (%s) from %s\n",
			countNoun(sweep.Entries, "outcome"), formatBytes(sweep.Bytes),
			countNoun(sweep.Workspaces, "workspace"))
	}
	writeSkipped(&b, sweep.Skipped)
	if err = emit(cmd.OutOrStdout(), b.String()); err != nil && sweepErr == nil {
		return err
	}
	return sweepErr
}

// cacheRoot resolves the directory these commands operate on.
//
// The configuration is read from the current directory so that the commands
// look where a run started here would look: a project that moved its cache with
// `cache.directory` gets its own cache surveyed and swept, not the default one.
// A directory with no configuration file is not a mistake — [config.Load]
// answers with the defaults — so the only failure here is a working directory
// that cannot be read or a file that cannot be understood.
func cacheRoot() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no configuration to find the cache with",
			Err:     err,
		}
	}
	cfg, err := config.Load(filepath.Join(root, config.FileName), config.Overlay{})
	if err != nil {
		return "", err
	}
	return cache.Root(cfg.Cache.Directory)
}

// writeSkipped lists the directories the walk would not touch, and says nothing
// when there were none.
func writeSkipped(b *strings.Builder, skipped []cache.Skipped) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(b, "skipped %s go-mutants will not touch:\n", countNoun(len(skipped), "directory"))
	for _, row := range skipped {
		fmt.Fprintf(b, "  %s: %s\n", row.Name, row.Reason)
	}
}

// emit writes one command's whole output in a single call.
//
// Composed in memory and written once, for the reason [RenderError] gives about
// errors: a half-printed listing is worse than an unprinted one, and a status
// that stopped in the middle of a workspace would read as a cache with fewer
// entries in it than it has. The write's failure is returned rather than
// dropped, because unlike an error report this is the command's whole answer.
func emit(w io.Writer, text string) error {
	_, err := io.WriteString(w, text)
	return err
}

// formatMoment renders a timestamp the way the run report does: RFC 3339 in
// UTC, to the second. The zero time is "never", which is what a workspace with
// no entries has.
func formatMoment(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

// byteUnits are the suffixes [formatBytes] steps through, in powers of 1024.
var byteUnits = []string{"KiB", "MiB", "GiB", "TiB"}

// formatBytes renders a size for a human.
//
// Powers of 1024 with the unambiguous suffixes, because the number beside them
// is going to be compared against what a file manager says. Bytes are printed
// exactly and everything above them to one decimal place: the difference
// between 4.0 and 4.1 MiB is worth seeing and the digits after it are not.
func formatBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n) / 1024
	unit := byteUnits[0]
	for _, next := range byteUnits[1:] {
		if value < 1024 {
			break
		}
		value /= 1024
		unit = next
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + unit
}
