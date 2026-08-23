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
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Version is the go-mutants version, and the only place the string exists.
//
// It is a var rather than a const for exactly one reason: the release build
// overrides it with
//
//	-X github.com/P4suta/go-mutants/internal/cli.Version=<tag>
//
// which the linker can only apply to a variable — and only to one whose
// initialiser is a constant string expression, which [defaultVersion] is. The
// string is read into report documents and cache keys, so a build that changed
// it mid-run would be describing two different tools in one file; the one
// assignment that ever happens is [init]'s, before main runs and before
// anything has read it. See .goreleaser.yaml, which is the only place the
// link-time override is written.
var Version = defaultVersion

// defaultVersion is what [Version] says in a build that stamped nothing.
//
// release-please rewrites the literal below in the Release PR: the trailing
// marker comment on that line is what marks it, and
// release-please-config.json's `extra-files` entry is what points at this file
// — so it tracks VERSION and .release-please-manifest.json without anybody
// having to remember.
//
// The marker is spelled out only once, in that trailing comment, and this
// paragraph deliberately does not repeat it: release-please's generic updater
// scans every line of the file for the token, so a second occurrence in prose
// would be a second line it believes it owns.
//
// It is a separate constant rather than [Version]'s own literal because
// something has to remember what an unstamped build looks like, and [Version]
// cannot: by the time the process runs, the linker may already have replaced
// it. [resolveVersion] compares the two, which is only possible while both
// exist.
const defaultVersion = "0.1.0-dev" // x-release-please-version

// init resolves [Version] exactly once, so that `--version`, `doctor`, every
// report document and every cache key cannot disagree about what this build
// is. See [resolveVersion] for what it resolves to and why.
func init() {
	Version = resolveVersion(Version, debug.ReadBuildInfo)
}

// resolveVersion works out what a build should call itself.
//
// Three kinds of build reach this, and only the third needs any work:
//
//   - A release build. goreleaser passed `-X …cli.Version=<tag>`, so stamped
//     differs from [defaultVersion] and is returned untouched. The stamp wins
//     over everything below it, because it is the only input that came from
//     the release rather than from the module graph.
//   - A build from a checkout — `go build`, `go run`, `go test`, the `mise run
//     dogfood` gate. Nothing stamped anything and the module has no version:
//     [debug.ReadBuildInfo] reports Main.Version as "(devel)" or as the empty
//     string, and [defaultVersion] is the honest answer.
//   - A build the module proxy resolved. `go install <module>/cmd/go-mutants@v0.1.0`
//     compiles a released tag without any of goreleaser's link flags, and
//     `…@main` compiles a commit that no tag names at all. Neither stamps
//     [Version], but the go command records what it fetched in Main.Version —
//     "v0.1.0", or a pseudo-version like
//     "v0.1.1-0.20260823101112-21b55cdc95bc" for the commit — which is a
//     strictly better answer than the last release's literal. That is the case
//     this function exists for, and it is the install route the README
//     recommends once a release exists.
//
// readBuildInfo is a parameter rather than a direct call so the three cases
// are testable; production passes [debug.ReadBuildInfo].
func resolveVersion(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != defaultVersion {
		return stamped
	}
	info, ok := readBuildInfo()
	if !ok {
		return stamped
	}
	// "(devel)" is what the go command records for a main module built out of
	// a working tree, and the empty string for a binary with no module
	// information at all. Neither is a version anybody can install.
	switch info.Main.Version {
	case "", "(devel)":
		return stamped
	}
	// Module versions carry a leading "v" and go-mutants' own strings never
	// have: the goreleaser stamp is `{{ .Version }}`, which is the tag without
	// it, and the report schemas and cache keys have recorded bare versions
	// since before this function existed.
	return strings.TrimPrefix(info.Main.Version, "v")
}

// exitCodeHelp is appended to the help of every command. The table is part of
// the command line contract — CI configurations branch on these numbers — so it
// is printed where somebody reading `--help` will find it, rather than only in
// the README.
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
file:// with the network unplugged.`

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
	root.AddCommand(newListCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newInitCommand())
	root.AddCommand(newReportCommand())
	root.AddCommand(newCacheCommand())
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
