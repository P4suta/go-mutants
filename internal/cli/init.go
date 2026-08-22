// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
)

const initLong = `Write a commented .go-mutants.toml with the built-in defaults in it.

The file it writes changes nothing: every value in it is what go-mutants would
have done without it, so a project can adopt the file first and decide things
afterwards. The comments say what each key means and what happens if it is left
out, which is what makes it a better starting point than an empty file.

It never overwrites. There is no --force, deliberately: a configuration file is
hand-edited and is usually the only record of decisions nobody wrote down
twice, and a flag that replaces it wholesale is a flag somebody will type by
accident. Deleting the file first is the deliberate act such a flag would only
have pretended to be.

--dry-run prints what would be written and touches nothing, including where a
real init would refuse, so it is also how to see the current defaults.

--check exits 0 when the file already there is byte-identical to what this
build would write, and 1 when it is not, so a CI job can hold a generated
configuration to the release that generated it. It is the one place in
go-mutants where 1 does not mean a policy gate failed — it is still an opt-in
gate somebody asked for, and it is still not an infrastructure failure.`

// initOptions holds the flag destinations for one `init`.
type initOptions struct {
	dryRun bool
	check  bool
}

// newInitCommand builds `init`.
func newInitCommand() *cobra.Command {
	o := &initOptions{}
	cmd := &cobra.Command{
		Use:   "init [flags]",
		Short: "Write a commented .go-mutants.toml with the built-in defaults in it",
		Long:  initLong,
		Args:  cobra.NoArgs,
		RunE:  o.execute,
	}
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "print what would be written, and write nothing")
	cmd.Flags().BoolVar(&o.check, "check", false,
		"exit 0 if the file already there is what this build would write, and 1 if it is not")
	// Neither flag is wrong on its own; asking for both is asking to print a
	// file and to compare it in the same breath.
	cmd.MarkFlagsMutuallyExclusive("dry-run", "check")
	return cmd
}

// execute is `init`'s body.
func (o *initOptions) execute(cmd *cobra.Command, _ []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is nowhere to write a configuration",
			Err:     err,
		}
	}
	path := filepath.Join(dir, config.FileName)
	content := StarterConfig()

	switch {
	case o.dryRun:
		return emit(cmd.OutOrStdout(), content)
	case o.check:
		return o.compare(cmd, path, content)
	}
	return o.write(cmd, path, content)
}

// write creates the file, refusing to replace one that is already there.
//
// The refusal is the create itself rather than a look followed by a write:
// O_EXCL is the filesystem's own answer to "does this exist", and it cannot be
// raced by an editor saving the file in the half-second between the two.
func (o *initOptions) write(cmd *cobra.Command, path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	switch {
	case errors.Is(err, fs.ErrExist):
		return &Error{
			Code:    CodeConfigurationExists,
			Message: path + " is already there, and go-mutants init never overwrites one",
			Hint:    "run `go-mutants init --dry-run` to see what it would have written, or delete the file first",
		}
	case err != nil:
		return &Error{
			Code:    CodeConfigurationNotWritten,
			Message: path + " could not be created",
			Err:     err,
		}
	}
	_, err = file.WriteString(content)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// The file exists and is incomplete, which is worse than no file: the
		// next `init` would refuse it and a run would read half a configuration.
		_ = os.Remove(path)
		return &Error{
			Code:    CodeConfigurationNotWritten,
			Message: path + " could not be written",
			Err:     err,
		}
	}
	return emit(cmd.OutOrStdout(), "wrote "+path+"\n"+
		"every value in it is the built-in default, so nothing about your runs has changed yet\n")
}

// compare is `init --check`.
//
// The comparison is byte-for-byte and deliberately so. A file that parses to
// the same configuration through different words is a fine file and a poor
// answer to the question this flag asks, which is whether the file in the
// repository is the one this release generates — a wording change in a comment
// is exactly the drift a freshness check exists to catch.
func (o *initOptions) compare(cmd *cobra.Command, path, content string) error {
	found, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return stale(path + " does not exist; `go-mutants init` would create it")
	case err != nil:
		return &Error{
			Code:    CodeConfigurationUnreadable,
			Message: path + " could not be read, so it cannot be compared",
			Err:     err,
		}
	}
	if !bytes.Equal(found, []byte(content)) {
		return stale(path + " is not what `go-mutants init` writes in " + Version)
	}
	return emit(cmd.OutOrStdout(), path+" is what `go-mutants init` writes in "+Version+"\n")
}

// stale builds the one failure in this package that exits 1. See [initLong].
func stale(message string) error {
	return &exitError{
		code: mutation.ExitPolicyFailure,
		err: &Error{
			Code:    CodeConfigurationStale,
			Message: message,
			Hint:    "run `go-mutants init --dry-run` to see the difference, or delete the file and run `go-mutants init`",
		},
	}
}

// StarterConfig returns the file `init` writes.
//
// It is exported so that the tests can hold it to the one property that makes
// it worth shipping: the file parses, and resolves to exactly
// [config.Defaults]. Every value in it is interpolated from that function
// rather than typed out here, so a changed default cannot leave a stale number
// behind in the text — and the three keys whose defaults cannot be written down
// are commented out rather than guessed at:
//
//   - `execution.jobs` is min(CPU count, 8), which is a different number on a
//     laptop and on a CI runner. Writing it would make the generated file
//     machine-dependent and `init --check` a gate that fails on the wrong
//     hardware.
//   - `test.timeout` is zero meaning "derive it from the baseline". No duration
//     spells that, and the file has no way back to derivation once it is set.
//   - `mutation.exclude`, `mutation.operators` and `[[mutation.expect]]` are
//     empty by default, and an empty list is a decision — "constrain nothing" —
//     that reads as an oversight when it is written out. They are shown as
//     commented examples instead.
func StarterConfig() string {
	c := config.Defaults()
	var b strings.Builder
	b.WriteString(`# go-mutants configuration.
#
# Every setting below is the built-in default written out, so this file changes
# nothing until you edit it. Keys are decoded strictly: an unknown one is an
# error carrying the line and column it was written at, never a typo that
# silently does nothing.
#
# Precedence is built-in defaults, then this file, then command line flags.

version = ` + strconv.Itoa(c.Version) + `

[mutation]
# Tiers are monotonically inclusive: balanced ⊂ strong ⊂ all.
profile = ` + tomlString(c.Mutation.Profile.String()) + `
# Which source files are considered. Test files are never mutated whatever this
# says, and neither are generated files; both are excluded structurally.
include = ` + tomlStrings(c.Mutation.Include) + `
# Excludes apply after includes. There are none by default: a pattern nobody
# asked for silently shrinks a run.
# exclude = ["internal/legacy/**"]
# Naming operators replaces the profile's selection entirely. Leave it out to
# let the tier decide, which is what keeps this file honest when a new family
# lands.
# operators = ["comparison", "error-swallowing"]

# Declared equivalent mutants. An expectation is evidence to check, not a skip
# list: the mutant still runs, survival fulfils the expectation, a kill means
# the ledger is lying and exits 2, and an id that has left the catalogue is
# stale and also exits 2.
# [[mutation.expect]]
# id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
# reason = "Equivalent: the branch is unreachable for all valid inputs."

[test]
# An argv vector, never a shell string. Nothing in it is word-split, expanded,
# or handed to a shell. It is also the run's scope: "go test" followed only by
# package patterns builds a test binary for those packages and no others, and
# keeps coverage-guided selection. Anything else -- a flag, another program --
# turns that off and measures every mutant against every binary, because
# go-mutants cannot reason about a command it did not write.
command = ` + tomlStrings(c.Test.Command) + `
# How many times the unmutated tests are measured before any mutant runs. Every
# observation is kept in the report, not just the slowest.
baseline_runs = ` + strconv.Itoa(c.Test.BaselineRuns) + `
# Left out, and therefore derived as max(10s, slowest baseline × 5). Write a Go
# duration to fix it instead; there is no flag that puts derivation back, so
# removing this line again is how a project returns to it.
# timeout = "60s"

[execution]
# Left out, and therefore min(CPU count, 8): a mutation run is a background
# chore that should leave the machine usable. Raise it on a CI runner that has
# nothing else to do.
# jobs = 4

[cache]
# auto reuses outcomes proven by a run whose tool version, toolchain, code,
# catalogue, command, timeout and environment all still match — and reuses
# nothing at all when test.command is not the built-in one. "on" is how you
# promise that your own command is reproducible; "off" measures everything
# every time. Nothing the cache does can change a verdict.
mode = ` + tomlString(c.Cache.Mode.String()) + `
# Where the cache lives, relative to the operating system's cache directory.
# Left out, it is go-mutants' own directory there.
# directory = "team-cache"

[policy]
# The build is not failed unless you ask. strict fails a run that has any
# unexpected survivor; minimum_score fails one that scores below the floor;
# require_mutants fails a run that found nothing to mutate, which is almost
# always a selection that matched no files.
strict = ` + tomlBool(c.Policy.Strict) + `
minimum_score = ` + tomlFloat(c.Policy.MinimumScore) + `
require_mutants = ` + tomlBool(c.Policy.RequireMutants) + `

[report]
# Where the project's own reports are written, relative to this directory. It
# is excluded from the snapshot and from cache identity, so what is in it never
# changes what a run measures.
directory = ` + tomlString(c.Report.Directory) + `
# An empty list turns project reports off without deleting the files a previous
# run wrote.
formats = ` + tomlStrings(formatNames(c.Report.Formats)) + `
# HTML colouring thresholds only, as percentages. They are deliberately
# independent of [policy]: making a report prettier must never change whether
# CI passes.
high = ` + strconv.Itoa(c.Report.High) + `
low = ` + strconv.Itoa(c.Report.Low) + `
`)
	return b.String()
}

// formatNames renders the report formats as the strings the file spells them
// with.
func formatNames(formats []config.ReportFormat) []string {
	names := make([]string, 0, len(formats))
	for _, format := range formats {
		names = append(names, format.String())
	}
	return names
}

// tomlString renders one TOML basic string. strconv.Quote is Go's escaping and
// TOML's are the same for everything a configuration value can contain here —
// and every value it is handed is an identifier, a path, or a flag name.
func tomlString(value string) string { return strconv.Quote(value) }

// tomlStrings renders a TOML array of strings on one line.
func tomlStrings(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// tomlBool renders a TOML boolean.
func tomlBool(value bool) string { return strconv.FormatBool(value) }

// tomlFloat renders a TOML float, and keeps it a float.
//
// The decimal point is not decoration: `minimum_score = 0` is a TOML integer,
// and the decoder refuses an integer for a key the schema types as a number
// with a fraction. A default of 0 would therefore generate a file this build
// cannot read, which is exactly the drift the round-trip test exists to catch.
func tomlFloat(value float64) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}
