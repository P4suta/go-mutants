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

// varyingFields is the whole of what the committed run-report golden says about
// the run rather than about the code, as JSON pointers and the values
// normalisation has to leave behind.
//
// It is written out rather than derived, because that is the value of it: adding
// a row means deciding that two runs may differ in that field, and forgetting
// one means a report golden that fails on the next machine's Go version.
var varyingFields = map[string]string{
	"/tool_version":                 mutantkit.NormalizedToolVersion,
	"/run_id":                       mutantkit.NormalizedRunID,
	"/started_at":                   mutantkit.NormalizedTimestamp,
	"/finished_at":                  mutantkit.NormalizedTimestamp,
	"/duration_ms":                  "0",
	"/workspace/go_version":         mutantkit.NormalizedGoVersion,
	"/workspace/platform/os":        mutantkit.NormalizedOS,
	"/workspace/platform/arch":      mutantkit.NormalizedArch,
	"/test/timeout_ms":              "0",
	"/test/baseline/slowest_ms":     "0",
	"/test/baseline/durations_ms/0": "0",
	"/test/baseline/durations_ms/1": "0",
	"/test/baseline/durations_ms/2": "0",
	"/mutants/0/duration_ms":        "0",
	"/mutants/1/duration_ms":        "0",
	"/mutants/2/duration_ms":        "0",
	"/mutants/3/duration_ms":        "0",
	"/mutants/4/duration_ms":        "0",
	"/mutants/5/duration_ms":        "0",
	// The seventh mutant was never run, so its duration is already zero: the
	// pointer is listed because it is a measured duration, not because the
	// fixture happens to make it move.
	"/mutants/6/duration_ms": "0",
	// The toolchain that ran the tests. Its path is a different one on every
	// machine and its version line changes with every Go release, so both are
	// facts about the run — and `test.resolved_command` is the same path again,
	// because it is `test.command` with that executable in place of `go`.
	"/test/toolchain/go_bin":   mutantkit.NormalizedPath,
	"/test/toolchain/version":  mutantkit.NormalizedToolchainVersion,
	"/test/resolved_command/0": mutantkit.NormalizedPath,
	// One row per attempt of every mutant this run executed: how long the pass
	// took, and which scheduler slot made it. The worker is here because it is
	// a fact about the run in the strongest sense — which goroutine won the
	// race to the queue — so two runs on one machine differ in it; several of
	// the fixture's rows already say 0 and are listed anyway, because a field
	// that happens not to move is covered by nothing otherwise. The `binaries`
	// beside them are deliberately absent: which binaries a pass started is
	// what the run did, and it is the same every time.
	"/mutants/1/executions/0/duration_ms": "0",
	"/mutants/1/executions/0/worker":      "0",
	"/mutants/3/executions/0/duration_ms": "0",
	"/mutants/3/executions/0/worker":      "0",
	"/mutants/3/executions/1/duration_ms": "0",
	"/mutants/3/executions/1/worker":      "0",
	"/mutants/4/executions/0/duration_ms": "0",
	"/mutants/4/executions/0/worker":      "0",
	"/mutants/5/executions/0/duration_ms": "0",
	"/mutants/5/executions/0/worker":      "0",
	"/mutants/7/duration_ms":              "0",
	"/mutants/7/executions/0/duration_ms": "0",
	"/mutants/7/executions/0/worker":      "0",
	"/mutants/7/executions/1/duration_ms": "0",
	"/mutants/7/executions/1/worker":      "0",
	// The timeline. Every phase and every stage is a measured duration; their
	// names are not, and stay.
	"/timing/phases/0/duration_ms": "0",
	"/timing/phases/1/duration_ms": "0",
	"/timing/phases/2/duration_ms": "0",
	"/timing/phases/3/duration_ms": "0",
	"/timing/stages/0/duration_ms": "0",
	"/timing/stages/1/duration_ms": "0",
	"/timing/stages/2/duration_ms": "0",
	"/timing/stages/3/duration_ms": "0",
	"/timing/stages/4/duration_ms": "0",
	"/timing/stages/5/duration_ms": "0",
	"/timing/stages/6/duration_ms": "0",
	"/timing/stages/7/duration_ms": "0",
	"/timing/stages/8/duration_ms": "0",
}

// TestNormalizeRunReportFixesOnlyTheVaryingFields is the claim that makes a
// report golden possible at all.
//
// A run report is mostly a fact about the code — which mutants there are, what
// happened to each, what the policy decided — and partly a fact about the run:
// when it started, how long every step took, which toolchain built it, where the
// snapshot was. A golden of the second kind fails on the next machine. So the
// second kind is replaced with fixed values, and the assertion here is in both
// directions: every varying field moved, and *nothing else did* — the digests
// and the mutant ids in particular, which are content-addressed and are the one
// part of the document that proves the run measured the same program.
func TestNormalizeRunReportFixesOnlyTheVaryingFields(t *testing.T) {
	t.Parallel()

	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	normalized := mutantkit.NormalizeRunReport(t, original)

	// Nothing outside the ledger moved. The mutant ids and the workspace digest
	// are what this is really about: they are content-addressed, so they are the
	// same on every machine, and a normaliser that touched one would leave a
	// golden that passes for a run of a different program.
	for _, pointer := range changedPointers(t, original, normalized) {
		if _, listed := varyingFields[pointer]; !listed {
			t.Errorf("normalising changed %s, which is not one of the fields that vary between runs", pointer)
		}
	}
	// And every field in the ledger now holds the value it should, which is the
	// half a "nothing else changed" assertion cannot make: a field the fixture
	// happens to have recorded as zero would otherwise be covered by nothing.
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

// valueAt resolves a JSON pointer to a scalar, rendered as text so that a
// json.Number and a string compare the way a reader of the document would.
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

// TestNormalizeRunReportIsIdempotent is what lets a normalised document be
// compared with a normalised golden: normalising the golden again has to be a
// no-op, or the comparison is against a moving target.
func TestNormalizeRunReportIsIdempotent(t *testing.T) {
	t.Parallel()

	original := testkit.ReadFile(t, filepath.Join(testkit.Root(t), "internal", "report", "testdata", "run-report.golden.json"))
	once := mutantkit.NormalizeRunReport(t, original)
	twice := mutantkit.NormalizeRunReport(t, once)

	if !bytes.Equal(once, twice) {
		t.Errorf("normalising twice is not normalising once:\n%s", string(twice))
	}
}

// TestNormalizeRunReportReplacesAToolchainPathAndATimestamp drives the one path
// the committed golden cannot: a real run's `test.command` starts with the
// located `go` binary — which is `/usr/local/go/bin/go` on one machine and
// `C:\hostedtoolcache\...` on another — while the fixture's is the `go` the
// user wrote. (The golden's own absolute paths, in `test.toolchain.go_bin` and
// `test.resolved_command`, are in the ledger above.)
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
	// "./..." is not an absolute path and must survive: a pattern replaced by a
	// path constant is a report that no longer says what was run.
	if !strings.Contains(normalized, `"./..."`) {
		t.Errorf("the package pattern was rewritten as if it were a path:\n%s", normalized)
	}
}

// TestNormalizeRunReportFixesTheHostPlatform is what makes one committed report
// golden usable on all three platforms CI runs.
//
// A run report records the host's GOOS and GOARCH, so a golden generated on
// ubuntu and compared on macOS differs in two fields that say nothing about the
// program under test. The ledger above cannot show this on its own — the
// committed fixture happens to be linux/amd64 already — so the document is
// edited to another platform first.
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

// TestDecodeJSONKeepsNumbersExact is the reason there is a decoder here at all.
//
// encoding/json decodes every number into a float64 unless it is told not to, so
// a document read and written back turns 88 into 88 by luck and 66.66666666666666
// into something shorter by arithmetic. Half the tests that use this decode a
// valid document, edit one field, and re-encode it to prove the *validator*
// rejects it — and a decoder that silently rewrote three other fields on the way
// would make those tests about the wrong thing.
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

// TestMustMarshalRefusesADocumentTheSchemaRejects keeps the validation inside
// the helper rather than in a test of its own, which is what makes it impossible
// to forget: every document any suite produces goes through here, and therefore
// through the same validator a consumer would use.
func TestMustMarshalRefusesADocumentTheSchemaRejects(t *testing.T) {
	t.Parallel()

	rec := expectFatal(t, func(tb testing.TB) {
		mutantkit.MustMarshal(tb, &report.Report{})
	})
	if said := rec.first(t, "marshalling a report the schema rejects"); !strings.Contains(said, "schema") {
		t.Errorf("the report does not say the schema refused the document:\n%s", said)
	}
}

// changedPointers lists the JSON pointers whose scalar value differs between two
// documents, sorted.
//
// Comparing the trees rather than the bytes is what makes "only these fields
// moved" an assertion about the document instead of about its formatting, and
// walking to the scalars is what makes the report name the field a reader has to
// go and look at.
func changedPointers(t *testing.T, before, after []byte) []string {
	t.Helper()
	var changed []string
	walkJSON(t, "", mutantkit.DecodeJSON(t, before), mutantkit.DecodeJSON(t, after), &changed)
	sort.Strings(changed)
	return changed
}

// walkJSON records the pointer of every scalar that differs, and of every key or
// element that is present in one document and not the other.
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
