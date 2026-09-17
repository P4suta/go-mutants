// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/evidence"
)

func TestASuiteKeyRefusesAnythingButTheObjectItDescribes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		encoded string
		message string
	}{
		{name: "not an object", encoded: `"suite"`, message: "cannot unmarshal"},
		{name: "an unknown field", encoded: `{"package":"p","key":"k","whole_tree":true,"extra":1}`, message: "unknown field"},
		{name: "no whole_tree at all", encoded: `{"package":"p","key":"k"}`, message: "requires whole_tree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var suite evidence.SuiteKey
			err := json.Unmarshal([]byte(test.encoded), &suite)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("decoding %s = %v, want %q", test.encoded, err, test.message)
			}
		})
	}
}

func TestAMutationStoreRefusesADocumentWithNoRecordsField(t *testing.T) {
	t.Parallel()
	var store evidence.MutationStore
	err := json.Unmarshal([]byte(`{"schema":"x","module_path":"m"}`), &store)
	if err == nil || !strings.Contains(err.Error(), "requires records") {
		t.Fatalf("decoding a store with no records = %v", err)
	}
}

func mutationRecordWith(outcome string, change func(*evidence.MutationRecord)) evidence.MutationRecord {
	var record evidence.MutationRecord
	switch outcome {
	case evidence.MutationOutcomeKilled:
		record = killedMutationRecord()
	case evidence.MutationOutcomeSurvived:
		record = survivedMutationRecord()
	case evidence.MutationOutcomeUnreached:
		record = unreachedMutationRecord()
	}
	if change != nil {
		change(&record)
	}
	return record
}

func TestEveryOutcomeRefusesTheFieldsThatDoNotBelongToIt(t *testing.T) {
	t.Parallel()
	killer := []evidence.TargetKey{{
		Package: mutationModulePath + "/pkg", Name: "TestKills", Kind: "test", Key: mutationDigest("1"),
	}}
	suite := &evidence.SuiteKey{Package: mutationModulePath + "/pkg", Key: mutationDigest("5")}
	finding := &evidence.FindingSeed{Kind: "surviving-mutant", Summary: "summary"}
	for _, test := range []struct {
		name    string
		outcome string
		change  func(*evidence.MutationRecord)
	}{
		{name: "killed without a killer", outcome: evidence.MutationOutcomeKilled, change: func(r *evidence.MutationRecord) { r.KilledBy = nil }},
		{name: "killed with exhausted targets", outcome: evidence.MutationOutcomeKilled, change: func(r *evidence.MutationRecord) { r.Exhausted = killer }},
		{name: "killed with a suite", outcome: evidence.MutationOutcomeKilled, change: func(r *evidence.MutationRecord) { r.Suite = suite }},
		{name: "killed with a finding", outcome: evidence.MutationOutcomeKilled, change: func(r *evidence.MutationRecord) { r.Finding = finding }},
		{name: "survived with a killer", outcome: evidence.MutationOutcomeSurvived, change: func(r *evidence.MutationRecord) { r.KilledBy = killer }},
		{name: "survived without exhausted targets", outcome: evidence.MutationOutcomeSurvived, change: func(r *evidence.MutationRecord) { r.Exhausted = nil }},
		{name: "survived with a suite", outcome: evidence.MutationOutcomeSurvived, change: func(r *evidence.MutationRecord) { r.Suite = suite }},
		{name: "survived without a finding", outcome: evidence.MutationOutcomeSurvived, change: func(r *evidence.MutationRecord) { r.Finding = nil }},
		{name: "unreached with a killer", outcome: evidence.MutationOutcomeUnreached, change: func(r *evidence.MutationRecord) { r.KilledBy = killer }},
		{name: "unreached with exhausted targets", outcome: evidence.MutationOutcomeUnreached, change: func(r *evidence.MutationRecord) { r.Exhausted = killer }},
		{name: "unreached without a suite", outcome: evidence.MutationOutcomeUnreached, change: func(r *evidence.MutationRecord) { r.Suite = nil }},
		{name: "unreached without a finding", outcome: evidence.MutationOutcomeUnreached, change: func(r *evidence.MutationRecord) { r.Finding = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := evidence.MutationStore{
				Schema: evidence.MutationSchemaV1, ModulePath: mutationModulePath,
				Records: []evidence.MutationRecord{mutationRecordWith(test.outcome, test.change)},
			}
			err := evidence.SaveMutation(t.TempDir()+"/mutation.json", store)
			if err == nil || !strings.Contains(err.Error(), "requires") {
				t.Fatalf("SaveMutation of %s = %v, want the shape refused", test.name, err)
			}
		})
	}
}

func TestADecodedRecordRefusesTheFieldsThatDoNotBelongToItsOutcome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		outcome string
		field   string
		value   any
		wantErr bool
	}{
		{name: "killed with a finding", outcome: evidence.MutationOutcomeKilled, field: "finding",
			value: map[string]any{"kind": "k", "summary": "s"}, wantErr: true},
		{name: "an unknown outcome carrying a finding", outcome: "retired", field: "finding",
			value: map[string]any{"kind": "k", "summary": "s"}},
		{name: "an unknown outcome carrying nothing", outcome: "retired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := mutationDocument(t, func(document map[string]any) {
				records := mutationRecords(t, document)
				record := records[0]
				record["outcome"] = test.outcome
				if test.field != "" {
					record[test.field] = test.value
				}
				document["records"] = []any{record}
			})
			var store evidence.MutationStore
			err := json.Unmarshal(data, &store)
			if (err != nil) != test.wantErr {
				t.Fatalf("decoding %s = %v, want an error %t", test.name, err, test.wantErr)
			}
		})
	}
}

func TestMutationEvidenceSchemaRefusesARepeatedTargetKey(t *testing.T) {
	t.Parallel()
	compiled := compileMutationSchema(t)
	for _, field := range []string{"killed_by", "exhausted"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			data := mutationDocument(t, func(document map[string]any) {
				for _, record := range mutationRecords(t, document) {
					entries, present := record[field].([]any)
					if !present {
						continue
					}
					record[field] = append(slices.Clone(entries), entries[0])
				}
			})
			if err := validateMutationInstance(t, compiled, data); err == nil {
				t.Fatalf("the schema accepted a repeated %s entry", field)
			}
		})
	}
}
