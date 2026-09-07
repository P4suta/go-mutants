// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/trace"
)

// traceContractPage is the document that explains the wire contract, relative
// to this package's own directory. `go test` runs a test binary in the
// directory of the package it was built from, so the page is one level up.
const traceContractPage = "../docs/trace-v1.md"

// TestEveryExecKindInTheSchemaIsDocumentedInTraceV1 keeps the label a reader
// branches on and the page they look it up in together.
//
// The exec `kind` is the whole of what a recording says a subprocess *was*: the
// schema closes the enumeration, so a command nobody labelled is a recording
// that does not validate, and [trace.ExecKinds] is the list a consumer switches
// on. What neither of them carries is what the label means. A kind added to the
// enum without a paragraph is a string a reader of a recording has nowhere to
// look up — which is the same failure the artifact and note kinds are already
// pinned against, applied to the vocabulary that appears most often in a
// stream.
//
// Both directions are checked. A kind in the code and not on the page is an
// undocumented label; a kind on the page and not in the code is a paragraph
// about a label no recording can carry, which sends a reader hunting for
// something that was renamed.
func TestEveryExecKindInTheSchemaIsDocumentedInTraceV1(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile(filepath.Clean(traceContractPage))
	if err != nil {
		t.Fatal(err)
	}
	kinds := trace.ExecKinds()
	if len(kinds) == 0 {
		t.Fatal("trace.ExecKinds is empty, so this test is pinning nothing")
	}

	for _, kind := range kinds {
		if !strings.Contains(string(page), "`"+kind+"`") {
			t.Errorf("docs/trace-v1.md never mentions the exec kind %q, so a reader of a"+
				" recording carrying it has nothing to look it up in", kind)
		}
	}

	// The other direction, read out of the schema rather than out of the page:
	// the enumeration is what a recording is validated against, so it is the
	// authority on which labels exist at all.
	document := map[string]any{}
	if err := json.Unmarshal(trace.JSONSchema(), &document); err != nil {
		t.Fatal(err)
	}
	enumerated := execKindEnum(t, document)
	if !slices.Equal(enumerated, kinds) {
		t.Errorf("the schema enumerates %v and trace.ExecKinds returns %v; a reader and a"+
			" validator would disagree about which labels exist", enumerated, kinds)
	}
}

// execKindEnum is the enumeration the schema constrains an exec event's `kind`
// to, in schema order.
func execKindEnum(t *testing.T, document map[string]any) []string {
	t.Helper()

	definitions, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("the schema carries no $defs")
	}
	payload, ok := definitions["exec"].(map[string]any)
	if !ok {
		t.Fatal("the schema defines no exec payload")
	}
	properties, ok := payload["properties"].(map[string]any)
	if !ok {
		t.Fatal("the exec payload has no properties")
	}
	kind, ok := properties["kind"].(map[string]any)
	if !ok {
		t.Fatal("the exec payload has no kind property")
	}
	values, ok := kind["enum"].([]any)
	if !ok {
		t.Fatal("the exec payload's kind is not enumerated")
	}
	enumerated := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("the exec kind enumeration holds a non-string: %v", value)
		}
		enumerated = append(enumerated, text)
	}
	return enumerated
}
