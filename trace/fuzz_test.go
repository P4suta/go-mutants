// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/trace"
)

// FuzzReadRecording is the promise this package makes about a file it wrote
// and may not have finished writing.
//
// A recording is the only account of a run that survives it, and the commands
// that read one -- `trace summary`, `trace diff`, `explain` -- are run after
// the fact, on a file that may have been truncated by a killed process, edited
// by somebody looking for a mutant, written by an older version of this tool,
// or copied out of a CI artifact. ADR 0001 is why none of that may be believed
// on sight: a trace is never evidence, so a reader that accepted a malformed
// one would be turning a file nobody checked into an answer.
//
// Three properties, and the third is the one no table states:
//
//   - it never panics, whatever the file holds;
//   - a file that is not a recording is an error, and a file that is not there
//     is [Summary.Missing] rather than one, because "there is no trace for that
//     run" is an answer;
//   - a summary it accepts is internally consistent -- the sequence numbers
//     run from the first to the last it reports, the events it counted are the
//     events it saw, and a gap it reports is a gap the numbers show.
func FuzzReadRecording(f *testing.F) {
	const start = `{"seq":1,"type":"run-start","schema":"go-mutants/trace-v1",` +
		`"timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,` +
		`"start":{"run_id":"abc","tool_version":"0.0.0-test"}}`
	const end = `{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z",` +
		`"elapsed_ms":1000,"run":{"verdict":"ok","exit_code":0}}`

	f.Add(start + "\n" + end + "\n")
	f.Add(start + "\n")
	f.Add("")
	f.Add("\n")
	f.Add("{}\n")
	f.Add("not json\n")
	f.Add(start + "\n" + start + "\n")
	// A truncated last line, which is what a killed process leaves.
	f.Add(start + "\n" + end[:len(end)/2])
	// A gap in the sequence, which the summary is meant to report rather than
	// to accept silently.
	f.Add(start + "\n" + `{"seq":9,"type":"run-end","timestamp":"2026-01-01T00:00:01Z",` +
		`"elapsed_ms":1000,"run":{"verdict":"ok","exit_code":0}}` + "\n")
	// Two payloads on one event, which is a shape the writer cannot produce.
	f.Add(`{"seq":1,"type":"phase-start","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,` +
		`"phase":{"name":"baseline"},"note":{"kind":"warning","message":"x"}}` + "\n")
	f.Add("\x00\x00\x00\n")
	f.Add(start + "\n\n" + end + "\n")

	f.Fuzz(func(t *testing.T, stream string) {
		path := filepath.Join(t.TempDir(), "trace.jsonl")
		if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
			t.Fatalf("writing the recording: %v", err)
		}

		summary, err := trace.ReadSummary(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a file that exists was read as one that does not: %v", err)
			}
			// And the reader of the events themselves agrees about it: two
			// readers of one file that disagreed about whether it is a
			// recording would be two answers to one question.
			if _, eventsErr := trace.Read(path); eventsErr == nil {
				t.Fatalf("ReadSummary refused (%v) and Read accepted the same file", err)
			}
			return
		}
		if summary.Missing {
			t.Fatalf("a file that exists was summarised as missing")
		}
		if summary.Path != path {
			t.Fatalf("the summary names %q, want the file it read %q", summary.Path, path)
		}

		events, err := trace.Read(path)
		if err != nil {
			t.Fatalf("ReadSummary accepted the file and Read refused it: %v", err)
		}
		if len(events) != summary.Events {
			t.Fatalf("Read returned %d events and the summary counted %d", len(events), summary.Events)
		}
		if summary.Events == 0 {
			return
		}

		if summary.FirstSequence != events[0].Seq {
			t.Fatalf("the summary's first sequence is %d and the first event's is %d",
				summary.FirstSequence, events[0].Seq)
		}
		last := events[len(events)-1].Seq
		if summary.LastSequence != last {
			t.Fatalf("the summary's last sequence is %d and the last event's is %d",
				summary.LastSequence, last)
		}
		// The gap count is what a reader uses to say "this recording is
		// incomplete", so it has to be the arithmetic and not an impression.
		if want := last - summary.FirstSequence + 1 - int64(summary.Events); summary.MissingSequences != want {
			t.Fatalf("the summary reports %d missing sequences over %d events from %d to %d, want %d",
				summary.MissingSequences, summary.Events, summary.FirstSequence, last, want)
		}
		counted := 0
		for _, n := range summary.Counts {
			counted += n
		}
		if counted != summary.Events {
			t.Fatalf("the summary's per-type counts total %d over %d events", counted, summary.Events)
		}
		for i, event := range events {
			if i > 0 && event.Seq <= events[i-1].Seq {
				t.Fatalf("event %d has sequence %d after %d", i, event.Seq, events[i-1].Seq)
			}
			if event.Type == "" {
				t.Fatalf("event %d was accepted with no type", i)
			}
		}
	})
}
