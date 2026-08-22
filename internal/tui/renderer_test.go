// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// headless returns a renderer that drives a real bubbletea program with no
// terminal: no input, no output, and no renderer to paint with. Everything
// about the lifecycle — the forwarding goroutine, the quit on
// [engine.RunCompleted], the drain — is the production path.
func headless(cancel func()) *Renderer {
	r := New(io.Discard, nil, "0.1.0-dev", cancel)
	r.programOptions = []tea.ProgramOption{tea.WithoutRenderer()}
	return r
}

// completed is the closing event of a synthetic run.
func completed() engine.RunCompleted {
	return engine.RunCompleted{
		Status: engine.StatusOK,
		Run: &engine.RunSummary{
			RunID:    "20260819T101112Z-a1b2",
			ExitCode: mutation.ExitOK,
			Counts:   engine.Counts{Total: 2, Killed: 1, Survived: 1},
			Score:    mutation.Score{Detected: 1, Denominator: 2},
		},
	}
}

func TestRunDrawsTheStreamAndKeepsWhatOutlivesTheScreen(t *testing.T) {
	r := headless(func() {})
	events := make(chan engine.Event, 16)
	for _, e := range planned(2) {
		events <- e
	}
	events <- engine.MutantFinished{Result: killedResult}
	events <- engine.Warning{Code: "GOM7301", Message: "a package was skipped"}
	events <- engine.MutantFinished{Result: survivorResult}
	events <- engine.ReportPublished{RunPath: "/c/runs/a1b2.json", LatestPath: "/c/latest.json"}
	events <- completed()
	close(events)

	if err := runWithin(t, r, events, 10*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// What is kept is what the alternate screen destroys and a user still
	// needs: the warnings, the report's paths, and the closing block. In the
	// order they arrived, so that internal/cli can replay them as a stream.
	final := r.Final()
	if len(final) != 3 {
		t.Fatalf("Final() kept %d events, want 3: %#v", len(final), final)
	}
	if _, ok := final[0].(engine.Warning); !ok {
		t.Errorf("Final()[0] is %T, want engine.Warning", final[0])
	}
	if _, ok := final[1].(engine.ReportPublished); !ok {
		t.Errorf("Final()[1] is %T, want engine.ReportPublished", final[1])
	}
	last, ok := final[2].(engine.RunCompleted)
	if !ok {
		t.Fatalf("Final()[2] is %T, want engine.RunCompleted", final[2])
	}
	if last.Run == nil || last.Run.RunID != "20260819T101112Z-a1b2" {
		t.Errorf("Final() lost the closing summary: %#v", last)
	}
}

func TestRunKeepsDrainingAfterTheDashboardHasQuit(t *testing.T) {
	// This is the second-Ctrl-C path in miniature: the program is gone and the
	// engine is still sending. A renderer that stopped reading here would
	// deadlock the very shutdown that publishes the report.
	r := headless(func() {})
	events := make(chan engine.Event)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), events) }()

	events <- engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 2}
	events <- completed() // the dashboard quits here

	for i := 0; i < 64; i++ {
		select {
		case events <- engine.Warning{Code: "GOM7520", Message: "still unwinding"}:
		case err := <-done:
			t.Fatalf("Run returned while the stream was still open: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("the renderer stopped draining after the dashboard quit")
		}
	}

	select {
	case err := <-done:
		t.Fatalf("Run returned before the stream closed: %v", err)
	default:
	}

	close(events)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the stream closed")
	}
}

func TestRunQuitsOnAStreamThatEndsWithoutRunCompleted(t *testing.T) {
	// The engine promises a RunCompleted on every path. If one ever went
	// missing the dashboard would be holding a terminal nobody could get back,
	// so a closed channel ends it too.
	r := headless(func() {})
	events := make(chan engine.Event, 2)
	events <- engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 1}
	close(events)

	if err := runWithin(t, r, events, 10*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(r.Final()); got != 0 {
		t.Errorf("Final() kept %d events from a stream with no summary, want 0", got)
	}
}

func TestAnInputThatIsNotAKeyboardDoesNotFailTheRun(t *testing.T) {
	// internal/cli hands the dashboard a terminal or nothing, so this should
	// not happen — but a dashboard is decoration over a run that has already
	// done the work, and a decoration that turned a successful run into a
	// non-zero exit would be the worst bug this package could have. An input
	// at end of file is what a redirected standard input looks like.
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close() //nolint:errcheck

	r := New(io.Discard, f, "0.1.0-dev", func() {})
	r.programOptions = []tea.ProgramOption{tea.WithoutRenderer()}
	events := make(chan engine.Event, 2)
	events <- engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 1}
	events <- completed()
	close(events)

	if err := runWithin(t, r, events, 10*time.Second); err != nil {
		t.Fatalf("Run reported %v for an input that is not a terminal, want nil", err)
	}
	if got := len(r.Final()); got != 1 {
		t.Errorf("Final() kept %d events, want the closing block", got)
	}
}

// TestKeyboardRefusesAFileThatIsNotATerminalAndNothingElse states the rule the
// test above depends on, directly and on every platform.
//
// The test above is the one that matters and the one that cannot be trusted to
// notice: it only fails on Linux, because only Linux refuses to poll a
// descriptor that is not a terminal, so widening [keyboard] to refuse every
// reader that is not a terminal — or dropping it from the program options
// altogether — would leave every other platform's gates green. os.DevNull is
// not a terminal anywhere and a reader with no descriptor is not a file
// anywhere, which is what makes both halves of the rule checkable here.
func TestKeyboardRefusesAFileThatIsNotATerminalAndNothingElse(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close() //nolint:errcheck

	if got := keyboard(f); got != nil {
		t.Errorf("keyboard(%s) = %v, want nil: bubbletea would poll a descriptor Linux rejects",
			os.DevNull, got)
	}
	if got := keyboard(nil); got != nil {
		t.Errorf("keyboard(nil) = %v, want nil", got)
	}
	// A reader that is no file at all is handed over unchanged. It never
	// reaches the platform machinery — cancelreader falls back to a goroutine
	// for anything without a descriptor — so refusing it would take input away
	// from a caller for no reason at all.
	keys := strings.NewReader("q")
	if got := keyboard(keys); got != keys {
		t.Errorf("keyboard(a strings.Reader) = %v, want the reader itself", got)
	}
}

func TestFinalIsACopy(t *testing.T) {
	r := headless(func() {})
	events := make(chan engine.Event, 2)
	events <- completed()
	close(events)
	if err := runWithin(t, r, events, 10*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := r.Final()
	first[0] = engine.Warning{Code: "GOM0000", Message: "clobbered"}
	if _, ok := r.Final()[0].(engine.RunCompleted); !ok {
		t.Error("Final() handed out its own slice; a caller mutated the renderer's state")
	}
}

// runWithin runs the renderer and fails the test if it has not returned in
// time, rather than letting the whole package time out with no clue why.
func runWithin(t *testing.T, r *Renderer, events <-chan engine.Event, limit time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), events) }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestCodesAreUniqueAndInBlock(t *testing.T) {
	seen := make(map[Code]bool, len(Codes()))
	for _, c := range Codes() {
		if seen[c] {
			t.Errorf("code %q is listed twice", c)
		}
		seen[c] = true
		// GOM770x, not the whole of GOM77xx: internal/gitdiff shares the block
		// from GOM7710 upwards, and the tens digit is what keeps the two
		// allocations from ever meeting.
		if !strings.HasPrefix(string(c), "GOM770") || len(c) != 7 {
			t.Errorf("code %q is outside the GOM770x range this package holds", c)
		}
	}
}

func TestErrorCarriesItsCodeAndItsCause(t *testing.T) {
	cause := errors.New("the underlying trouble")
	err := &Error{Code: CodeProgram, Message: "the live dashboard stopped before the run did", Err: cause}
	want := "GOM7701: the live dashboard stopped before the run did: the underlying trouble"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	bare := &Error{Code: CodeProgram, Message: "no terminal"}
	if got, want := bare.Error(), "GOM7701: no terminal"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
