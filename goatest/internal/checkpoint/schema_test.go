// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compiledCheckpointSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(checkpoint.JSONSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const id = "https://goatest.invalid/assurance-checkpoint-v1.schema.json"
	if err := compiler.AddResource(id, document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func checkpointDocument(t *testing.T, state checkpoint.State) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(checkpoint.JSON(state), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestTheCheckpointSchemaRefusesEveryShapeTheValidatorDoes(t *testing.T) {
	t.Parallel()
	compiled := compiledCheckpointSchema(t)
	sound := checkpointDocument(t, validCheckpoint())
	if err := compiled.Validate(sound); err != nil {
		t.Fatalf("the schema refused a checkpoint the validator accepts: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{
			name: "a measured baseline suite with no coverage",
			change: func(document map[string]any) {
				suite := checkpointBaselineSuite(t, document)
				suite["measured"] = true
				delete(suite, "covered")
			},
		},
		{
			name: "a measured baseline suite with no instrumentation",
			change: func(document map[string]any) {
				suite := checkpointBaselineSuite(t, document)
				suite["measured"] = true
				delete(suite, "instrumented")
			},
		},
		{
			name: "an unmeasured baseline suite that claims the whole tree",
			change: func(document map[string]any) {
				suite := checkpointBaselineSuite(t, document)
				suite["measured"] = false
				suite["whole_tree"] = true
				delete(suite, "covered")
				delete(suite, "instrumented")
				suite["duration_ns"] = float64(0)
			},
		},
		{
			name: "an unmeasured baseline suite that took time",
			change: func(document map[string]any) {
				suite := checkpointBaselineSuite(t, document)
				suite["measured"] = false
				suite["duration_ns"] = float64(1)
				delete(suite, "covered")
				delete(suite, "instrumented")
			},
		},
		{
			name: "an unmeasured baseline suite that carries coverage",
			change: func(document map[string]any) {
				suite := checkpointBaselineSuite(t, document)
				suite["measured"] = false
				suite["duration_ns"] = float64(0)
				delete(suite, "instrumented")
			},
		},
		{
			name: "a baseline target list holding something that is not a target",
			change: func(document map[string]any) {
				baseline, _ := document["baseline"].(map[string]any)
				baseline["targets"] = []any{float64(1)}
			},
		},
		{
			name: "a race package list holding something that is not a name",
			change: func(document map[string]any) {
				race, _ := document["race"].(map[string]any)
				race["packages"] = []any{map[string]any{}}
			},
		},
		{
			name: "a mutation result list holding something that is not a result",
			change: func(document map[string]any) {
				mutation, _ := document["mutation"].(map[string]any)
				mutation["results"] = []any{"m-1"}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := checkpointDocument(t, validCheckpoint())
			test.change(document)
			if err := compiled.Validate(document); err == nil {
				t.Fatalf("the schema accepted %s", test.name)
			}
		})
	}
}

func checkpointBaselineSuite(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	baseline, ok := document["baseline"].(map[string]any)
	if !ok {
		t.Fatal("the document carries no baseline")
	}
	suites, ok := baseline["suites"].([]any)
	if !ok || len(suites) == 0 {
		t.Fatal("the document carries no partial baseline suite")
	}
	suite, ok := suites[0].(map[string]any)
	if !ok {
		t.Fatal("the first partial baseline suite is not an object")
	}
	return suite
}
