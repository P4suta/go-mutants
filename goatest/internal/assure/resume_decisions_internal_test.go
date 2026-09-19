// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	resumeMetadataAttempts = 3
	resumeMetadataTargets  = 4
	resumeMetadataRace     = 5
	resumeMetadataMutants  = 6
)

type resumeDecisionCache struct {
	coordinatorCache
	deleteErr error
}

func (cache *resumeDecisionCache) DeleteCheckpoint(string) error {
	cache.checkpointDeletes++
	if cache.deleteErr != nil {
		return cache.deleteErr
	}
	cache.checkpoint = checkpoint.State{}
	cache.checkpointFound = false
	return nil
}

func baselineJournalFixture() (checkpoint.Baseline, checkpoint.Baseline) {
	first := checkpoint.BaselineTarget{ID: "a", Inventory: report.TargetDisposition{ID: "a"}}
	second := checkpoint.BaselineTarget{ID: "b", Inventory: report.TargetDisposition{ID: "b"}}
	return checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{first}}, checkpoint.Baseline{
		BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{second, first},
	}
}

func suiteJournalFixture() (checkpoint.Baseline, checkpoint.Baseline) {
	first := checkpoint.BaselineSuite{Package: "a"}
	second := checkpoint.BaselineSuite{Package: "b"}
	return checkpoint.Baseline{BuildVetComplete: true, Suites: []checkpoint.BaselineSuite{first}}, checkpoint.Baseline{
		BuildVetComplete: true, Suites: []checkpoint.BaselineSuite{second, first},
	}
}

func TestBaselineTargetJournalSuffixAcceptsOnlyAnExactExtension(t *testing.T) {
	previous, next := baselineJournalFixture()
	suffix, ok := baselineCheckpointJournalSuffix(previous, next)
	if !ok || len(suffix) != 1 || suffix[0].ID != "b" {
		t.Fatalf("valid suffix = (%+v, %t)", suffix, ok)
	}
	for _, test := range []struct {
		name   string
		change func(*checkpoint.Baseline, *checkpoint.Baseline)
	}{
		{name: "previous checks incomplete", change: func(before, _ *checkpoint.Baseline) { before.BuildVetComplete = false }},
		{name: "next checks incomplete", change: func(_, after *checkpoint.Baseline) { after.BuildVetComplete = false }},
		{name: "previous complete", change: func(before, _ *checkpoint.Baseline) { before.Complete = true }},
		{name: "next complete", change: func(_, after *checkpoint.Baseline) { after.Complete = true }},
		{name: "evidence changed", change: func(_, after *checkpoint.Baseline) { after.Evidence = []report.Evidence{{ID: "changed"}} }},
		{name: "findings changed", change: func(_, after *checkpoint.Baseline) { after.Findings = []report.Finding{{ID: "changed"}} }},
		{name: "suites changed", change: func(_, after *checkpoint.Baseline) { after.Suites = []checkpoint.BaselineSuite{{Package: "changed"}} }},
		{name: "no growth", change: func(_, after *checkpoint.Baseline) { after.Targets = after.Targets[:1] }},
		{name: "duplicate before", change: func(before, after *checkpoint.Baseline) {
			before.Targets = append(before.Targets, before.Targets[0])
			after.Targets = append(after.Targets, checkpoint.BaselineTarget{ID: "c"})
		}},
		{name: "duplicate after", change: func(_, after *checkpoint.Baseline) { after.Targets[0].ID = after.Targets[1].ID }},
		{name: "saved unit changed", change: func(_, after *checkpoint.Baseline) { after.Targets[1].Executed = true }},
		{name: "saved unit missing", change: func(_, after *checkpoint.Baseline) { after.Targets[1].ID = "c" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, after := baselineJournalFixture()
			test.change(&before, &after)
			got, ok := baselineCheckpointJournalSuffix(before, after)
			if ok || got != nil {
				t.Fatalf("invalid suffix = (%+v, %t)", got, ok)
			}
		})
	}
}

func TestBaselineSuiteJournalSuffixAcceptsOnlyAnExactExtension(t *testing.T) {
	previous, next := suiteJournalFixture()
	suffix, ok := baselineSuiteCheckpointJournalSuffix(previous, next)
	if !ok || len(suffix) != 1 || suffix[0].Package != "b" {
		t.Fatalf("valid suffix = (%+v, %t)", suffix, ok)
	}
	for _, test := range []struct {
		name   string
		change func(*checkpoint.Baseline, *checkpoint.Baseline)
	}{
		{name: "previous checks incomplete", change: func(before, _ *checkpoint.Baseline) { before.BuildVetComplete = false }},
		{name: "next checks incomplete", change: func(_, after *checkpoint.Baseline) { after.BuildVetComplete = false }},
		{name: "previous complete", change: func(before, _ *checkpoint.Baseline) { before.Complete = true }},
		{name: "next complete", change: func(_, after *checkpoint.Baseline) { after.Complete = true }},
		{name: "evidence changed", change: func(_, after *checkpoint.Baseline) { after.Evidence = []report.Evidence{{ID: "changed"}} }},
		{name: "findings changed", change: func(_, after *checkpoint.Baseline) { after.Findings = []report.Finding{{ID: "changed"}} }},
		{name: "targets changed", change: func(_, after *checkpoint.Baseline) { after.Targets = []checkpoint.BaselineTarget{{ID: "changed"}} }},
		{name: "no growth", change: func(_, after *checkpoint.Baseline) { after.Suites = after.Suites[:1] }},
		{name: "duplicate before", change: func(before, after *checkpoint.Baseline) {
			before.Suites = append(before.Suites, before.Suites[0])
			after.Suites = append(after.Suites, checkpoint.BaselineSuite{Package: "c"})
		}},
		{name: "duplicate after", change: func(_, after *checkpoint.Baseline) { after.Suites[0].Package = after.Suites[1].Package }},
		{name: "saved unit changed", change: func(_, after *checkpoint.Baseline) { after.Suites[1].Measured = true }},
		{name: "saved unit missing", change: func(_, after *checkpoint.Baseline) { after.Suites[1].Package = "c" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, after := suiteJournalFixture()
			test.change(&before, &after)
			got, ok := baselineSuiteCheckpointJournalSuffix(before, after)
			if ok || got != nil {
				t.Fatalf("invalid suffix = (%+v, %t)", got, ok)
			}
		})
	}
}

func TestOpenRunCheckpointDecidesColdResumeAndFailureStates(t *testing.T) {
	if openRunCheckpoint(nil, "digest", Options{}, true) != nil || openRunCheckpoint(&coordinatorCache{}, "digest", Options{}, false) != nil {
		t.Fatal("disabled checkpointing returned a controller")
	}
	for _, test := range []struct {
		name        string
		cache       *coordinatorCache
		attempts    int
		enabled     bool
		claimed     bool
		wantWarning bool
		wantDeletes int
	}{
		{name: "cold", cache: &coordinatorCache{}, attempts: 1, enabled: true, claimed: true},
		{name: "resume", cache: &coordinatorCache{checkpointFound: true, checkpoint: checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: "digest", Attempts: 4}}, attempts: 5, enabled: true, claimed: true},
		{name: "read failure", cache: &coordinatorCache{checkpointFound: true, checkpointGetErr: errors.New("read failed")}, attempts: 1, enabled: true, claimed: true, wantWarning: true, wantDeletes: 1},
		{name: "claim failure", cache: &coordinatorCache{checkpointPutErr: errors.New("write failed")}, attempts: 1, wantWarning: true, wantDeletes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []Event
			controller := openRunCheckpoint(test.cache, "digest", Options{Progress: func(event Event) { events = append(events, event) }}, true)
			if controller == nil || controller.state.Schema != checkpoint.SchemaV1 || controller.state.InputDigest != "digest" ||
				controller.state.Attempts != test.attempts || controller.enabled != test.enabled || controller.claimed != test.claimed ||
				(len(events) != 0) != test.wantWarning || test.cache.checkpointDeletes != test.wantDeletes {
				t.Fatalf("controller = %+v, events=%+v cache=%+v", controller, events, test.cache)
			}
		})
	}
}

func TestCheckpointControllerSaveMethodsHonorDisabledAndExactState(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "b"}, {ID: "a"}}}
	fingerprint := MutationCatalogFingerprint(catalog)
	cache := &coordinatorCache{}
	controller := &runCheckpointController{store: cache, digest: "digest", enabled: true, state: checkpoint.State{
		Mutation: &checkpoint.Mutation{CatalogFingerprint: fingerprint},
	}}
	controller.saveRace([]string{"b", "a"}, RaceResult{Evidence: []report.Evidence{{ID: "race"}}})
	if controller.state.Race == nil || !controller.state.Race.Complete || !slices.Equal(controller.state.Race.Packages, []string{"b", "a"}) {
		t.Fatalf("saved race = %+v", controller.state.Race)
	}
	evaluation := ProbeEvaluation{Targets: []TargetEvidence{{Target: goanalysis.Target{ID: "target"}, Probed: true}}}
	controller.saveProbe(catalog, evaluation)
	if controller.state.Mutation.Probe == nil {
		t.Fatal("valid probe was not saved")
	}
	controller.saveMutant("b", MutationEvaluation{Provenance: "first"})
	controller.saveMutant("b", MutationEvaluation{Provenance: "replacement"})
	controller.saveMutant("a", MutationEvaluation{Provenance: "second"})
	if len(controller.state.Mutation.Results) != 2 || controller.state.Mutation.Results[0].Provenance != "replacement" {
		t.Fatalf("saved mutation results = %+v", controller.state.Mutation.Results)
	}
	controller.completeMutation()
	if !controller.state.Mutation.Complete || controller.state.Mutation.Results[0].ID != "a" || controller.state.Mutation.Results[1].ID != "b" {
		t.Fatalf("completed mutation = %+v", controller.state.Mutation)
	}
	baseline := checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{{ID: "b"}, {ID: "a"}}}
	controller.saveBaseline(baseline)
	if len(controller.state.Baseline.Targets) != 2 || controller.state.Baseline.Targets[0].ID != "a" || controller.state.Baseline.Targets[1].ID != "b" {
		t.Fatalf("saved baseline = %+v", controller.state.Baseline)
	}
	disabledState := controller.state
	controller.enabled = false
	controller.saveRace([]string{"changed"}, RaceResult{})
	controller.saveProbe(catalog, ProbeEvaluation{})
	controller.saveMutant("changed", MutationEvaluation{})
	controller.completeMutation()
	controller.saveBaseline(checkpoint.Baseline{Complete: true})
	if !reflect.DeepEqual(controller.state, disabledState) {
		t.Fatalf("disabled controller changed state: before=%+v after=%+v", disabledState, controller.state)
	}
	(*runCheckpointController)(nil).saveRace(nil, RaceResult{})
	(*runCheckpointController)(nil).saveProbe(catalog, ProbeEvaluation{})
	(*runCheckpointController)(nil).saveMutant("id", MutationEvaluation{})
	(*runCheckpointController)(nil).completeMutation()
	(*runCheckpointController)(nil).saveBaseline(checkpoint.Baseline{})
}

func TestCheckpointControllerMutationRejectsEveryCatalogMismatch(t *testing.T) {
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "a"}, {ID: "b"}}}
	fingerprint := MutationCatalogFingerprint(catalog)
	if got := (*runCheckpointController)(nil).mutation(catalog); got != nil {
		t.Fatalf("nil controller mutation = %#v", got)
	}
	disabled := &runCheckpointController{}
	if got := disabled.mutation(catalog); got != nil {
		t.Fatalf("disabled controller mutation = %#v", got)
	}
	for _, test := range []struct {
		name     string
		mutation *checkpoint.Mutation
		want     map[string]MutationEvaluation
		warning  bool
	}{
		{name: "no saved mutation"},
		{name: "fingerprint mismatch", mutation: &checkpoint.Mutation{CatalogFingerprint: "old"}, warning: true},
		{name: "missing catalog id", mutation: &checkpoint.Mutation{CatalogFingerprint: fingerprint, Results: []checkpoint.MutationResult{{ID: "missing"}}}, warning: true},
		{name: "valid", mutation: &checkpoint.Mutation{CatalogFingerprint: fingerprint, Results: []checkpoint.MutationResult{{ID: "a", Provenance: "saved"}}}, want: map[string]MutationEvaluation{"a": {Provenance: "saved"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []Event
			controller := &runCheckpointController{
				store: &coordinatorCache{}, digest: "digest", enabled: true,
				options: Options{Progress: func(event Event) { events = append(events, event) }},
				state:   checkpoint.State{Mutation: test.mutation},
			}
			got := controller.mutation(catalog)
			if !reflect.DeepEqual(got, test.want) || (len(events) != 0) != test.warning || controller.state.Mutation == nil || controller.state.Mutation.CatalogFingerprint != fingerprint {
				t.Fatalf("mutation resume = %+v, events=%+v state=%+v", got, events, controller.state)
			}
		})
	}
}

func TestCheckpointControllerFailurePathsDisableOrWarn(t *testing.T) {
	putCause := errors.New("put failed")
	putCache := &coordinatorCache{checkpointPutErr: putCause}
	var putEvents []Event
	controller := &runCheckpointController{store: putCache, digest: "digest", enabled: true, options: Options{Progress: func(event Event) { putEvents = append(putEvents, event) }}}
	controller.persistLocked()
	if controller.enabled || putCache.checkpointDeletes != 1 || len(putEvents) != 1 {
		t.Fatalf("put failure controller=%+v cache=%+v events=%+v", controller, putCache, putEvents)
	}
	deleteCause := errors.New("delete failed")
	deleteCache := &resumeDecisionCache{deleteErr: deleteCause}
	var deleteEvents []Event
	controller = &runCheckpointController{store: deleteCache, digest: "digest", enabled: true, options: Options{Progress: func(event Event) { deleteEvents = append(deleteEvents, event) }}}
	controller.discard()
	if controller.enabled || deleteCache.checkpointDeletes != 1 || len(deleteEvents) != 1 || !strings.Contains(deleteEvents[0].Detail, deleteCause.Error()) {
		t.Fatalf("delete failure controller=%+v cache=%+v events=%+v", controller, deleteCache, deleteEvents)
	}
	(*runCheckpointController)(nil).discard()
}

func TestCheckpointJournalFailuresDisableEveryAppendPath(t *testing.T) {
	target := checkpoint.BaselineTarget{ID: "target", Inventory: report.TargetDisposition{ID: "target"}}
	suite := checkpoint.BaselineSuite{Package: "pkg"}
	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{{ID: "mutant"}}}
	for _, test := range []struct {
		name string
		run  func(*runCheckpointController)
	}{
		{name: "target", run: func(controller *runCheckpointController) {
			controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true})
			controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true, Targets: []checkpoint.BaselineTarget{target}})
		}},
		{name: "suite", run: func(controller *runCheckpointController) {
			controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true})
			controller.saveBaseline(checkpoint.Baseline{BuildVetComplete: true, Suites: []checkpoint.BaselineSuite{suite}})
		}},
		{name: "mutation", run: func(controller *runCheckpointController) {
			controller.mutation(catalog)
			controller.saveMutant("mutant", MutationEvaluation{})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &journalCheckpointCache{journalErr: errors.New(test.name + " append failed")}
			var events []Event
			controller := openRunCheckpoint(store, "digest", Options{Progress: func(event Event) { events = append(events, event) }}, true)
			test.run(controller)
			if controller.enabled || store.checkpointDeletes != 1 || len(events) != 1 || events[0].Kind != "checkpoint-warning" {
				t.Fatalf("journal failure controller=%+v store=%+v events=%+v", controller, store, events)
			}
		})
	}
}

func TestCheckpointProbeMethodsDistinguishMissingInvalidAndSavedState(t *testing.T) {
	catalog := probeCatalog()
	targets := []TargetEvidence{probeEvidence("TestValue", goanalysis.KindTest, 0)}
	for _, controller := range []*runCheckpointController{nil, {}} {
		evaluation, reused, valid := controller.probe(catalog, targets, nil)
		if !reflect.DeepEqual(evaluation, ProbeEvaluation{}) || reused || !valid {
			t.Fatalf("unavailable probe = (%+v, %t, %t)", evaluation, reused, valid)
		}
	}
	controller := &runCheckpointController{enabled: true, store: &coordinatorCache{}, state: checkpoint.State{}}
	if evaluation, reused, valid := controller.probe(catalog, targets, nil); !reflect.DeepEqual(evaluation, ProbeEvaluation{}) || reused || !valid {
		t.Fatalf("missing mutation probe = (%+v, %t, %t)", evaluation, reused, valid)
	}
	controller.state.Mutation = &checkpoint.Mutation{CatalogFingerprint: MutationCatalogFingerprint(catalog)}
	if evaluation, reused, valid := controller.probe(catalog, targets, nil); !reflect.DeepEqual(evaluation, ProbeEvaluation{}) || reused || !valid {
		t.Fatalf("missing saved probe = (%+v, %t, %t)", evaluation, reused, valid)
	}

	evaluation := ProbeEvaluation{Targets: []TargetEvidence{{Target: targets[0].Target, Probed: true}}}
	for _, test := range []struct {
		name      string
		enabled   bool
		mutation  *checkpoint.Mutation
		wantSaved bool
	}{
		{name: "disabled", mutation: &checkpoint.Mutation{CatalogFingerprint: MutationCatalogFingerprint(catalog)}},
		{name: "missing mutation", enabled: true},
		{name: "wrong catalog", enabled: true, mutation: &checkpoint.Mutation{CatalogFingerprint: "wrong"}},
		{name: "valid", enabled: true, mutation: &checkpoint.Mutation{CatalogFingerprint: MutationCatalogFingerprint(catalog)}, wantSaved: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &runCheckpointController{enabled: test.enabled, store: &coordinatorCache{}, state: checkpoint.State{Mutation: test.mutation}}
			controller.saveProbe(catalog, evaluation)
			got := controller.state.Mutation != nil && controller.state.Mutation.Probe != nil
			if got != test.wantSaved {
				t.Fatalf("saved probe = %t, state=%+v", got, controller.state)
			}
		})
	}
}

func TestCheckpointMutationAndMetadataNoopStatesAreExact(t *testing.T) {
	controller := &runCheckpointController{enabled: true, store: &coordinatorCache{}}
	controller.saveMutant("mutant", MutationEvaluation{})
	controller.completeMutation()
	if controller.state.Mutation != nil {
		t.Fatalf("missing mutation state changed: %+v", controller.state)
	}
	if controller.resumeMetadata() != nil {
		t.Fatal("unclaimed controller reported resume metadata")
	}
	controller.claimed = true
	controller.state.Attempts = resumeMetadataAttempts
	controller.reusedTargets = resumeMetadataTargets
	controller.reusedRace = resumeMetadataRace
	controller.reusedMutants = resumeMetadataMutants
	if got := controller.resumeMetadata(); got == nil || got.Attempts != resumeMetadataAttempts || got.ReusedTargets != resumeMetadataTargets || got.ReusedRacePackages != resumeMetadataRace || got.ReusedMutants != resumeMetadataMutants {
		t.Fatalf("resume metadata = %+v", got)
	}
	if (*runCheckpointController)(nil).resumeMetadata() != nil {
		t.Fatal("nil controller reported resume metadata")
	}
	if race, ok := (*runCheckpointController)(nil).race(nil); race != nil || ok {
		t.Fatalf("nil controller race = (%+v, %t)", race, ok)
	}
	if race, ok := (&runCheckpointController{}).race(nil); race != nil || ok {
		t.Fatalf("disabled controller race = (%+v, %t)", race, ok)
	}
	controller = &runCheckpointController{enabled: true, store: &coordinatorCache{}, state: checkpoint.State{Race: &checkpoint.Race{}}}
	if race, ok := controller.race(nil); race != nil || ok {
		t.Fatalf("incomplete race = (%+v, %t)", race, ok)
	}
}
