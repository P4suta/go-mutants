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

const (
	NormalizedToolVersion      = "0.0.0-normalized"
	NormalizedRunID            = "00000000T000000Z-0000"
	NormalizedTimestamp        = "2000-01-01T00:00:00Z"
	NormalizedGoVersion        = "0.0"
	NormalizedToolchainVersion = "go version go0.0.0 normalized/normalized"
	NormalizedOS               = "linux"
	NormalizedArch             = "amd64"
	NormalizedPath             = "/normalized/path"
	NormalizedDurationMS       = 0
	NormalizedElapsed          = "(0.00s)"
	NormalizedWorker           = 0
)

var (
	normalizedDuration         = json.Number(strconv.Itoa(NormalizedDurationMS))
	normalizedWorker           = json.Number(strconv.Itoa(NormalizedWorker))
	normalizedPeak             = json.Number("0")
	normalizedMemorySource any = "derived"
	normalizedBound            = json.Number("1")
)

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

func EncodeJSON(t testing.TB, doc map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding the document: %v", err)
		return nil
	}
	return data
}

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
	forceValue(doc, normalizedBound, "test", "memory_bytes")
	setValue(doc, normalizedMemorySource, "test", "memory_source")
	setNumber(doc, "test", "baseline", "slowest_ms")
	setNumberSlice(doc, "test", "baseline", "durations_ms")
	setString(doc, NormalizedToolchainVersion, "test", "toolchain", "version")
	for _, mutant := range array(doc, "mutants") {
		setNumber(mutant, "duration_ms")
		forceValue(mutant, normalizedPeak, "peak_memory_bytes")
		for _, execution := range array(mutant, "executions") {
			setNumber(execution, "duration_ms")
			setValue(execution, normalizedWorker, "worker")
			forceValue(execution, normalizedPeak, "peak_memory_bytes")
		}
	}
	if timing, ok := doc["timing"].(map[string]any); ok {
		for _, phase := range array(timing, "phases") {
			setNumber(phase, "duration_ms")
		}
		for _, stage := range array(timing, "stages") {
			setNumber(stage, "duration_ms")
		}
	}

	setPath(doc, "test", "toolchain", "go_bin")
	setPathSlice(doc, "test", "command")
	setPathSlice(doc, "test", "resolved_command")

	rewriteText(doc)
	return EncodeJSON(t, doc)
}

var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func looksAbsolute(value string) bool {
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) || windowsPath.MatchString(value)
}

func setPath(doc map[string]any, keys ...string) {
	node, key, ok := parentOf(doc, keys)
	if !ok {
		return
	}
	if value, isString := node[key].(string); isString && looksAbsolute(value) {
		node[key] = NormalizedPath
	}
}

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

var goTestElapsed = regexp.MustCompile(`(?m)^([ \t]*--- (?:PASS|FAIL|SKIP|BENCH): .*?) \([0-9]+\.[0-9]+s\)$`)

var goTestPackageElapsed = regexp.MustCompile(`(?m)^((?:ok|FAIL)[ \t]+\S+[ \t]+)[0-9]+\.[0-9]+s$`)

var absolutePath = regexp.MustCompile(`(^|[\s"'=(\[])((?:[A-Za-z]:)?[\\/][^\s"'\[\]()]+)`)

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

func normalizeText(text string) string {
	text = absolutePath.ReplaceAllString(text, "${1}"+NormalizedPath)
	text = goTestElapsed.ReplaceAllString(text, "${1} "+NormalizedElapsed)
	return goTestPackageElapsed.ReplaceAllString(text, "${1}"+strings.Trim(NormalizedElapsed, "()"))
}

func setString(doc map[string]any, value string, keys ...string) {
	if node, key, ok := parentOf(doc, keys); ok {
		if _, present := node[key]; present {
			node[key] = value
		}
	}
}

func setNumber(doc map[string]any, keys ...string) {
	setValue(doc, normalizedDuration, keys...)
}

func setValue(doc map[string]any, value any, keys ...string) {
	if node, key, ok := parentOf(doc, keys); ok {
		if _, present := node[key]; present {
			node[key] = value
		}
	}
}

func forceValue(doc map[string]any, value any, keys ...string) {
	if node, key, ok := parentOf(doc, keys); ok {
		node[key] = value
	}
}

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
