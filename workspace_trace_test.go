// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"testing"

	"github.com/P4suta/go-mutants/trace"
)

// TestRecordingIsNilWhenASinkWasSupplied is the one rule that keeps "where does
// this workspace record" a single answer.
//
// A caller that hands [OpenOptions.Trace] a sink owns the recording: every
// event goes there, and [Workspace.Recording] returns nil rather than a second,
// shorter copy of what the sink already holds — because a caller holding both
// would have to work out which of them is the account of the run, and the ring
// is the one that silently drops its oldest events. A caller that hands over
// nothing gets the ring, and Recording is how it reads it.
//
// It is checked over [newRecording] rather than over [Open], because what is
// under test is the choice and not the toolchain: Open locates a `go` and
// copies a module before it could ever be asked this question.
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
	// A workspace is opened by a program rather than by a command line, and the
	// schema refuses either field on one. The check is here as well as in the
	// schema because this is where the record is built.
	if start := events[0].Start; start.RunID != "" || start.Args != nil {
		t.Errorf("a workspace run-start carries run_id %q and args %v, want neither", start.RunID, start.Args)
	}
	if events[0].Start.ToolVersion == "" {
		t.Error("a workspace run-start names no tool version, and the schema requires one")
	}

	// Nil is the workspace that was never opened, not a panic.
	var absent *Workspace
	if got := absent.Recording(); got != nil {
		t.Errorf("Recording() on a nil workspace = %v, want nil", got)
	}
}
