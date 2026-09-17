// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

var varyingFields = map[string]string{
	"/tool_version":                             mutantkit.NormalizedToolVersion,
	"/run_id":                                   mutantkit.NormalizedRunID,
	"/started_at":                               mutantkit.NormalizedTimestamp,
	"/finished_at":                              mutantkit.NormalizedTimestamp,
	"/duration_ms":                              "0",
	"/workspace/go_version":                     mutantkit.NormalizedGoVersion,
	"/workspace/platform/os":                    mutantkit.NormalizedOS,
	"/workspace/platform/arch":                  mutantkit.NormalizedArch,
	"/test/timeout_ms":                          "0",
	"/test/baseline/slowest_ms":                 "0",
	"/test/baseline/durations_ms/0":             "0",
	"/test/baseline/durations_ms/1":             "0",
	"/test/baseline/durations_ms/2":             "0",
	"/mutants/0/duration_ms":                    "0",
	"/mutants/1/duration_ms":                    "0",
	"/mutants/2/duration_ms":                    "0",
	"/mutants/3/duration_ms":                    "0",
	"/mutants/4/duration_ms":                    "0",
	"/mutants/5/duration_ms":                    "0",
	"/mutants/6/duration_ms":                    "0",
	"/test/toolchain/go_bin":                    mutantkit.NormalizedPath,
	"/test/toolchain/version":                   mutantkit.NormalizedToolchainVersion,
	"/test/memory_bytes":                        "1",
	"/test/memory_source":                       "derived",
	"/test/resolved_command/0":                  mutantkit.NormalizedPath,
	"/mutants/0/peak_memory_bytes":              "0",
	"/mutants/1/peak_memory_bytes":              "0",
	"/mutants/2/peak_memory_bytes":              "0",
	"/mutants/3/peak_memory_bytes":              "0",
	"/mutants/4/peak_memory_bytes":              "0",
	"/mutants/5/peak_memory_bytes":              "0",
	"/mutants/6/peak_memory_bytes":              "0",
	"/mutants/7/peak_memory_bytes":              "0",
	"/mutants/1/executions/0/duration_ms":       "0",
	"/mutants/1/executions/0/worker":            "0",
	"/mutants/1/executions/0/peak_memory_bytes": "0",
	"/mutants/3/executions/0/duration_ms":       "0",
	"/mutants/3/executions/0/worker":            "0",
	"/mutants/3/executions/0/peak_memory_bytes": "0",
	"/mutants/3/executions/1/duration_ms":       "0",
	"/mutants/3/executions/1/worker":            "0",
	"/mutants/3/executions/1/peak_memory_bytes": "0",
	"/mutants/4/executions/0/duration_ms":       "0",
	"/mutants/4/executions/0/worker":            "0",
	"/mutants/4/executions/0/peak_memory_bytes": "0",
	"/mutants/5/executions/0/duration_ms":       "0",
	"/mutants/5/executions/0/worker":            "0",
	"/mutants/5/executions/0/peak_memory_bytes": "0",
	"/mutants/7/duration_ms":                    "0",
	"/mutants/7/executions/0/duration_ms":       "0",
	"/mutants/7/executions/0/worker":            "0",
	"/mutants/7/executions/0/peak_memory_bytes": "0",
	"/mutants/7/executions/1/duration_ms":       "0",
	"/mutants/7/executions/1/worker":            "0",
	"/mutants/7/executions/1/peak_memory_bytes": "0",
	"/timing/phases/0/duration_ms":              "0",
	"/timing/phases/1/duration_ms":              "0",
	"/timing/phases/2/duration_ms":              "0",
	"/timing/phases/3/duration_ms":              "0",
	"/timing/stages/0/duration_ms":              "0",
	"/timing/stages/1/duration_ms":              "0",
	"/timing/stages/2/duration_ms":              "0",
	"/timing/stages/3/duration_ms":              "0",
	"/timing/stages/4/duration_ms":              "0",
	"/timing/stages/5/duration_ms":              "0",
	"/timing/stages/6/duration_ms":              "0",
	"/timing/stages/7/duration_ms":              "0",
	"/timing/stages/8/duration_ms":              "0",
	"/mutants/0/output_tail":                    "--- FAIL: TestAdd " + mutantkit.NormalizedElapsed,
}

func TestNormalizeRunReportFixesOnlyTheVaryingFields(t *testing.T) {
	t.Parallel()

	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	normalized := mutantkit.NormalizeRunReport(t, original)

	for _, pointer := range changedPointers(t, original, normalized) {
		if _, listed := varyingFields[pointer]; !listed {
			t.Errorf("normalising changed %s, which is not one of the fields that vary between runs", pointer)
		}
	}
	after := mutantkit.DecodeJSON(t, normalized)
	for _, pointer := range slices.Sorted(maps.Keys(varyingFields)) {
		got, ok := valueAt(after, pointer)
		if !ok {
			t.Errorf("%s is not in the normalised report at all", pointer)
			continue
		}
		if want := varyingFields[pointer]; got != want {
			t.Errorf("%s = %s after normalising, want %s", pointer, got, want)
		}
	}
	if err := schemas.Validate(schemas.RunReportV1, normalized); err != nil {
		t.Errorf("the normalised report no longer satisfies its own schema: %v\n%s", err, normalized)
	}
}

func valueAt(doc map[string]any, pointer string) (string, bool) {
	var node any = doc
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		switch current := node.(type) {
		case map[string]any:
			child, ok := current[token]
			if !ok {
				return "", false
			}
			node = child
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index >= len(current) {
				return "", false
			}
			node = current[index]
		default:
			return "", false
		}
	}
	return fmt.Sprintf("%v", node), true
}

func TestNormalizeRunReportIsIdempotent(t *testing.T) {
	t.Parallel()

	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	for _, c := range []struct {
		name string
		edit func(doc map[string]any)
	}{
		{name: "the committed golden", edit: func(map[string]any) {}},
		{
			name: "a document holding what a real run puts in it",
			edit: func(doc map[string]any) {
				doc["test"].(map[string]any)["command"] = []any{"/home/somebody/sdk/go1.26.6/bin/go", "test", "./..."}
				mutant(doc, 0)["output_tail"] = "--- FAIL: TestClamp (0.01s)\n    clamp_test.go:14: want 3"
				mutant(doc, 2)["output_tail"] = "--- FAIL: TestSlow (12.34s)"
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			doc := mutantkit.DecodeJSON(t, original)
			c.edit(doc)
			once := mutantkit.NormalizeRunReport(t, mutantkit.EncodeJSON(t, doc))
			twice := mutantkit.NormalizeRunReport(t, once)

			if !bytes.Equal(once, twice) {
				t.Errorf("normalising twice is not normalising once:\n%s", string(twice))
			}
			if err := schemas.Validate(schemas.RunReportV1, once); err != nil {
				t.Errorf("the normalised document does not satisfy its own schema: %v", err)
			}
		})
	}
}

func TestNormalizeRunReportReplacesAToolchainPathAndATimestamp(t *testing.T) {
	t.Parallel()

	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	doc := mutantkit.DecodeJSON(t, original)
	doc["test"].(map[string]any)["command"] = []any{"/home/somebody/sdk/go1.26.6/bin/go", "test", "./..."}

	normalized := string(mutantkit.NormalizeRunReport(t, mutantkit.EncodeJSON(t, doc)))

	if strings.Contains(normalized, "/home/somebody") {
		t.Errorf("the toolchain path survived normalisation:\n%s", normalized)
	}
	if !strings.Contains(normalized, mutantkit.NormalizedPath) {
		t.Errorf("the toolchain path was not replaced by %s:\n%s", mutantkit.NormalizedPath, normalized)
	}
	if !strings.Contains(normalized, `"./..."`) {
		t.Errorf("the package pattern was rewritten as if it were a path:\n%s", normalized)
	}
}

func TestNormalizeRunReportFlattensGoTestElapsedTimes(t *testing.T) {
	t.Parallel()

	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	doc := mutantkit.DecodeJSON(t, original)
	tail := strings.Join([]string{
		"--- FAIL: TestClamp (0.01s)",
		"    clamp_test.go:14: want 3, got 4 after 0.25s",
		"--- FAIL: TestSlow (12.34s)",
		"panic: test timed out after 10s",
		"    retried (twice) with go1.26.6 in (0.5) and (1.5 s)",
	}, "\n")
	mutant(doc, 0)["output_tail"] = tail

	normalized := mutantkit.DecodeJSON(t, mutantkit.NormalizeRunReport(t, mutantkit.EncodeJSON(t, doc)))
	got, ok := mutant(normalized, 0)["output_tail"].(string)
	if !ok {
		t.Fatalf("output_tail = %v, want the normalised text", mutant(normalized, 0)["output_tail"])
	}
	want := strings.Join([]string{
		"--- FAIL: TestClamp " + mutantkit.NormalizedElapsed,
		"    clamp_test.go:14: want 3, got 4 after 0.25s",
		"--- FAIL: TestSlow " + mutantkit.NormalizedElapsed,
		"panic: test timed out after 10s",
		"    retried (twice) with go1.26.6 in (0.5) and (1.5 s)",
	}, "\n")
	if got != want {
		t.Errorf("output_tail =\n%s\nwant\n%s", got, want)
	}
}

func mutant(doc map[string]any, i int) map[string]any {
	return doc["mutants"].([]any)[i].(map[string]any)
}

func TestNormalizeRunReportFixesTheHostPlatform(t *testing.T) {
	t.Parallel()

	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	doc := mutantkit.DecodeJSON(t, original)
	platform := doc["workspace"].(map[string]any)["platform"].(map[string]any)
	platform["os"], platform["arch"] = "darwin", "arm64"

	normalized := mutantkit.DecodeJSON(t, mutantkit.NormalizeRunReport(t, mutantkit.EncodeJSON(t, doc)))
	got := normalized["workspace"].(map[string]any)["platform"].(map[string]any)

	if got["os"] != mutantkit.NormalizedOS || got["arch"] != mutantkit.NormalizedArch {
		t.Errorf("platform = %v/%v after normalising, want %s/%s",
			got["os"], got["arch"], mutantkit.NormalizedOS, mutantkit.NormalizedArch)
	}
}

func TestDecodeJSONKeepsNumbersExact(t *testing.T) {
	t.Parallel()

	const document = `{"score_percent":66.66666666666666,"total":88,"zero":0,"big":10000000000000000001}`
	doc := mutantkit.DecodeJSON(t, []byte(document))

	for name, want := range map[string]string{
		"score_percent": "66.66666666666666",
		"total":         "88",
		"zero":          "0",
		"big":           "10000000000000000001",
	} {
		number, ok := doc[name].(json.Number)
		if !ok {
			t.Errorf("%s decoded to %T, want a json.Number", name, doc[name])
			continue
		}
		if got := number.String(); got != want {
			t.Errorf("%s decoded to %s, want %s", name, got, want)
		}
	}
	if got := string(mutantkit.EncodeJSON(t, doc)); !strings.Contains(got, "66.66666666666666") {
		t.Errorf("re-encoding lost the exact number:\n%s", got)
	}
}

func TestMustMarshalRefusesADocumentTheSchemaRejects(t *testing.T) {
	t.Parallel()

	rec := expectFatal(t, func(tb testing.TB) {
		mutantkit.MustMarshal(tb, &report.Report{})
	})
	if said := rec.first(t, "marshalling a report the schema rejects"); !strings.Contains(said, "schema") {
		t.Errorf("the report does not say the schema refused the document:\n%s", said)
	}
}

func changedPointers(t *testing.T, before, after []byte) []string {
	t.Helper()
	var changed []string
	walkJSON(t, "", mutantkit.DecodeJSON(t, before), mutantkit.DecodeJSON(t, after), &changed)
	sort.Strings(changed)
	return changed
}

func walkJSON(t *testing.T, pointer string, before, after any, changed *[]string) {
	t.Helper()
	switch want := before.(type) {
	case map[string]any:
		got, ok := after.(map[string]any)
		if !ok {
			*changed = append(*changed, pointer)
			return
		}
		for key, value := range want {
			other, present := got[key]
			if !present {
				*changed = append(*changed, pointer+"/"+key)
				continue
			}
			walkJSON(t, pointer+"/"+key, value, other, changed)
		}
		for key := range got {
			if _, present := want[key]; !present {
				*changed = append(*changed, pointer+"/"+key)
			}
		}
	case []any:
		got, ok := after.([]any)
		if !ok || len(got) != len(want) {
			*changed = append(*changed, pointer)
			return
		}
		for i := range want {
			walkJSON(t, fmt.Sprintf("%s/%d", pointer, i), want[i], got[i], changed)
		}
	default:
		if fmt.Sprintf("%v", before) != fmt.Sprintf("%v", after) {
			*changed = append(*changed, pointer)
		}
	}
}

func TestElapsedTimesAreFlattenedOnlyOnGoTestsOwnLines(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"--- FAIL: TestClamp (0.01s)", "--- FAIL: TestClamp (0.00s)"},
		{"    --- PASS: TestClamp/inside (12.34s)", "    --- PASS: TestClamp/inside (0.00s)"},
		{"--- SKIP: TestLater (0.50s)", "--- SKIP: TestLater (0.00s)"},
		{"ok  \tfixture.example/killable\t0.123s", "ok  \tfixture.example/killable\t0.00s"},
		{"FAIL\tfixture.example/killable\t0.002s", "FAIL\tfixture.example/killable\t0.00s"},
		{"    clamp_test.go:14: request completed (0.25s)", "    clamp_test.go:14: request completed (0.25s)"},
		{"--- FAIL: TestClamp (0.01s) trailing", "--- FAIL: TestClamp (0.01s) trailing"},
		{"waited (1.5 s) then (0.5)", "waited (1.5 s) then (0.5)"},
	} {
		if got := mutantkit.NormalizeText(c.in); got != c.want {
			t.Errorf("normalizeText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeRunReportReplacesAToolchainPathHoldingASpace(t *testing.T) {
	t.Parallel()

	const windows = `C:\Program Files\Go\bin\go.exe`
	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	doc := mutantkit.DecodeJSON(t, original)
	doc["test"].(map[string]any)["toolchain"].(map[string]any)["go_bin"] = windows
	doc["test"].(map[string]any)["resolved_command"] = []any{windows, "test", "./..."}

	normalized := string(mutantkit.NormalizeRunReport(t, mutantkit.EncodeJSON(t, doc)))

	for _, machine := range []string{"Program Files", `Go\bin`, "go.exe"} {
		if strings.Contains(normalized, machine) {
			t.Errorf("%q survived normalisation, so the document still names the machine it was recorded on:\n%s",
				machine, normalized)
		}
	}
	if !strings.Contains(normalized, mutantkit.NormalizedPath) {
		t.Errorf("the toolchain path was not replaced by %s:\n%s", mutantkit.NormalizedPath, normalized)
	}
	for _, kept := range []string{`"./..."`, `"go"`, `"test"`} {
		if !strings.Contains(normalized, kept) {
			t.Errorf("%s was rewritten as if it were a path:\n%s", kept, normalized)
		}
	}
}
