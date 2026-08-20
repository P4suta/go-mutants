// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// updateGolden rewrites the golden document instead of comparing against it.
// The document is generated rather than typed, and it is read by eye before it
// is committed — which is the whole point of a golden file.
var updateGolden = flag.Bool("update", false, "rewrite the golden run report")

// goldenPath is the committed document the fixture run must marshal to, byte
// for byte.
var goldenPath = filepath.Join("testdata", "run-report.golden.json")

// TestGoldenReport pins every byte of a complete run report.
//
// A byte-exact fixture is the right assertion here rather than a field-by-field
// comparison. The document is a published format: field order, indentation, the
// spelling of every enumerated value, and the difference between `[]` and
// `null` are all part of what somebody's decoder sees, and none of them would
// be caught by asserting that the values are equal.
func TestGoldenReport(t *testing.T) {
	t.Parallel()

	got, err := buildFixture(t).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if *updateGolden {
		if writeErr := os.WriteFile(goldenPath, got, 0o644); writeErr != nil {
			t.Fatalf("rewriting %s: %v", goldenPath, writeErr)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the marshalled report does not match %s\n--- got ---\n%s\n--- want ---\n%s",
			goldenPath, got, want)
	}
}

// TestGoldenReportValidates checks the committed document against the published
// schema, through the same validator a consumer would use.
func TestGoldenReportValidates(t *testing.T) {
	t.Parallel()

	doc, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v", goldenPath, err)
	}
	if err := schemas.Validate(schemas.RunReportV1, doc); err != nil {
		t.Fatalf("the golden report does not satisfy its own schema: %v", err)
	}
}

// TestDocumentIdentityMatchesTheSchema holds the two spellings of the document
// type together.
//
// This package writes the string and internal/schemas registers it, and neither
// imports the other: the validator would otherwise be linked into the shipped
// binary for the sake of one constant. This test is what makes that duplication
// safe.
func TestDocumentIdentityMatchesTheSchema(t *testing.T) {
	t.Parallel()

	if report.DocumentType != schemas.RunReportV1 {
		t.Errorf("report.DocumentType = %q, schemas.RunReportV1 = %q", report.DocumentType, schemas.RunReportV1)
	}
	if !slices.Contains(schemas.DocumentTypes(), report.DocumentType) {
		t.Errorf("internal/schemas cannot validate %q; it knows %v", report.DocumentType, schemas.DocumentTypes())
	}
}

// TestMarshalIsDeterministic proves that the same inputs produce the same
// bytes, twice, from two independently built reports.
//
// Determinism is not a nicety here. Two shards of one run have to agree that
// they saw one catalogue, `report merge` compares documents, and a report that
// moved a field or reordered an array between two identical runs would make
// every one of those comparisons noise.
func TestMarshalIsDeterministic(t *testing.T) {
	t.Parallel()

	first, err := buildFixture(t).Marshal()
	if err != nil {
		t.Fatalf("first Marshal: %v", err)
	}
	for i := range 4 {
		next, err := buildFixture(t).Marshal()
		if err != nil {
			t.Fatalf("Marshal %d: %v", i, err)
		}
		if !bytes.Equal(first, next) {
			t.Fatalf("build %d produced different bytes:\n%s", i, next)
		}
	}
}

// TestSkipsAreSortedWhateverOrderTheyArriveIn proves the builder imposes the
// order rather than inheriting it.
func TestSkipsAreSortedWhateverOrderTheyArriveIn(t *testing.T) {
	t.Parallel()

	forward := fixtureOptions(t)
	reversed := fixtureOptions(t)
	reversed.Skips = slices.Clone(reversed.Skips)
	slices.Reverse(reversed.Skips)

	one := marshal(t, forward)
	two := marshal(t, reversed)
	if !bytes.Equal(one, two) {
		t.Errorf("reversing the skips changed the document:\n%s", two)
	}

	r, err := report.Build(forward)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []report.Skip{
		{Path: "internal/alpha/alpha.go", Reason: "array-length", Count: 1},
		{Path: "internal/alpha/alpha.go", Reason: "const-decl", Count: 4},
		{Path: "internal/gamma/gamma.go", Reason: "excluded", Count: 1},
	}
	if !slices.Equal(r.Skips, want) {
		t.Errorf("skips = %+v, want %+v", r.Skips, want)
	}
}

// TestWarningsKeepPublicationOrder proves the one array that is deliberately
// not sorted stays in the order the run published it.
func TestWarningsKeepPublicationOrder(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	if !slices.Equal(r.Warnings, fixtureWarnings) {
		t.Errorf("warnings = %+v, want %+v", r.Warnings, fixtureWarnings)
	}
}

// TestSummaryCountsTheFixture pins the arithmetic the score rests on.
//
// Expected survivors are excluded from the denominator, which is the whole
// point of the expectations ledger: the fixture has two survivors, one of them
// predicted, so the score is over one killed, one confirmed timeout, and one
// unexpected survivor.
func TestSummaryCountsTheFixture(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	s := r.Summary
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"total", s.Total, 7},
		{"killed", s.Killed, 1},
		{"survived", s.Survived, 2},
		{"timed_out", s.TimedOut, 1},
		{"inconclusive", s.Inconclusive, 1},
		{"errored", s.Errored, 1},
		{"not_run", s.NotRun, 1},
		{"selection.candidates", r.Selection.Candidates, 8},
		{"selection.rejected", r.Selection.Rejected, 1},
		{"selection.selected", r.Selection.Selected, 7},
		{"mutants", len(r.Mutants), 7},
		{"rejected", len(r.Rejected), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if s.ScorePercent == nil {
		t.Fatal("score_percent is null for a run with a denominator")
	}
	// (1 killed + 1 confirmed timeout) / (2 detections + 1 unexpected survivor),
	// computed the way [mutation.Score] computes it: the report must carry that
	// number and not a differently rounded one.
	if want := float64(2) / float64(3) * 100; *s.ScorePercent != want {
		t.Errorf("score_percent = %v, want %v", *s.ScorePercent, want)
	}
}

// TestScoreIsNullWhenNothingWasMeasured proves the undefined score is a null
// rather than a flattering or a damning number.
func TestScoreIsNullWhenNothingWasMeasured(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	for i := range opts.Results {
		opts.Results[i] = report.MutantResult{ID: opts.Results[i].ID, Outcome: mutation.OutcomeNotRun}
	}
	opts.Selected = 0
	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Summary.ScorePercent != nil {
		t.Errorf("score_percent = %v, want null", *r.Summary.ScorePercent)
	}
	encoded := string(mustMarshal(t, r))
	if !strings.Contains(encoded, `"score_percent": null`) {
		t.Error("an undefined score was not written as null")
	}
	if err := schemas.Validate(schemas.RunReportV1, []byte(encoded)); err != nil {
		t.Fatalf("a scoreless report does not satisfy the schema: %v", err)
	}
}

// TestTallyRoundTripsThroughTheDocument proves the document is lossless where
// losslessness is least obvious.
//
// The summary counts survivors as one number while the score's denominator
// needs them split, and the split is recovered by joining the expectations
// ledger. If that join were wrong, a consumer recomputing the score from the
// file would disagree with the file's own score_percent — so the tally the
// report reconstructs is compared against the one the run counted.
func TestTallyRoundTripsThroughTheDocument(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	got, err := r.Tally()
	if err != nil {
		t.Fatalf("Tally: %v", err)
	}
	want := mutation.Tally{
		Killed:              1,
		TimedOut:            1,
		UnexpectedSurvivors: 1,
		ExpectedSurvivors:   1,
		Inconclusive:        1,
		Errored:             1,
		NotRun:              1,
	}
	if got != want {
		t.Errorf("Tally() = %+v, want %+v", got, want)
	}
	percent, defined := mutation.ScoreOf(got).Percent()
	if !defined {
		t.Fatal("the reconstructed tally has no score")
	}
	if r.Summary.ScorePercent == nil || *r.Summary.ScorePercent != percent {
		t.Errorf("summary.score_percent = %v, recomputed %v", r.Summary.ScorePercent, percent)
	}
}

// TestOutcomeSpellingsAreTotalAndReversible holds the two vocabularies
// together: every core outcome has a document spelling, and every document
// spelling resolves back to the outcome it came from.
func TestOutcomeSpellingsAreTotalAndReversible(t *testing.T) {
	t.Parallel()

	seen := make(map[report.Outcome]bool, len(mutation.Outcomes()))
	for _, core := range mutation.Outcomes() {
		name, err := report.OutcomeOf(core)
		if err != nil {
			t.Fatalf("OutcomeOf(%s): %v", core, err)
		}
		if seen[name] {
			t.Errorf("two core outcomes render as %q", name)
		}
		seen[name] = true
		back, err := name.Mutation()
		if err != nil {
			t.Fatalf("%q.Mutation(): %v", name, err)
		}
		if back != core {
			t.Errorf("%s round-tripped through %q as %s", core, name, back)
		}
	}
	if _, err := report.OutcomeOf(mutation.Outcome(200)); report.CodeOf(err) != report.CodeInvalidOutcome {
		t.Errorf("an undefined outcome was rendered rather than refused: %v", err)
	}
	if _, err := report.Outcome("kild").Mutation(); report.CodeOf(err) != report.CodeInvalidOutcome {
		t.Errorf("an unknown outcome name was resolved rather than refused: %v", err)
	}
}

// TestEverySkipReasonIsInTheSchema is the drift guard between the reasons
// discovery emits and the enumeration the schema publishes.
//
// The builder deliberately copies a reason through rather than checking it, so
// that a new reason cannot fail a run at the very end of it. This is where that
// choice is paid for: a reason added to internal/discover without being added
// to the schema fails here, in the commit that adds it.
//
// The reasons come from [discover.AllSkipReasons] and not from a list typed out
// here, so that the guard covers whatever that package declares today rather
// than whatever it declared when this test was written. Discovery's own tests
// hold that list to the Skip* constants it really declares.
func TestEverySkipReasonIsInTheSchema(t *testing.T) {
	t.Parallel()

	reasons := discover.AllSkipReasons()
	if len(reasons) == 0 {
		t.Fatal("discovery reports no skip reasons, so this guard is checking nothing")
	}
	base := decode(t, mustMarshal(t, buildFixture(t)))
	for _, reason := range reasons {
		doc := decode(t, mustMarshal(t, buildFixture(t)))
		doc["skips"] = []any{map[string]any{"path": "x.go", "reason": string(reason), "count": 1.0}}
		if err := schemas.Validate(schemas.RunReportV1, encode(t, doc)); err != nil {
			t.Errorf("the schema rejects the skip reason %q that discovery emits: %v", reason, err)
		}
	}
	// The reserved reasons instrumentation will emit are in the enumeration
	// too, so that landing them is a code change and not a schema change.
	for _, reserved := range []string{"struct-tag", "label-or-goto", "unnameable-decl-type"} {
		doc := decode(t, mustMarshal(t, buildFixture(t)))
		doc["skips"] = []any{map[string]any{"path": "x.go", "reason": reserved, "count": 1.0}}
		if err := schemas.Validate(schemas.RunReportV1, encode(t, doc)); err != nil {
			t.Errorf("the schema rejects the reserved skip reason %q: %v", reserved, err)
		}
	}
	if len(base) == 0 {
		t.Fatal("the fixture document is empty")
	}
}

// TestSchemaRejects walks one violation of each class through the validator and
// checks that the failure is located where a person would look for it.
//
// The cases are made by editing a document that is known to be valid, so that
// each one differs from a passing document in exactly one way. Every case
// asserts the JSON pointer as well as the failure, because "the report is
// invalid" is not a diagnosis.
func TestSchemaRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		pointer string
		mutate  func(doc map[string]any)
	}{
		{
			name:    "unknown top-level field",
			pointer: "/vcs",
			mutate:  func(doc map[string]any) { doc["vcs"] = "git" },
		},
		{
			name:    "missing top-level field",
			pointer: "/coverage",
			mutate:  func(doc map[string]any) { delete(doc, "coverage") },
		},
		{
			name:    "wrong document type",
			pointer: "/document_type",
			mutate:  func(doc map[string]any) { doc["document_type"] = "go-mutants/catalog" },
		},
		{
			name:    "wrong schema version",
			pointer: "/schema_version",
			mutate:  func(doc map[string]any) { doc["schema_version"] = 2.0 },
		},
		{
			name:    "malformed run id",
			pointer: "/run_id",
			mutate:  func(doc map[string]any) { doc["run_id"] = "2026-02-18T09:15:00Z-3f9c" },
		},
		{
			name:    "unknown status",
			pointer: "/status",
			mutate:  func(doc map[string]any) { doc["status"] = "ok" },
		},
		{
			name:    "timestamp that is not UTC",
			pointer: "/started_at",
			mutate:  func(doc map[string]any) { doc["started_at"] = "2026-02-18T09:15:00+09:00" },
		},
		{
			name:    "negative duration",
			pointer: "/duration_ms",
			mutate:  func(doc map[string]any) { doc["duration_ms"] = -1.0 },
		},
		{
			name:    "truncated workspace digest",
			pointer: "/workspace/workspace_digest",
			mutate: func(doc map[string]any) {
				workspace(doc)["workspace_digest"] = strings.Repeat("ab", 16)
			},
		},
		{
			name:    "unknown platform field",
			pointer: "/workspace/platform/libc",
			mutate: func(doc map[string]any) {
				workspace(doc)["platform"].(map[string]any)["libc"] = "musl"
			},
		},
		{
			name:    "unknown selection mode",
			pointer: "/selection/mode",
			mutate:  func(doc map[string]any) { object(doc, "selection")["mode"] = "changed" },
		},
		{
			name:    "empty test command",
			pointer: "/test/command",
			mutate:  func(doc map[string]any) { object(doc, "test")["command"] = []any{} },
		},
		{
			name:    "unknown timeout source",
			pointer: "/test/timeout_source",
			mutate:  func(doc map[string]any) { object(doc, "test")["timeout_source"] = "guessed" },
		},
		{
			name:    "unknown coverage mode",
			pointer: "/coverage/mode",
			mutate:  func(doc map[string]any) { object(doc, "coverage")["mode"] = "package" },
		},
		{
			name:    "score above 100",
			pointer: "/summary/score_percent",
			mutate:  func(doc map[string]any) { object(doc, "summary")["score_percent"] = 101.0 },
		},
		{
			name:    "empty policy failure",
			pointer: "/summary/policy/failure",
			mutate: func(doc map[string]any) {
				object(doc, "summary")["policy"].(map[string]any)["failure"] = ""
			},
		},
		{
			name:    "unknown outcome",
			pointer: "/mutants/0/outcome",
			mutate:  func(doc map[string]any) { mutant(doc, 0)["outcome"] = "timed_out" },
		},
		{
			name:    "missing mutant field",
			pointer: "/mutants/0/killed_by",
			mutate:  func(doc map[string]any) { delete(mutant(doc, 0), "killed_by") },
		},
		{
			name:    "zero line number",
			pointer: "/mutants/1/line",
			mutate:  func(doc map[string]any) { mutant(doc, 1)["line"] = 0.0 },
		},
		{
			name:    "short display id",
			pointer: "/mutants/2/display_id",
			mutate:  func(doc map[string]any) { mutant(doc, 2)["display_id"] = "abcd" },
		},
		{
			name:    "rejection with no diagnostic",
			pointer: "/rejected/0/diagnostic",
			mutate:  func(doc map[string]any) { object(doc, "rejected", 0)["diagnostic"] = "" },
		},
		{
			name:    "undocumented skip reason",
			pointer: "/skips/0/reason",
			mutate:  func(doc map[string]any) { object(doc, "skips", 0)["reason"] = "because" },
		},
		{
			name:    "skip that counted nothing",
			pointer: "/skips/0/count",
			mutate:  func(doc map[string]any) { object(doc, "skips", 0)["count"] = 0.0 },
		},
		{
			name:    "unknown expectation state",
			pointer: "/expectations/0/state",
			mutate:  func(doc map[string]any) { object(doc, "expectations", 0)["state"] = "pending" },
		},
		{
			name:    "warning with no code",
			pointer: "/warnings/0/code",
			mutate:  func(doc map[string]any) { object(doc, "warnings", 0)["code"] = "4040" },
		},
	}

	valid := mustMarshal(t, buildFixture(t))
	if err := schemas.Validate(schemas.RunReportV1, valid); err != nil {
		t.Fatalf("the unedited fixture is already invalid: %v", err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			doc := decode(t, valid)
			c.mutate(doc)
			err := schemas.Validate(schemas.RunReportV1, encode(t, doc))
			if err == nil {
				t.Fatal("the schema accepted the document")
			}
			if code := schemas.CodeOf(err); code != schemas.CodeInvalidDocument {
				t.Fatalf("code = %q, want %q (%v)", code, schemas.CodeInvalidDocument, err)
			}
			pointer, ok := schemas.PointerOf(err)
			if !ok {
				t.Fatalf("the failure carries no pointer: %v", err)
			}
			if pointer != c.pointer {
				t.Errorf("pointer = %q, want %q (%v)", pointer, c.pointer, err)
			}
		})
	}
}

// marshal builds and marshals in one step, for the tests that only compare
// bytes.
func marshal(t *testing.T, opts report.Options) []byte {
	t.Helper()
	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return mustMarshal(t, r)
}

// mustMarshal marshals a report and checks it against the published schema.
//
// The check is here rather than in a test of its own so that it is impossible
// to forget: every document any test in this package produces goes through this
// helper, and therefore through the same validator a consumer would use. The
// tests that need an invalid document build one by editing the bytes this
// returns.
func mustMarshal(t *testing.T, r *report.Report) []byte {
	t.Helper()
	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := schemas.Validate(schemas.RunReportV1, data); err != nil {
		t.Fatalf("the report does not satisfy its own schema: %v\n%s", err, data)
	}
	return data
}

// decode reads a document back into a generic tree, so a test can edit one
// field of an otherwise valid document.
func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	return doc
}

// encode writes an edited tree back out. Map keys are sorted by encoding/json,
// so the bytes do not depend on iteration order.
func encode(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding the document: %v", err)
	}
	return data
}

// workspace returns the workspace object of a decoded document.
func workspace(doc map[string]any) map[string]any {
	return doc["workspace"].(map[string]any)
}

// mutant returns one row of the decoded mutants array.
func mutant(doc map[string]any, i int) map[string]any {
	return doc["mutants"].([]any)[i].(map[string]any)
}

// object returns a named object of a decoded document, or, when an index is
// given, one element of a named array.
func object(doc map[string]any, name string, index ...int) map[string]any {
	if len(index) == 0 {
		return doc[name].(map[string]any)
	}
	return doc[name].([]any)[index[0]].(map[string]any)
}
