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

const beforeTiming = "run-report-v1-before-timing.json"

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
	r, err := report.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Timing != nil || r.Validation != nil {
		t.Errorf("the older document parsed with timing %v and validation %v, want neither", r.Timing, r.Validation)
	}
}

func TestReportValidateAcceptsADocumentWithTheAdditiveFields(t *testing.T) {
	t.Parallel()

	data := mutantkit.MustMarshal(t, buildFixture(t))
	if err := schemas.Validate(schemas.RunReportV1, data); err != nil {
		t.Fatalf("the document this build writes does not satisfy its own schema: %v", err)
	}
	if _, err := report.Parse(data); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestBuildRefusesExecutionsOnACachedOrUncoveredMutant(t *testing.T) {
	t.Parallel()

	one := []report.Execution{{
		Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 95,
	}}
	cases := []struct {
		name    string
		options func(t *testing.T) report.Options
		which   func(result report.MutantResult) bool
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
	shard := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, set[0]))
	for _, keys := range absent {
		if _, present := at(shard, keys...); !present {
			t.Errorf("a shard document does not carry /%s either", strings.Join(keys, "/"))
		}
	}
}

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

func TestAnExecutionCarriesWhatItCostAndWhetherABoundStoppedIt(t *testing.T) {
	t.Parallel()

	data := mutantkit.MustMarshal(t, buildFixture(t))
	doc := mutantkit.DecodeJSON(t, data)

	rows, ok := mutant(doc, 1)["executions"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("mutants[1] carries no executions to work with: %v", mutant(doc, 1)["executions"])
	}
	row, _ := rows[0].(map[string]any)
	if row["peak_memory_bytes"] == nil {
		t.Error("an ordinary execution row carries no peak_memory_bytes, and every execution started a process")
	}

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
	if got.PeakMemoryBytes <= 0 {
		t.Errorf("peak_memory_bytes = %d, want what the execution reached", got.PeakMemoryBytes)
	}
}

func TestAnExecutionWrittenBeforeTheBoundExistedStillValidates(t *testing.T) {
	t.Parallel()

	doc := mutantkit.DecodeJSON(t, mutantkit.MustMarshal(t, buildFixture(t)))
	stripped := 0
	for i := range doc["mutants"].([]any) {
		rows, _ := mutant(doc, i)["executions"].([]any)
		for _, r := range rows {
			row, _ := r.(map[string]any)
			delete(row, "memory_exceeded")
			delete(row, "peak_memory_bytes")
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
			if execution.MemoryExceeded || execution.PeakMemoryBytes != 0 {
				t.Errorf("a stripped row parsed as %+v, want both memory fields at their zero", execution)
			}
		}
	}
}

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

func TestAMutantSaysWhatItCostWithoutItsExecutionRows(t *testing.T) {
	t.Parallel()

	data := mutantkit.MustMarshal(t, buildFixture(t))
	doc := mutantkit.DecodeJSON(t, data)

	executed := mutant(doc, 1)
	if executed["peak_memory_bytes"] == nil {
		t.Error("an executed mutant says nothing about what it cost")
	}
	rows, _ := executed["executions"].([]any)
	var highest float64
	for _, r := range rows {
		row, _ := r.(map[string]any)
		if row["peak_memory_bytes"] == nil {
			continue
		}
		value, err := row["peak_memory_bytes"].(json.Number).Float64()
		if err != nil {
			t.Fatalf("peak_memory_bytes is not a number: %v", err)
		}
		highest = max(highest, value)
	}
	got, err := executed["peak_memory_bytes"].(json.Number).Float64()
	if err != nil {
		t.Fatalf("peak_memory_bytes is not a number: %v", err)
	}
	if got != highest {
		t.Errorf("the mutant reports %v, want the %v its rows do", got, highest)
	}

	for i := range doc["mutants"].([]any) {
		m := mutant(doc, i)
		delete(m, "peak_memory_bytes")
		delete(m, "memory_exceeded")
	}
	silent := mutantkit.EncodeJSON(t, doc)
	if err := schemas.Validate(schemas.RunReportV1, silent); err != nil {
		t.Fatalf("a document with no mutant-level memory facts no longer validates: %v", err)
	}
	if _, err := report.Parse(silent); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestACachedMemoryKillCarriesItsFactsWithNoRowsUnderIt(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	for i := range opts.Results {
		if !opts.Results[i].Cached {
			continue
		}
		opts.Results[i].MemoryExceeded = true
		opts.Results[i].PeakMemory = 3435973836
		break
	}
	built, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var cached *report.Mutant
	for i := range built.Mutants {
		if built.Mutants[i].Cached {
			cached = &built.Mutants[i]
			break
		}
	}
	if cached == nil {
		t.Fatal("the fixture has no cached mutant")
	}
	if len(cached.Executions) != 0 {
		t.Fatalf("a cached mutant carries %d execution rows, so this proves nothing", len(cached.Executions))
	}
	if !cached.MemoryExceeded {
		t.Error("a cached memory kill does not say the bound settled it")
	}
	if cached.PeakMemoryBytes != 3435973836 {
		t.Errorf("PeakMemoryBytes = %d, want what the measuring run recorded", cached.PeakMemoryBytes)
	}
	if err := schemas.Validate(schemas.RunReportV1, mutantkit.MustMarshal(t, built)); err != nil {
		t.Fatalf("the document does not satisfy its own schema: %v", err)
	}
}

func TestBuildRefusesAMemoryBoundThatContradictsItsSource(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		memory int64
		source report.MemorySource
		ok     bool
	}{
		{"neither, as a caller that predates the bound writes", 0, "", true},
		{"no bound, and it says so", 0, report.MemoryUnavailable, true},
		{"a derived bound with its number", 1 << 30, report.MemoryDerived, true},
		{"an explicit bound with its number", 2 << 30, report.MemoryExplicit, true},
		{"derived, with no number to have derived", 0, report.MemoryDerived, false},
		{"explicit, with no number the user wrote", 0, report.MemoryExplicit, false},
		{"no bound, and a number beside it", 1 << 30, report.MemoryUnavailable, false},
		{"a number with nothing saying where it came from", 1 << 30, "", false},
		{"a negative bound", -1, report.MemoryExplicit, false},
		{"a source that is not one", 1 << 30, report.MemorySource("plenty"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.Memory = c.memory
			opts.MemorySource = c.source
			_, err := report.Build(opts)
			switch {
			case c.ok && err != nil:
				t.Errorf("Build refused %d (%q): %v", c.memory, c.source, err)
			case !c.ok && err == nil:
				t.Errorf("Build accepted %d (%q)", c.memory, c.source)
			}
		})
	}
}

func TestBuildRefusesAMemoryKillThatIsNotAKill(t *testing.T) {
	t.Parallel()

	for _, outcome := range []report.Outcome{
		report.OutcomeSurvived, report.OutcomeTimedOut, report.OutcomeErrored,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			opts := fixtureOptions(t)
			marked := false
			for i := range opts.Results {
				rows := slices.Clone(opts.Results[i].Executions)
				for j := range rows {
					if rows[j].Outcome != outcome {
						continue
					}
					rows[j].MemoryExceeded = true
					opts.Results[i].Executions = rows
					marked = true
					break
				}
				if marked {
					break
				}
			}
			if !marked {
				t.Skipf("the fixture holds no %s execution row", outcome)
			}
			if _, err := report.Build(opts); err == nil {
				t.Errorf("Build accepted a %s row claiming a memory bound settled it", outcome)
			}
		})
	}
}

func TestAMutantSaysWhetherACountedLoopSettledIt(t *testing.T) {
	t.Parallel()

	t.Run("a mutant this run executed says what its rows say", func(t *testing.T) {
		t.Parallel()

		opts := fixtureOptions(t)
		var target string
		for i := range opts.Results {
			if len(opts.Results[i].Executions) == 0 || opts.Results[i].Outcome != mutation.OutcomeTimedOut {
				continue
			}
			opts.Results[i].Executions[0].Diverged = true
			opts.Results[i].Diverged = false
			target = opts.Results[i].ID
			break
		}
		if target == "" {
			t.Fatal("the fixture has no executed timeout, so this proves nothing")
		}

		built, err := report.Build(opts)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		m := mutantWithID(t, built, target)
		if !m.Diverged {
			t.Error("a mutant whose row says a counted loop settled it does not say so itself")
		}
		if !m.Executions[0].Diverged {
			t.Error("the row itself lost the fact on the way into the document")
		}
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.MustMarshal(t, built)); err != nil {
			t.Fatalf("the document does not satisfy its own schema: %v", err)
		}
	})

	t.Run("a cached mutant says it with no rows under it", func(t *testing.T) {
		t.Parallel()

		opts := fixtureOptions(t)
		var target string
		for i := range opts.Results {
			if !opts.Results[i].Cached {
				continue
			}
			opts.Results[i].Outcome = mutation.OutcomeTimedOut
			opts.Results[i].Diverged = true
			target = opts.Results[i].ID
			break
		}
		if target == "" {
			t.Fatal("the fixture has no cached mutant")
		}

		built, err := report.Build(opts)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		m := mutantWithID(t, built, target)
		if len(m.Executions) != 0 {
			t.Fatalf("a cached mutant carries %d rows, so this proves nothing", len(m.Executions))
		}
		if !m.Diverged {
			t.Error("a cached divergence lost the fact the measuring run recorded")
		}
		if err := schemas.Validate(schemas.RunReportV1, mutantkit.MustMarshal(t, built)); err != nil {
			t.Fatalf("the document does not satisfy its own schema: %v", err)
		}
	})

	t.Run("a mutant no loop settled says nothing", func(t *testing.T) {
		t.Parallel()

		built, err := report.Build(fixtureOptions(t))
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		for _, m := range built.Mutants {
			if m.Diverged {
				t.Errorf("mutant %s claims a counted loop settled it", m.DisplayID)
			}
			for _, execution := range m.Executions {
				if execution.Diverged {
					t.Errorf("attempt %d of %s claims a counted loop settled it", execution.Attempt, m.DisplayID)
				}
			}
		}
	})
}

func mutantWithID(t *testing.T, built *report.Report, id string) report.Mutant {
	t.Helper()
	for _, m := range built.Mutants {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("the built document holds no mutant %s", id)
	return report.Mutant{}
}
