// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"testing"

	"github.com/P4suta/go-mutants/trace"
)

func TestRecordingIsNilWhenASinkWasSupplied(t *testing.T) {
	supplied := trace.NewMemorySink(0)
	traced := &Workspace{recording: newRecording(t.TempDir(), supplied)}
	if got := traced.Recording(); got != nil {
		t.Errorf("Recording() returned %d events for a workspace whose caller supplied a sink,"+
			" want nil: the sink is where that recording is", len(got))
	}
	if events := supplied.Events(); len(events) != 1 || events[0].Type != trace.TypeRunStart {
		t.Fatalf("the supplied sink holds %+v, want exactly the run-start", events)
	}
	if kind := supplied.Events()[0].Start.Kind; kind != trace.StartKindWorkspace {
		t.Errorf("run-start kind = %q, want %q", kind, trace.StartKindWorkspace)
	}

	ringed := &Workspace{recording: newRecording(t.TempDir(), nil)}
	events := ringed.Recording()
	if len(events) != 1 || events[0].Type != trace.TypeRunStart {
		t.Fatalf("Recording() = %+v for a workspace with no sink, want the run-start it recorded", events)
	}
	if start := events[0].Start; start.RunID != "" || start.Args != nil {
		t.Errorf("a workspace run-start carries run_id %q and args %v, want neither", start.RunID, start.Args)
	}
	if events[0].Start.ToolVersion == "" {
		t.Error("a workspace run-start names no tool version, and the schema requires one")
	}

	var absent *Workspace
	if got := absent.Recording(); got != nil {
		t.Errorf("Recording() on a nil workspace = %v, want nil", got)
	}
}
