// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
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
	// NormalizedToolchainVersion stands in for what `go version` printed, which
	// is a fact about the machine's toolchain and moves with every release. It
	// keeps the shape of the real line so that a reader of a normalised
	// document sees a plausible one rather than wondering what broke.
	NormalizedToolchainVersion = "go version go0.0.0 normalized/normalized"
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
	// NormalizedElapsed stands in for the elapsed time `go test` prints beside
	// a test's name — `--- FAIL: TestClamp (0.01s)` — wherever it appears
	// inside the free text a report carries. It is a real marker rather than a
	// placeholder because the text around it is the program's own output and
	// has to go on reading like it.
	NormalizedElapsed = "(0.00s)"
	// NormalizedWorker stands in for the scheduler slot that executed one
	// attempt. Which worker claimed a mutant is decided by whichever goroutine
	// reached the queue first, so two runs of one workspace differ in it and
	// mean the same thing — the same reason internal/execute's own scheduling
	// test sets it aside before comparing two runs. Zero is a real worker
	// number, which is unavoidable: the field has no value that is not one.
	NormalizedWorker = 0
)

// normalizedDuration and normalizedWorker are [NormalizedDurationMS] and
// [NormalizedWorker] as a document carries them, built once so that the
// constants and the values written can never disagree.
var (
	normalizedDuration = json.Number(strconv.Itoa(NormalizedDurationMS))
	normalizedWorker   = json.Number(strconv.Itoa(NormalizedWorker))
	// normalizedPeak stands in for a measured peak resident size. It is
	// unexported because nothing outside this package has to name it: unlike a
	// duration, which several suites assert around, a peak is a number every
	// golden replaces and no test reasons about.
	normalizedPeak = json.Number("0")
	// normalizedMemorySource stands in for where a run's memory bound came
	// from. It is a valid source rather than a marker, because the normalised
	// document is validated against the published schema and the field is an
	// enum there.
	normalizedMemorySource any = "derived"
)

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
// directive, the toolchain's own version line, the host's GOOS and GOARCH, the
// wall clock at both ends, every measured duration — the run's, each mutant's,
// each of its attempts', and every phase and stage of the timeline — the
// scheduler slot each attempt ran in, the peak resident memory each attempt
// reached, every absolute path (the located
// toolchain, in `test.command` and in `test.resolved_command` and in
// `test.toolchain.go_bin`, the snapshot root, the report directory), and the
// elapsed times `go test` writes into the output a report carries. A golden
// that kept them would fail on the next machine, on the next toolchain, on the
// other two platforms CI runs, and on the second run of the same day — the last
// of them on nothing more than a busy runner, which is exactly how it was
// found.
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
	// The memory budget, which is a fact about the machine in both halves. The
	// number is derived from what the baseline cost, so it is the floor on a
	// small machine and four times a measurement on a large one; the source is
	// `derived` where a bound can be enforced and `unavailable` where it cannot,
	// which is the platform and nothing else. Both are replaced for the reason
	// `workspace.platform.os` is, and *whether* a bound was enforced is asserted
	// by tests that say so rather than by a golden that would have to be three
	// files.
	//
	// The number is forced rather than replaced, as the per-execution peak is:
	// its absence is itself the platform fact, so leaving a run that recorded no
	// bound saying nothing would make one platform's golden a different shape.
	forceValue(doc, normalizedPeak, "test", "memory_bytes")
	setValue(doc, normalizedMemorySource, "test", "memory_source")
	setNumber(doc, "test", "baseline", "slowest_ms")
	setNumberSlice(doc, "test", "baseline", "durations_ms")
	setString(doc, NormalizedToolchainVersion, "test", "toolchain", "version")
	for _, mutant := range array(doc, "mutants") {
		setNumber(mutant, "duration_ms")
		// One row per attempt: how long the pass took, and which of the
		// scheduler's slots made it. The worker is a fact about the run in the
		// strongest sense — it is which goroutine won the race to the queue, so
		// two runs of one workspace on one machine differ in it — and the
		// binaries beside it are left alone, because which binaries a pass
		// started is what the run *did* and is the same every time.
		for _, execution := range array(mutant, "executions") {
			setNumber(execution, "duration_ms")
			setValue(execution, normalizedWorker, "worker")
			// What the pass cost the machine, which is a fact about the machine
			// in the plainest sense there is: the same suite is a different
			// number of bytes under `-cover`, under `-race`, on a different
			// allocator and on a different page size.
			//
			// The key is *added* where it is missing rather than only replaced
			// where it is present, and that is the opposite of every other rule
			// here. Every platform go-mutants supports measures a peak for a
			// process that started, so an execution row without one is a
			// machine whose `ru_maxrss` came back zero — a container, a kernel
			// nobody tested — and normalising the key away would turn that into
			// a golden that quietly passes there and fails the day somebody
			// looks. Written in, it fails as a diff against the recorded
			// golden, which names the row.
			forceValue(execution, normalizedPeak, "peak_rss_bytes")
		}
	}
	// The timeline, which is every measured duration there is left. The phase
	// and stage *names* stay: which steps a run took is a fact about the
	// pipeline, and a golden that could not see a stage appear or disappear
	// would be pinning nothing worth pinning.
	if timing, ok := doc["timing"].(map[string]any); ok {
		for _, phase := range array(timing, "phases") {
			setNumber(phase, "duration_ms")
		}
		for _, stage := range array(timing, "stages") {
			setNumber(stage, "duration_ms")
		}
	}

	// The paths of the *programs* a run started, replaced by name and before the
	// walk below. They are the one kind of absolute path a walk cannot finish:
	// an installation directory may hold a space — `C:\Program Files\Go\bin\go.exe`
	// is where a Windows toolchain lives by default — and [absolutePath] ends a
	// path at whitespace, because a path found in the middle of a sentence
	// otherwise swallows the words after it. Replaced whole here, they are gone
	// before the walk can half-rewrite them.
	//
	// Only a value that already looks absolute is touched, which is what keeps
	// `go` and `./...` in `test.command` as the user wrote them: a bare program
	// name and a package pattern are not paths, and a report that turned either
	// into one would stop saying what was run.
	setPath(doc, "test", "toolchain", "go_bin")
	setPathSlice(doc, "test", "command")
	setPathSlice(doc, "test", "resolved_command")

	// The free text is rewritten last and by a walk rather than by name; see
	// [rewriteText] for what it does and why it is not a list of fields.
	rewriteText(doc)
	return EncodeJSON(t, doc)
}

// windowsPath matches a path that begins with a drive letter, which is the
// spelling [absolutePath] can start and cannot finish.
var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// looksAbsolute reports whether a string is a path rather than a program name,
// a package pattern or a word.
func looksAbsolute(value string) bool {
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) || windowsPath.MatchString(value)
}

// setPath replaces one string with [NormalizedPath] when it is a path.
func setPath(doc map[string]any, keys ...string) {
	node, key, ok := parentOf(doc, keys)
	if !ok {
		return
	}
	if value, isString := node[key].(string); isString && looksAbsolute(value) {
		node[key] = NormalizedPath
	}
}

// setPathSlice is [setPath] for every element of an argv.
func setPathSlice(doc map[string]any, keys ...string) {
	node, key, ok := parentOf(doc, keys)
	if !ok {
		return
	}
	values, ok := node[key].([]any)
	if !ok {
		return
	}
	for i, element := range values {
		if value, isString := element.(string); isString && looksAbsolute(value) {
			values[i] = NormalizedPath
		}
	}
}

// goTestElapsed matches the elapsed time `go test` prints beside a test's own
// name, on the line where it prints it, and nothing else.
//
// The line shape is the whole of the discrimination: `go test` writes
// `--- PASS: TestName (0.01s)`, `--- FAIL: …` and `--- SKIP: …` for every test
// and subtest, and `ok  \tpkg\t0.123s` or `FAIL\tpkg\t0.002s` for every package.
// A parenthesised duration anywhere else is the program's own output, where a
// number is evidence — `request completed (0.25s)` printed by the code under
// test must survive normalisation exactly as it was written, or a golden could
// accept an output tail that is wrong.
var goTestElapsed = regexp.MustCompile(`(?m)^([ \t]*--- (?:PASS|FAIL|SKIP|BENCH): .*?) \([0-9]+\.[0-9]+s\)$`)

// goTestPackageElapsed matches the elapsed time on `go test`'s per-package
// summary line, `ok  \tpkg\t0.123s` and `FAIL\tpkg\t0.002s`.
var goTestPackageElapsed = regexp.MustCompile(`(?m)^((?:ok|FAIL)[ \t]+\S+[ \t]+)[0-9]+\.[0-9]+s$`)

// absolutePath matches a POSIX or Windows absolute path inside a string value.
//
// The leading boundary is what keeps it from matching the paths that are *not*
// absolute and must survive: `internal/alpha/alpha.go` is how every mutant in a
// report names its file, and `./...` is how a test command names its packages.
// Both have a slash with an ordinary character in front of it; an absolute path
// has one at the start of the string or after a space, a quote, an equals sign
// or an opening bracket.
//
// It ends a path at whitespace, and that is a known limit rather than an
// oversight. This runs over free text — a warning, a command's argv, the tail
// of a failing test's output — where most of what follows a path is a sentence,
// and a rule that ran past a space would rewrite the sentence too. The cost is
// a path that legitimately holds one: `C:\Program Files\Go\bin\go.exe` comes
// out as `/normalized/path Files\Go\bin\go.exe`, which still names the machine
// it was recorded on. The fields that can carry such a path — an installation
// directory rather than something inside a temporary tree — are therefore
// replaced by name in [NormalizeRunReport] before this walk runs, and a new
// field holding a program's path belongs on that list.
// TestNormalizeRunReportReplacesAToolchainPathHoldingASpace is the test of it.
var absolutePath = regexp.MustCompile(`(^|[\s"'=(\[])((?:[A-Za-z]:)?[\\/][^\s"'\[\]()]+)`)

// rewriteText normalises every string of a decoded document, in place.
//
// It is a walk rather than a list of fields because both of the things it
// rewrites turn up in text nothing can enumerate: an absolute path appears in a
// command's argv, in a warning's message and in the tail of a failing test's
// output, and a `go test` elapsed marker appears wherever a test binary's own
// output is carried. Today that is `mutants[].output_tail` alone — the other
// free text in the document is compiler diagnostics (`rejected[].diagnostic`,
// `coverage.unavailable_reason`), engine-composed warnings, and the user's own
// expectation reasons, none of which is a test binary talking, and
// `executions[]` carries no free text at all — but a walk is what keeps the
// next field that does from being a green CI run away from a red one.
func rewriteText(value any) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if text, ok := child.(string); ok {
				node[key] = normalizeText(text)
				continue
			}
			rewriteText(child)
		}
	case []any:
		for i, child := range node {
			if text, ok := child.(string); ok {
				node[i] = normalizeText(text)
				continue
			}
			rewriteText(child)
		}
	}
}

// normalizeText replaces the absolute paths in one string, keeping the
// character that preceded each one, and flattens every `go test` elapsed time
// it holds.
func normalizeText(text string) string {
	text = absolutePath.ReplaceAllString(text, "${1}"+NormalizedPath)
	text = goTestElapsed.ReplaceAllString(text, "${1} "+NormalizedElapsed)
	return goTestPackageElapsed.ReplaceAllString(text, "${1}"+strings.Trim(NormalizedElapsed, "()"))
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
	setValue(doc, normalizedDuration, keys...)
}

// setValue replaces whatever is at a path of keys, when it is there. A missing
// key is not an error, for the reason [setString] gives.
func setValue(doc map[string]any, value any, keys ...string) {
	if node, key, ok := parentOf(doc, keys); ok {
		if _, present := node[key]; present {
			node[key] = value
		}
	}
}

// forceValue writes a value whether or not the key is already there.
//
// It is [setValue]'s counterpart for the one field whose *absence* is itself a
// fact about the machine: see the peak in [NormalizeRunReport]. Everywhere else
// a missing key means "this document does not carry that", and adding one would
// be inventing a fact; there it means "this machine could not measure what
// every supported machine measures", and hiding it is what would be invented.
func forceValue(doc map[string]any, value any, keys ...string) {
	if node, key, ok := parentOf(doc, keys); ok {
		node[key] = value
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
