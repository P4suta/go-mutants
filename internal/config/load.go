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

// A FileConfig is what one .go-mutants.toml contributed, already decoded and
// validated on its own terms.
//
// It keeps the positions it was parsed with, so a caller can still underline a
// setting after the fact — `doctor` explaining where a value came from, `init
// --check` reporting on a file it did not write.
type FileConfig struct {
	// Path is the file as the caller spelled it, kept even when the file was
	// not there so that a message can say which path was looked at.
	Path string
	// Present reports whether the file existed. A false here is not a
	// failure; see [LoadFile].
	Present bool
	// Overlay is what the file set. It is empty when Present is false.
	Overlay Overlay

	// positions locates each key that was written, by the same path the
	// validators use. It is nil for an absent file.
	positions map[string]Position
}

// Position returns where a key was written, by the dotted-and-indexed path
// the diagnostics use ("report.low", "mutation.expect[1].id"). The second
// result is false when the key was not in the file, or when the file was not
// there at all.
func (f FileConfig) Position(key string) (Position, bool) {
	position, ok := f.positions[key]
	return position, ok
}

// Keys returns every key path the file wrote, sorted. The order is fixed
// rather than map order so that anything printing it — `doctor` listing what a
// project configured — is the same on two runs and diffable between them.
func (f FileConfig) Keys() []string {
	keys := make([]string, 0, len(f.positions))
	for key := range f.positions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// Load is the whole sequence in one call: read the file, check the flags,
// merge over the built-in defaults, and check the result. It is what the CLI
// calls, and the shortest correct way to obtain a [Config].
//
// The flags are checked before the merge so that a mistake the user has just
// typed is reported as the flag they typed, ahead of anything the file has to
// say.
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

// LoadFile reads and validates a configuration file.
//
// A file that is not there is not an error: it yields an empty, valid
// FileConfig with Present false, so that go-mutants works out of the box in a
// project that has never configured it. Everything else about the path is an
// error — a directory, a permission failure, an unreadable device — because
// those mean the user meant to configure something and did not get it.
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

// ioMessage strips the *fs.PathError wrapper from a read failure, which would
// otherwise repeat the path this package already prints.
func ioMessage(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// Parse decodes and validates a configuration document that is already in
// memory. path is used only to locate diagnostics and may name a file that
// does not exist, which is what makes this the entry point for tests and for
// `init --check` on generated content.
//
// The result is Present, because the bytes were there to parse.
func Parse(path string, data []byte) (FileConfig, error) {
	file := FileConfig{Path: path, Present: true}

	// The version is read first, on its own, and deliberately without strict
	// mode. A file written against a later schema is full of keys this build
	// has never heard of, and answering it with "unknown key mutation.foo"
	// would send someone hunting a typo that is not there. The one true
	// sentence about such a file is that its version is not one this build
	// reads, and that sentence is only available before strictness has a say.
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
	// Strictness is the point. Without it a key one letter off its intended
	// spelling is accepted, ignored, and reported nowhere, which is how a
	// project ends up believing it excludes a directory it does not.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return file, decodeError(path, err)
	}

	overlay, problems := document.overlay(report)
	file.Overlay = overlay
	problems = append(problems, validateOverlay(overlay, report))
	return file, join(problems)
}

// decodeError converts go-toml's failures into this package's codes, keeping
// the position and the key path it worked out.
//
// Strict-mode failures are reported together: go-toml collects every unknown
// field in document order, and a file with three typos should cost one round
// trip to fix, not three.
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

// decodeMessage states in this package's own words why a value was refused.
//
// go-toml explains a type mismatch by naming the Go field it was decoding
// into: "cannot decode TOML string into struct field config.documentReport.High
// of type int64". That sentence cannot be the one a user reads. It asks them to
// think about `config.documentReport.High`, a type this package does not export
// and nobody can act on, and it makes the wording of GOM3002 — a code that
// means one thing forever — hostage to the name of an unexported struct field,
// so that a rename nobody reviewed as a user-facing change would become one. A
// mismatch is therefore restated from the schema: the type the key takes, and
// the kind of value that was actually written, which is the value the reported
// position underlines in both the whole-value and the array-element case.
//
// Everything else the decoder refuses is a complaint about the file in the
// file's own vocabulary — an unterminated table header, a bad escape, a key
// defined twice. Those are already the sentence this package would write, so
// they pass through unchanged. The two prefixes below are every message
// go-toml's unmarshaler can reach with this schema that names a Go type; the
// rest of what it reports comes from the TOML parser and names nothing but
// TOML.
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
		// A key the schema does not define, reached by writing a dotted key or
		// a header through a value that is not a table: `version.x = 1` makes
		// `version` a table, and the thing worth saying is that it cannot be.
		return kind + " cannot be written here"
	default:
		return "the value written here does not fit the key it was written under"
	}
}

// mismatchKind reports whether a decode failure is a type mismatch and, when
// the message named the kind of value that was written, what that kind was.
//
// Reading one word out of the library's sentence is a small and bounded
// coupling: the words are TOML's own, the set is closed, and anything outside
// it is dropped rather than repeated. An upstream rewording can therefore cost
// a clause of a diagnostic; it cannot leak a Go identifier into one.
func mismatchKind(message string) (kind string, mismatch bool) {
	if rest, ok := strings.CutPrefix(message, "cannot decode TOML "); ok {
		name, _, _ := strings.Cut(rest, " into ")
		return tomlKinds[name], true
	}
	// A `[header]` or `[[header]]` naming a key the schema holds something
	// other than a table in.
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

// tomlKinds renders each kind of value go-toml can name as this package says
// it. The vocabulary is the TOML specification's, because that is the document
// the reader has open.
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

// expectedTypes names the kind of value each key of the schema takes, in the
// vocabulary of the file rather than of Go. It is what a type mismatch is
// reported against, and it is why a GOM3002 message survives a rename of any of
// the unexported structs below.
//
// Every key the schema defines has an entry, tables included, and the package
// tests walk those structs to prove it: a field added without an entry here
// would quietly degrade to a vaguer sentence rather than fail anything.
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
	"test.baseline_runs": "an integer",

	"execution":      "a table",
	"execution.jobs": "an integer",

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

// positionOf reads a decode error's one-based position.
func positionOf(decode *toml.DecodeError) Position {
	line, column := decode.Position()
	return Position{Line: line, Column: column}
}

// keyPath renders go-toml's key parts the way this package names settings.
func keyPath(key toml.Key) string { return strings.Join(key, ".") }

// document is the decoded shape of .go-mutants.toml.
//
// Every field is a pointer, or a slice for the repeated tables, so that "the
// key was written" and "the key was written with a zero value" stay
// distinguishable. That distinction is not academic: `formats = []` is the
// documented way to turn project reports off, and `strict = false` in a file
// has to beat a default that is already false in order to survive a future
// change to that default.
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
	BaselineRuns *int64    `toml:"baseline_runs"`
}

type documentExecution struct {
	Jobs *int64 `toml:"jobs"`
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

// overlay converts the decoded document into the layer [Merge] consumes,
// together with the problems that only conversion can find.
//
// Only the conversions that cannot be represented at all are reported here: a
// profile name that is not a tier and a timeout that is not a duration have no
// value to carry forward. Everything else keeps the user's spelling and is
// judged by the validator, so that one check exists per rule rather than one
// per entry point.
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
		if t.BaselineRuns != nil {
			overlay.BaselineRuns = Explicit(toInt(*t.BaselineRuns))
		}
	}

	if e := d.Execution; e != nil {
		if e.Jobs != nil {
			overlay.Jobs = Explicit(toInt(*e.Jobs))
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

// derefString reads an optional string, treating an absent key as empty. The
// emptiness is then rejected by the validator with a position, which is a
// better message than "expected a string".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// toInt narrows a decoded TOML integer, saturating rather than truncating.
//
// TOML integers are 64 bit and Go's int is not, on every platform. A truncating
// conversion would let `jobs = 4294967297` arrive as 1 on a 32-bit build:
// accepted, in range, and nothing like what was written. Saturating keeps an
// absurd number absurd, so the range check refuses it everywhere.
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
