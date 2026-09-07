// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// The committed documents the fixture runs must marshal to, byte for byte. The
// second is a coverage-guided run, which publishes two fields an `off` run
// leaves out entirely.
//
// They are names rather than paths because [testkit.Golden] resolves them
// against testdata/, and the comparison, the diff on a mismatch and the single
// repository-wide `-update` flag all live there. This package used to register
// an `-update` flag of its own; internal/instrument registered a second, and a
// third in any package linking either would have panicked "flag redefined"
// before a test ran.
const (
	goldenReport         = "run-report.golden.json"
	goldenCoverageReport = "run-report-coverage.golden.json"
	// goldenTestCoverageReport is the coverage-guided report narrowed one
	// step further, to tests: the same three mutants, with the tests that
	// reach each of them named and every pass narrowed to them.
	goldenTestCoverageReport = "run-report-tests.golden.json"
)

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
	testkit.Golden(t, goldenReport, got)
}

// TestGoldenReportValidates checks the committed document against the published
// schema, through the same validator a consumer would use.
func TestGoldenReportValidates(t *testing.T) {
	t.Parallel()

	doc := testkit.ReadFile(t, testkit.GoldenPath(goldenReport))
	if err := schemas.Validate(schemas.RunReportV1, doc); err != nil {
		t.Fatalf("the golden report does not satisfy its own schema: %v", err)
	}
}

// TestGoldenCoverageReport pins the second shape of the document: the one a
// coverage-guided run publishes.
//
// It is a golden of its own rather than a field-by-field check for the reason
// [TestGoldenReport] is: `coverage.binaries` and `coverage.mutants_uncovered`
// are *absent* from an off-mode document and present here, and the difference
// between an absent key and a zero-valued one is exactly what a consumer's
// decoder sees and exactly what no equality assertion would notice.
func TestGoldenCoverageReport(t *testing.T) {
	t.Parallel()

	got, err := buildCoverageFixture(t).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	testkit.Golden(t, goldenCoverageReport, got)

	want := testkit.ReadFile(t, testkit.GoldenPath(goldenCoverageReport))
	if err := schemas.Validate(schemas.RunReportV1, want); err != nil {
		t.Fatalf("the golden coverage report does not satisfy its own schema: %v", err)
	}
}

// TestGoldenTestCoverageReport pins every byte of a test-narrowed run report,
// and checks it against the schema, for the reason the coverage golden is:
// `covering_tests`, `executions[].tests` and `coverage.tests` are a published
// shape, and the only way `mode: "test"` can be additive is for every older
// key to stay exactly where it was.
func TestGoldenTestCoverageReport(t *testing.T) {
	t.Parallel()

	got, err := buildTestCoverageFixture(t).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	testkit.Golden(t, goldenTestCoverageReport, got)

	want := testkit.ReadFile(t, testkit.GoldenPath(goldenTestCoverageReport))
	if err := schemas.Validate(schemas.RunReportV1, want); err != nil {
		t.Fatalf("the golden test-narrowed report does not satisfy its own schema: %v", err)
	}
}

// TestTestCoverageBlockNamesTheTestsUnderneathIt is the test-narrowed twin of
// [TestCoverageBlockDescribesTheMutantsUnderneathIt]: the block says `test`
// and counts the tests, every covered mutant names the tests that reach it,
// every pass names the tests it was narrowed to, and the uncovered mutant
// names none of either.
func TestTestCoverageBlockNamesTheTestsUnderneathIt(t *testing.T) {
	t.Parallel()

	r := buildTestCoverageFixture(t)
	if r.Coverage.Mode != report.CoverageTest {
		t.Fatalf("coverage mode = %q, want %q", r.Coverage.Mode, report.CoverageTest)
	}
	if r.Coverage.Binaries == nil || *r.Coverage.Binaries != coverageBinaries {
		t.Errorf("coverage.binaries = %v, want %d", r.Coverage.Binaries, coverageBinaries)
	}
	if r.Coverage.Tests == nil || *r.Coverage.Tests != testCoverageTests {
		t.Errorf("coverage.tests = %v, want %d", r.Coverage.Tests, testCoverageTests)
	}
	for _, m := range r.Mutants {
		if m.Uncovered {
			if len(m.CoveringTests) != 0 {
				t.Errorf("uncovered mutant %s names %v as covering it", m.DisplayID, m.CoveringTests)
			}
			continue
		}
		if len(m.CoveringTests) == 0 {
			t.Errorf("covered mutant %s names no covering test", m.DisplayID)
		}
		for _, execution := range m.Executions {
			if len(execution.Tests) == 0 {
				t.Errorf("attempt %d of mutant %s was not narrowed to any test", execution.Attempt, m.DisplayID)
			}
		}
	}
}

// TestCoverageBlockDescribesTheMutantsUnderneathIt reads the two summary
// numbers back out of the document and checks them against the rows they
// summarise.
//
// `mutants_uncovered` is counted by the builder rather than passed in, which is
// what makes this checkable at all: the assertion is that the number a consumer
// would branch on and the rows a consumer would count agree, which is the one
// way a summary can quietly stop being one.
func TestCoverageBlockDescribesTheMutantsUnderneathIt(t *testing.T) {
	t.Parallel()

	r := buildCoverageFixture(t)
	if r.Coverage.Mode != report.CoveragePackage {
		t.Fatalf("coverage mode = %q, want %q", r.Coverage.Mode, report.CoveragePackage)
	}
	if r.Coverage.Binaries == nil || *r.Coverage.Binaries != coverageBinaries {
		t.Errorf("coverage.binaries = %v, want %d", r.Coverage.Binaries, coverageBinaries)
	}

	counted := 0
	for _, m := range r.Mutants {
		if !m.Uncovered {
			if len(m.CoveringTestPackages) == 0 {
				t.Errorf("mutant %s is covered by nothing and is not marked uncovered", m.DisplayID)
			}
			continue
		}
		counted++
		// The three things `uncovered` claims, each of which the builder
		// refuses to publish without.
		if m.Outcome != report.OutcomeSurvived {
			t.Errorf("uncovered mutant %s is %s, want %s", m.DisplayID, m.Outcome, report.OutcomeSurvived)
		}
		if m.Attempts != 0 || m.DurationMS != 0 {
			t.Errorf("uncovered mutant %s reports %d attempts in %dms, want none of either",
				m.DisplayID, m.Attempts, m.DurationMS)
		}
		if len(m.CoveringTestPackages) != 0 {
			t.Errorf("uncovered mutant %s lists %v as covering it", m.DisplayID, m.CoveringTestPackages)
		}
	}
	if r.Coverage.MutantsUncovered == nil || *r.Coverage.MutantsUncovered != counted {
		t.Errorf("coverage.mutants_uncovered = %v, but %d rows carry the flag", r.Coverage.MutantsUncovered, counted)
	}
	if counted == 0 {
		t.Fatal("the coverage fixture has no uncovered mutant, so this checks nothing")
	}
}

// TestCoverageOffStatesNoNumbersItDidNotMeasure is the other half of the same
// contract, in Go rather than in JSON Schema.
func TestCoverageOffStatesNoNumbersItDidNotMeasure(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	if r.Coverage.Mode != report.CoverageOff {
		t.Fatalf("coverage mode = %q, want %q", r.Coverage.Mode, report.CoverageOff)
	}
	if r.Coverage.Binaries != nil || r.Coverage.MutantsUncovered != nil {
		t.Errorf("a run that narrowed nothing reports %+v", r.Coverage)
	}
	for _, m := range r.Mutants {
		if m.Uncovered {
			t.Errorf("mutant %s is marked uncovered in a run with coverage off", m.DisplayID)
		}
		if m.CoveringTestPackages == nil {
			t.Errorf("mutant %s carries a null covering list, not an empty one", m.DisplayID)
		}
	}
}

// TestBuildRefusesACoverageBlockTheMutantsContradict walks the combinations the
// builder must never publish.
func TestBuildRefusesACoverageBlockTheMutantsContradict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(opts *report.Options)
	}{
		{
			name: "unknown mode",
			mutate: func(opts *report.Options) {
				opts.CoverageMode = report.CoverageMode("line")
			},
		},
		{
			name: "uncovered with coverage off",
			mutate: func(opts *report.Options) {
				opts.CoverageMode = report.CoverageOff
			},
		},
		{
			name: "uncovered mutant that was executed",
			mutate: func(opts *report.Options) {
				for i := range opts.Results {
					if opts.Results[i].Uncovered {
						opts.Results[i].Attempts = 1
					}
				}
			},
		},
		{
			name: "uncovered mutant that was killed",
			mutate: func(opts *report.Options) {
				for i := range opts.Results {
					if opts.Results[i].Uncovered {
						opts.Results[i].Outcome = mutation.OutcomeKilled
					}
				}
			},
		},
		{
			name: "negative binary count",
			mutate: func(opts *report.Options) {
				opts.CoverageBinaries = -1
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := coverageOptions(t)
			c.mutate(&opts)
			_, err := report.Build(opts)
			if err == nil {
				t.Fatal("Build published a document whose coverage block contradicts its mutants")
			}
			if code := report.CodeOf(err); code != report.CodeInvalidCoverage {
				t.Errorf("code = %q, want %q (%v)", code, report.CodeInvalidCoverage, err)
			}
		})
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
		{"total", s.Total, 8},
		{"killed", s.Killed, 1},
		{"survived", s.Survived, 2},
		{"timed_out", s.TimedOut, 2},
		{"inconclusive", s.Inconclusive, 1},
		{"errored", s.Errored, 1},
		{"not_run", s.NotRun, 1},
		{"selection.candidates", r.Selection.Candidates, 9},
		{"selection.rejected", r.Selection.Rejected, 1},
		{"selection.selected", r.Selection.Selected, 8},
		{"mutants", len(r.Mutants), 8},
		{"rejected", len(r.Rejected), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if s.ScorePercent == nil {
		t.Fatal("score_percent is null for a run with a denominator")
	}
	// (1 killed + 2 confirmed timeouts) / (3 detections + 1 unexpected
	// survivor), computed the way [mutation.Score] computes it: the report must
	// carry that number and not a differently rounded one.
	if want := float64(3) / float64(4) * 100; *s.ScorePercent != want {
		t.Errorf("score_percent = %v, want %v", *s.ScorePercent, want)
	}
}

// TestScoreIsNullWhenNothingWasMeasured proves the undefined score is a null
// rather than a flattering or a damning number.
func TestScoreIsNullWhenNothingWasMeasured(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	for i := range opts.Results {
		opts.Results[i] = report.MutantResult{
			ID:           opts.Results[i].ID,
			Outcome:      mutation.OutcomeNotRun,
			NotRunReason: report.NotRunOutOfSelection,
		}
	}
	opts.Selected = 0
	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Summary.ScorePercent != nil {
		t.Errorf("score_percent = %v, want null", *r.Summary.ScorePercent)
	}
	encoded := string(mutantkit.MustMarshal(t, r))
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
		TimedOut:            2,
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
	base := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, buildFixture(t)))
	for _, reason := range reasons {
		doc := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, buildFixture(t)))
		doc["skips"] = []any{map[string]any{"path": "x.go", "reason": string(reason), "count": 1.0}}
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.EncodeJSON(t, doc)); err != nil {
			t.Errorf("the schema rejects the skip reason %q that discovery emits: %v", reason, err)
		}
	}
	// The reserved reasons instrumentation will emit are in the enumeration
	// too, so that landing them is a code change and not a schema change.
	for _, reserved := range []string{"struct-tag", "label-or-goto", "unnameable-decl-type"} {
		doc := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, buildFixture(t)))
		doc["skips"] = []any{map[string]any{"path": "x.go", "reason": reserved, "count": 1.0}}
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.EncodeJSON(t, doc)); err != nil {
			t.Errorf("the schema rejects the reserved skip reason %q: %v", reserved, err)
		}
	}
	if len(base) == 0 {
		t.Fatal("the fixture document is empty")
	}
}

// TestEveryEnumeratedValueIsInTheSchema is the same drift guard for the two
// enumerations this package declares itself.
//
// [report.SelectionModes] and [report.NotRunReasons] are what the builder will
// write, and the schema is what a consumer will branch on. A value added to one
// and not the other is a document go-mutants writes and its own schema refuses,
// which the run only finds out about at the very end — so it is found out about
// here instead, in the commit that adds the value.
//
// The not-run reason is checked on the fixture's one not-run mutant rather than
// on an invented row, because the schema only allows a reason there: the
// biconditional is part of what is being checked.
func TestEveryEnumeratedValueIsInTheSchema(t *testing.T) {
	t.Parallel()

	for _, mode := range report.SelectionModes() {
		doc := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, buildFixture(t)))
		selection := object(doc, "selection")
		selection["mode"] = string(mode)
		// The two modes that come with a fact attached carry it, since the
		// schema is entitled to expect one.
		if mode == report.ModeShard {
			doc["shard"] = map[string]any{"index": 1.0, "total": 2.0, "assignment": mutation.ShardAssignment}
		}
		if mode == report.ModeChanged {
			selection["changed_ref"] = "origin/main"
		}
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.EncodeJSON(t, doc)); err != nil {
			t.Errorf("the schema rejects the selection mode %q this package writes: %v", mode, err)
		}
	}

	notRun := -1
	for i, m := range buildFixture(t).Mutants {
		if m.Outcome == report.OutcomeNotRun {
			notRun = i
		}
	}
	if notRun < 0 {
		t.Fatal("the fixture has no not-run mutant, so this guard is checking nothing")
	}
	for _, reason := range report.NotRunReasons() {
		doc := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, buildFixture(t)))
		object(doc, "mutants", notRun)["not_run_reason"] = string(reason)
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.EncodeJSON(t, doc)); err != nil {
			t.Errorf("the schema rejects the not-run reason %q this package writes: %v", reason, err)
		}
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
			mutate:  func(doc map[string]any) { object(doc, "selection")["mode"] = "diffed" },
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
			mutate:  func(doc map[string]any) { object(doc, "coverage")["mode"] = "line" },
		},
		{
			// The `else` half of the conditional: a run that narrowed nothing
			// must not carry the numbers that describe narrowing, because a
			// reader cannot tell a measured zero from a default one.
			name:    "coverage off carrying a binary count",
			pointer: "/coverage/binaries",
			mutate:  func(doc map[string]any) { object(doc, "coverage")["binaries"] = 2.0 },
		},
		{
			name:    "coverage off carrying an uncovered count",
			pointer: "/coverage/mutants_uncovered",
			mutate:  func(doc map[string]any) { object(doc, "coverage")["mutants_uncovered"] = 0.0 },
		},
		{
			name:    "mutant with no covering list",
			pointer: "/mutants/0/covering_test_packages",
			mutate:  func(doc map[string]any) { delete(mutant(doc, 0), "covering_test_packages") },
		},
		{
			name:    "covering list is not an array",
			pointer: "/mutants/0/covering_test_packages",
			mutate:  func(doc map[string]any) { mutant(doc, 0)["covering_test_packages"] = "example.com/m" },
		},
		{
			name:    "uncovered is not a boolean",
			pointer: "/mutants/0/uncovered",
			mutate:  func(doc map[string]any) { mutant(doc, 0)["uncovered"] = "no" },
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
		{
			name:    "a measured mutant that says why it was not run",
			pointer: "/mutants/0/not_run_reason",
			mutate:  func(doc map[string]any) { object(doc, "mutants", 0)["not_run_reason"] = "interrupted" },
		},
		{
			name:    "a not-run reason nobody defined",
			pointer: "/mutants/6/not_run_reason",
			mutate:  func(doc map[string]any) { object(doc, "mutants", 6)["not_run_reason"] = "shrugged" },
		},
		{
			name:    "a changed ref that is not a ref",
			pointer: "/selection/changed_ref",
			mutate:  func(doc map[string]any) { object(doc, "selection")["changed_ref"] = "" },
		},
		{
			name:    "a shard whose index is not a shard number",
			pointer: "/shard/index",
			mutate: func(doc map[string]any) {
				doc["shard"] = map[string]any{"index": 0.0, "total": 2.0, "assignment": "id-hash-v1"}
			},
		},
		{
			name:    "a shard assigned by a function nobody has",
			pointer: "/shard/assignment",
			mutate: func(doc map[string]any) {
				doc["shard"] = map[string]any{"index": 1.0, "total": 2.0, "assignment": "positional-v1"}
			},
		},
		{
			name:    "a document that is both a shard and a merge of them",
			pointer: "/shard",
			mutate: func(doc map[string]any) {
				doc["shard"] = map[string]any{"index": 1.0, "total": 2.0, "assignment": "id-hash-v1"}
				doc["merge"] = map[string]any{"shards": 2.0}
			},
		},
		{
			name:    "a merge of no shards",
			pointer: "/merge/shards",
			mutate:  func(doc map[string]any) { doc["merge"] = map[string]any{"shards": 0.0} },
		},
		{
			// An attempt that took less than no time is not a slow measurement,
			// it is a clock nobody may reason about.
			name:    "an execution that took a negative time",
			pointer: "/mutants/1/executions/0/duration_ms",
			mutate:  func(doc map[string]any) { execution(doc, 1, 0)["duration_ms"] = -1.0 },
		},
		{
			// A quantity of memory, so a negative one is not a small budget: it
			// is a number nothing could have measured, and every comparison a
			// consumer makes against it comes out the wrong way round.
			name:    "an execution that reached a negative peak",
			pointer: "/mutants/1/executions/0/peak_memory_bytes",
			mutate:  func(doc map[string]any) { execution(doc, 1, 0)["peak_memory_bytes"] = -1.0 },
		},
		{
			name:    "an execution whose memory_exceeded is not a boolean",
			pointer: "/mutants/1/executions/0/memory_exceeded",
			mutate:  func(doc map[string]any) { execution(doc, 1, 0)["memory_exceeded"] = "yes" },
		},
		{
			// The cross-field rule the whole vocabulary rests on: a bound
			// settles a mutant as killed and as nothing else, so a survivor
			// claiming one describes a pass that both was and was not stopped.
			name: "an execution that survived and says a bound stopped it",
			// The rejection lands on the outcome rather than on the flag,
			// because the flag is what the schema branches on and the outcome is
			// what it then requires.
			pointer: "/mutants/1/executions/0/outcome",
			mutate:  func(doc map[string]any) { execution(doc, 1, 0)["memory_exceeded"] = true },
		},
		{
			name:    "a mutant that survived and says a bound stopped it",
			pointer: "/mutants/1/outcome",
			mutate:  func(doc map[string]any) { mutant(doc, 1)["memory_exceeded"] = true },
		},
		{
			name:    "a run whose memory bound came from nowhere",
			pointer: "/test/memory_source",
			mutate:  func(doc map[string]any) { delete(testFacts(doc), "memory_source") },
		},
		{
			name:    "a run whose memory bound has no number",
			pointer: "/test/memory_bytes",
			mutate:  func(doc map[string]any) { delete(testFacts(doc), "memory_bytes") },
		},
		{
			name: "a run with no bound that reports one anyway",
			// The whole `test` object, because "these two keys may not both be
			// here" is a statement about the object rather than about either of
			// them.
			pointer: "/test",
			mutate:  func(doc map[string]any) { testFacts(doc)["memory_source"] = "unavailable" },
		},
		{
			// `additionalProperties: false` inside the new rows too: a typo'd
			// key is a bug, and the moment it is cheap to catch is before the
			// file is written.
			name:    "an unknown key inside an execution",
			pointer: "/mutants/1/executions/0/attempt_number",
			mutate:  func(doc map[string]any) { execution(doc, 1, 0)["attempt_number"] = 1.0 },
		},
	}

	valid := mutantkit.MustMarshal(t, buildFixture(t))
	if err := schemas.Validate(schemas.RunReportV1, valid); err != nil {
		t.Fatalf("the unedited fixture is already invalid: %v", err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			doc := mutantkit.DecodeJSON(t, valid)
			c.mutate(doc)
			err := schemas.Validate(schemas.RunReportV1, mutantkit.EncodeJSON(t, doc))
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
	return mutantkit.MustMarshal(t, r)
}

// workspace returns the workspace object of a decoded document.
func workspace(doc map[string]any) map[string]any {
	return doc["workspace"].(map[string]any)
}

// mutant returns one row of the decoded mutants array.
func mutant(doc map[string]any, i int) map[string]any {
	return doc["mutants"].([]any)[i].(map[string]any)
}

// testFacts returns the decoded document's `test` object, which is where the
// run's own budgets live.
func testFacts(doc map[string]any) map[string]any {
	return doc["test"].(map[string]any)
}

// execution returns one execution of one decoded mutant.
func execution(doc map[string]any, mutantIndex, i int) map[string]any {
	return mutant(doc, mutantIndex)["executions"].([]any)[i].(map[string]any)
}

// object returns a named object of a decoded document, or, when an index is
// given, one element of a named array.
func object(doc map[string]any, name string, index ...int) map[string]any {
	if len(index) == 0 {
		return doc[name].(map[string]any)
	}
	return doc[name].([]any)[index[0]].(map[string]any)
}
