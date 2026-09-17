// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cli is the go-mutants command tree.
package cli

import (
	"context"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/mutation"
)

var Version = defaultVersion

const defaultVersion = "0.2.0-dev"

func init() {
	Version = resolveVersion(Version, debug.ReadBuildInfo)
}

func resolveVersion(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != defaultVersion {
		return stamped
	}
	info, ok := readBuildInfo()
	if !ok {
		return stamped
	}
	switch info.Main.Version {
	case "", "(devel)":
		return stamped
	}
	return strings.TrimPrefix(info.Main.Version, "v")
}

const exitCodeHelp = `
Exit codes:
  0    the run completed and no policy gate failed
  1    an opt-in gate failed (--strict, policy.minimum_score, init --check)
  2    an infrastructure, configuration, baseline, or expectation failure
  130  interrupted (Ctrl-C)
  143  terminated (SIGTERM)
`

const rootLong = `go-mutants is a mutation testing tool for Go modules.

It copies your workspace into a disposable snapshot, proves the unmutated tests
pass there, rewrites the copy, and measures how many of those rewrites your
tests notice. Your own tree is only ever read.

This is a pre-release build, and the v1 feature set is complete: the whole
operator catalogue is discovered, a run can be narrowed to a git diff
(--changed) or to one shard of a matrix (--shard), outcomes it has proven are
reused between runs (--cache), and every run publishes its report into
reports/mutation/ as JSON and as a self-contained HTML page that opens from
file:// with the network unplugged.

Every run also keeps a diagnostic account of itself, in memory by default and in
reports/mutation/trace/ under --trace, which ` + "`go-mutants trace`" + ` reads. A trace is
never evidence: it takes no part in a verdict, in a mutant identity, or in a
cache key.

` + "`go-mutants explain`" + ` joins the two per mutant: what one mutant is, what happened
to it, which suites cover it, every pass the run made over the test binaries
with the commands underneath, and a command to paste that runs the mutant
again.`

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "go-mutants",
		Short:         "Mutation testing for Go modules",
		Long:          rootLong,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       Version,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SuggestionsMinimumDistance = 2
	root.Flags().Bool("version", false, "print the version and exit")
	root.SetHelpTemplate(root.HelpTemplate() + exitCodeHelp)
	root.SetVersionTemplate("go-mutants {{.Version}}\n")
	root.AddCommand(newRunCommand())
	root.AddCommand(newListCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newInitCommand())
	root.AddCommand(newExplainCommand())
	root.AddCommand(newReportCommand())
	root.AddCommand(newCacheCommand())
	root.AddCommand(newTraceCommand())
	return root
}

func Execute() int {
	return ExecuteContext(context.Background(),
		withEnvironmentFlags(os.Args[1:], os.LookupEnv), os.Stdout, os.Stderr)
}

func ExecuteContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand()
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return int(mutation.ExitOK)
	}
	RenderError(stderr, err)
	return int(ExitCode(err))
}
