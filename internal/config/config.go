// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"runtime"
	"slices"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Schema constants of the v1 configuration.
const (
	// Version is the only schema version this build reads. It is required in
	// every file: a configuration that does not say which schema it is written
	// against cannot be migrated later without guessing.
	Version = 1

	// FileName is the configuration file's fixed name. There is no search up
	// the directory tree and no alternative spelling.
	FileName = ".go-mutants.toml"
)

// Ranges and defaults for the numeric settings. Each bound is a decision, not
// a technical limit, and each is stated once here so that the validator, the
// help text, and the documentation cannot drift apart.
const (
	// MinJobs is the smallest worker count. Zero would mean "do nothing",
	// which `--jobs` should never be able to say by accident.
	MinJobs = 1
	// MaxJobs is the largest worker count. Mutation runs are dominated by
	// process starts and file system contention, so far beyond this the extra
	// workers cost more than they buy; the ceiling exists to catch a typo such
	// as `--jobs 320` before it forks a machine into swap.
	MaxJobs = 32
	// DefaultJobCap is the ceiling the default worker count is clamped to.
	// The default is deliberately not "every core": a mutation run is a
	// background chore that should leave a laptop usable.
	DefaultJobCap = 8

	// MinBaselineRuns is the smallest number of baseline observations. One is
	// enough to prove the tests pass, which is the baseline's first job.
	MinBaselineRuns = 1
	// MaxBaselineRuns is the largest. The baseline is measured before any
	// mutant runs, so every extra repetition is latency the user waits
	// through; ten is already far past the point where another sample changes
	// the derived timeout.
	MaxBaselineRuns = 10
	// DefaultBaselineRuns is the shipped number of baseline observations.
	// Three is the smallest count that can show a spread rather than a point.
	DefaultBaselineRuns = 3

	// MinPercent and MaxPercent bound every percentage setting.
	MinPercent = 0
	MaxPercent = 100

	// DefaultReportDirectory is where project reports are written, relative to
	// the workspace root.
	DefaultReportDirectory = "reports/mutation"
	// DefaultReportHigh and DefaultReportLow are the HTML colouring
	// thresholds. They affect nothing but colour; see [Report].
	DefaultReportHigh = 80
	DefaultReportLow  = 60
)

// A CacheMode says whether proven outcomes may be reused between runs.
type CacheMode string

// The cache modes.
const (
	// CacheAuto reuses cached outcomes when the cache key matches and the
	// environment looks reproducible. It is the default.
	CacheAuto CacheMode = "auto"
	// CacheOn always reuses matching cached outcomes.
	CacheOn CacheMode = "on"
	// CacheOff never reads and never writes the cache.
	CacheOff CacheMode = "off"
)

// CacheModes returns the modes in the order they are documented.
func CacheModes() []CacheMode { return []CacheMode{CacheAuto, CacheOn, CacheOff} }

// Valid reports whether m is one of the defined modes.
func (m CacheMode) Valid() bool {
	return m == CacheAuto || m == CacheOn || m == CacheOff
}

// String returns the mode as it is written in TOML.
func (m CacheMode) String() string { return string(m) }

// A ReportFormat is one project report artefact.
type ReportFormat string

// The report formats.
const (
	// FormatJSON is the lossless RunReport, the source of truth.
	FormatJSON ReportFormat = "json"
	// FormatHTML is the self-contained human report.
	FormatHTML ReportFormat = "html"
)

// ReportFormats returns the formats in the order they are documented.
func ReportFormats() []ReportFormat { return []ReportFormat{FormatJSON, FormatHTML} }

// Valid reports whether f is one of the defined formats.
func (f ReportFormat) Valid() bool { return f == FormatJSON || f == FormatHTML }

// String returns the format as it is written in TOML.
func (f ReportFormat) String() string { return string(f) }

// An Expectation is one row of the `[[mutation.expect]]` ledger: a mutant that
// is expected to survive, and why.
//
// An expectation is evidence to check, not a skip list. The mutant still runs,
// survival fulfils the expectation, a kill means the ledger is lying, and an
// id that has disappeared from the catalogue is stale. This package only
// checks that the row is well formed; internal/engine decides what it means.
type Expectation struct {
	// ID is the full 64 lowercase hex mutant id. Prefixes are not accepted
	// here even though `--mutant` accepts them: a ledger entry outlives the
	// run that produced it, and a prefix that is unique today can become
	// ambiguous after one commit.
	ID string
	// Reason is why this mutant is expected to survive, in the author's own
	// words. It is required because an unexplained expectation is
	// indistinguishable from a mistake six months later.
	Reason string
}

// Mutation is the `[mutation]` section: what to mutate and with what.
//
// In a resolved [Config], a nil slice and an empty slice mean the same thing
// in every field here, and consumers must not distinguish them: no include
// patterns and no exclude patterns and no operator names each say "this list
// constrains nothing". The difference that does carry meaning — whether a
// layer set a list at all — lives in [Set] and is spent by the time [Merge]
// returns. Anything serialising a Config should therefore emit `[]` for both,
// rather than letting `null` leak into a report as a third state.
type Mutation struct {
	// Include lists the glob patterns a source file must match to be
	// considered.
	Include []string
	// Exclude lists the patterns that remove a file again. Excludes apply
	// after includes.
	Exclude []string
	// Operators narrows the selection to these operator families or rules.
	// Empty means "whatever Profile selects", which is what `init` writes and
	// what keeps a configuration honest when a new family lands.
	Operators []string
	// Profile is the tier of operators to run. Tiers are monotonically
	// inclusive: balanced ⊂ strong ⊂ all.
	Profile mutation.Tier
	// Expect is the expectations ledger, in file order.
	Expect []Expectation
}

// Test is the `[test]` section: how to run the project's tests.
type Test struct {
	// Command is the argv vector that runs the tests. It is executed
	// directly, never through a shell, so no element is ever word-split,
	// glob-expanded, or variable-substituted.
	Command []string
	// Timeout is the per-mutant timeout. Zero means "derive it", as
	// max(10s, slowest baseline × 5).
	//
	// Derivation is a file-level choice, and v1 has no flag that restores it:
	// `--timeout` can only replace a derived timeout with a fixed one, never
	// the other way around. A project that wants derivation back removes
	// `test.timeout` from its configuration.
	Timeout time.Duration
	// BaselineRuns is how many times the unmutated tests are measured before
	// any mutant runs. Every observation is kept in the report, not just the
	// slowest.
	BaselineRuns int
}

// Execution is the `[execution]` section.
type Execution struct {
	// Jobs is the number of mutants executed concurrently.
	Jobs int
}

// Cache is the `[cache]` section.
type Cache struct {
	// Mode says whether proven outcomes may be reused.
	Mode CacheMode
	// Directory overrides where the cache lives. It is relative and resolves
	// under the OS cache root, never under the workspace. Empty means the
	// default location.
	Directory string
}

// Report is the `[report]` section.
type Report struct {
	// Directory is where project reports are written, relative to the
	// workspace root.
	Directory string
	// Formats are the artefacts to write. An empty slice writes none, which
	// is a supported way to turn project reports off without deleting the
	// files a previous run produced. As in [Mutation], a nil slice and an
	// empty one say the same thing.
	Formats []ReportFormat
	// High and Low are the HTML colouring thresholds, as percentages. They
	// are deliberately independent of [Config.Policy]: making a report
	// prettier must never change whether CI passes.
	High int
	Low  int
}

// A Config is a fully resolved configuration: defaults, file, and flags
// merged, with every value present.
//
// It is plain data. Nothing here remembers which layer a value came from,
// because by the time a run acts on a configuration that question has no
// bearing on what it should do. Diagnostics that need the answer are raised
// earlier, where the layer is still known.
type Config struct {
	// Version is the schema version, always [Version] in a valid Config.
	Version int
	// Mutation is `[mutation]`.
	Mutation Mutation
	// Test is `[test]`.
	Test Test
	// Execution is `[execution]`.
	Execution Execution
	// Cache is `[cache]`.
	Cache Cache
	// Policy is `[policy]`, shared with internal/mutation so that the gate
	// this package validates is literally the one internal/mutation applies.
	Policy mutation.Policy
	// Report is `[report]`.
	Report Report
}

// DefaultJobs returns the default worker count: min(logical CPUs, 8).
//
// It is a function rather than a constant because it depends on the machine,
// and it is exported because the help text has to print the number the user
// will actually get.
func DefaultJobs() int {
	return min(runtime.NumCPU(), DefaultJobCap)
}

// DefaultTestCommand returns the argv vector used when a project does not name
// one.
func DefaultTestCommand() []string { return []string{"go", "test", "./..."} }

// DefaultInclude returns the default include patterns: every Go file in the
// module.
//
// Narrowing this further is the user's call. Test files are not excluded here
// because they are already excluded structurally — discovery builds and runs
// _test.go files but never mutates them — and an exclude that repeats a
// structural rule only creates a second place for it to be wrong.
func DefaultInclude() []string { return []string{"**/*.go"} }

// Defaults returns the built-in configuration: the bottom layer of the
// precedence stack, and a complete, valid configuration on its own.
//
// Every call returns freshly allocated slices, so a caller may mutate the
// result without affecting anybody else's defaults.
func Defaults() Config {
	return Config{
		Version: Version,
		Mutation: Mutation{
			Include: DefaultInclude(),
			// No default excludes and no default operators: the profile
			// decides which operators run, and adding a pattern here that
			// nobody asked for would silently shrink a run.
			Exclude:   nil,
			Operators: nil,
			Profile:   mutation.TierBalanced,
			Expect:    nil,
		},
		Test: Test{
			Command: DefaultTestCommand(),
			// Zero means derive from the baseline, which is a better default
			// than any fixed number: the right timeout depends on how slow
			// this project's tests actually are.
			Timeout:      0,
			BaselineRuns: DefaultBaselineRuns,
		},
		Execution: Execution{Jobs: DefaultJobs()},
		Cache:     Cache{Mode: CacheAuto, Directory: ""},
		Policy:    mutation.DefaultPolicy(),
		Report: Report{
			Directory: DefaultReportDirectory,
			Formats:   ReportFormats(),
			High:      DefaultReportHigh,
			Low:       DefaultReportLow,
		},
	}
}

// Clone returns a deep copy: the result shares no slice with the receiver.
func (c Config) Clone() Config {
	c.Mutation.Include = slices.Clone(c.Mutation.Include)
	c.Mutation.Exclude = slices.Clone(c.Mutation.Exclude)
	c.Mutation.Operators = slices.Clone(c.Mutation.Operators)
	c.Mutation.Expect = slices.Clone(c.Mutation.Expect)
	c.Test.Command = slices.Clone(c.Test.Command)
	c.Report.Formats = slices.Clone(c.Report.Formats)
	return c
}
