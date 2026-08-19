// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"slices"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// An Overlay is a partial configuration: every setting that a layer may
// override, each recording whether that layer actually set it.
//
// The shape is deliberately flat rather than mirroring [Config]'s sections. A
// flag overlay is built one field at a time from pflag, and a flat struct
// makes that a list of assignments with no intermediate structs to allocate
// and no section that exists only because one of its fields was set:
//
//	var o config.Overlay
//	o.Jobs = config.When(flags.Changed("jobs"), jobs)
//	o.Profile = config.When(flags.Changed("profile"), profile)
//
// The same type carries what a file contributed, so [Merge] applies both
// layers with one rule instead of two.
type Overlay struct {
	// Version is `version`. Files carry it; flags never set it.
	Version Set[int]

	// Include is `mutation.include`, overridden by repeating --include.
	Include Set[[]string]
	// Exclude is `mutation.exclude`, overridden by repeating --exclude.
	Exclude Set[[]string]
	// Operators is `mutation.operators`, overridden by repeating --operator.
	Operators Set[[]string]
	// Profile is `mutation.profile`, overridden by --profile.
	Profile Set[mutation.Tier]
	// Expect is the `[[mutation.expect]]` ledger. There is no flag for it: an
	// expectation carries a written reason, which is not something to type on
	// a command line.
	Expect Set[[]Expectation]

	// TestCommand is `test.command`, overridden by the argv after `--`.
	TestCommand Set[[]string]
	// Timeout is `test.timeout`, overridden by --timeout.
	Timeout Set[time.Duration]
	// BaselineRuns is `test.baseline_runs`.
	BaselineRuns Set[int]

	// Jobs is `execution.jobs`, overridden by -j/--jobs.
	Jobs Set[int]

	// CacheMode is `cache.mode`, overridden by --cache.
	CacheMode Set[CacheMode]
	// CacheDirectory is `cache.directory`.
	CacheDirectory Set[string]

	// Strict is `policy.strict`, overridden by --strict/--no-strict.
	Strict Set[bool]
	// MinimumScore is `policy.minimum_score`.
	MinimumScore Set[float64]
	// RequireMutants is `policy.require_mutants`.
	RequireMutants Set[bool]

	// ReportDirectory is `report.directory`.
	ReportDirectory Set[string]
	// ReportFormats is `report.formats`, overridden by --report.
	ReportFormats Set[[]ReportFormat]
	// ReportHigh is `report.high`.
	ReportHigh Set[int]
	// ReportLow is `report.low`.
	ReportLow Set[int]
}

// IsEmpty reports whether the overlay sets nothing at all, which is what an
// absent configuration file and an invocation with no flags both produce.
//
// It is spelled out field by field rather than compared against the zero
// Overlay because a Set of a slice is not comparable; the compiler would
// reject `o == Overlay{}`, and a reflect.DeepEqual would quietly start
// answering "no" the day a field is given a non-nil zero value.
func (o Overlay) IsEmpty() bool { return !o.setsAnything() }

// setsAnything reports whether any field of the overlay was set.
func (o Overlay) setsAnything() bool {
	return o.Version.IsSet() ||
		o.Include.IsSet() ||
		o.Exclude.IsSet() ||
		o.Operators.IsSet() ||
		o.Profile.IsSet() ||
		o.Expect.IsSet() ||
		o.TestCommand.IsSet() ||
		o.Timeout.IsSet() ||
		o.BaselineRuns.IsSet() ||
		o.Jobs.IsSet() ||
		o.CacheMode.IsSet() ||
		o.CacheDirectory.IsSet() ||
		o.Strict.IsSet() ||
		o.MinimumScore.IsSet() ||
		o.RequireMutants.IsSet() ||
		o.ReportDirectory.IsSet() ||
		o.ReportFormats.IsSet() ||
		o.ReportHigh.IsSet() ||
		o.ReportLow.IsSet()
}

// Merge resolves the three layers into one configuration: defaults first, then
// whatever the file set, then whatever the flags set.
//
// Each set field replaces the value below it whole. Arrays are not appended
// to, and sections are not deep merged, because both would make a
// configuration impossible to narrow from the command line: with append
// semantics there is no way to say "only this include pattern", and with a
// deep merge there is no way to say "no exclude patterns at all".
//
// Merge does not validate. A configuration is checked where it was written, so
// that an error can point at the line that caused it; the cross-field rules
// that can only be judged after merging live in [Config.Validate], which every
// caller runs before acting on the result.
//
// The result shares no slice with any argument.
func Merge(defaults Config, file FileConfig, flags Overlay) Config {
	c := defaults.Clone()
	apply(&c, file.Overlay)
	apply(&c, flags)
	return c
}

// MergeOverlays is [Merge] for callers holding a bare overlay rather than a
// parsed file, such as tests and `init --dry-run`.
func MergeOverlays(defaults Config, layers ...Overlay) Config {
	c := defaults.Clone()
	for _, layer := range layers {
		apply(&c, layer)
	}
	return c
}

// apply writes one layer's set fields over c, copying every slice so that the
// resulting configuration cannot be changed by mutating the overlay it came
// from.
func apply(c *Config, o Overlay) {
	if v, ok := o.Version.Get(); ok {
		c.Version = v
	}

	if v, ok := o.Include.Get(); ok {
		c.Mutation.Include = slices.Clone(v)
	}
	if v, ok := o.Exclude.Get(); ok {
		c.Mutation.Exclude = slices.Clone(v)
	}
	if v, ok := o.Operators.Get(); ok {
		c.Mutation.Operators = slices.Clone(v)
	}
	if v, ok := o.Profile.Get(); ok {
		c.Mutation.Profile = v
	}
	if v, ok := o.Expect.Get(); ok {
		c.Mutation.Expect = slices.Clone(v)
	}

	if v, ok := o.TestCommand.Get(); ok {
		c.Test.Command = slices.Clone(v)
	}
	if v, ok := o.Timeout.Get(); ok {
		c.Test.Timeout = v
	}
	if v, ok := o.BaselineRuns.Get(); ok {
		c.Test.BaselineRuns = v
	}

	if v, ok := o.Jobs.Get(); ok {
		c.Execution.Jobs = v
	}

	if v, ok := o.CacheMode.Get(); ok {
		c.Cache.Mode = v
	}
	if v, ok := o.CacheDirectory.Get(); ok {
		c.Cache.Directory = canonicalDirectory(v)
	}

	if v, ok := o.Strict.Get(); ok {
		c.Policy.Strict = v
	}
	if v, ok := o.MinimumScore.Get(); ok {
		c.Policy.MinimumScore = v
	}
	if v, ok := o.RequireMutants.Get(); ok {
		c.Policy.RequireMutants = v
	}

	if v, ok := o.ReportDirectory.Get(); ok {
		c.Report.Directory = canonicalDirectory(v)
	}
	if v, ok := o.ReportFormats.Get(); ok {
		c.Report.Formats = slices.Clone(v)
	}
	if v, ok := o.ReportHigh.Get(); ok {
		c.Report.High = v
	}
	if v, ok := o.ReportLow.Get(); ok {
		c.Report.Low = v
	}
}

// canonicalDirectory puts a configured directory into the one spelling the
// rest of go-mutants uses: forward slashes, cleaned, and relative. It is
// [mutation.NormalizePath], the same normalization mutant identities are built
// from, so "reports\out" and "reports/out" name one directory here for exactly
// the reason they name one file there.
//
// It runs during the merge rather than during decoding, so that a directory
// reaches a resolved [Config] in one spelling no matter which layer set it. A
// path that cannot be normalized at all is handed back exactly as written,
// which leaves the validator quoting what its author typed rather than a
// half-cleaned version of it.
func canonicalDirectory(directory string) string {
	canonical, err := relativeDirectory(directory)
	if err != nil {
		return directory
	}
	return canonical
}

// overlay renders a resolved configuration back as a fully set overlay, so
// that [Config.Validate] can run exactly the same per-value checks the file
// and the flags went through instead of a second, drifting copy of them.
//
// The two settings whose zero value means "unset" — a derived timeout and a
// default cache directory — are left unset, because that is what they mean.
func (c Config) overlay() Overlay {
	o := Overlay{
		Version:         Explicit(c.Version),
		Include:         Explicit(c.Mutation.Include),
		Exclude:         Explicit(c.Mutation.Exclude),
		Operators:       Explicit(c.Mutation.Operators),
		Profile:         Explicit(c.Mutation.Profile),
		Expect:          Explicit(c.Mutation.Expect),
		TestCommand:     Explicit(c.Test.Command),
		BaselineRuns:    Explicit(c.Test.BaselineRuns),
		Jobs:            Explicit(c.Execution.Jobs),
		CacheMode:       Explicit(c.Cache.Mode),
		Strict:          Explicit(c.Policy.Strict),
		MinimumScore:    Explicit(c.Policy.MinimumScore),
		RequireMutants:  Explicit(c.Policy.RequireMutants),
		ReportDirectory: Explicit(c.Report.Directory),
		ReportFormats:   Explicit(c.Report.Formats),
		ReportHigh:      Explicit(c.Report.High),
		ReportLow:       Explicit(c.Report.Low),
	}
	if c.Test.Timeout != 0 {
		o.Timeout = Explicit(c.Test.Timeout)
	}
	if c.Cache.Directory != "" {
		o.CacheDirectory = Explicit(c.Cache.Directory)
	}
	return o
}
