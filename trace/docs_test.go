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

// traceContractPage is the document that explains the wire contract, relative
// to this package's own directory. `go test` runs a test binary in the
// directory of the package it was built from, so the page is one level up.
const traceContractPage = "../docs/trace-v1.md"

// execKindTableHeading is the first line of the table that enumerates the exec
// kinds, and is how that table is told from the others on the page.
//
// By its heading rather than by a line number, and by *this* heading rather
// than by "the first table of backticked words": docs/trace-v1.md carries a
// table per payload and several of them have a first column of backticked
// field names, so a scan that took whichever it found first would silently
// re-point itself the day somebody adds a table above this one.
const execKindTableHeading = "| `kind` | The command |"

// tableRow matches one row of that table and captures its backticked first
// cell.
var tableRow = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|")

// TestEveryExecKindInTheSchemaIsDocumentedInTraceV1 keeps the label a reader
// branches on, the enumeration a recording is validated against, and the page
// they look it up in as one list.
//
// The exec `kind` is the whole of what a recording says a subprocess *was*: the
// schema closes the enumeration, so a command nobody labelled is a recording
// that does not validate, and [trace.ExecKinds] is the list a consumer switches
// on. What neither of them carries is what a label means; what the page cannot
// carry is which labels a build actually emits.
//
// So all three are compared as sets, and both directions have a failure worth
// naming. A kind in the code and not on the page is an undocumented label. A
// kind on the page and not in the code is a paragraph about a label no
// recording can carry, which sends a reader hunting for something that was
// renamed — and that is exactly the direction a one-way check misses, because a
// removed kind leaves its documentation behind and nothing complains.
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

// TestTheExecKindDocCheckSeesADocumentedKindThatDoesNotExist runs the
// comparison above against a page doctored on purpose.
//
// One direction of that check has always been able to fail: a new kind arrives
// undocumented and the test says so, which is how it was seen working. The
// other could not be, because making it fail means removing a kind from the
// code — so it is driven here instead, against the real page with one row
// planted in it. A gate that has only ever been watched to fail one of its two
// ways is half a gate.
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

// execKindDisagreement is the two ways the code and the page can differ: the
// kinds the code emits and the page does not name, and the kinds the page names
// and the code does not emit. Both sorted, so one failure reads the same twice.
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

// documentedExecKinds is every kind the page's own table enumerates.
//
// It reads the table rather than searching the prose for each kind, and that is
// the whole difference between a check that can fail both ways and one that can
// only fail one. Every kind's name appears in the prose as well — `control-run`
// is discussed three paragraphs further down — so "is this string somewhere on
// the page" answers the first question and cannot answer the second at all: it
// has no list of its own to compare against.
func documentedExecKinds(page string) ([]string, error) {
	start := strings.Index(page, execKindTableHeading)
	if start < 0 {
		return nil, errNoExecKindTable
	}
	rows := strings.Split(page[start:], "\n")
	// The heading and the separator under it are not rows.
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

// The two ways the table can be unreadable. Both are failures of this test
// rather than of the page's content — a heading that moved and a table with no
// rows under it each mean the scan is pinning nothing — so they are errors
// rather than an empty result somebody could mistake for agreement.
var (
	errNoExecKindTable    = docError("docs/trace-v1.md holds no table headed " + execKindTableHeading)
	errEmptyExecKindTable = docError("the exec kind table in docs/trace-v1.md has no rows under it")
)

// A docError is a fixed message about the shape of a documentation page.
type docError string

func (e docError) Error() string { return string(e) }

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
