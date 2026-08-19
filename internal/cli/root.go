// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cli is the go-mutants command tree.
//
// It owns four things nothing else may: the version string, the exit code
// mapping, the rendering of errors to standard error, and the choice of
// renderer. cmd/go-mutants is a two-line main precisely so that all four stay
// unit-testable, and so that a future entry point — a `go tool` invocation, an
// in-process test harness — gets exactly the same behaviour.
package cli

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Version is the go-mutants version, and the only place the string exists.
// Releases stamp it at link time; a development build says so.
const Version = "0.1.0-dev"

// exitCodeHelp is appended to the help of every command. The table is part of
// the command line contract — CI configurations branch on these numbers — so it
// is printed where somebody reading `--help` will find it, rather than only in
// the README.
const exitCodeHelp = `
Exit codes:
  0    the run completed and no policy gate failed
  1    an opt-in policy gate failed (--strict, policy.minimum_score)
  2    an infrastructure, configuration, baseline, or expectation failure
  130  interrupted (Ctrl-C)
  143  terminated (SIGTERM)
`

const rootLong = `go-mutants is a mutation testing tool for Go modules.

It copies your workspace into a disposable snapshot, proves the unmutated tests
pass there, rewrites the copy, and measures how many of those rewrites your
tests notice. Your own tree is only ever read.

This is a pre-release build: a run stops after the baseline and says so.`

// NewRootCommand builds the command tree.
//
// It is exported so that tests can drive the whole tree in process with their
// own streams and arguments, which is the only way to assert on help output and
// on the exit code mapping together.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "go-mutants",
		Short: "Mutation testing for Go modules",
		Long:  rootLong,
		// Errors are rendered in one place, by [RenderError], so that every
		// failure — cobra's, the configuration's, the engine's — comes out with
		// the same shape and a code. Usage is silenced for the same reason: a
		// wall of flags on top of a one-line mistake buries it.
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       Version,
		// No Args validator: cobra's default for a command with subcommands
		// reports an unrecognised first argument as "unknown command" with a
		// did-you-mean list, which is exactly the behaviour wanted here.
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Bare `go-mutants` prints help and succeeds. It is not a usage
			// error: somebody typing the name of a tool to find out what it
			// does has done nothing wrong, and a non-zero status there breaks
			// `go-mutants || echo failed` in a shell script.
			return cmd.Help()
		},
	}
	// Two edits away from a real command still gets a suggestion; three does
	// not, which keeps `go-mutants list` from being offered for `go-mutants
	// clean`.
	root.SuggestionsMinimumDistance = 2
	// Registered here rather than left to cobra, which would give --version the
	// -v shorthand because nothing has claimed it yet. -v belongs to verbosity
	// (-q/-v/-vv), and a shorthand that means "print the version" for one
	// release and "be verbose" for the next is worse than no shorthand at all.
	// Cobra honours a --version flag it finds already defined.
	root.Flags().Bool("version", false, "print the version and exit")
	root.SetHelpTemplate(root.HelpTemplate() + exitCodeHelp)
	root.SetVersionTemplate("go-mutants {{.Version}}\n")
	root.AddCommand(newRunCommand())
	return root
}

// Execute runs the command tree against the process's arguments and streams,
// and returns the exit status. It never calls os.Exit itself, so that the one
// place the process ends is main.
func Execute() int {
	return ExecuteContext(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
}

// ExecuteContext is [Execute] with everything injected, for tests and for any
// future embedding.
func ExecuteContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand()
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
