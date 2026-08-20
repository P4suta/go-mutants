// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/tui"
)

// probing returns a probe that answers a fixed way, which is how these tests
// exercise the branch a test process can never reach for real: `go test` does
// not hand its output a terminal.
func probing(isTerminal bool, profile colorprofile.Profile) terminalProbe {
	return func(io.Writer) (bool, colorprofile.Profile) { return isTerminal, profile }
}

func TestTheDashboardIsChosenOnlyWhenItWouldWork(t *testing.T) {
	tests := []struct {
		name    string
		options runOptions
		env     map[string]string
		probe   terminalProbe
		want    bool
	}{
		{
			name:  "a colour terminal gets the dashboard",
			probe: probing(true, colorprofile.TrueColor),
			want:  true,
		},
		{
			name:  "sixteen colours are enough",
			probe: probing(true, colorprofile.ANSI),
			want:  true,
		},
		{
			name:    "--no-tui is the escape hatch",
			options: runOptions{noTUI: true},
			probe:   probing(true, colorprofile.TrueColor),
		},
		{
			name:    "--json owns standard output",
			options: runOptions{json: true},
			probe:   probing(true, colorprofile.TrueColor),
		},
		{
			name:    "--quiet asks for less, not for different",
			options: runOptions{quiet: true},
			probe:   probing(true, colorprofile.TrueColor),
		},
		{
			name:    "--no-color asks for text",
			options: runOptions{noColor: true},
			probe:   probing(true, colorprofile.TrueColor),
		},
		{
			name:  "NO_COLOR asks for text",
			env:   map[string]string{"NO_COLOR": "1"},
			probe: probing(true, colorprofile.TrueColor),
		},
		{
			name:  "CI means the output is a log",
			env:   map[string]string{"CI": "true"},
			probe: probing(true, colorprofile.TrueColor),
		},
		{
			name:  "an empty CI is not set at all",
			env:   map[string]string{"CI": ""},
			probe: probing(true, colorprofile.TrueColor),
			want:  true,
		},
		{
			name:  "a pipe is not a terminal",
			probe: probing(false, colorprofile.NoTTY),
		},
		{
			name:  "a terminal that cannot colour gets the plain lines",
			probe: probing(true, colorprofile.Ascii),
		},
		{
			name:  "a terminal colorprofile calls no terminal at all",
			probe: probing(true, colorprofile.NoTTY),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Both are cleared first: the machine running the tests may have
			// either of them set, and the matrix is about what the flags and
			// the environment say, not about where it runs.
			t.Setenv("CI", "")
			t.Setenv("NO_COLOR", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			options := tc.options
			if got := wantsDashboard(io.Discard, &options, tc.probe); got != tc.want {
				t.Errorf("wantsDashboard = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectTerminalRefusesWhatIsNotAFile(t *testing.T) {
	// A bytes.Buffer has no file descriptor to ask about, and answering
	// anything but "not a terminal" would put escape sequences into whatever
	// it is standing in for.
	isTerminal, profile := detectTerminal(&bytes.Buffer{})
	if isTerminal {
		t.Error("a bytes.Buffer was reported as a terminal")
	}
	if profile != colorprofile.NoTTY {
		t.Errorf("profile = %v for a bytes.Buffer, want NoTTY", profile)
	}

	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close() //nolint:errcheck
	if isTerminal, _ := detectTerminal(f); isTerminal {
		t.Errorf("%s was reported as a terminal", os.DevNull)
	}
}

func TestOnlyATerminalIsHandedToTheDashboardAsInput(t *testing.T) {
	// Anything that is not a keyboard is refused, so that bubbletea never puts
	// somebody's redirected data into raw mode and reads it.
	if got := dashboardInput(&bytes.Buffer{}); got != nil {
		t.Errorf("dashboardInput(&bytes.Buffer{}) = %v, want nil", got)
	}
	if got := dashboardInput(nil); got != nil {
		t.Errorf("dashboardInput(nil) = %v, want nil", got)
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close() //nolint:errcheck
	if got := dashboardInput(f); got != nil {
		t.Errorf("dashboardInput(%s) = %v, want nil", os.DevNull, got)
	}
}

// A synthetic run, as both renderers see it.
func syntheticRun() []engine.Event {
	survivor := engine.MutantResult{
		ID:          strings.Repeat("9f8e7d6c", 8),
		DisplayID:   "9f8e7d6c5b4a3928170f",
		Path:        "untested.go",
		Line:        9,
		Column:      12,
		Rule:        "neq-to-eq",
		Original:    "!=",
		Replacement: "==",
		Outcome:     mutation.OutcomeSurvived,
		Duration:    176 * time.Millisecond,
	}
	return []engine.Event{
		engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 2},
		engine.PhaseChanged{Phase: engine.PhaseMutate, Detail: "executing the mutants"},
		engine.Validated{Accepted: 4, Rejected: 0},
		engine.MutantStarted{DisplayID: survivor.DisplayID, Path: survivor.Path, Line: survivor.Line, Rule: survivor.Rule},
		engine.MutantFinished{Result: survivor},
		engine.Warning{Code: "GOM7301", Message: "a package was skipped"},
		engine.ReportPublished{RunPath: "/cache/runs/20260819T101112Z-a1b2.json", LatestPath: "/cache/latest.json"},
		engine.RunCompleted{
			Status: engine.StatusOK,
			Run: &engine.RunSummary{
				RunID:    "20260819T101112Z-a1b2",
				ExitCode: mutation.ExitOK,
				Notable:  []engine.MutantResult{survivor},
				Counts:   engine.Counts{Total: 4, Killed: 3, Survived: 1},
				Score:    mutation.Score{Detected: 3, Denominator: 4},
				Warnings: 1,
			},
		},
	}
}

// renderPlain renders a whole run the way a run without a terminal does.
func renderPlain(t *testing.T, events []engine.Event) string {
	t.Helper()
	stream := make(chan engine.Event, len(events))
	for _, e := range events {
		stream <- e
	}
	close(stream)
	var buf bytes.Buffer
	if err := console.NewPlain(&buf, Version, false, false).Run(context.Background(), stream); err != nil {
		t.Fatalf("plain render: %v", err)
	}
	return buf.String()
}

func TestTheDashboardsScrollbackIsThePlainRunsClosingBlock(t *testing.T) {
	events := syntheticRun()

	// What a plain run leaves in the scrollback.
	plain := renderPlain(t, events)

	// What a dashboard run leaves in it: the events the dashboard kept, put
	// back through the same renderer once the alternate screen is gone. The
	// dashboard here is the real one, drawing into a writer that is not a
	// terminal and reading from no input at all, which is as close to a run
	// under a terminal as a test process can get.
	dashboard := tui.New(io.Discard, nil, Version, func() {})
	stream := make(chan engine.Event, len(events))
	for _, e := range events {
		stream <- e
	}
	close(stream)
	done := make(chan error, 1)
	go func() { done <- dashboard.Run(context.Background(), stream) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dashboard render: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dashboard did not finish")
	}

	var replayed bytes.Buffer
	if err := replayFinal(&replayed, Version, false, dashboard.Final()); err != nil {
		t.Fatalf("replayFinal: %v", err)
	}

	// The closing block — from the report's paths to the exit line — has to be
	// the same bytes in both. It is the part a user reads, greps, and pastes.
	const marker = "report run: "
	plainTail := plain[strings.LastIndex(plain, marker):]
	replayedTail := replayed.String()[strings.LastIndex(replayed.String(), marker):]
	if plainTail != replayedTail {
		t.Errorf("the closing block differs between the renderers\n--- plain ---\n%s\n--- dashboard ---\n%s", plainTail, replayedTail)
	}

	// And the warning, whose text the alternate screen took with it, is back.
	warning := "warning GOM7301: a package was skipped"
	if !strings.Contains(replayed.String(), warning) {
		t.Errorf("the replayed scrollback lost %q:\n%s", warning, replayed.String())
	}
	if !strings.Contains(plain, warning) {
		t.Fatalf("the plain run did not print %q; the test's premise is wrong", warning)
	}
}

func TestReplayingNothingWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	if err := replayFinal(&buf, Version, false, nil); err != nil {
		t.Fatalf("replayFinal: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("replayFinal wrote %q for an empty stream, want nothing", buf.String())
	}
}
