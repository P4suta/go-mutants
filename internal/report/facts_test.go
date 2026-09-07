// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/schema"
	"github.com/P4suta/go-mutants/trace"
)

// beforeTiming is the run report this build wrote before the run facts were
// added to it, frozen byte for byte.
//
// It is not a golden — nothing regenerates it, and `mise run golden-update`
// must never touch it — because the whole of its value is that it was written
// by an older build. A document that was regenerated alongside the schema
// proves nothing about whether the schema still accepts one that was not.
const beforeTiming = "run-report-v1-before-timing.json"

// TestGoldenDocumentCarriesTimingValidationSnapshotToolchainAndExecutions reads
// the committed document and checks that the five run facts are in it.
//
// It is asserted against the golden file rather than against a freshly built
// report on purpose: [TestGoldenReport] pins the bytes, and this pins what a
// consumer will find in them. A reader who wants to know why a run was slow, or
// how one mutant was actually executed, has to be able to answer it from this
// file with no trace beside it.
func TestGoldenDocumentCarriesTimingValidationSnapshotToolchainAndExecutions(t *testing.T) {
	t.Parallel()

	doc := mutantkit.DecodeJSON(t, testkit.ReadFile(t, testkit.GoldenPath(goldenReport)))

	timing := object(doc, "timing")
	phases, ok := timing["phases"].([]any)
	if !ok || len(phases) == 0 {
		t.Fatalf("timing.phases = %v, want the run's phases in run order", timing["phases"])
	}
	first := phases[0].(map[string]any)
	if first["name"] != fixturePhases[0].Name {
		t.Errorf("the first phase is %v, want %q", first["name"], fixturePhases[0].Name)
	}
	stages, ok := timing["stages"].([]any)
	if !ok || len(stages) == 0 {
		t.Fatalf("timing.stages = %v, want one row per recorded stage", timing["stages"])
	}
	stage := stages[0].(map[string]any)
	for _, key := range []string{"phase", "name", "duration_ms", "result"} {
		if _, present := stage[key]; !present {
			t.Errorf("the first stage carries no %s: %v", key, stage)
		}
	}

	if got := object(doc, "validation")["builds"]; got != json.Number("3") {
		t.Errorf("validation.builds = %v, want the 3 builds the fixture's bisection cost", got)
	}

	snapshot := object(workspace(doc), "snapshot")
	if snapshot["stable_dir"] != true || snapshot["files"] != json.Number("128") {
		t.Errorf("workspace.snapshot = %v, want the fixture's stable copy of 128 files", snapshot)
	}

	toolchain := object(object(doc, "test"), "toolchain")
	if toolchain["go_bin"] != fixtureGoBin || toolchain["version"] != fixtureGoVersionLine {
		t.Errorf("test.toolchain = %v, want %q and %q", toolchain, fixtureGoBin, fixtureGoVersionLine)
	}
	resolved, ok := object(doc, "test")["resolved_command"].([]any)
	if !ok || len(resolved) == 0 || resolved[0] != fixtureGoBin {
		t.Errorf("test.resolved_command = %v, want the argv the run really started, beginning with %q",
			object(doc, "test")["resolved_command"], fixtureGoBin)
	}

	// One mutant the run executed, and one it adopted from the cache. The
	// second is the interesting half: it has attempts, because the run that
	// measured it did, and no executions, because this run performed none.
	executed := mutant(doc, 1)
	executions, ok := executed["executions"].([]any)
	if !ok || len(executions) != 1 {
		t.Fatalf("mutants[1].executions = %v, want one row per attempt", executed["executions"])
	}
	execution := executions[0].(map[string]any)
	want := map[string]any{
		"attempt":     json.Number("1"),
		"worker":      json.Number("0"),
		"outcome":     "survived",
		"duration_ms": json.Number("95"),
	}
	for key, value := range want {
		if execution[key] != value {
			t.Errorf("mutants[1].executions[0].%s = %v, want %v", key, execution[key], value)
		}
	}
	// A survivor was caught by nothing, so the row says nothing rather than
	// naming a binary that is not a name.
	if _, present := execution["killed_by"]; present {
		t.Errorf("the surviving attempt names a killer: %v", execution["killed_by"])
	}
	if binaries, ok := execution["binaries"].([]any); !ok || len(binaries) == 0 {
		t.Errorf("mutants[1].executions[0].binaries = %v, want the binaries the attempt started",
			execution["binaries"])
	}
	cached := mutant(doc, 0)
	if rows, ok := cached["executions"].([]any); !ok || len(rows) != 0 {
		t.Errorf("the cached mutant's executions = %v, want an empty list: this run executed nothing",
			cached["executions"])
	}
	if cached["attempts"] == json.Number("0") {
		t.Error("the cached mutant reports no attempts, so it no longer shows that attempts and executions differ")
	}
}

// TestAnOlderDocumentWithoutTheAdditiveFieldsStillValidates is the compatibility
// claim the whole change rests on.
//
// Every field added here is optional, so a report an older build wrote — with
// no timing, no validation, no snapshot, no toolchain and no executions — is
// still a valid run-report v1 and still readable by this build. The frozen
// document is the evidence: it was produced before any of it existed.
func TestAnOlderDocumentWithoutTheAdditiveFieldsStillValidates(t *testing.T) {
	t.Parallel()

	data := testkit.ReadFile(t, testkit.GoldenPath(beforeTiming))
	doc := mutantkit.DecodeJSON(t, data)
	for _, key := range []string{"timing", "validation"} {
		if _, present := doc[key]; present {
			t.Fatalf("the frozen document already carries %q, so it proves nothing about an older one", key)
		}
	}
	if _, present := workspace(doc)["snapshot"]; present {
		t.Fatal("the frozen document already carries workspace.snapshot")
	}
	if _, present := mutant(doc, 0)["executions"]; present {
		t.Fatal("the frozen document already carries executions")
	}

	if err := schemas.Validate(schemas.RunReportV1, data); err != nil {
		t.Fatalf("a document written before the run facts existed no longer validates: %v", err)
	}
	// And this build still reads it, which is the other half of additive: the
	// decoder refuses unknown fields, so a missing one must stay optional there
	// too.
	r, err := report.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Timing != nil || r.Validation != nil {
		t.Errorf("the older document parsed with timing %v and validation %v, want neither", r.Timing, r.Validation)
	}
}

// TestReportValidateAcceptsADocumentWithTheAdditiveFields is the other
// direction: what this build writes today is what the published schema says it
// may write.
func TestReportValidateAcceptsADocumentWithTheAdditiveFields(t *testing.T) {
	t.Parallel()

	data := mutantkit.MustMarshal(t, buildFixture(t))
	if err := schemas.Validate(schemas.RunReportV1, data); err != nil {
		t.Fatalf("the document this build writes does not satisfy its own schema: %v", err)
	}
	// Through the same door `go-mutants report validate FILE` uses, so the
	// command and the test cannot disagree about what a valid document is.
	if _, err := report.Parse(data); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

// TestBuildRefusesExecutionsOnACachedOrUncoveredMutant states the one thing an
// execution list may never say.
//
// An execution is a pass this run made over the test binaries. A cached mutant
// was answered from a file, an uncovered one was settled by a coverage profile,
// and a not-run one was never reached — so a row of per-attempt evidence
// attached to any of the three would be this run claiming work it did not do,
// in the one document everything else is derived from.
func TestBuildRefusesExecutionsOnACachedOrUncoveredMutant(t *testing.T) {
	t.Parallel()

	one := []report.Execution{{
		Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 95,
	}}
	cases := []struct {
		name    string
		options func(t *testing.T) report.Options
		// which names the result the executions are attached to.
		which func(result report.MutantResult) bool
	}{
		{
			name:    "a cached mutant",
			options: fixtureOptions,
			which:   func(r report.MutantResult) bool { return r.Cached },
		},
		{
			name:    "an uncovered mutant",
			options: coverageOptions,
			which:   func(r report.MutantResult) bool { return r.Uncovered },
		},
		{
			name:    "a mutant the run never reached",
			options: fixtureOptions,
			which:   func(r report.MutantResult) bool { return r.Outcome == mutation.OutcomeNotRun },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := c.options(t)
			attached := ""
			for i, result := range opts.Results {
				if !c.which(result) {
					continue
				}
				opts.Results[i].Executions = one
				attached = result.ID
				break
			}
			if attached == "" {
				t.Fatal("the fixture no longer holds such a mutant, so this case is checking nothing")
			}
			r, err := report.Build(opts)
			if err == nil {
				t.Fatalf("Build accepted it and returned a report with %d mutants", len(r.Mutants))
			}
			if got := report.CodeOf(err); got != report.CodeInvalidExecutions {
				t.Fatalf("code = %q, want %q (%v)", got, report.CodeInvalidExecutions, err)
			}
			if !strings.Contains(err.Error(), attached[:mutation.DisplayIDLength]) {
				t.Errorf("the refusal does not name the mutant: %v", err)
			}
		})
	}
}

// TestExecutionsCountEqualsAttempts pins the invariant that keeps one document
// from stating two different numbers of attempts.
//
// `attempts` was the whole of what a report said about how a mutant was run,
// and it stays: consumers count it, the cache stores it, and the console prints
// it. The execution rows are the same fact in detail, so a document where the
// two disagree is a document one of whose halves is wrong, and Build refuses it
// rather than publishing the pair.
func TestExecutionsCountEqualsAttempts(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	executed := 0
	for _, m := range r.Mutants {
		if len(m.Executions) == 0 {
			continue
		}
		executed++
		if len(m.Executions) != m.Attempts {
			t.Errorf("mutant %s reports %d attempts and %d executions",
				m.DisplayID, m.Attempts, len(m.Executions))
		}
		for i, execution := range m.Executions {
			if execution.Attempt != i+1 {
				t.Errorf("mutant %s: executions[%d].attempt = %d, want %d: the rows are in attempt order",
					m.DisplayID, i, execution.Attempt, i+1)
			}
		}
	}
	if executed == 0 {
		t.Fatal("no mutant of the fixture carries executions, so this test is checking nothing")
	}

	opts := fixtureOptions(t)
	broken := ""
	for i, result := range opts.Results {
		if len(result.Executions) == 0 {
			continue
		}
		opts.Results[i].Executions = append(slices.Clone(result.Executions), report.Execution{
			Attempt: len(result.Executions) + 1, Outcome: report.OutcomeSurvived,
		})
		broken = result.ID
		break
	}
	if broken == "" {
		t.Fatal("the fixture no longer supplies executions, so the refusal is checking nothing")
	}
	if _, err := report.Build(opts); report.CodeOf(err) != report.CodeInvalidExecutions {
		t.Fatalf("Build over %d executions and one attempt = %v, want %q",
			len(opts.Results), err, report.CodeInvalidExecutions)
	}
}

// TestMergeOmitsTimingValidationSnapshotToolchainAndExecutions is the rule that
// keeps a merged document honest.
//
// Every fact this change adds describes one run: how long its phases took, how
// many builds its validation spent, whose toolchain it used, which worker
// executed which mutant. A merge of four shards is four runs on four machines,
// so there is no single answer to give — and a merged document that reported
// the first shard's would be quoting one machine's clock as though it were the
// run's. It says nothing instead, which is the one honest answer.
func TestMergeOmitsTimingValidationSnapshotToolchainAndExecutions(t *testing.T) {
	t.Parallel()

	set := shards(t, 3)
	for i, shard := range set {
		if shard.Timing == nil || shard.Validation == nil {
			t.Fatalf("shard %d carries no run facts, so the merge has nothing to drop", i+1)
		}
	}
	merged := mergeShards(t, set)

	if merged.Timing != nil {
		t.Errorf("the merged document reports timing %+v", merged.Timing)
	}
	if merged.Validation != nil {
		t.Errorf("the merged document reports validation %+v", merged.Validation)
	}
	if merged.Workspace.Snapshot != nil {
		t.Errorf("the merged document reports a snapshot %+v", merged.Workspace.Snapshot)
	}
	if merged.Test.Toolchain != nil {
		t.Errorf("the merged document reports a toolchain %+v", merged.Test.Toolchain)
	}
	if merged.Test.ResolvedCommand != nil {
		t.Errorf("the merged document reports a resolved command %v", merged.Test.ResolvedCommand)
	}
	for _, m := range merged.Mutants {
		if m.Executions != nil {
			t.Errorf("mutant %s carries %d executions in the merged document", m.DisplayID, len(m.Executions))
		}
	}

	// Absent rather than null or empty, so that a consumer's decoder sees a
	// missing key and not a measurement of zero.
	//
	// The keys are looked up where they would be rather than searched for in
	// the bytes: `snapshot` and `toolchain` are also the names of two stages, so
	// a substring search for either finds a timeline that is still there and
	// calls it a merged document that carries a snapshot.
	doc := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, merged))
	absent := [][]string{
		{"timing"},
		{"validation"},
		{"workspace", "snapshot"},
		{"test", "toolchain"},
		{"test", "resolved_command"},
		{"coverage", "unavailable_reason"},
		{"coverage", "build_fallback"},
	}
	for _, keys := range absent {
		if value, present := at(doc, keys...); present {
			t.Errorf("the merged document carries /%s = %v", strings.Join(keys, "/"), value)
		}
	}
	for i := range merged.Mutants {
		if value, present := at(doc, "mutants", strconv.Itoa(i), "executions"); present {
			t.Errorf("the merged document carries /mutants/%d/executions = %v", i, value)
		}
	}
	// And the document it was built from does carry them, or the absences above
	// would be a statement about the fixture rather than about the merge.
	shard := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, set[0]))
	for _, keys := range absent {
		if _, present := at(shard, keys...); !present {
			t.Errorf("a shard document does not carry /%s either", strings.Join(keys, "/"))
		}
	}
}

// at resolves a path of keys in a decoded document, reporting whether it is
// there. An array index is a key like any other, spelled as a number.
func at(doc map[string]any, keys ...string) (any, bool) {
	var node any = doc
	for _, key := range keys {
		switch current := node.(type) {
		case map[string]any:
			child, ok := current[key]
			if !ok {
				return nil, false
			}
			node = child
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(current) {
				return nil, false
			}
			node = current[index]
		default:
			return nil, false
		}
	}
	return node, true
}

// TestAnExecutionOutcomeIsAnObservationAndNotAVerdict holds the narrower enum
// to the two places that have to agree about it.
//
// A row of `executions` is what one pass over the test binaries saw, and two of
// the six outcomes are not that: `inconclusive` is what a timeout and a retry
// that disagree come to, and `not-run` is what a mutant nobody measured is.
// Both are judgements about several passes, or about none, so a row claiming
// either would be a verdict wearing an observation's clothes — and the schema
// and [report.Build] refuse it from opposite directions.
func TestAnExecutionOutcomeIsAnObservationAndNotAVerdict(t *testing.T) {
	t.Parallel()

	valid := mutantkit.MustMarshal(t, buildFixture(t))
	for _, outcome := range report.Observations() {
		doc := mutantkit.DecodeJSON(t, valid)
		execution(doc, 1, 0)["outcome"] = string(outcome)
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.EncodeJSON(t, doc)); err != nil {
			t.Errorf("the schema rejects %q, which is something one pass can observe: %v", outcome, err)
		}
	}
	for _, verdict := range []report.Outcome{report.OutcomeInconclusive, report.OutcomeNotRun} {
		if verdict.Observed() {
			t.Errorf("%q is listed as something one pass can observe", verdict)
		}
		doc := mutantkit.DecodeJSON(t, valid)
		execution(doc, 1, 0)["outcome"] = string(verdict)
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.EncodeJSON(t, doc)); err == nil {
			t.Errorf("the schema accepted an execution that observed %q", verdict)
		}
	}

	opts := fixtureOptions(t)
	broken := false
	for i, result := range opts.Results {
		if len(result.Executions) == 0 {
			continue
		}
		rows := slices.Clone(result.Executions)
		rows[0].Outcome = report.OutcomeInconclusive
		opts.Results[i].Executions = rows
		broken = true
		break
	}
	if !broken {
		t.Fatal("the fixture supplies no executions, so the refusal is checking nothing")
	}
	if _, err := report.Build(opts); report.CodeOf(err) != report.CodeInvalidExecutions {
		t.Fatalf("Build over an inconclusive execution = %v, want %q", err, report.CodeInvalidExecutions)
	}
}

// TestStageResultVocabularyIsTheTraceVocabulary holds the report's stage
// results to the recording's.
//
// A stage that `succeeded` in a trace and `ok` in a report would be two words
// for one fact, and a reader joining the two documents would have to learn a
// mapping that exists for no reason. The constants are the trace's own, and this
// is the assertion that the published enum is too.
func TestStageResultVocabularyIsTheTraceVocabulary(t *testing.T) {
	t.Parallel()

	want := []string{trace.ResultSucceeded, trace.ResultFailed, trace.ResultSkipped}
	got := make([]string, 0, len(report.StageResults()))
	for _, result := range report.StageResults() {
		got = append(got, string(result))
	}
	if !slices.Equal(got, want) {
		t.Errorf("report.StageResults() = %v, want the trace's %v", got, want)
	}
	if published := stageResultEnum(t); !slices.Equal(published, want) {
		t.Errorf("the schema's stage result enum = %v, want the trace's %v", published, want)
	}
}

// stageResultEnum reads the published enumeration out of the schema itself,
// rather than out of a copy of it typed into this test.
func stageResultEnum(t *testing.T) []string {
	t.Helper()

	data, err := schema.FS.ReadFile("run-report-v1.schema.json")
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	var document struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decoding the schema: %v", err)
	}
	stage, ok := document.Defs["stage_timing"]
	if !ok {
		t.Fatal("the schema has no stage_timing definition")
	}
	return stage.Properties["result"].Enum
}

// TestAnExecutionCarriesWhatItCostAndWhetherABoundStoppedIt pins the two
// additive fields a bounded run writes.
//
// They are tested at the document rather than through [report.Build] because
// the claim is about the published contract: the schema accepts them where they
// belong, this build reads them back, and the outcome beside them is still one
// of the four an observation may be. A bound is a budget on evidence and not a
// new kind of verdict, so `memory_exceeded` had to be a fact next to `killed`
// rather than a fifth word in the enum, and that is exactly what a consumer
// would break if somebody widened the vocabulary instead.
func TestAnExecutionCarriesWhatItCostAndWhetherABoundStoppedIt(t *testing.T) {
	t.Parallel()

	data := mutantkit.MustMarshal(t, buildFixture(t))
	doc := mutantkit.DecodeJSON(t, data)

	rows, ok := mutant(doc, 1)["executions"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("mutants[1] carries no executions to work with: %v", mutant(doc, 1)["executions"])
	}
	row, _ := rows[0].(map[string]any)
	if row["peak_rss_bytes"] == nil {
		t.Error("an ordinary execution row carries no peak_rss_bytes, and every execution started a process")
	}

	// A kill by the bound, written where a real one would be written.
	row["outcome"] = "killed"
	row["killed_by"] = alphaPackage
	row["memory_exceeded"] = true
	bounded := mutantkit.EncodeJSON(t, doc)
	if err := schemas.Validate(schemas.RunReportV1, bounded); err != nil {
		t.Fatalf("a killed execution carrying memory_exceeded was rejected: %v", err)
	}
	parsed, err := report.Parse(bounded)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := parsed.Mutants[1].Executions[0]
	if !got.MemoryExceeded {
		t.Error("memory_exceeded did not survive the round trip")
	}
	if got.Outcome != report.OutcomeKilled {
		t.Errorf("outcome = %s, want killed: a bound settles a mutant with the vocabulary that already exists", got.Outcome)
	}
	if got.PeakRSSBytes <= 0 {
		t.Errorf("peak_rss_bytes = %d, want what the execution reached", got.PeakRSSBytes)
	}
}

// TestAnExecutionWrittenBeforeTheBoundExistedStillValidates is the additive
// half of the same claim, from the other side.
//
// The frozen document [TestAnOlderDocumentWithoutTheAdditiveFieldsStillValidates]
// reads has no `executions` at all, so it cannot say anything about a row
// written before these two fields were added to one. This does: a row with
// neither key is a row this schema accepts and this build parses, which is what
// "additive" has to mean for a consumer holding last month's reports.
func TestAnExecutionWrittenBeforeTheBoundExistedStillValidates(t *testing.T) {
	t.Parallel()

	doc := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, buildFixture(t)))
	stripped := 0
	for i := range doc["mutants"].([]any) {
		rows, _ := mutant(doc, i)["executions"].([]any)
		for _, r := range rows {
			row, _ := r.(map[string]any)
			delete(row, "memory_exceeded")
			delete(row, "peak_rss_bytes")
			stripped++
		}
	}
	if stripped == 0 {
		t.Fatal("the fixture holds no execution rows, so nothing was stripped")
	}

	older := mutantkit.EncodeJSON(t, doc)
	if err := schemas.Validate(schemas.RunReportV1, older); err != nil {
		t.Fatalf("an execution row without the memory fields no longer validates: %v", err)
	}
	parsed, err := report.Parse(older)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, m := range parsed.Mutants {
		for _, execution := range m.Executions {
			if execution.MemoryExceeded || execution.PeakRSSBytes != 0 {
				t.Errorf("a stripped row parsed as %+v, want both memory fields at their zero", execution)
			}
		}
	}
}

// TestTheDocumentSaysWhatTheMemoryBoundWasAndWhereItCameFrom pins the run fact
// the execution rows are meaningless without.
//
// `memory_exceeded` on a row says a bound stopped that pass; without the bound
// itself, a reader looking at a report from somebody else's CI has no way to
// tell a runaway mutant from a budget somebody set too tight. The pair is
// written together and read together, exactly as `timeout_ms` and
// `timeout_source` are.
func TestTheDocumentSaysWhatTheMemoryBoundWasAndWhereItCameFrom(t *testing.T) {
	t.Parallel()

	data := mutantkit.MustMarshal(t, buildFixture(t))
	doc := mutantkit.DecodeJSON(t, data)
	test, _ := doc["test"].(map[string]any)
	if test == nil {
		t.Fatal("the document carries no test facts")
	}
	if got := test["memory_bytes"]; got == nil {
		t.Error("the document says nothing about the memory bound the executions were measured under")
	}
	if got, want := test["memory_source"], "derived"; got != want {
		t.Errorf("memory_source = %v, want %q", got, want)
	}

	// And a document that says nothing about memory is still a document: both
	// keys are optional, so a report an older build wrote — or one from a run
	// that bounded nothing and had no reason to say so — still validates.
	delete(test, "memory_bytes")
	delete(test, "memory_source")
	silent := mutantkit.EncodeJSON(t, doc)
	if err := schemas.Validate(schemas.RunReportV1, silent); err != nil {
		t.Fatalf("a document with no memory facts no longer validates: %v", err)
	}
	parsed, err := report.Parse(silent)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Test.MemoryBytes != 0 || parsed.Test.MemorySource != "" {
		t.Errorf("a silent document parsed as %d (%q), want both at their zero",
			parsed.Test.MemoryBytes, parsed.Test.MemorySource)
	}
}
