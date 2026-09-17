//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/app"
	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/report"
	"github.com/P4suta/go-mutants/goatest/internal/testkit"
	"github.com/P4suta/go-mutants/goatest/internal/trace"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const traceSchemaURL = "https://goatest.invalid/goatest-trace-v1.schema.json"

func validateTraceStream(t *testing.T, directory string) {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(trace.JSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(traceSchemaURL, document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(traceSchemaURL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for index, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatalf("trace line %d is not JSON: %v", index+1, err)
		}
		if err := compiled.Validate(instance); err != nil {
			t.Errorf("trace line %d was rejected by the schema: %v", index+1, err)
		}
	}
}

func TestTracedVerifyRecordsThePhasesCommandsAndRoutesOfARealRun(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).BoundaryFixture().Git()
	directory := filepath.Join(t.TempDir(), "trace")
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}
	var stdout, stderr bytes.Buffer
	exit := cli.Run(t.Context(), []string{"verify", "--json", "--trace=" + directory}, &stdout, &stderr, service)
	if exit != cli.ExitAssured {
		t.Fatalf("verify exit = %d\nstdout: %s\nstderr: %s", exit, stdout.String(), stderr.String())
	}
	var result report.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != report.VerdictAssured {
		t.Fatalf("report = %+v", result)
	}

	recording := traceRun(t, directory)
	validateTraceStream(t, recording)
	events := readTrace(t, recording)
	if len(events) < minimumTraceLifecycleEvents || events[0].Type != trace.TypeRunStart || events[0].Schema != trace.SchemaV1 {
		t.Fatalf("recorded %d events beginning %+v", len(events), events[0])
	}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil || last.Run.Verdict != string(result.Verdict) || last.Run.Error != "" {
		t.Fatalf("run-end = %+v", last)
	}
	if last.Run.EventsDropped != 0 || last.Run.EventsEmitted != int64(len(events))-1 {
		t.Fatalf("accounting = %+v of %d events", last.Run, len(events))
	}

	var open string
	var phases []string
	for _, event := range events {
		switch event.Type {
		case trace.TypePhaseStart:
			if open != "" {
				t.Fatalf("phase %q began while %q was still open", event.Phase.Name, open)
			}
			open = event.Phase.Name
			phases = append(phases, open)
		case trace.TypePhaseEnd:
			if open != event.Phase.Name {
				t.Fatalf("phase-end %q closed while %q was open", event.Phase.Name, open)
			}
			open = ""
		}
	}
	if open != "" {
		t.Fatalf("phase %q was never ended", open)
	}
	for _, name := range []string{"snapshot", "discover", "baseline", "probe", "mutation", "finalize"} {
		if !slices.Contains(phases, name) {
			t.Errorf("phase %q is absent from %v", name, phases)
		}
	}
	if slices.Contains(phases, "mutation-prepare") {
		t.Errorf("mutation preparation was recorded as a linear phase: %v", phases)
	}
	prepares := traceOfType(events, trace.TypePrepare)
	if len(prepares) == 0 || !slices.ContainsFunc(prepares, func(event trace.Event) bool {
		return event.Prepare.State == trace.PrepareStateStarted
	}) || !slices.ContainsFunc(prepares, func(event trace.Event) bool {
		return event.Prepare.State == trace.PrepareStateFinished
	}) {
		t.Errorf("prepare spans = %+v", prepares)
	}

	if probes := slices.Index(phases, "probe"); probes < 1 || probes+1 >= len(phases) ||
		phases[probes-1] != "race" || phases[probes+1] != "mutation" {
		t.Errorf("probe phase sits at %d of %v", probes, phases)
	}
	var measured int
	measuredProbes := make(map[string][]string)
	for _, event := range traceOfType(events, trace.TypeProbeExec) {
		if event.Probe.Target == "" {
			t.Errorf("probe %+v names no target", event.Probe)
		}
		if (event.Probe.Outcome == "") == (event.Probe.Error == "") {
			t.Errorf("probe %+v reached neither an outcome nor an error, or both", event.Probe)
		}
		if !event.Probe.Control && event.Probe.Outcome == trace.ProbeOutcomeMeasured {
			measured++
			measuredProbes[event.Probe.Target] = event.Probe.Infected
		}
	}
	if measured == 0 {
		t.Error("no target was measured against the probe tree")
	}

	var executed [][]string
	for _, event := range traceOfType(events, trace.TypeExec) {
		executed = append(executed, event.Exec.Argv)
		if event.Exec.OutputPath == "" {
			continue
		}
		preserved := filepath.Join(recording, filepath.FromSlash(event.Exec.OutputPath))
		if info, err := os.Stat(preserved); err != nil || info.IsDir() {
			t.Errorf("preserved output %s = %v", event.Exec.OutputPath, err)
		}
	}
	for _, argv := range [][]string{{"go", "list"}} {
		if !slices.ContainsFunc(executed, func(candidate []string) bool {
			return len(candidate) >= len(argv) && slices.Equal(candidate[:len(argv)], argv)
		}) {
			t.Errorf("no exec event ran %v: %v", argv, executed)
		}
	}

	routed := map[string]int{}
	blockRouted := 0
	for _, event := range traceOfType(events, trace.TypeRoute) {
		if event.Route.Reason != trace.ReasonCoverageReaching &&
			event.Route.Reason != trace.ReasonProbeReaching && event.Route.Reason != trace.ReasonUnreached {
			t.Errorf("route %+v has no reason", event.Route)
		}

		if !routeHasPlanOrProof(*event.Route, measuredProbes) {
			t.Errorf("route %+v has no plan", event.Route)
		}
		if event.Route.Granularity != trace.GranularityBlock && event.Route.Granularity != trace.GranularityFile {
			t.Errorf("route %+v has no granularity", event.Route)
		}
		if event.Route.Granularity == trace.GranularityBlock && event.Route.Fallback == "" {
			blockRouted++
		}
		routed[event.Route.MutantID] = int(event.Seq)
	}

	if blockRouted == 0 {
		t.Error("no mutant was routed by the coverage blocks that contain it")
	}
	if selected := result.Accounting.Mutants.Selected; len(routed) != selected || selected == 0 {
		t.Fatalf("routed %d mutants, report selected %d", len(routed), selected)
	}
	for _, event := range traceOfType(events, trace.TypeMutantExec) {
		sequence, ok := routed[event.Mutant.ID]
		if !ok {
			t.Errorf("mutant %s ran unrouted", event.Mutant.ID)
			continue
		}
		if int64(sequence) > event.Seq {
			t.Errorf("mutant %s was routed at %d, after it ran at %d", event.Mutant.ID, sequence, event.Seq)
		}
	}
	assertInfectionDischargesAreSelfConsistent(t, result, events)
}

func assertInfectionDischargesAreSelfConsistent(t *testing.T, result report.Report, events []trace.Event) {
	t.Helper()
	measured := make(map[string][]string)
	for _, event := range traceOfType(events, trace.TypeProbeExec) {
		if event.Probe.Outcome == trace.ProbeOutcomeMeasured {
			measured[event.Probe.Target] = event.Probe.Infected
		}
	}
	named := make(map[string]string, len(result.Targets))
	for _, target := range result.Targets {
		named[target.ID] = target.Name
	}
	discharges := 0
	for _, event := range traceOfType(events, trace.TypeRoute) {
		for _, discharge := range event.Route.Discharged {
			if discharge.Reason != trace.DischargeNeverInfected {
				continue
			}
			discharges++
			if !event.Route.Probed {
				t.Errorf("route %+v discharged %s without a probe form of the mutant", event.Route, discharge.Target)
			}
			infected, ran := measured[discharge.Target]
			if !ran {
				t.Errorf("route %+v discharged %s, which the pass never measured", event.Route, discharge.Target)
				continue
			}
			if slices.Contains(infected, event.Route.MutantID) {
				t.Errorf("route %+v discharged %s, which the pass saw make its site differ", event.Route, discharge.Target)
			}
			for _, arguments := range mutantArguments(events, event.Route.MutantID) {
				if slices.Contains(selectedTests(arguments), named[discharge.Target]) {
					t.Errorf("the discharged target ran anyway: %s", arguments)
				}
			}
		}
	}
	if discharges == 0 {
		t.Error("no reaching target was discharged as never-infected")
	}
}

func selectedTests(arguments string) []string {
	for _, argument := range strings.Fields(arguments) {
		pattern, selective := strings.CutPrefix(argument, "-test.run=")
		if !selective {
			continue
		}
		pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
		pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "("), ")")
		return strings.Split(pattern, "|")
	}
	return nil
}

func oneRoute(t *testing.T, events []trace.Event, rule string) trace.RouteRecord {
	t.Helper()
	var found []trace.RouteRecord
	for _, event := range traceOfType(events, trace.TypeRoute) {
		if event.Route.Rule == rule {
			found = append(found, *event.Route)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the recording holds %d routes for %s, want one: %+v", len(found), rule, found)
	}
	return found[0]
}

func mutantArguments(events []trace.Event, id string) []string {
	var arguments []string
	for _, event := range traceOfType(events, trace.TypeMutantExec) {
		if event.Mutant.ID == id {
			arguments = append(arguments, strings.Join(event.Mutant.Args, " "))
		}
	}
	return arguments
}

func TestTracedVerifyDischargesTheTestsThatNeverTakeANarrowedBranch(t *testing.T) {
	t.Parallel()
	repository := testkit.NewRepo(t).NarrowedBranchFixture().Git()
	directory := filepath.Join(t.TempDir(), "trace")
	service := app.Service{
		Root: repository.Root(), GoBinary: testkit.GoBinary(t), TempDirectory: t.TempDir(),
		Environment: os.Environ(),
	}
	var stdout, stderr bytes.Buffer

	exit := cli.Run(t.Context(), []string{"verify", "--json", "--trace=" + directory}, &stdout, &stderr, service)
	if exit != cli.ExitInsufficient {
		t.Fatalf("verify exit = %d\nstdout: %s\nstderr: %s", exit, stdout.String(), stderr.String())
	}
	var result report.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	recording := traceRun(t, directory)
	validateTraceStream(t, recording)
	events := readTrace(t, recording)

	identified := make(map[string]string, len(result.Targets))
	for _, target := range result.Targets {
		identified[target.Name] = target.ID
	}

	clamp := oneRoute(t, events, "le-to-lt")
	if clamp.Reason != trace.ReasonCoverageReaching || clamp.Granularity != trace.GranularityBlock {
		t.Fatalf("clamp route = %+v, want a route decided by coverage blocks", clamp)
	}
	// TestClampAbove is discharged for the reason this test is named after, and
	// it is asserted by presence rather than by being the only one.
	//
	// The engine discharges more than it used to. Against the pinned version
	// this was exactly one entry; against the engine in this workspace a second
	// target comes back `never-infected`, because a probe observed that the
	// mutated value never differs from the original there. That is the engine
	// getting better at the same question, and a test that demanded a list of
	// length one would refuse the improvement while reporting it as a
	// regression -- which is what it did the first time both products were
	// built from one tree, and is the kind of thing two repositories could not
	// show anybody.
	//
	// What the reasons may be is still closed: trace.DischargeReasons() is the
	// vocabulary, and a reason outside it is a failure here.
	if !slices.ContainsFunc(clamp.Discharged, func(d trace.Discharge) bool {
		return d.Target == identified["TestClampAbove"] && d.Reason == trace.DischargeBranchNeverTaken
	}) {
		t.Fatalf("clamp route discharged %+v, want TestClampAbove for %s",
			clamp.Discharged, trace.DischargeBranchNeverTaken)
	}
	for _, discharge := range clamp.Discharged {
		if !slices.Contains(trace.DischargeReasons(), discharge.Reason) {
			t.Errorf("clamp route discharged a target for %q, which is not a reason this vocabulary has",
				discharge.Reason)
		}
	}
	// Reaching and discharged partition the targets that executed the block,
	// and the partition is what this asserts rather than either half's members.
	//
	// A list of two was right against the pinned engine and is wrong against
	// this one, for the same reason the discharge list grew: every target the
	// engine can rule out is a target that no longer reaches. Asserting the
	// members would make a better engine look like a broken runner. Asserting
	// the partition says the thing that has to stay true however good the
	// engine gets -- nothing is in both, nothing that executed is in neither,
	// and TestClampAbove is on the discharged side.
	discharged := make(map[string]bool, len(clamp.Discharged))
	for _, d := range clamp.Discharged {
		discharged[d.Target] = true
	}
	if len(clamp.ReachingTargets) == 0 {
		t.Fatalf("clamp route reaches nothing and was still run; discharged %+v", clamp.Discharged)
	}
	for _, target := range clamp.ReachingTargets {
		if discharged[target] {
			t.Errorf("target %s is both reaching and discharged", target)
		}
	}
	if discharged[identified["TestClampAtLimit"]] && discharged[identified["TestClampBelow"]] {
		t.Error("every target that observes the clamp was discharged, so nothing would run it")
	}
	for _, arguments := range mutantArguments(events, clamp.MutantID) {
		if strings.Contains(arguments, "TestClampAbove") {
			t.Errorf("the discharged target ran anyway: %s", arguments)
		}
	}
	if status := mutantStatus(t, result, clamp.MutantID); status != report.MutantKilled {
		t.Fatalf("clamp mutant %s = %s, want it killed by the test the proof kept", clamp.MutantID, status)
	}

	load := oneRoute(t, events, "nil-error-branch")
	if load.Reason != trace.ReasonCoverageReaching || load.Granularity != trace.GranularityBlock ||
		len(load.ReachingTargets) != 0 || len(load.Plan) != 0 {
		t.Fatalf("load route = %+v, want a coverage-reaching route with nothing left to run", load)
	}
	// By presence, for the reason the clamp route above is.
	if !slices.ContainsFunc(load.Discharged, func(d trace.Discharge) bool {
		return d.Target == identified["TestLoad"] && d.Reason == trace.DischargeBranchNeverTaken
	}) {
		t.Fatalf("load route discharged %+v, want TestLoad for %s",
			load.Discharged, trace.DischargeBranchNeverTaken)
	}
	if arguments := mutantArguments(events, load.MutantID); len(arguments) != 0 {
		t.Fatalf("the fully discharged mutant ran %d times: %v", len(arguments), arguments)
	}
	wantSummary := "no reaching test was run: every one was discharged because none takes the branch this mutation narrows"
	if summary := mutantFinding(t, result, load.MutantID); summary.Kind != "surviving-mutant" || summary.Summary != wantSummary {
		t.Fatalf("load finding = %+v, want a surviving-mutant summarised %q", summary, wantSummary)
	}
}

func mutantStatus(t *testing.T, result report.Report, id string) report.MutantStatus {
	t.Helper()
	for _, mutant := range result.Mutants {
		if mutant.ID == id {
			return mutant.Status
		}
	}
	t.Fatalf("mutant %s is absent from the inventory", id)
	return ""
}

func mutantFinding(t *testing.T, result report.Report, id string) report.Finding {
	t.Helper()
	var found []report.Finding
	for _, finding := range result.Findings {
		if finding.MutantID == id {
			found = append(found, finding)
		}
	}
	if len(found) != 1 {
		t.Fatalf("mutant %s has %d findings, want one: %+v", id, len(found), found)
	}
	return found[0]
}
