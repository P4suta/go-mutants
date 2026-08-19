// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/engine"
)

// stream is the event sequence of a successful pre-release run, in the order
// the engine publishes it.
func stream() []engine.Event {
	return []engine.Event{
		engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 8},
		engine.PhaseChanged{Phase: engine.PhaseDiscover, Detail: "locating the Go toolchain and copying the workspace"},
		engine.PhaseChanged{Phase: engine.PhaseBaseline, Detail: "building the snapshot, then 3 timed runs of go test ./..."},
		engine.BaselineProgress{Run: 1, Of: 3, Duration: 152 * time.Millisecond},
		engine.BaselineProgress{Run: 2, Of: 3, Duration: 149 * time.Millisecond},
		engine.BaselineProgress{Run: 3, Of: 3, Duration: 210 * time.Millisecond},
		engine.BaselineCompleted{
			Runs:          []time.Duration{152 * time.Millisecond, 149 * time.Millisecond, 210 * time.Millisecond},
			Average:       170 * time.Millisecond,
			Slowest:       210 * time.Millisecond,
			Timeout:       10 * time.Second,
			TimeoutSource: engine.TimeoutDerived,
		},
		engine.Warning{Code: "GOM0001", Message: "mutation phases not yet implemented — run ends after baseline (pre-release)"},
		engine.RunCompleted{Status: engine.StatusOK, Summary: "baseline only: 3 files snapshotted, workspace digest 260c7b0beff72d8c"},
	}
}

// render feeds events through a renderer and returns what it wrote.
func render(t *testing.T, r *PlainRenderer, events []engine.Event) string {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	ch := make(chan engine.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	if err := r.Run(context.Background(), ch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String()
}

func TestPlainRendererIsByteExact(t *testing.T) {
	const want = "go-mutants 0.1.0-dev (run 20260819T101112Z-a1b2)\n" +
		"phase discover: locating the Go toolchain and copying the workspace\n" +
		"phase baseline: building the snapshot, then 3 timed runs of go test ./...\n" +
		"baseline run 1/3: 152ms\n" +
		"baseline run 2/3: 149ms\n" +
		"baseline run 3/3: 210ms\n" +
		"baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)\n" +
		"warning GOM0001: mutation phases not yet implemented — run ends after baseline (pre-release)\n" +
		"run ok: baseline only: 3 files snapshotted, workspace digest 260c7b0beff72d8c\n"

	got := render(t, NewPlain(nil, "0.1.0-dev", false, false), stream())
	if got != want {
		t.Errorf("plain output mismatch:\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Error("the renderer emitted an escape sequence with colour off")
	}
}

func TestQuietKeepsWhatMatters(t *testing.T) {
	const want = "baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)\n" +
		"warning GOM0001: mutation phases not yet implemented — run ends after baseline (pre-release)\n" +
		"run ok: baseline only: 3 files snapshotted, workspace digest 260c7b0beff72d8c\n"

	got := render(t, NewPlain(nil, "0.1.0-dev", false, true), stream())
	if got != want {
		t.Errorf("quiet output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFailedRunRendersItsStatus(t *testing.T) {
	events := []engine.Event{
		engine.RunCompleted{Status: engine.StatusFailed, Summary: "GOM4011: baseline run 1 of 3 failed: exited with status 1"},
	}
	const want = "run failed: GOM4011: baseline run 1 of 3 failed: exited with status 1\n"
	if got := render(t, NewPlain(nil, "0.1.0-dev", false, false), events); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestInterruptedRunRendersItsStatus(t *testing.T) {
	events := []engine.Event{engine.RunCompleted{Status: engine.StatusInterrupted}}
	if got := render(t, NewPlain(nil, "0.1.0-dev", false, false), events); got != "run interrupted:\n" {
		t.Errorf("got %q, want %q", got, "run interrupted:\n")
	}
}

func TestReportPublishedRenders(t *testing.T) {
	events := []engine.Event{engine.ReportPublished{Format: "json", Path: "/w/reports/mutation/mutation.json", Bytes: 42}}
	const want = "report json: /w/reports/mutation/mutation.json\n"
	if got := render(t, NewPlain(nil, "0.1.0-dev", false, false), events); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestColorOnlyChangesBytesWhenEnabled(t *testing.T) {
	plain := render(t, NewPlain(nil, "0.1.0-dev", false, false), stream())
	colored := render(t, NewPlain(nil, "0.1.0-dev", true, false), stream())
	if plain == colored {
		// lipgloss may legitimately fall back to no styling when it decides the
		// environment has no colour profile, so this is a soft signal rather
		// than a hard requirement.
		t.Log("colour produced identical bytes; lipgloss found no colour profile in this environment")
	}
	// Whatever styling did or did not happen, the information must survive.
	for _, needle := range []string{"baseline ok:", "warning GOM0001:", "run ok:"} {
		if !strings.Contains(colored, needle) {
			t.Errorf("coloured output lost %q", needle)
		}
	}
}

// failingWriter refuses every write, so that a renderer's error path can be
// exercised against a sender that is still producing events.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRunReportsAWriteFailureWithoutDeadlocking(t *testing.T) {
	boom := errors.New("no room on device")
	r := NewPlain(failingWriter{err: boom}, "0.1.0-dev", false, false)

	events := stream()
	// Unbuffered: a renderer that stopped reading would deadlock the sender,
	// which is exactly the failure this test is about.
	ch := make(chan engine.Event)
	go func() {
		for _, e := range events {
			ch <- e
		}
		close(ch)
	}()

	err := r.Run(context.Background(), ch)
	if !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want the write error", err)
	}
}

// syncBuffer is a bytes.Buffer safe to read from the test goroutine while the
// renderer writes from its own.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLinesAppearBeforeTheStreamCloses(t *testing.T) {
	// A renderer that buffered the whole run would pass every byte-exact test
	// above and still show a user nothing until the run was over, which is the
	// opposite of what a progress banner is for.
	out := &syncBuffer{}
	r := NewPlain(out, "0.1.0-dev", false, false)

	events := make(chan engine.Event)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), events) }()

	events <- engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 8}
	// A second event, unbuffered, cannot be accepted until the first has been
	// taken off the channel and rendered — so by the time this send returns,
	// the header has been through Run's loop.
	events <- engine.PhaseChanged{Phase: engine.PhaseDiscover, Detail: "copying the workspace"}

	if got := out.String(); !strings.Contains(got, "go-mutants 0.1.0-dev (run 20260819T101112Z-a1b2)") {
		t.Errorf("the header had not reached the writer while the stream was still open; buffer held %q", got)
	}

	close(events)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunSurvivesACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := renderWithContext(t, ctx, NewPlain(nil, "0.1.0-dev", false, false), stream())
	if !strings.Contains(got, "run ok:") {
		t.Error("a cancelled context stopped the renderer before the terminal event")
	}
}

func renderWithContext(t *testing.T, ctx context.Context, r *PlainRenderer, events []engine.Event) string {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	ch := make(chan engine.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	if err := r.Run(ctx, ch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String()
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"},
		{151987300 * time.Nanosecond, "152ms"},
		{10 * time.Second, "10s"},
		{90 * time.Second, "1m30s"},
		{1500 * time.Millisecond, "1.5s"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestColorEnabledRefusesNonFiles(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "")
	if ColorEnabled(&bytes.Buffer{}, false) {
		t.Error("colour was enabled for a writer that is not a terminal")
	}
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(&bytes.Buffer{}, false) {
		t.Error("NO_COLOR did not turn colour off")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "true")
	if ColorEnabled(&bytes.Buffer{}, false) {
		t.Error("CI did not turn colour off")
	}
	if ColorEnabled(&bytes.Buffer{}, true) {
		t.Error("--no-color did not turn colour off")
	}
}

// TestEveryEventRenders is the guard against an event type that nothing
// prints. The list below has to be extended by hand when the sealed interface
// grows, which is the point: adding a case to the renderer and adding a line
// here are the same review.
func TestEveryEventRenders(t *testing.T) {
	all := []engine.Event{
		engine.RunPlanned{},
		engine.PhaseChanged{},
		engine.BaselineProgress{},
		engine.BaselineCompleted{},
		engine.Warning{},
		engine.ReportPublished{},
		engine.RunCompleted{},
	}
	r := NewPlain(nil, "0.1.0-dev", false, false)
	r.Out = &bytes.Buffer{}
	for _, e := range all {
		if _, ok := r.line(e); !ok {
			t.Errorf("%T renders nothing", e)
		}
	}
}
