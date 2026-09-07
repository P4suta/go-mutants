// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/trace"
)

// schemaID is the name the schema is registered and compiled under. The host is
// deliberately unresolvable: these identifiers are names, not addresses.
const schemaID = "https://go-mutants.invalid/schema/trace-v1.schema.json"

// documentOf marshals an event and decodes it back as a free-form document, so
// a test can add a field the Go type has no room for.
func documentOf(t *testing.T, event trace.Event) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

// scriptedDocuments is the scripted recording as free-form documents.
func scriptedDocuments(t *testing.T) []map[string]any {
	t.Helper()
	events := scriptedEvents(t)
	documents := make([]map[string]any, 0, len(events))
	for _, event := range events {
		documents = append(documents, documentOf(t, event))
	}
	return documents
}

// validates reports whether a document satisfies the published contract,
// through the very validator a consumer would use.
func validates(t *testing.T, document map[string]any) error {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return schemas.Validate(schemas.TraceEventV1, encoded)
}

// payloadNames is every payload key in the contract.
var payloadNames = []string{
	"start", "phase", "stage", "prepare", "exec", "mutant", "probe", "validate",
	"coverage", "cache", "snapshot", "sweep", "artifact", "note", "run",
}

// ownPayload is the payload key the given event type carries.
func ownPayload(eventType string) string {
	switch eventType {
	case trace.TypeRunStart:
		return "start"
	case trace.TypePhaseStart, trace.TypePhaseEnd:
		return "phase"
	case trace.TypeStage:
		return "stage"
	case trace.TypePrepare:
		return "prepare"
	case trace.TypeExec:
		return "exec"
	case trace.TypeMutantExec:
		return "mutant"
	case trace.TypeProbeExec:
		return "probe"
	case trace.TypeValidate:
		return "validate"
	case trace.TypeCoverageMap:
		return "coverage"
	case trace.TypeCache:
		return "cache"
	case trace.TypeSnapshot:
		return "snapshot"
	case trace.TypeSweep:
		return "sweep"
	case trace.TypeArtifact:
		return "artifact"
	case trace.TypeNote:
		return "note"
	case trace.TypeRunEnd:
		return "run"
	default:
		return ""
	}
}

func TestSchemaIdentifiesTheTraceFormat(t *testing.T) {
	t.Parallel()

	var document map[string]any
	if err := json.Unmarshal(trace.JSONSchema(), &document); err != nil {
		t.Fatalf("the embedded schema is not JSON: %v", err)
	}
	if document["$id"] != schemaID {
		t.Errorf("$id = %v, want %q", document["$id"], schemaID)
	}
	if document["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v, want draft 2020-12", document["$schema"])
	}
	// JSONSchema hands out a copy, so a consumer that writes into what it was
	// given cannot change what the next one reads.
	first := trace.JSONSchema()
	first[0] = 'x'
	if slices.Equal(first, trace.JSONSchema()) {
		t.Error("JSONSchema returned the embedded bytes rather than a copy")
	}
}

func TestSchemaAcceptsEveryRecordedEvent(t *testing.T) {
	t.Parallel()

	for _, document := range scriptedDocuments(t) {
		if err := validates(t, document); err != nil {
			t.Errorf("the recorded %v event was rejected: %v", document["type"], err)
		}
	}
}

func TestSchemaRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	for _, document := range scriptedDocuments(t) {
		eventType, _ := document["type"].(string)
		own := ownPayload(eventType)

		envelope := documentOf(t, trace.Event{})
		for key, value := range document {
			envelope[key] = value
		}
		envelope["unknown"] = true
		if err := validates(t, envelope); err == nil {
			t.Errorf("a %s event with an unknown envelope field was accepted", eventType)
		}

		payload, ok := document[own].(map[string]any)
		if !ok {
			t.Fatalf("the %s event carries no %s payload", eventType, own)
		}
		payload["unknown"] = true
		if err := validates(t, document); err == nil {
			t.Errorf("a %s event with an unknown %s field was accepted", eventType, own)
		}
	}
}

// TestSchemaRejectsAPayloadThatIsNotTheEventsOwn checks the pairing in both
// directions, for every type against every foreign payload: a line carries the
// payload its type names and never another one, so a reader may switch on
// `type` and reach for that payload alone.
func TestSchemaRejectsAPayloadThatIsNotTheEventsOwn(t *testing.T) {
	t.Parallel()

	documents := scriptedDocuments(t)
	payloads := map[string]any{}
	for _, document := range documents {
		eventType, _ := document["type"].(string)
		payloads[ownPayload(eventType)] = document[ownPayload(eventType)]
	}
	if len(payloads) != len(payloadNames) {
		t.Fatalf("the scripted recording carries %d payloads, want %d", len(payloads), len(payloadNames))
	}

	for _, document := range documents {
		eventType, _ := document["type"].(string)
		own := ownPayload(eventType)
		for _, foreign := range payloadNames {
			if foreign == own {
				continue
			}
			// Beside its own payload: two payloads is one too many.
			both := clone(document)
			both[foreign] = payloads[foreign]
			if err := validates(t, both); err == nil {
				t.Errorf("a %s event carrying both %s and %s was accepted", eventType, own, foreign)
			}
			// And instead of it: the type names a payload that is not there.
			instead := clone(document)
			delete(instead, own)
			instead[foreign] = payloads[foreign]
			if err := validates(t, instead); err == nil {
				t.Errorf("a %s event carrying %s instead of %s was accepted", eventType, foreign, own)
			}
		}
		// And with nothing at all.
		empty := clone(document)
		delete(empty, own)
		if err := validates(t, empty); err == nil {
			t.Errorf("a %s event carrying no payload was accepted", eventType)
		}
	}
}

func clone(document map[string]any) map[string]any {
	copied := make(map[string]any, len(document))
	for key, value := range document {
		copied[key] = value
	}
	return copied
}

func TestSchemaRejectsAnUnknownExecKind(t *testing.T) {
	t.Parallel()

	// The enum is what makes "every subprocess is labelled" checkable: a call
	// site can only forget to label one, and an unlabelled command is a
	// recording that does not validate.
	document := documentOf(t, execEvent(t))
	payload, _ := document["exec"].(map[string]any)
	for _, kind := range []string{"", "go-build", "mutant_run", "MUTANT-RUN"} {
		payload["kind"] = kind
		if err := validates(t, document); err == nil {
			t.Errorf("the exec kind %q was accepted", kind)
		}
	}
	delete(payload, "kind")
	if err := validates(t, document); err == nil {
		t.Error("an exec event with no kind at all was accepted")
	}
	for _, kind := range trace.ExecKinds() {
		payload["kind"] = kind
		if err := validates(t, document); err != nil {
			t.Errorf("the documented exec kind %q was rejected: %v", kind, err)
		}
	}
}

// execEvent is the scripted exec event.
func execEvent(t *testing.T) trace.Event {
	t.Helper()
	for _, event := range scriptedEvents(t) {
		if event.Type == trace.TypeExec {
			return event
		}
	}
	t.Fatal("the scripted recording holds no exec event")
	return trace.Event{}
}

func TestSchemaRejectsEnvironmentValues(t *testing.T) {
	t.Parallel()

	// The recorder is what reduces an entry to its name, and this is the
	// backstop: an `env_names` item holding a value is not a trace, so a
	// recording carrying one does not validate whatever produced it.
	document := documentOf(t, execEvent(t))
	payload, _ := document["exec"].(map[string]any)
	payload["env_names"] = []any{"PATH=/usr/bin"}
	if err := validates(t, document); err == nil {
		t.Error("an env_names entry carrying a value was accepted")
	}
	payload["env_names"] = []any{"PATH", "PATH"}
	if err := validates(t, document); err == nil {
		t.Error("a repeated env_names entry was accepted")
	}
	payload["env_names"] = []any{"PATH", "GOFLAGS"}
	if err := validates(t, document); err != nil {
		t.Errorf("plain environment names were rejected: %v", err)
	}
}

func TestSchemaRejectsRunStartWithoutSchemaAndSchemaOffRunStart(t *testing.T) {
	t.Parallel()

	documents := scriptedDocuments(t)
	runStart := documents[0]
	if runStart["type"] != trace.TypeRunStart {
		t.Fatalf("the first event is %v", runStart["type"])
	}
	delete(runStart, "schema")
	if err := validates(t, runStart); err == nil {
		t.Error("a run-start without a schema was accepted")
	}
	runStart["schema"] = "goatest-trace-v1"
	if err := validates(t, runStart); err == nil {
		t.Error("a run-start carrying another tool's schema was accepted")
	}
	for _, document := range documents[1:] {
		document["schema"] = trace.SchemaV1
		if err := validates(t, document); err == nil {
			t.Errorf("a %v event carrying a schema was accepted", document["type"])
		}
	}
}

func TestSchemaRequiresAccountingOnRunEnd(t *testing.T) {
	t.Parallel()

	// The accounting is never optional, because it is what tells a complete
	// recording from a lossy one.
	documents := scriptedDocuments(t)
	runEnd := documents[len(documents)-1]
	if runEnd["type"] != trace.TypeRunEnd {
		t.Fatalf("the last event is %v", runEnd["type"])
	}
	payload, _ := runEnd["run"].(map[string]any)
	for _, field := range []string{"events_emitted", "events_dropped"} {
		kept := payload[field]
		delete(payload, field)
		if err := validates(t, runEnd); err == nil {
			t.Errorf("a run-end without %s was accepted", field)
		}
		payload[field] = -1
		if err := validates(t, runEnd); err == nil {
			t.Errorf("a run-end with a negative %s was accepted", field)
		}
		payload[field] = kept
	}
	if err := validates(t, runEnd); err != nil {
		t.Errorf("the restored run-end was rejected: %v", err)
	}
}

func TestSchemaTiesInfectedToAMeasuredProbe(t *testing.T) {
	t.Parallel()

	// Facts come from a measured pass alone. The other outcomes say nothing
	// about any mutant, so a list of infections beside one of them would be a
	// measurement to one reader and an error to another.
	var probe map[string]any
	for _, document := range scriptedDocuments(t) {
		if document["type"] == trace.TypeProbeExec {
			probe = document
		}
	}
	if probe == nil {
		t.Fatal("the scripted recording holds no probe-exec event")
	}
	payload, _ := probe["probe"].(map[string]any)
	if err := validates(t, probe); err != nil {
		t.Fatalf("a measured probe with infections was rejected: %v", err)
	}
	for _, outcome := range []string{trace.ProbeOutcomeTestFailed, trace.ProbeOutcomeTimedOut, trace.ProbeOutcomeUnavailable} {
		payload["outcome"] = outcome
		if err := validates(t, probe); err == nil {
			t.Errorf("a %s probe carrying infections was accepted", outcome)
		}
		delete(payload, "infected")
		if err := validates(t, probe); err != nil {
			t.Errorf("a %s probe without infections was rejected: %v", outcome, err)
		}
		payload["infected"] = []any{fixtureMutantID}
	}
	payload["outcome"] = "invented"
	delete(payload, "infected")
	if err := validates(t, probe); err == nil {
		t.Error("an unknown probe outcome was accepted")
	}
}

// TestSchemaAcceptsEveryEventOfTheFailureRecording validates the shapes a
// healthy run never produces, against the same contract.
//
// The happy path is the easy half. An error string, a timeout, a probe pass
// that never reached an outcome, an uncovered mutant and a lossy accounting are
// the lines a reader actually opens a recording for, and a schema that only
// ever saw a green run would be a contract for half the format.
func TestSchemaAcceptsEveryEventOfTheFailureRecording(t *testing.T) {
	t.Parallel()

	events := scriptedFailureEvents(t)
	if len(events) != fixtureFailureCount {
		t.Fatalf("the failure recording holds %d events, want %d", len(events), fixtureFailureCount)
	}
	for _, event := range events {
		if err := validates(t, documentOf(t, event)); err != nil {
			t.Errorf("the recorded %v event was rejected: %v", event.Type, err)
		}
	}
}

// TestSchemaAcceptsTheEnginesOwnContextKey ties the schema's `context_key`
// pattern to the value the engine will actually record.
//
// A cache key in a trace is the identity a warm run answered from, and it is
// `cache.Context.ContextKey()` — the truncation to `cache.ContextKeyLength`
// that names an entry's directory, not the full digest. A pattern written for
// the wrong length would reject every real recording while accepting every
// fixture, which is the one way a published contract can be wrong and green at
// the same time.
func TestSchemaAcceptsTheEnginesOwnContextKey(t *testing.T) {
	t.Parallel()

	context := cache.Context{
		ToolVersion:      fixtureToolVersion,
		ToolDigest:       fixtureDigest,
		ToolchainVersion: "go1.26.6",
		WorkspaceDigest:  fixtureDigest,
		CatalogDigest:    fixtureDigest,
		TestCommand:      []string{"go", "test", "./..."},
	}
	key, err := context.ContextKey()
	if err != nil {
		t.Fatalf("ContextKey: %v", err)
	}
	if len(key) != cache.ContextKeyLength {
		t.Fatalf("ContextKey returned %d characters, want %d", len(key), cache.ContextKeyLength)
	}
	record := fixtureCacheRecord()
	record.ContextKey = key
	document := documentOf(t, trace.Event{
		Seq: 1, Type: trace.TypeCache, Timestamp: fixtureStart.Format(time.RFC3339Nano), Cache: &record,
	})
	if validateErr := validates(t, document); validateErr != nil {
		t.Errorf("the engine's own context key %q was rejected: %v", key, validateErr)
	}
	// The full key is not what an entry is filed under, and a schema that took
	// either would not be describing anything.
	full, err := context.Key()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := document["cache"].(map[string]any)
	payload["context_key"] = full
	if err := validates(t, document); err == nil {
		t.Error("the untruncated key was accepted as a context key")
	}
}

// TestSchemaRequiresInfectedOnAMeasuredProbe is the required half of the same
// pairing [TestSchemaTiesInfectedToAMeasuredProbe] checks the forbidden half
// of.
func TestSchemaRequiresInfectedOnAMeasuredProbe(t *testing.T) {
	t.Parallel()

	var probe map[string]any
	for _, document := range scriptedDocuments(t) {
		if document["type"] == trace.TypeProbeExec {
			probe = document
		}
	}
	payload, _ := probe["probe"].(map[string]any)
	delete(payload, "infected")
	if err := validates(t, probe); err == nil {
		t.Error("a measured probe with no infection set at all was accepted")
	}
	payload["infected"] = []any{}
	if err := validates(t, probe); err != nil {
		t.Errorf("a measured probe that infected nothing was rejected: %v", err)
	}
}

// TestSchemaPairsPhaseDurationWithThePhaseThatEnded keeps a phase's timing
// where it means something. A phase is only timed when it ends, so a
// `phase-start` carrying a duration and a `phase-end` missing one each describe
// a span that was never measured.
func TestSchemaPairsPhaseDurationWithThePhaseThatEnded(t *testing.T) {
	t.Parallel()

	var start, end map[string]any
	for _, document := range scriptedDocuments(t) {
		switch document["type"] {
		case trace.TypePhaseStart:
			start = document
		case trace.TypePhaseEnd:
			end = document
		}
	}
	if start == nil || end == nil {
		t.Fatal("the scripted recording holds no phase pair")
	}
	startPayload, _ := start["phase"].(map[string]any)
	if _, carried := startPayload["duration_ms"]; carried {
		t.Error("the recorded phase-start carries a duration")
	}
	startPayload["duration_ms"] = 0
	if err := validates(t, start); err == nil {
		t.Error("a phase-start carrying a duration was accepted")
	}

	endPayload, _ := end["phase"].(map[string]any)
	if _, carried := endPayload["duration_ms"]; !carried {
		t.Error("the recorded phase-end carries no duration")
	}
	delete(endPayload, "duration_ms")
	if err := validates(t, end); err == nil {
		t.Error("a phase-end without a duration was accepted")
	}
	// Zero is a duration a phase really can have, and absent is not the same
	// statement, which is why the field is a pointer in Go.
	endPayload["duration_ms"] = 0
	if err := validates(t, end); err != nil {
		t.Errorf("a phase that took under a millisecond was rejected: %v", err)
	}
}

// TestSchemaAcceptsADisplayIDOfTheConfiguredLength keeps the pattern off a
// value the catalogue decides at run time.
//
// `mutation.DisplayIDLength` is 20 today, and it is a constant the project may
// change; a pattern pinned to twenty would turn that change into a recording
// nothing accepts.
func TestSchemaAcceptsADisplayIDOfTheConfiguredLength(t *testing.T) {
	t.Parallel()

	var mutant map[string]any
	for _, document := range scriptedDocuments(t) {
		if document["type"] == trace.TypeMutantExec {
			mutant = document
		}
	}
	payload, _ := mutant["mutant"].(map[string]any)
	for _, length := range []int{4, mutation.DisplayIDLength, 64} {
		payload["display_id"] = fixtureMutantID[:length]
		if err := validates(t, mutant); err != nil {
			t.Errorf("a %d-character display id was rejected: %v", length, err)
		}
	}
	for _, bad := range []string{"", "abc", fixtureMutantID + "00", "4B2F8C1D0E6A39571C84"} {
		payload["display_id"] = bad
		if err := validates(t, mutant); err == nil {
			t.Errorf("the display id %q was accepted", bad)
		}
	}
}

// TestSchemaAcceptsTheMemoryFactsAndRejectsNonsense pins the two fields a
// bounded run adds, on both records that carry them.
//
// They are additive and optional, so the first thing checked is that a
// recording without them still validates — which is what makes a recording an
// older build wrote readable by this one. What is rejected is a negative size,
// which is not a quantity of memory, and `memory_exceeded` on an `exec` record,
// which does not have it: whether a *command* was stopped by a bound is a fact
// the attempt above it states, and inventing a second place to say it is how
// two places come to disagree.
func TestSchemaAcceptsTheMemoryFactsAndRejectsNonsense(t *testing.T) {
	t.Parallel()

	var exec, mutant map[string]any
	for _, document := range scriptedDocuments(t) {
		switch document["type"] {
		case trace.TypeExec:
			exec = document
		case trace.TypeMutantExec:
			mutant = document
		}
	}
	if exec == nil || mutant == nil {
		t.Fatal("the scripted recording holds no exec or no mutant-exec event")
	}

	for _, c := range []struct {
		name     string
		document map[string]any
		payload  string
	}{
		{"exec", exec, "exec"},
		{"mutant-exec", mutant, "mutant"},
	} {
		payload, _ := c.document[c.payload].(map[string]any)

		delete(payload, "peak_memory_bytes")
		if err := validates(t, c.document); err != nil {
			t.Errorf("a %s record with no peak_memory_bytes was rejected: %v", c.name, err)
		}
		for _, size := range []float64{0, 1, 1 << 30} {
			payload["peak_memory_bytes"] = size
			if err := validates(t, c.document); err != nil {
				t.Errorf("a %s peak_memory_bytes of %v was rejected: %v", c.name, size, err)
			}
		}
		payload["peak_memory_bytes"] = -1.0
		if err := validates(t, c.document); err == nil {
			t.Errorf("a %s peak_memory_bytes of -1 was accepted; that is not a quantity of memory", c.name)
		}
		payload["peak_memory_bytes"] = 268435456.0
	}

	mutantPayload, _ := mutant["mutant"].(map[string]any)
	mutantPayload["memory_exceeded"] = true
	if err := validates(t, mutant); err != nil {
		t.Errorf("a mutant-exec record with memory_exceeded was rejected: %v", err)
	}

	execPayload, _ := exec["exec"].(map[string]any)
	execPayload["memory_exceeded"] = true
	if err := validates(t, exec); err == nil {
		t.Error("an exec record with memory_exceeded was accepted; only the attempt above it says that")
	}
}
