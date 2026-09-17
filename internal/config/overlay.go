// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"slices"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type Overlay struct {
	Version Set[int]

	Include   Set[[]string]
	Exclude   Set[[]string]
	Operators Set[[]string]
	Profile   Set[mutation.Tier]
	Expect    Set[[]Expectation]

	TestCommand  Set[[]string]
	Timeout      Set[time.Duration]
	Memory       Set[int64]
	BaselineRuns Set[int]
	Narrowing    Set[Narrowing]
	Probing      Set[Probing]

	Jobs    Set[int]
	Isolate Set[bool]

	CacheMode      Set[CacheMode]
	CacheDirectory Set[string]

	Strict         Set[bool]
	MinimumScore   Set[float64]
	RequireMutants Set[bool]

	ReportDirectory Set[string]
	ReportFormats   Set[[]ReportFormat]
	ReportHigh      Set[int]
	ReportLow       Set[int]
}

func (o Overlay) IsEmpty() bool { return !o.setsAnything() }

func (o Overlay) setsAnything() bool {
	return o.Version.IsSet() ||
		o.Include.IsSet() ||
		o.Exclude.IsSet() ||
		o.Operators.IsSet() ||
		o.Profile.IsSet() ||
		o.Expect.IsSet() ||
		o.TestCommand.IsSet() ||
		o.Timeout.IsSet() ||
		o.Memory.IsSet() ||
		o.BaselineRuns.IsSet() ||
		o.Narrowing.IsSet() ||
		o.Probing.IsSet() ||
		o.Jobs.IsSet() ||
		o.Isolate.IsSet() ||
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

func Merge(defaults Config, file FileConfig, flags Overlay) Config {
	c := defaults.Clone()
	apply(&c, file.Overlay)
	apply(&c, flags)
	return c
}

func MergeOverlays(defaults Config, layers ...Overlay) Config {
	c := defaults.Clone()
	for _, layer := range layers {
		apply(&c, layer)
	}
	return c
}

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
	if v, ok := o.Memory.Get(); ok {
		c.Test.Memory = v
	}
	if v, ok := o.BaselineRuns.Get(); ok {
		c.Test.BaselineRuns = v
	}
	if v, ok := o.Probing.Get(); ok {
		c.Test.Probing = v
	}
	if v, ok := o.Narrowing.Get(); ok {
		c.Test.Narrowing = v
	}

	if v, ok := o.Jobs.Get(); ok {
		c.Execution.Jobs = v
	}
	if v, ok := o.Isolate.Get(); ok {
		c.Execution.Isolate = v
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

func canonicalDirectory(directory string) string {
	canonical, err := relativeDirectory(directory)
	if err != nil {
		return directory
	}
	return canonical
}

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
		Narrowing:       Explicit(c.Test.Narrowing),
		Probing:         Explicit(c.Test.Probing),
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
	if c.Test.Memory != 0 {
		o.Memory = Explicit(c.Test.Memory)
	}
	if c.Cache.Directory != "" {
		o.CacheDirectory = Explicit(c.Cache.Directory)
	}
	return o
}
