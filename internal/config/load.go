// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type FileConfig struct {
	Path    string
	Present bool
	Overlay Overlay

	positions map[string]Position
}

func (f FileConfig) Position(key string) (Position, bool) {
	position, ok := f.positions[key]
	return position, ok
}

func (f FileConfig) Keys() []string {
	keys := make([]string, 0, len(f.positions))
	for key := range f.positions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func Load(path string, flags Overlay) (Config, error) {
	file, err := LoadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := flags.Validate(); err != nil {
		return Config{}, err
	}
	resolved := Merge(Defaults(), file, flags)
	if err := resolved.Validate(); err != nil {
		return Config{}, err
	}
	return resolved, nil
}

func LoadFile(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return FileConfig{Path: path, Present: false}, nil
		}
		return FileConfig{Path: path}, &Error{
			Code:    CodeUnreadable,
			File:    path,
			Message: "the configuration file could not be read: " + ioMessage(err),
			Err:     err,
		}
	}
	return Parse(path, data)
}

func ioMessage(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		return pathErr.Err.Error()
	}
	return err.Error()
}

func Parse(path string, data []byte) (FileConfig, error) {
	file := FileConfig{Path: path, Present: true}

	var preamble struct {
		Version *int64 `toml:"version"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).Decode(&preamble); err != nil {
		return file, decodeError(path, err)
	}

	file.positions = indexPositions(data)
	report := fileReporter(path, file.positions)

	if preamble.Version == nil {
		return file, &Error{
			Code: CodeMissingVersion,
			File: path,
			Key:  "version",
			Message: "the configuration file does not say which schema it is written against: add `version = " +
				strconv.Itoa(Version) + "` at the top",
		}
	}
	if *preamble.Version != Version {
		return file, report.errorf(CodeUnsupportedVersion, "version",
			"configuration version %d is not supported: this build reads version %d",
			*preamble.Version, Version)
	}

	var document document
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return file, decodeError(path, err)
	}

	overlay, problems := document.overlay(report)
	file.Overlay = overlay
	problems = append(problems, validateOverlay(overlay, report))
	return file, join(problems)
}

func decodeError(path string, err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		problems := make([]error, 0, len(strict.Errors))
		for i := range strict.Errors {
			decode := &strict.Errors[i]
			key := keyPath(decode.Key())
			problems = append(problems, &Error{
				Code:     CodeUnknownKey,
				File:     path,
				Position: positionOf(decode),
				Key:      key,
				Message:  "unknown key: no version of the go-mutants configuration defines it",
				Err:      decode,
			})
		}
		return join(problems)
	}

	var decode *toml.DecodeError
	if errors.As(err, &decode) {
		key := keyPath(decode.Key())
		return &Error{
			Code:     CodeInvalidTOML,
			File:     path,
			Position: positionOf(decode),
			Key:      key,
			Message:  decodeMessage(key, strings.TrimPrefix(decode.Error(), "toml: ")),
			Err:      decode,
		}
	}

	return &Error{
		Code:    CodeInvalidTOML,
		File:    path,
		Message: "the configuration file is not valid TOML: " + err.Error(),
		Err:     err,
	}
}

func decodeMessage(key, message string) string {
	kind, mismatch := mismatchKind(message)
	if !mismatch {
		return message
	}
	expected, known := expectedTypes[key]
	switch {
	case known && kind != "":
		return "must be " + expected + ", not " + kind
	case known:
		return "must be " + expected
	case kind != "":
		return kind + " cannot be written here"
	default:
		return "the value written here does not fit the key it was written under"
	}
}

func mismatchKind(message string) (kind string, mismatch bool) {
	if rest, ok := strings.CutPrefix(message, "cannot decode TOML "); ok {
		name, _, _ := strings.Cut(rest, " into ")
		return tomlKinds[name], true
	}
	if rest, ok := strings.CutPrefix(message, "cannot store "); ok {
		switch {
		case strings.HasPrefix(rest, "a table in "):
			return tomlKinds["table"], true
		case strings.HasPrefix(rest, "an array table in "):
			return "an array of tables", true
		}
		return "", true
	}
	return "", false
}

var tomlKinds = map[string]string{
	"string":         "a string",
	"integer":        "an integer",
	"float":          "a float",
	"boolean":        "a boolean",
	"datetime":       "a date-time",
	"local datetime": "a local date-time",
	"local date":     "a local date",
	"local time":     "a local time",
	"array":          "an array",
	"table":          "a table",
	"inline table":   "an inline table",
}

var expectedTypes = map[string]string{
	"version": "an integer",

	"mutation":               "a table",
	"mutation.include":       "a list of strings",
	"mutation.exclude":       "a list of strings",
	"mutation.operators":     "a list of strings",
	"mutation.profile":       "a string",
	"mutation.expect":        "a list of tables",
	"mutation.expect.id":     "a string",
	"mutation.expect.reason": "a string",

	"test":               "a table",
	"test.command":       "a list of strings",
	"test.timeout":       "a string",
	"test.memory":        "a string",
	"test.baseline_runs": "an integer",
	"test.narrowing":     "a string",
	"test.probing":       "a string",

	"execution":         "a table",
	"execution.jobs":    "an integer",
	"execution.isolate": "a boolean",

	"cache":           "a table",
	"cache.mode":      "a string",
	"cache.directory": "a string",

	"policy":                 "a table",
	"policy.strict":          "a boolean",
	"policy.minimum_score":   "a number",
	"policy.require_mutants": "a boolean",

	"report":           "a table",
	"report.directory": "a string",
	"report.formats":   "a list of strings",
	"report.high":      "an integer",
	"report.low":       "an integer",
}

func SchemaKeys() []string {
	keys := make([]string, 0, len(expectedTypes))
	for key := range expectedTypes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func positionOf(decode *toml.DecodeError) Position {
	line, column := decode.Position()
	return Position{Line: line, Column: column}
}

func keyPath(key toml.Key) string { return strings.Join(key, ".") }

type document struct {
	Version   *int64             `toml:"version"`
	Mutation  *documentMutation  `toml:"mutation"`
	Test      *documentTest      `toml:"test"`
	Execution *documentExecution `toml:"execution"`
	Cache     *documentCache     `toml:"cache"`
	Policy    *documentPolicy    `toml:"policy"`
	Report    *documentReport    `toml:"report"`
}

type documentMutation struct {
	Include   *[]string        `toml:"include"`
	Exclude   *[]string        `toml:"exclude"`
	Operators *[]string        `toml:"operators"`
	Profile   *string          `toml:"profile"`
	Expect    []documentExpect `toml:"expect"`
}

type documentExpect struct {
	ID     *string `toml:"id"`
	Reason *string `toml:"reason"`
}

type documentTest struct {
	Command      *[]string `toml:"command"`
	Timeout      *string   `toml:"timeout"`
	Memory       *string   `toml:"memory"`
	BaselineRuns *int64    `toml:"baseline_runs"`
	Narrowing    *string   `toml:"narrowing"`
	Probing      *string   `toml:"probing"`
}

type documentExecution struct {
	Jobs    *int64 `toml:"jobs"`
	Isolate *bool  `toml:"isolate"`
}

type documentCache struct {
	Mode      *string `toml:"mode"`
	Directory *string `toml:"directory"`
}

type documentPolicy struct {
	Strict         *bool    `toml:"strict"`
	MinimumScore   *float64 `toml:"minimum_score"`
	RequireMutants *bool    `toml:"require_mutants"`
}

type documentReport struct {
	Directory *string   `toml:"directory"`
	Formats   *[]string `toml:"formats"`
	High      *int64    `toml:"high"`
	Low       *int64    `toml:"low"`
}

func (d *document) overlay(report reporter) (Overlay, []error) {
	var overlay Overlay
	var problems []error

	if d.Version != nil {
		overlay.Version = Explicit(toInt(*d.Version))
	}

	if m := d.Mutation; m != nil {
		if m.Include != nil {
			overlay.Include = Explicit(*m.Include)
		}
		if m.Exclude != nil {
			overlay.Exclude = Explicit(*m.Exclude)
		}
		if m.Operators != nil {
			overlay.Operators = Explicit(*m.Operators)
		}
		if m.Profile != nil {
			tier, err := mutation.ParseTier(*m.Profile)
			if err != nil {
				problems = append(problems, report.wrapf(CodeUnknownProfile, "mutation.profile", err,
					"unknown profile %q: expected %s", *m.Profile, tierList()))
			} else {
				overlay.Profile = Explicit(tier)
			}
		}
		if m.Expect != nil {
			expectations := make([]Expectation, 0, len(m.Expect))
			for _, row := range m.Expect {
				expectations = append(expectations, Expectation{
					ID:     derefString(row.ID),
					Reason: derefString(row.Reason),
				})
			}
			overlay.Expect = Explicit(expectations)
		}
	}

	if t := d.Test; t != nil {
		if t.Command != nil {
			overlay.TestCommand = Explicit(*t.Command)
		}
		if t.Timeout != nil {
			timeout, err := time.ParseDuration(*t.Timeout)
			if err != nil {
				problems = append(problems, report.wrapf(CodeInvalidDuration, "test.timeout", err,
					"%q is not a duration: write a Go duration such as \"90s\", \"2m\", or \"1m30s\"", *t.Timeout))
			} else {
				overlay.Timeout = Explicit(timeout)
			}
		}
		if t.Memory != nil {
			size, err := parseSize(*t.Memory)
			if err != nil {
				problems = append(problems, report.wrapf(CodeInvalidSize, "test.memory", err,
					"%s", err.Error()))
			} else {
				overlay.Memory = Explicit(size)
			}
		}
		if t.BaselineRuns != nil {
			overlay.BaselineRuns = Explicit(toInt(*t.BaselineRuns))
		}
		if t.Narrowing != nil {
			overlay.Narrowing = Explicit(Narrowing(*t.Narrowing))
		}
		if t.Probing != nil {
			overlay.Probing = Explicit(Probing(*t.Probing))
		}
	}

	if e := d.Execution; e != nil {
		if e.Jobs != nil {
			overlay.Jobs = Explicit(toInt(*e.Jobs))
		}
		if e.Isolate != nil {
			overlay.Isolate = Explicit(*e.Isolate)
		}
	}

	if c := d.Cache; c != nil {
		if c.Mode != nil {
			overlay.CacheMode = Explicit(CacheMode(*c.Mode))
		}
		if c.Directory != nil {
			overlay.CacheDirectory = Explicit(*c.Directory)
		}
	}

	if p := d.Policy; p != nil {
		if p.Strict != nil {
			overlay.Strict = Explicit(*p.Strict)
		}
		if p.MinimumScore != nil {
			overlay.MinimumScore = Explicit(*p.MinimumScore)
		}
		if p.RequireMutants != nil {
			overlay.RequireMutants = Explicit(*p.RequireMutants)
		}
	}

	if r := d.Report; r != nil {
		if r.Directory != nil {
			overlay.ReportDirectory = Explicit(*r.Directory)
		}
		if r.Formats != nil {
			formats := make([]ReportFormat, 0, len(*r.Formats))
			for _, name := range *r.Formats {
				formats = append(formats, ReportFormat(name))
			}
			overlay.ReportFormats = Explicit(formats)
		}
		if r.High != nil {
			overlay.ReportHigh = Explicit(toInt(*r.High))
		}
		if r.Low != nil {
			overlay.ReportLow = Explicit(toInt(*r.Low))
		}
	}

	return overlay, problems
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func toInt(v int64) int {
	if v > int64(maxInt) {
		return maxInt
	}
	if v < int64(minInt) {
		return minInt
	}
	return int(v)
}

const (
	maxInt = int(^uint(0) >> 1)
	minInt = -maxInt - 1
)
