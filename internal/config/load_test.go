// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/mutation"
)

func hexID(fill string) string { return strings.Repeat(fill, mutation.IDHexLength/len(fill)) }

type decodeCase struct {
	name    string
	source  string
	code    Code
	key     string
	line    int
	column  int
	message string
	exact   bool
}

func TestParseRejects(t *testing.T) {
	tests := []decodeCase{
		{
			name:    "unknown top level key",
			source:  "version = 1\nflavour = \"vanilla\"\n",
			code:    CodeUnknownKey,
			key:     "flavour",
			line:    2,
			column:  1,
			message: "unknown key",
		},
		{
			name:    "unknown key inside a section",
			source:  "version = 1\n\n[mutation]\nprofil = \"balanced\"\n",
			code:    CodeUnknownKey,
			key:     "mutation.profil",
			line:    4,
			column:  1,
			message: "unknown key",
		},
		{
			name:    "unknown key inside a repeated table",
			source:  "version = 1\n\n[[mutation.expect]]\nid = \"" + hexID("a") + "\"\nreason = \"why\"\nwhy = \"no\"\n",
			code:    CodeUnknownKey,
			key:     "mutation.expect.why",
			line:    6,
			column:  1,
			message: "unknown key",
		},
		{
			name:    "value of the wrong type",
			source:  "version = 1\n\n[report]\nhigh = \"eighty\"\n",
			code:    CodeInvalidTOML,
			key:     "report.high",
			line:    4,
			column:  8,
			message: "must be an integer, not a string",
			exact:   true,
		},
		{
			name:    "array of the wrong element type",
			source:  "version = 1\n\n[mutation]\ninclude = [1, 2]\n",
			code:    CodeInvalidTOML,
			key:     "mutation.include",
			line:    4,
			column:  12,
			message: "must be a list of strings, not an integer",
			exact:   true,
		},
		{
			name:    "malformed table header",
			source:  "version = 1\n[mutation\n",
			code:    CodeInvalidTOML,
			key:     "",
			line:    2,
			column:  10,
			message: "expected ']'",
		},
		{
			name:    "no version",
			source:  "[mutation]\nprofile = \"balanced\"\n",
			code:    CodeMissingVersion,
			key:     "version",
			line:    0,
			column:  0,
			message: "which schema it is written against",
		},
		{
			name:    "unsupported version",
			source:  "version = 2\n",
			code:    CodeUnsupportedVersion,
			key:     "version",
			line:    1,
			column:  11,
			message: "is not supported",
		},
		{
			name:    "invalid glob names the element",
			source:  "version = 1\n\n[mutation]\ninclude = [\"ok/**.go\", \"/bad.go\"]\n",
			code:    CodeInvalidGlob,
			key:     "mutation.include[1]",
			line:    4,
			column:  24,
			message: "invalid pattern",
		},
		{
			name:    "invalid exclude glob",
			source:  "version = 1\n\n[mutation]\nexclude = [\"a//b\"]\n",
			code:    CodeInvalidGlob,
			key:     "mutation.exclude[0]",
			line:    4,
			column:  12,
			message: "empty path element",
		},
		{
			name:    "unknown operator",
			source:  "version = 1\n\n[mutation]\noperators = [\"comparison\", \"telepathy\"]\n",
			code:    CodeUnknownOperator,
			key:     "mutation.operators[1]",
			line:    4,
			column:  28,
			message: "unknown operator",
		},
		{
			name:    "duplicate operator",
			source:  "version = 1\n\n[mutation]\noperators = [\"comparison\", \"comparison\"]\n",
			code:    CodeDuplicateOperator,
			key:     "mutation.operators[1]",
			line:    4,
			column:  28,
			message: "already selected",
		},
		{
			name:    "unknown profile",
			source:  "version = 1\n\n[mutation]\nprofile = \"aggressive\"\n",
			code:    CodeUnknownProfile,
			key:     "mutation.profile",
			line:    4,
			column:  11,
			message: "unknown profile",
		},
		{
			name:    "expectation id is not a full id",
			source:  "version = 1\n\n[[mutation.expect]]\nid = \"abc123\"\nreason = \"equivalent\"\n",
			code:    CodeInvalidExpectationID,
			key:     "mutation.expect[0].id",
			line:    4,
			column:  6,
			message: "not a mutant id",
		},
		{
			name:    "expectation id is missing",
			source:  "version = 1\n\n[[mutation.expect]]\nreason = \"equivalent\"\n",
			code:    CodeInvalidExpectationID,
			key:     "mutation.expect[0].id",
			line:    0,
			column:  0,
			message: "needs an id",
		},
		{
			name: "duplicate expectation",
			source: "version = 1\n\n[[mutation.expect]]\nid = \"" + hexID("a") + "\"\nreason = \"first\"\n\n" +
				"[[mutation.expect]]\nid = \"" + hexID("a") + "\"\nreason = \"second\"\n",
			code:    CodeDuplicateExpectation,
			key:     "mutation.expect[1].id",
			line:    8,
			column:  6,
			message: "already expected",
		},
		{
			name:    "expectation with a blank reason",
			source:  "version = 1\n\n[[mutation.expect]]\nid = \"" + hexID("a") + "\"\nreason = \"   \"\n",
			code:    CodeEmptyExpectationReason,
			key:     "mutation.expect[0].reason",
			line:    5,
			column:  10,
			message: "needs a reason",
		},
		{
			name:    "empty test command",
			source:  "version = 1\n\n[test]\ncommand = []\n",
			code:    CodeEmptyTestCommand,
			key:     "test.command",
			line:    4,
			column:  1,
			message: "test command is empty",
		},
		{
			name:    "blank program name",
			source:  "version = 1\n\n[test]\ncommand = [\"\", \"test\"]\n",
			code:    CodeEmptyCommandName,
			key:     "test.command[0]",
			line:    4,
			column:  12,
			message: "names the program to run",
		},
		{
			name:    "timeout is not a duration",
			source:  "version = 1\n\n[test]\ntimeout = \"soon\"\n",
			code:    CodeInvalidDuration,
			key:     "test.timeout",
			line:    4,
			column:  11,
			message: "is not a duration",
		},
		{
			name:    "timeout is not positive",
			source:  "version = 1\n\n[test]\ntimeout = \"0s\"\n",
			code:    CodeNonPositiveTimeout,
			key:     "test.timeout",
			line:    4,
			column:  11,
			message: "cannot be waited for",
		},
		{
			name:    "memory is not a size",
			source:  "version = 1\n\n[test]\nmemory = \"plenty\"\n",
			code:    CodeInvalidSize,
			key:     "test.memory",
			line:    4,
			column:  10,
			message: "is not a size",
		},
		{
			name:    "memory is not positive",
			source:  "version = 1\n\n[test]\nmemory = \"0\"\n",
			code:    CodeNonPositiveMemory,
			key:     "test.memory",
			line:    4,
			column:  10,
			message: "leaves no room for a test binary",
		},
		{
			name:    "baseline runs below the range",
			source:  "version = 1\n\n[test]\nbaseline_runs = 0\n",
			code:    CodeBaselineRunsOutOfRange,
			key:     "test.baseline_runs",
			line:    4,
			column:  17,
			message: "outside 1..10",
		},
		{
			name:    "baseline runs above the range",
			source:  "version = 1\n\n[test]\nbaseline_runs = 11\n",
			code:    CodeBaselineRunsOutOfRange,
			key:     "test.baseline_runs",
			line:    4,
			column:  17,
			message: "outside 1..10",
		},
		{
			name:    "an unknown narrowing",
			source:  "version = 1\n\n[test]\nnarrowing = \"binary\"\n",
			code:    CodeUnknownNarrowing,
			key:     "test.narrowing",
			line:    4,
			column:  13,
			message: `unknown narrowing "binary": expected "test", "package"`,
		},
		{
			name:    "jobs below the range",
			source:  "version = 1\n\n[execution]\njobs = 0\n",
			code:    CodeJobsOutOfRange,
			key:     "execution.jobs",
			line:    4,
			column:  8,
			message: "outside 1..32",
		},
		{
			name:    "jobs above the range",
			source:  "version = 1\n\n[execution]\njobs = 33\n",
			code:    CodeJobsOutOfRange,
			key:     "execution.jobs",
			line:    4,
			column:  8,
			message: "outside 1..32",
		},
		{
			name:    "unknown cache mode",
			source:  "version = 1\n\n[cache]\nmode = \"sometimes\"\n",
			code:    CodeUnknownCacheMode,
			key:     "cache.mode",
			line:    4,
			column:  8,
			message: "unknown cache mode",
		},
		{
			name:    "absolute cache directory",
			source:  "version = 1\n\n[cache]\ndirectory = \"/var/cache\"\n",
			code:    CodeInvalidCacheDirectory,
			key:     "cache.directory",
			line:    4,
			column:  13,
			message: "not usable as a cache directory",
		},
		{
			name:    "escaping cache directory",
			source:  "version = 1\n\n[cache]\ndirectory = \"../elsewhere\"\n",
			code:    CodeInvalidCacheDirectory,
			key:     "cache.directory",
			line:    4,
			column:  13,
			message: "not usable as a cache directory",
		},
		{
			name:    "cleaned windows volume cache directory",
			source:  "version=1\n[CAChe]\ndireCtorY=\"./A:\"",
			code:    CodeInvalidCacheDirectory,
			key:     "cache.directory",
			line:    3,
			column:  11,
			message: "not usable as a cache directory",
		},
		{
			name:    "minimum score above the range",
			source:  "version = 1\n\n[policy]\nminimum_score = 101\n",
			code:    CodeMinimumScoreOutOfRange,
			key:     "policy.minimum_score",
			line:    4,
			column:  17,
			message: "outside 0..100",
		},
		{
			name:    "minimum score below the range",
			source:  "version = 1\n\n[policy]\nminimum_score = -0.5\n",
			code:    CodeMinimumScoreOutOfRange,
			key:     "policy.minimum_score",
			line:    4,
			column:  17,
			message: "outside 0..100",
		},
		{
			name:    "escaping report directory",
			source:  "version = 1\n\n[report]\ndirectory = \"../reports\"\n",
			code:    CodeInvalidReportDirectory,
			key:     "report.directory",
			line:    4,
			column:  13,
			message: "not usable as a report directory",
		},
		{
			name:    "unknown report format",
			source:  "version = 1\n\n[report]\nformats = [\"json\", \"pdf\"]\n",
			code:    CodeUnknownReportFormat,
			key:     "report.formats[1]",
			line:    4,
			column:  20,
			message: "unknown report format",
		},
		{
			name:    "duplicate report format",
			source:  "version = 1\n\n[report]\nformats = [\"json\", \"json\"]\n",
			code:    CodeDuplicateReportFormat,
			key:     "report.formats[1]",
			line:    4,
			column:  20,
			message: "already listed",
		},
		{
			name:    "high threshold out of range",
			source:  "version = 1\n\n[report]\nhigh = 120\n",
			code:    CodeThresholdOutOfRange,
			key:     "report.high",
			line:    4,
			column:  8,
			message: "outside 0..100",
		},
		{
			name:    "low threshold out of range",
			source:  "version = 1\n\n[report]\nlow = -1\n",
			code:    CodeThresholdOutOfRange,
			key:     "report.low",
			line:    4,
			column:  7,
			message: "outside 0..100",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(".go-mutants.toml", []byte(test.source))
			if err == nil {
				t.Fatalf("Parse accepted the document")
			}
			got := only(t, err)
			if got.Code != test.code {
				t.Errorf("code = %s, want %s (%v)", got.Code, test.code, err)
			}
			if got.Key != test.key {
				t.Errorf("key = %q, want %q", got.Key, test.key)
			}
			if got.Position.Line != test.line || got.Position.Column != test.column {
				t.Errorf("position = %d:%d, want %d:%d", got.Position.Line, got.Position.Column, test.line, test.column)
			}
			if got.File != ".go-mutants.toml" {
				t.Errorf("file = %q", got.File)
			}
			switch {
			case test.exact && got.Message != test.message:
				t.Errorf("message = %q, want %q", got.Message, test.message)
			case !test.exact && !strings.Contains(got.Message, test.message):
				t.Errorf("message %q does not contain %q", got.Message, test.message)
			}
			assertNoImplementationDetail(t, got.Message)
		})
	}
}

func assertNoImplementationDetail(t *testing.T, message string) {
	t.Helper()
	for _, leak := range []string{"struct field", " of type ", "cannot decode TOML", "cannot store "} {
		if strings.Contains(message, leak) {
			t.Errorf("message %q repeats the decoder's own wording (%q)", message, leak)
		}
	}
}

func TestParseDescribesTypeMismatchesFromTheSchema(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		key     string
		message string
	}{
		{"version is not a number", "version = \"1\"\n", "version", "must be an integer, not a string"},
		{"a table where a scalar belongs", "[version]\nx = 1\n", "version", "must be an integer, not a table"},
		{
			"a repeated table where a table belongs",
			"version = 1\n[[report]]\nhigh = 80\n", "report", "must be a table, not an array of tables",
		},
		{
			"an inline table where a scalar belongs",
			"version = 1\n[report]\nhigh = { a = 1 }\n", "report.high", "must be an integer, not an inline table",
		},
		{"a float where an integer belongs", "version = 1\n[report]\nhigh = 1.5\n", "report.high", "must be an integer, not a float"},
		{
			"a boolean where a number belongs",
			"version = 1\n[policy]\nminimum_score = true\n", "policy.minimum_score", "must be a number, not a boolean",
		},
		{
			"a scalar where the ledger belongs",
			"version = 1\n[mutation]\nexpect = 5\n", "mutation.expect", "must be a list of tables, not an integer",
		},
		{
			"a number where a duration belongs",
			"version = 1\n[test]\ntimeout = 5\n", "test.timeout", "must be a string, not an integer",
		},
		{
			"a number where a size belongs",
			"version = 1\n[test]\nmemory = 5\n", "test.memory", "must be a string, not an integer",
		},
		{"a string inside the ledger", "version = 1\n[[mutation.expect]]\nid = 5\n", "mutation.expect.id", "must be a string, not an integer"},
		{"a dotted key through a scalar", "version = 1\n[report]\nhigh.y = 2\n", "report.high.y", "a table cannot be written here"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(FileName, []byte(test.source))
			if err == nil {
				t.Fatalf("Parse accepted the document")
			}
			got := only(t, err)
			if got.Code != CodeInvalidTOML {
				t.Fatalf("code = %s, want %s (%v)", got.Code, CodeInvalidTOML, err)
			}
			if got.Key != test.key {
				t.Errorf("key = %q, want %q", got.Key, test.key)
			}
			if got.Message != test.message {
				t.Errorf("message = %q, want %q", got.Message, test.message)
			}
			assertNoImplementationDetail(t, got.Message)
			if !got.Position.Known() {
				t.Errorf("the mismatch was reported without a position")
			}
			var decode *toml.DecodeError
			if !errors.As(err, &decode) {
				t.Errorf("errors.As did not reach the *toml.DecodeError")
			}
		})
	}
}

func TestParseKeepsTOMLLevelMessages(t *testing.T) {
	for _, test := range []struct{ name, source, message string }{
		{"unterminated header", "version = 1\n[mutation\n", "expected ']' to close table name"},
		{"bad escape", "version = 1\nhigh = \"\\q\"\n", "invalid escape character U+0071 'q'"},
		{"key defined twice", "version = 1\nversion = 2\n", "key version is already defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(FileName, []byte(test.source))
			got := only(t, err)
			if got.Code != CodeInvalidTOML {
				t.Fatalf("code = %s, want %s (%v)", got.Code, CodeInvalidTOML, err)
			}
			if got.Message != test.message {
				t.Errorf("message = %q, want %q", got.Message, test.message)
			}
		})
	}
}

func TestDecodeMessageDegradesRatherThanLeakingAGoType(t *testing.T) {
	for _, test := range []struct{ name, key, message, want string }{
		{
			name:    "a known key and a kind this package names",
			key:     "report.high",
			message: "cannot decode TOML string into struct field config.documentReport.High of type int64",
			want:    "must be an integer, not a string",
		},
		{
			name:    "a known key and a kind this package does not name",
			key:     "report.high",
			message: "cannot store a rune array in report.high",
			want:    "must be an integer",
		},
		{
			name:    "an unknown key and a kind this package names",
			key:     "report.high.y",
			message: "cannot store a table in report.high",
			want:    "a table cannot be written here",
		},
		{
			name:    "an unknown key and a kind this package does not name",
			key:     "report.high.y",
			message: "cannot store a rune array in report.high",
			want:    "the value written here does not fit the key it was written under",
		},
		{
			name:    "a complaint about the document itself",
			key:     "",
			message: "key version is already defined",
			want:    "key version is already defined",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := decodeMessage(test.key, test.message)
			if got != test.want {
				t.Errorf("decodeMessage(%q, %q) = %q, want %q", test.key, test.message, got, test.want)
			}
			assertNoImplementationDetail(t, got)
		})
	}
}

func TestDecodeErrorKeepsAFailureItDoesNotRecognise(t *testing.T) {
	cause := errors.New("the reader gave up")
	err := decodeError(FileName, cause)

	got := only(t, err)
	if got.Code != CodeInvalidTOML {
		t.Errorf("code = %s, want %s", got.Code, CodeInvalidTOML)
	}
	if got.File != FileName {
		t.Errorf("File = %q, want %q", got.File, FileName)
	}
	if got.Position.Known() {
		t.Errorf("an unrecognised failure claimed a position: %s", got.Position)
	}
	if want := "the configuration file is not valid TOML: the reader gave up"; got.Message != want {
		t.Errorf("message = %q, want %q", got.Message, want)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is did not reach the cause")
	}
}

func TestSchemaKeysAreTheWholeSchemaInAFixedOrder(t *testing.T) {
	keys := SchemaKeys()
	if len(keys) != len(expectedTypes) {
		t.Fatalf("SchemaKeys() returned %d keys, want the %d expectedTypes describes", len(keys), len(expectedTypes))
	}
	if !slices.IsSorted(keys) {
		t.Errorf("SchemaKeys() is not sorted, so two runs can print it differently: %v", keys)
	}
	for _, key := range keys {
		if _, ok := expectedTypes[key]; !ok {
			t.Errorf("SchemaKeys() named %q, which the schema does not define", key)
		}
	}
	if !slices.Contains(keys, "mutation") || !slices.Contains(keys, "mutation.include") {
		t.Errorf("SchemaKeys() is missing a section or a setting: %v", keys)
	}
}

func TestExpectedTypesCoversTheSchema(t *testing.T) {
	schema := make(map[string]bool)
	collectSchemaKeys(t, reflect.TypeOf(document{}), "", schema)
	if len(schema) == 0 {
		t.Fatalf("the schema walk found no keys at all")
	}
	for key := range schema {
		if _, ok := expectedTypes[key]; !ok {
			t.Errorf("the schema defines %s, which expectedTypes does not describe", key)
		}
	}
	for key := range expectedTypes {
		if !schema[key] {
			t.Errorf("expectedTypes describes %s, which the schema does not define", key)
		}
	}
}

func collectSchemaKeys(t *testing.T, typ reflect.Type, prefix string, out map[string]bool) {
	t.Helper()
	for i := range typ.NumField() {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("toml"), ",")
		if name == "" {
			t.Errorf("%s.%s has no toml tag", typ.Name(), field.Name)
			continue
		}
		key := joinKey(prefix, name)
		out[key] = true

		switch phrase := schemaPhrase(field.Type); {
		case phrase == "":
			t.Errorf("%s is a %s, a shape schemaPhrase has no words for", key, field.Type)
		case expectedTypes[key] != phrase:
			t.Errorf("expectedTypes describes %s as %q, but it holds a %s, which is %q",
				key, expectedTypes[key], field.Type, phrase)
		}

		if inner := schemaElement(field.Type); inner.Kind() == reflect.Struct {
			collectSchemaKeys(t, inner, key, out)
		}
	}
}

func schemaPhrase(typ reflect.Type) string {
	element := schemaElement(typ)
	if schemaRepeats(typ) {
		switch element.Kind() {
		case reflect.Struct:
			return "a list of tables"
		case reflect.String:
			return "a list of strings"
		default:
			return ""
		}
	}
	switch element.Kind() {
	case reflect.Struct:
		return "a table"
	case reflect.String:
		return "a string"
	case reflect.Int64:
		return "an integer"
	case reflect.Float64:
		return "a number"
	case reflect.Bool:
		return "a boolean"
	default:
		return ""
	}
}

func schemaRepeats(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Kind() == reflect.Slice
}

func schemaElement(typ reflect.Type) reflect.Type {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	return typ
}

func TestParseReportsTheVersionBeforeUnknownKeys(t *testing.T) {
	source := "version = 2\n\n[mutation]\nprofile = \"balanced\"\n\n[telemetry]\nendpoint = \"https://example.invalid\"\n"
	_, err := Parse(".go-mutants.toml", []byte(source))
	got := only(t, err)
	if got.Code != CodeUnsupportedVersion {
		t.Errorf("code = %s, want %s (%v)", got.Code, CodeUnsupportedVersion, err)
	}
	if got.Position.Line != 1 || got.Position.Column != 11 {
		t.Errorf("position = %v, want 1:11", got.Position)
	}
}

func TestParseRejectsANonNumericVersion(t *testing.T) {
	_, err := Parse(".go-mutants.toml", []byte("version = \"1\"\n"))
	got := only(t, err)
	if got.Code != CodeInvalidTOML {
		t.Errorf("code = %s, want %s", got.Code, CodeInvalidTOML)
	}
	if got.Key != "version" {
		t.Errorf("key = %q, want version", got.Key)
	}
}

const (
	typoProfile = "prof" + "il"
	typoInclude = "inc" + "udes"
	typoHigh    = "hi" + "hg"
)

func TestParseReportsEveryUnknownKey(t *testing.T) {
	source := "version = 1\n\n[mutation]\n" + typoProfile + " = \"balanced\"\n" + typoInclude + " = []\n\n" +
		"[report]\n" + typoHigh + " = 80\n"
	_, err := Parse(".go-mutants.toml", []byte(source))
	got := problems(t, err)

	wantKeys := []string{"mutation." + typoProfile, "mutation." + typoInclude, "report." + typoHigh}
	gotKeys := make([]string, 0, len(got))
	for _, problem := range got {
		if problem.Code != CodeUnknownKey {
			t.Errorf("code = %s, want %s", problem.Code, CodeUnknownKey)
		}
		gotKeys = append(gotKeys, problem.Key)
	}
	if diff := cmp.Diff(wantKeys, gotKeys); diff != "" {
		t.Errorf("unknown keys (-want +got):\n%s", diff)
	}
	wantLines := []int{4, 5, 8}
	for i, problem := range got {
		if i < len(wantLines) && problem.Position.Line != wantLines[i] {
			t.Errorf("problem %d at line %d, want %d", i, problem.Position.Line, wantLines[i])
		}
	}
}

func TestParseReportsEveryBadValue(t *testing.T) {
	source := "version = 1\n\n[execution]\njobs = 99\n\n[report]\nhigh = 120\nformats = [\"pdf\"]\n"
	_, err := Parse(".go-mutants.toml", []byte(source))
	got := codesOf(problems(t, err))
	want := []Code{CodeJobsOutOfRange, CodeUnknownReportFormat, CodeThresholdOutOfRange}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("codes (-want +got):\n%s", diff)
	}
}

func TestParseAccepts(t *testing.T) {
	source := "version = 1\n\n" +
		"[mutation]\n" +
		"profile = \"all\"\n" +
		"include = [\"internal/**/*.go\"]\n" +
		"exclude = []\n" +
		"operators = [\"comparison\", \"eq-to-neq\"]\n\n" +
		"[[mutation.expect]]\n" +
		"id = \"" + hexID("a") + "\"\n" +
		"reason = \"equivalent\"\n\n" +
		"[test]\n" +
		"command = [\"go\", \"test\", \"-run\", \"\"]\n" +
		"timeout = \"1m30s\"\n" +
		"memory = \"2GiB\"\n" +
		"baseline_runs = 1\n" +
		"narrowing = \"package\"\n" +
		"probing = \"on\"\n\n" +
		"[execution]\njobs = 32\nisolate = true\n\n" +
		"[cache]\nmode = \"off\"\ndirectory = \"team/cache\"\n\n" +
		"[policy]\nstrict = true\nminimum_score = 66.5\nrequire_mutants = false\n\n" +
		"[report]\ndirectory = \"out/mutation\"\nformats = [\"html\"]\nhigh = 100\nlow = 0\n"

	file, err := Parse(".go-mutants.toml", []byte(source))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !file.Present {
		t.Errorf("Present = false for a document that was parsed")
	}

	want := Overlay{
		Version:         Explicit(1),
		Include:         Explicit([]string{"internal/**/*.go"}),
		Exclude:         Explicit([]string{}),
		Operators:       Explicit([]string{"comparison", "eq-to-neq"}),
		Profile:         Explicit(mutation.TierAll),
		Expect:          Explicit([]Expectation{{ID: hexID("a"), Reason: "equivalent"}}),
		TestCommand:     Explicit([]string{"go", "test", "-run", ""}),
		Timeout:         Explicit(90 * time.Second),
		Memory:          Explicit(int64(2) << 30),
		BaselineRuns:    Explicit(1),
		Narrowing:       Explicit(NarrowingPackage),
		Probing:         Explicit(ProbingOn),
		Jobs:            Explicit(32),
		Isolate:         Explicit(true),
		CacheMode:       Explicit(CacheOff),
		CacheDirectory:  Explicit("team/cache"),
		Strict:          Explicit(true),
		MinimumScore:    Explicit(66.5),
		RequireMutants:  Explicit(false),
		ReportDirectory: Explicit("out/mutation"),
		ReportFormats:   Explicit([]ReportFormat{FormatHTML}),
		ReportHigh:      Explicit(100),
		ReportLow:       Explicit(0),
	}
	if diff := cmp.Diff(want, file.Overlay); diff != "" {
		t.Errorf("overlay (-want +got):\n%s", diff)
	}
}

func TestParseEmptyDocument(t *testing.T) {
	file, err := Parse(".go-mutants.toml", []byte("version = 1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	overlay := file.Overlay
	overlay.Version = Set[int]{}
	if !overlay.IsEmpty() {
		t.Errorf("a version-only document set something: %+v", file.Overlay)
	}
	if diff := cmp.Diff(Defaults(), Merge(Defaults(), file, Overlay{})); diff != "" {
		t.Errorf("merging a version-only document changed the defaults (-want +got):\n%s", diff)
	}
}

func TestDirectoriesAreCanonicalisedOnce(t *testing.T) {
	source := "version = 1\n\n[report]\ndirectory = \"./out\\\\mutation\"\n\n[cache]\ndirectory = \"team//cache\"\n"
	file, err := Parse(".go-mutants.toml", []byte(source))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := file.Overlay.ReportDirectory.Or(""); got != `./out\mutation` {
		t.Errorf("the overlay changed the author's text: %q", got)
	}

	fromFile := Merge(Defaults(), file, Overlay{})
	if got := fromFile.Report.Directory; got != "out/mutation" {
		t.Errorf("report.directory from the file = %q, want %q", got, "out/mutation")
	}
	if got := fromFile.Cache.Directory; got != "team/cache" {
		t.Errorf("cache.directory from the file = %q, want %q", got, "team/cache")
	}

	fromFlags := Merge(Defaults(), FileConfig{}, Overlay{
		ReportDirectory: Explicit(`./out\mutation`),
		CacheDirectory:  Explicit("team//cache"),
	})
	if fromFlags.Report.Directory != fromFile.Report.Directory {
		t.Errorf("report.directory = %q from a flag but %q from the file",
			fromFlags.Report.Directory, fromFile.Report.Directory)
	}
	if fromFlags.Cache.Directory != fromFile.Cache.Directory {
		t.Errorf("cache.directory = %q from a flag but %q from the file",
			fromFlags.Cache.Directory, fromFile.Cache.Directory)
	}
}

func TestUncanonicalisableDirectoriesReachTheValidatorVerbatim(t *testing.T) {
	const (
		absolute = "/absolute/out"
		escaping = "../escape"
	)
	resolved := MergeOverlays(Defaults(), Overlay{
		ReportDirectory: Explicit(absolute),
		CacheDirectory:  Explicit(escaping),
	})
	if got := resolved.Report.Directory; got != absolute {
		t.Errorf("report.directory = %q, want the author's own %q", got, absolute)
	}
	if got := resolved.Cache.Directory; got != escaping {
		t.Errorf("cache.directory = %q, want the author's own %q", got, escaping)
	}

	got := problems(t, resolved.Validate())
	if len(got) != 2 {
		t.Fatalf("want 2 problems, got %d: %v", len(got), got)
	}
	for _, problem := range got {
		written, named := map[Code]string{
			CodeInvalidReportDirectory: absolute,
			CodeInvalidCacheDirectory:  escaping,
		}[problem.Code]
		if !named {
			t.Errorf("unexpected problem: %s", problem)
			continue
		}
		if !strings.Contains(problem.Message, strconv.Quote(written)) {
			t.Errorf("%s does not quote %q", problem, written)
		}
	}
}

func TestLoadFileAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile of an absent file: %v", err)
	}
	if file.Present {
		t.Errorf("Present = true for an absent file")
	}
	if !file.Overlay.IsEmpty() {
		t.Errorf("an absent file contributed %+v", file.Overlay)
	}
	if file.Path != path {
		t.Errorf("Path = %q, want %q", file.Path, path)
	}
	if diff := cmp.Diff(Defaults(), Merge(Defaults(), file, Overlay{})); diff != "" {
		t.Errorf("an absent file changed the defaults (-want +got):\n%s", diff)
	}
}

func TestLoadFileUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := LoadFile(path)
	if err == nil {
		t.Fatalf("LoadFile accepted a directory")
	}
	got := only(t, err)
	if got.Code != CodeUnreadable {
		t.Errorf("code = %s, want %s (%v)", got.Code, CodeUnreadable, err)
	}
	if got.File != path {
		t.Errorf("File = %q, want %q", got.File, path)
	}
	if strings.Contains(got.Message, path) {
		t.Errorf("the message repeats the path the error already carries: %q", got.Message)
	}
	if !strings.HasPrefix(got.Message, "the configuration file could not be read: ") {
		t.Errorf("message = %q", got.Message)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("errors.As did not reach the *fs.PathError: %v", err)
	}
	if want := "the configuration file could not be read: " + pathErr.Err.Error(); got.Message != want {
		t.Errorf("message = %q, want %q", got.Message, want)
	}
}

func TestIOMessageStripsThePathWrapperAndNothingElse(t *testing.T) {
	inner := errors.New("is a directory")
	wrapped := &fs.PathError{Op: "read", Path: filepath.Join("project", FileName), Err: inner}
	if got := ioMessage(wrapped); got != "is a directory" {
		t.Errorf("ioMessage of a *fs.PathError = %q, want %q", got, "is a directory")
	}
	if got := ioMessage(fmt.Errorf("reading configuration: %w", wrapped)); got != "is a directory" {
		t.Errorf("ioMessage of a wrapped *fs.PathError = %q, want %q", got, "is a directory")
	}
	plain := errors.New("the device is not ready")
	if got := ioMessage(plain); got != "the device is not ready" {
		t.Errorf("ioMessage of a plain error = %q, want %q", got, "the device is not ready")
	}
}

func TestLoadFileReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("version = 1\n\n[execution]\njobs = 4\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := file.Overlay.Jobs.Or(0); got != 4 {
		t.Errorf("jobs = %d, want 4", got)
	}
	position, ok := file.Position("execution.jobs")
	if !ok || position.Line != 4 || position.Column != 8 {
		t.Errorf("Position(execution.jobs) = %v, %v; want 4:8, true", position, ok)
	}
	if _, ok := file.Position("report.high"); ok {
		t.Errorf("Position reported a key the file does not have")
	}
	if diff := cmp.Diff([]string{"execution.jobs", "version"}, file.Keys()); diff != "" {
		t.Errorf("Keys() (-want +got):\n%s", diff)
	}

	absent, err := LoadFile(filepath.Join(dir, "nothing.toml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := absent.Keys(); len(got) != 0 {
		t.Errorf("Keys() on an absent file = %v", got)
	}
	if _, ok := absent.Position("version"); ok {
		t.Errorf("an absent file reported a position")
	}
}

func TestErrorsWrapTheirCause(t *testing.T) {
	_, err := Parse(".go-mutants.toml", []byte("version = 1\n\n[mutation]\ninclude = [\"a//b\"]\n"))
	var syntax *glob.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("errors.As did not reach the glob syntax error: %v", err)
	}
	if syntax.Pattern != "a//b" {
		t.Errorf("wrapped pattern = %q", syntax.Pattern)
	}

	_, err = Parse(".go-mutants.toml", []byte("version = 1\n\n[cache]\ndirectory = \"/abs\"\n"))
	if !errors.Is(err, mutation.ErrAbsolutePath) {
		t.Errorf("errors.Is did not reach ErrAbsolutePath: %v", err)
	}
}
