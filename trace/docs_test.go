// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/trace"
)

const traceContractPage = "../docs/trace-v1.md"

const execKindTableHeading = "| `kind` | The command |"

var tableRow = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|")

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

	document := map[string]any{}
	if err = json.Unmarshal(trace.JSONSchema(), &document); err != nil {
		t.Fatal(err)
	}
	enumerated := execKindEnum(t, document)
	if !slices.Equal(enumerated, kinds) {
		t.Errorf("the schema enumerates %v and trace.ExecKinds returns %v; a reader and a"+
			" validator would disagree about which labels exist", enumerated, kinds)
	}

	documented, err := documentedExecKinds(string(page))
	if err != nil {
		t.Fatalf("reading the exec kind table out of %s: %v", traceContractPage, err)
	}
	undocumented, invented := execKindDisagreement(kinds, documented)
	for _, kind := range undocumented {
		t.Errorf("docs/trace-v1.md never mentions the exec kind %q, so a reader of a"+
			" recording carrying it has nothing to look it up in", kind)
	}
	for _, kind := range invented {
		t.Errorf("docs/trace-v1.md documents the exec kind %q, which this build never emits:"+
			" a reader is sent looking for a label that was renamed or removed", kind)
	}
}

func TestTheExecKindDocCheckSeesADocumentedKindThatDoesNotExist(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile(filepath.Clean(traceContractPage))
	if err != nil {
		t.Fatal(err)
	}
	const planted = "reticulate-splines"
	header := execKindTableHeading + "\n| --- | --- |\n"
	doctored := strings.Replace(string(page), header,
		header+"| `"+planted+"` | a command nothing issues |\n", 1)
	if doctored == string(page) {
		t.Fatalf("the exec kind table in %s no longer starts with the heading and separator"+
			" this test plants a row after, so the plant went nowhere", traceContractPage)
	}

	documented, err := documentedExecKinds(doctored)
	if err != nil {
		t.Fatalf("reading the exec kind table out of the doctored page: %v", err)
	}
	undocumented, invented := execKindDisagreement(trace.ExecKinds(), documented)
	if len(undocumented) != 0 {
		t.Errorf("planting one row reported %v as undocumented, which it should not have touched",
			undocumented)
	}
	if !slices.Equal(invented, []string{planted}) {
		t.Errorf("the check reported %v as documented but never emitted, want exactly [%s]",
			invented, planted)
	}
}

func execKindDisagreement(emitted, documented []string) (undocumented, invented []string) {
	for _, kind := range emitted {
		if !slices.Contains(documented, kind) {
			undocumented = append(undocumented, kind)
		}
	}
	for _, kind := range documented {
		if !slices.Contains(emitted, kind) {
			invented = append(invented, kind)
		}
	}
	slices.Sort(undocumented)
	slices.Sort(invented)
	return undocumented, invented
}

func documentedExecKinds(page string) ([]string, error) {
	start := strings.Index(page, execKindTableHeading)
	if start < 0 {
		return nil, errNoExecKindTable
	}
	rows := strings.Split(page[start:], "\n")
	if len(rows) < 3 {
		return nil, errEmptyExecKindTable
	}
	var kinds []string
	for _, row := range rows[2:] {
		match := tableRow.FindStringSubmatch(row)
		if match == nil {
			break
		}
		kinds = append(kinds, match[1])
	}
	if len(kinds) == 0 {
		return nil, errEmptyExecKindTable
	}
	return kinds, nil
}

var (
	errNoExecKindTable    = docError("docs/trace-v1.md holds no table headed " + execKindTableHeading)
	errEmptyExecKindTable = docError("the exec kind table in docs/trace-v1.md has no rows under it")
)

type docError string

func (e docError) Error() string { return string(e) }

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
