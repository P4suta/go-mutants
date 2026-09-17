// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"runtime"
	"slices"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
)

const (
	Version = 1

	FileName = ".go-mutants.toml"
)

const (
	MinJobs       = 1
	MaxJobs       = 32
	DefaultJobCap = 8

	MinBaselineRuns     = 1
	MaxBaselineRuns     = 10
	DefaultBaselineRuns = 3

	MinPercent = 0
	MaxPercent = 100

	DefaultReportDirectory = "reports/mutation"
	DefaultReportHigh      = 80
	DefaultReportLow       = 60
)

type CacheMode string

const (
	CacheAuto CacheMode = "auto"
	CacheOn   CacheMode = "on"
	CacheOff  CacheMode = "off"
)

func CacheModes() []CacheMode { return []CacheMode{CacheAuto, CacheOn, CacheOff} }

func (m CacheMode) Valid() bool {
	return m == CacheAuto || m == CacheOn || m == CacheOff
}

func (m CacheMode) String() string { return string(m) }

type Narrowing string

const (
	NarrowingTest    Narrowing = "test"
	NarrowingPackage Narrowing = "package"
)

func Narrowings() []Narrowing { return []Narrowing{NarrowingTest, NarrowingPackage} }

func (n Narrowing) Valid() bool {
	return n == NarrowingTest || n == NarrowingPackage
}

func (n Narrowing) String() string { return string(n) }

type Probing string

const (
	ProbingOff Probing = "off"
	ProbingOn  Probing = "on"
)

func Probings() []Probing { return []Probing{ProbingOff, ProbingOn} }

func (p Probing) Valid() bool { return p == ProbingOff || p == ProbingOn }

func (p Probing) String() string { return string(p) }

type ReportFormat string

const (
	FormatJSON ReportFormat = "json"
	FormatHTML ReportFormat = "html"
)

func ReportFormats() []ReportFormat { return []ReportFormat{FormatJSON, FormatHTML} }

func (f ReportFormat) Valid() bool { return f == FormatJSON || f == FormatHTML }

func (f ReportFormat) String() string { return string(f) }

type Expectation struct {
	ID     string
	Reason string
}

type Mutation struct {
	Include   []string
	Exclude   []string
	Operators []string
	Profile   mutation.Tier
	Expect    []Expectation
}

type Test struct {
	Command      []string
	Timeout      time.Duration
	Memory       int64
	BaselineRuns int
	Narrowing    Narrowing
	Probing      Probing
}

type Execution struct {
	Jobs int

	Isolate bool
}

type Cache struct {
	Mode      CacheMode
	Directory string
}

type Report struct {
	Directory string
	Formats   []ReportFormat
	High      int
	Low       int
}

type Config struct {
	Version   int
	Mutation  Mutation
	Test      Test
	Execution Execution
	Cache     Cache
	Policy    mutation.Policy
	Report    Report
}

func DefaultJobs() int {
	return min(runtime.NumCPU(), DefaultJobCap)
}

func DefaultTestCommand() []string { return []string{"go", "test", "./..."} }

func DefaultInclude() []string { return []string{"**/*.go"} }

func Defaults() Config {
	return Config{
		Version: Version,
		Mutation: Mutation{
			Include:   DefaultInclude(),
			Exclude:   nil,
			Operators: nil,
			Profile:   mutation.TierBalanced,
			Expect:    nil,
		},
		Test: Test{
			Command:      DefaultTestCommand(),
			Timeout:      0,
			Memory:       0,
			BaselineRuns: DefaultBaselineRuns,
			Narrowing:    NarrowingTest,
			Probing:      ProbingOff,
		},
		Execution: Execution{Jobs: DefaultJobs(), Isolate: false},
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

func (c Config) Clone() Config {
	c.Mutation.Include = slices.Clone(c.Mutation.Include)
	c.Mutation.Exclude = slices.Clone(c.Mutation.Exclude)
	c.Mutation.Operators = slices.Clone(c.Mutation.Operators)
	c.Mutation.Expect = slices.Clone(c.Mutation.Expect)
	c.Test.Command = slices.Clone(c.Test.Command)
	c.Report.Formats = slices.Clone(c.Report.Formats)
	return c
}
