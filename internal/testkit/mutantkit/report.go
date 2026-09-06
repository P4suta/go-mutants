// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// The values every varying field of a run report is replaced with.
//
// They are constants rather than zero values so that a normalised document says
// what happened to it: a reader looking at `"tool_version": "0.0.0-normalized"`
// knows the field was replaced, where `""` would look like a bug in the builder.
const (
	// NormalizedToolVersion stands in for the version stamped at link time,
	// which is a fact about how the binary was built.
	NormalizedToolVersion = "0.0.0-normalized"
	// NormalizedRunID stands in for the run identity, which carries the wall
	// clock and four random hex digits. It keeps the schema's shape, because a
	// normalised report still has to validate.
	NormalizedRunID = "00000000T000000Z-0000"
	// NormalizedTimestamp stands in for started_at and finished_at, in the
	// RFC 3339 UTC form the schema's pattern requires.
	NormalizedTimestamp = "2000-01-01T00:00:00Z"
	// NormalizedGoVersion stands in for the `go` directive of the module under
	// test, which is a fact about the fixture's go.mod as the toolchain of the
	// day reads it.
	NormalizedGoVersion = "0.0"
	// NormalizedOS and NormalizedArch stand in for the host a run happened on.
	//
	// They are what makes one committed report golden usable on all three
	// platforms CI runs: the document records GOOS and GOARCH, so a golden that
	// kept them would be a golden of the machine that generated it and would fail
	// on the other two the moment anybody looked. Real values rather than a
	// marker, because the schema wants a non-empty string and because a reader of
	// the normalised document should see a plausible platform rather than wonder
	// whether the field is broken.
	NormalizedOS   = "linux"
	NormalizedArch = "amd64"
	// NormalizedPath stands in for every absolute path: a snapshot root, a
	// located toolchain, a report directory. All three are different on every
	// machine and in every run.
	NormalizedPath = "/normalized/path"
	// NormalizedDurationMS stands in for every measured duration. Zero, because
	// the schema's milliseconds type has a minimum of zero and because "no time
	// passed" is unmistakably not a measurement.
	NormalizedDurationMS = 0
)

// normalizedDuration is [NormalizedDurationMS] as a document carries it, built
// once so that the constant and the value written can never disagree.
var normalizedDuration = json.Number(strconv.Itoa(NormalizedDurationMS))

// MustMarshal marshals a report and checks it against the published schema.
//
// The validation is inside the helper rather than in a test of its own so that
// it is impossible to forget: every document any suite produces goes through
// here, and therefore through the same validator a consumer would use. A test
// that needs an invalid document builds one by editing the bytes this returns.
func MustMarshal(t testing.TB, r *report.Report) []byte {
	t.Helper()
	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("marshalling the report: %v", err)
		return nil
	}
	if err := schemas.Validate(schemas.RunReportV1, data); err != nil {
		t.Fatalf("the report does not satisfy its own schema: %v\n%s", err, data)
		return nil
	}
	return data
}

// DecodeJSON reads a document into a tree a test can edit, keeping every number
// exactly as it was written.
//
// The exactness is the point. encoding/json decodes numbers into float64 unless
// it is told otherwise, so a document read and written back turns 88 into 88 by
// luck and 66.66666666666666 into something shorter by arithmetic. Half the
// tests that use this decode a valid document, edit one field, and re-encode it
// to prove the validator rejects it — and a decoder that quietly rewrote three
// other fields on the way would make those tests about something else.
func DecodeJSON(t testing.TB, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		t.Fatalf("decoding the document: %v\n%s", err, data)
		return nil
	}
	return doc
}

// EncodeJSON writes an edited tree back out.
//
// Map keys are sorted by encoding/json, so the bytes do not depend on iteration
// order — which is what makes a document normalised through this comparable with
// another one.
func EncodeJSON(t testing.TB, doc map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding the document: %v", err)
		return nil
	}
	return data
}

// NormalizeRunReport replaces everything in a run report that is a fact about
// the run rather than about the code.
//
// A run report is mostly the second kind — which mutants there are, what
// happened to each of them, what the policy decided — and that is the part a
// golden can pin. The first kind is the tool's version, the module's go
// directive, the host's GOOS and GOARCH, the wall clock at both ends, every
// measured duration, and every absolute path: the located toolchain, the
// snapshot root, the report directory. A golden that carried them would fail on
// the next machine, on the next toolchain, on the other two platforms CI runs,
// and on the second run of the same day.
//
// Two things are deliberately *not* normalised. Mutant ids and workspace digests
// are content-addressed: they are derived from the bytes of the program under
// test, so they are the same on every machine and they are exactly what proves
// two runs measured the same thing. Replacing them would leave a golden that
// passes for a run of a different program.
func NormalizeRunReport(t testing.TB, data []byte) []byte {
	t.Helper()
	doc := DecodeJSON(t, data)

	setString(doc, NormalizedToolVersion, "tool_version")
	setString(doc, NormalizedRunID, "run_id")
	setString(doc, NormalizedTimestamp, "started_at")
	setString(doc, NormalizedTimestamp, "finished_at")
	setNumber(doc, "duration_ms")
	setString(doc, NormalizedGoVersion, "workspace", "go_version")
	setString(doc, NormalizedOS, "workspace", "platform", "os")
	setString(doc, NormalizedArch, "workspace", "platform", "arch")
	setNumber(doc, "test", "timeout_ms")
	setNumber(doc, "test", "baseline", "slowest_ms")
	setNumberSlice(doc, "test", "baseline", "durations_ms")
	for _, mutant := range array(doc, "mutants") {
		setNumber(mutant, "duration_ms")
	}

	// The paths are replaced last and by a walk rather than by name, because
	// they turn up in fields nothing can enumerate: a command's argv, a
	// warning's message, the tail of a failing test's output.
	replacePaths(doc)
	return EncodeJSON(t, doc)
}

// absolutePath matches a POSIX or Windows absolute path inside a string value.
//
// The leading boundary is what keeps it from matching the paths that are *not*
// absolute and must survive: `internal/alpha/alpha.go` is how every mutant in a
// report names its file, and `./...` is how a test command names its packages.
// Both have a slash with an ordinary character in front of it; an absolute path
// has one at the start of the string or after a space, a quote, an equals sign
// or an opening bracket.
var absolutePath = regexp.MustCompile(`(^|[\s"'=(\[])((?:[A-Za-z]:)?[\\/][^\s"'\[\]()]+)`)

// replacePaths rewrites every absolute path in every string of a decoded
// document, in place.
func replacePaths(value any) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if text, ok := child.(string); ok {
				node[key] = normalizePathsIn(text)
				continue
			}
			replacePaths(child)
		}
	case []any:
		for i, child := range node {
			if text, ok := child.(string); ok {
				node[i] = normalizePathsIn(text)
				continue
			}
			replacePaths(child)
		}
	}
}

// normalizePathsIn replaces the absolute paths in one string, keeping the
// character that preceded each one.
func normalizePathsIn(text string) string {
	return absolutePath.ReplaceAllString(text, "${1}"+NormalizedPath)
}

// setString replaces a string at a path of keys, when it is there.
//
// A missing key is not an error: the shape of a run report depends on the run —
// `coverage.binaries` is absent from an off-mode document, `merge` is absent
// unless shards were merged — and a normaliser that insisted on every field
// would refuse the very documents it exists to compare.
func setString(doc map[string]any, value string, keys ...string) {
	if node, key, ok := parentOf(doc, keys); ok {
		if _, present := node[key]; present {
			node[key] = value
		}
	}
}

// setNumber replaces a measured duration with [NormalizedDurationMS].
func setNumber(doc map[string]any, keys ...string) {
	if node, key, ok := parentOf(doc, keys); ok {
		if _, present := node[key]; present {
			node[key] = normalizedDuration
		}
	}
}

// setNumberSlice replaces every element of an array of measured durations.
func setNumberSlice(doc map[string]any, keys ...string) {
	node, key, ok := parentOf(doc, keys)
	if !ok {
		return
	}
	values, ok := node[key].([]any)
	if !ok {
		return
	}
	for i := range values {
		values[i] = normalizedDuration
	}
}

// array returns a named array of objects, or nothing when the document has none.
func array(doc map[string]any, key string) []map[string]any {
	values, ok := doc[key].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

// parentOf walks to the object holding the last key.
func parentOf(doc map[string]any, keys []string) (map[string]any, string, bool) {
	if len(keys) == 0 {
		return nil, "", false
	}
	node := doc
	for _, key := range keys[:len(keys)-1] {
		child, ok := node[key].(map[string]any)
		if !ok {
			return nil, "", false
		}
		node = child
	}
	return node, keys[len(keys)-1], true
}
