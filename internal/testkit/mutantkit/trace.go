// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

const TraceRingSize = 4096

const TraceTailLines = 200

const traceToolVersion = "go-mutants test harness"

func Trace(t testing.TB) *trace.Recorder {
	t.Helper()
	r := attach(t)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lent {
		return nil
	}
	if r.recorder == nil {
		r.recorder = trace.New(r.sink, nil, trace.StartRecord{
			Kind:        trace.StartKindWorkspace,
			ToolVersion: traceToolVersion,
			PID:         os.Getpid(),
			Root:        testkit.Root(t),
		})
		r.owned = true
	}
	return r.recorder
}

func TraceSink(t testing.TB) trace.Sink {
	t.Helper()
	r := attach(t)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lent = true
	return r.sink
}

type recording struct {
	mu       sync.Mutex
	sink     trace.Sink
	ring     *trace.MemorySink
	recorder *trace.Recorder
	owned    bool
	lent     bool
}

var recordings struct {
	mu     sync.Mutex
	byTest map[string]*recording
}

func attach(t testing.TB) *recording {
	recordings.mu.Lock()
	if recordings.byTest == nil {
		recordings.byTest = map[string]*recording{}
	}
	name := t.Name()
	if existing, ok := recordings.byTest[name]; ok {
		recordings.mu.Unlock()
		return existing
	}
	ring := trace.NewMemorySink(TraceRingSize)
	fresh := &recording{sink: trace.Digested(ring), ring: ring}
	recordings.byTest[name] = fresh
	recordings.mu.Unlock()

	kept := testkit.KeptDir(t)
	testkit.KeepSection(t, "Trace tail", func() []string { return traceTail(ring, TraceTailLines) })

	t.Cleanup(func() {
		recordings.mu.Lock()
		delete(recordings.byTest, name)
		recordings.mu.Unlock()

		fresh.close(t)
		if t.Failed() {
			tail := traceTail(ring, TraceTailLines)
			held, dropped := accounting(ring)
			t.Logf("trace tail (%d of %d events held, %d lost):\n%s",
				len(tail), held, dropped, strings.Join(tail, "\n"))
		}
		if !testkit.Keeping(t) || kept == "" {
			return
		}
		if err := writeRecording(filepath.Join(kept, trace.FileName), ring.Events()); err != nil {
			t.Logf("mutantkit: the recording could not be kept: %v", err)
		}
	})
	return fresh
}

func (r *recording) close(t testing.TB) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.owned {
		return
	}
	verdict := "ok"
	if t.Failed() {
		verdict = "failed"
	}
	r.recorder.RunEnd(verdict, 0, nil)
}

func accounting(ring *trace.MemorySink) (held, dropped int64) {
	events := ring.Events()
	if len(events) != 0 {
		if last := events[len(events)-1]; last.Type == trace.TypeRunEnd && last.Run != nil {
			return int64(len(events)), last.Run.EventsDropped
		}
	}
	return int64(len(events)), ring.Dropped()
}

func writeRecording(path string, events []trace.Event) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	var failures []error
	for _, event := range events {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			failures = append(failures, marshalErr)
			continue
		}
		if _, writeErr := file.Write(append(encoded, '\n')); writeErr != nil {
			failures = append(failures, writeErr)
			break
		}
	}
	if closeErr := file.Close(); closeErr != nil {
		failures = append(failures, closeErr)
	}
	if len(failures) != 0 {
		return failures[0]
	}
	return nil
}

func traceTail(ring *trace.MemorySink, limit int) []string {
	events := ring.Events()
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, traceLine(event))
	}
	return lines
}

func traceLine(event trace.Event) string {
	fields := []string{strconv.FormatInt(event.Seq, 10), event.Type}
	switch {
	case event.Start != nil:
		fields = append(fields, event.Start.Kind, event.Start.Root)
	case event.Phase != nil:
		fields = append(fields, event.Phase.Name, durationField(event.Phase.DurationMS))
	case event.Stage != nil:
		fields = append(fields, event.Stage.Name, event.Stage.State, event.Stage.Result,
			durationField(event.Stage.DurationMS))
	case event.Prepare != nil:
		fields = append(fields, event.Prepare.Phase, event.Prepare.State, event.Prepare.Result,
			durationField(event.Prepare.DurationMS))
	case event.Exec != nil:
		fields = append(fields, event.Exec.Kind, event.Exec.Subject,
			"exit="+strconv.Itoa(event.Exec.ExitCode),
			millis(event.Exec.DurationMS), digest8(event.Exec.OutputSHA256),
			strings.Join(event.Exec.Argv, " "))
	case event.Mutant != nil:
		fields = append(fields, event.Mutant.DisplayID, event.Mutant.ID, event.Mutant.Outcome,
			millis(event.Mutant.DurationMS))
	case event.Probe != nil:
		fields = append(fields, event.Probe.Outcome, "exit="+strconv.Itoa(event.Probe.ExitCode))
	case event.Validate != nil:
		fields = append(fields, event.Validate.Tree, event.Validate.Op)
	case event.Cache != nil:
		fields = append(fields, event.Cache.Op, event.Cache.Result)
	case event.Snapshot != nil:
		fields = append(fields, event.Snapshot.Kind, event.Snapshot.Dir, digest8(event.Snapshot.Digest))
	case event.Sweep != nil:
		fields = append(fields, event.Sweep.Parent)
	case event.Artifact != nil:
		fields = append(fields, event.Artifact.Kind, event.Artifact.Path)
	case event.Note != nil:
		fields = append(fields, event.Note.Kind, event.Note.Code, event.Note.Detail)
	case event.Run != nil:
		fields = append(fields, event.Run.Verdict, "exit="+strconv.Itoa(event.Run.ExitCode), event.Run.Error)
	}
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, " ")
}

func durationField(ms *int64) string {
	if ms == nil {
		return ""
	}
	return millis(*ms)
}

func millis(ms int64) string { return fmt.Sprintf("%dms", ms) }

func digest8(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}
