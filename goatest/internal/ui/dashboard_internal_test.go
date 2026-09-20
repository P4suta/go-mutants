// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	completedMutationFixture = 4
	dashboardStopDeadline    = 5 * time.Second
)

func TestDashboardPhaseVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ kind, phase string }{
		{"snapshot", "snapshot"}, {"cache-hit", "snapshot"}, {"cache-wait", "snapshot"},
		{"impact-broad", "impact"}, {"impact-targeted", "impact"},
		{"baseline-progress", "baseline"}, {"resume-baseline", "baseline"},
		{"race", "race"}, {"resume-race", "race"},
		{"mutation-target", "mutation"}, {"mutation-progress", "mutation"},
		{"probe-target", "probe"}, {"probe-progress", "probe"}, {"probe-summary", "probe"},
		{"repair-applied", "repair"},
	} {
		if phase, known := dashboardPhase(test.kind); !known || phase != test.phase {
			t.Errorf("dashboardPhase(%q) = (%q, %t), want (%q, true)", test.kind, phase, known, test.phase)
		}
	}
	if phase, known := dashboardPhase("checkpoint-warning"); known || phase != "" {
		t.Fatalf("unknown phase = (%q, %t)", phase, known)
	}
}

func TestDashboardFormattingAndEstimateBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input time.Duration
		want  string
	}{
		{-time.Second, "00:00"}, {0, "00:00"}, {59 * time.Second, "00:59"}, {65 * time.Second, "01:05"},
		{time.Hour, "1:00:00"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1:02:03"},
	} {
		if got := formatElapsed(test.input); got != test.want {
			t.Errorf("formatElapsed(%s) = %q, want %q", test.input, got, test.want)
		}
	}
	now := time.Date(2026, 9, 1, 12, 0, 10, 0, time.UTC)
	renderer := &dashboard{now: func() time.Time { return now }, mutationTotal: completedMutationFixture}
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed before progress")
	}
	renderer.mutationStarted = now.Add(-time.Second)
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed at zero completed mutants")
	}
	renderer.mutationDone = completedMutationFixture
	renderer.mutationStarted = now.Add(-time.Second)
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed after completion")
	}
	renderer.mutationDone = 1
	renderer.mutationStarted = now
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed at zero elapsed time")
	}
	renderer.mutationStarted = now.Add(time.Second)
	if _, ok := renderer.estimatedRemainder(); ok {
		t.Fatal("estimate existed for non-positive elapsed time")
	}
	renderer.mutationStarted = now.Add(-2 * time.Second)
	if remaining, ok := renderer.estimatedRemainder(); !ok || remaining != 6*time.Second {
		t.Fatalf("estimate = (%s, %t), want (6s, true)", remaining, ok)
	}
}

func TestDashboardNoOpRenderingAndDefaultTickerLifecycle(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	renderer := &dashboard{writer: &output, now: time.Now, width: 80}
	renderer.eraseLocked()
	renderer.renderLocked()
	if output.Len() != 0 {
		t.Fatalf("empty dashboard rendered %q", output.String())
	}
	renderer.rendered = true
	renderer.eraseLocked()
	if renderer.rendered {
		t.Fatal("erase left the dashboard marked as rendered")
	}
	output.Reset()

	synchronised := &syncBuffer{}
	notes := NewDashboard(synchronised, DashboardOptions{})
	owned := notes.(*dashboard)
	stopped := make(chan struct{})
	previous := owned.stopTicker
	owned.stopTicker = func() {
		previous()
		close(stopped)
	}
	notes.Note("snapshot", "default options")
	drawn := synchronised.len()
	deadline := time.After(defaultTickerDeadline)
	for synchronised.len() == drawn {
		select {
		case <-deadline:
			t.Fatal("a dashboard given no tick stream of its own never redrew; it owns no ticker")
		case <-time.After(time.Millisecond):
		}
	}
	notes.Close()
	select {
	case <-stopped:
	default:
		t.Fatal("closing a dashboard that owns its ticker did not stop it")
	}
	output.WriteString(synchronised.String())
	if !strings.Contains(output.String(), "default options") || strings.Contains(output.String(), "0/0") {
		t.Fatalf("default dashboard output = %q", output.String())
	}
}

func TestDashboardInvalidAndEarlyMutationProgress(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	tick := make(chan time.Time)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	notes := NewDashboard(&output, DashboardOptions{Now: func() time.Time { return now }, Tick: tick})
	renderer := notes.(*dashboard)
	notes.Note("baseline-progress", "not-a-fraction")
	if renderer.baselineTotal != 0 || renderer.detail != "not-a-fraction" {
		t.Fatalf("invalid baseline progress state = %+v", renderer)
	}
	notes.Note("baseline-progress", "0/3")
	if renderer.baselineDone != 0 || renderer.baselineTotal != 3 || renderer.detail != "" {
		t.Fatalf("baseline progress state = %+v", renderer)
	}
	notes.Note("mutation-progress", "not-a-fraction")
	if renderer.mutationTotal != 0 || renderer.detail != "not-a-fraction" || !renderer.mutationStarted.IsZero() {
		t.Fatalf("invalid progress state = %+v", renderer)
	}
	notes.Note("mutation-progress", "1/0")
	if renderer.mutationDone != 0 || renderer.mutationTotal != 0 || renderer.detail != "1/0" || !renderer.mutationStarted.IsZero() {
		t.Fatalf("zero-total progress state = %+v", renderer)
	}
	notes.Note("mutation-progress", "0/3")
	if renderer.mutationDone != 0 || renderer.mutationTotal != 3 || renderer.detail != "" || renderer.mutationStarted != now {
		t.Fatalf("early progress state = %+v", renderer)
	}
	notes.Note("mutation-target", "3 mutants")
	if renderer.mutationDone != 0 || renderer.mutationTotal != 0 || renderer.mutationStarted != now {
		t.Fatalf("new mutation phase retained prior progress = %+v", renderer)
	}
	notes.Close()
}

func TestDashboardWatcherStopsWhenTickStreamCloses(t *testing.T) {
	t.Parallel()
	tick := make(chan time.Time)
	renderer := NewDashboard(io.Discard, DashboardOptions{Tick: tick}).(*dashboard)
	close(tick)
	select {
	case <-renderer.done:
	case <-time.After(dashboardStopDeadline):
		t.Fatal("watcher did not stop")
	}
	renderer.Close()
}

func TestDashboardWatchDoesNotRenderAClosedDashboard(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tick := make(chan time.Time, 1)
	tick <- now
	close(tick)
	renderer := &dashboard{
		writer: &output, now: func() time.Time { return now }, width: 80,
		started: now, phase: "snapshot", closed: true,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	returned := make(chan struct{})
	go func() { renderer.watch(tick); close(returned) }()
	defer close(renderer.stop)
	select {
	case <-returned:
	case <-time.After(dashboardStopDeadline):
		t.Fatal("watch did not return when its tick stream closed")
	}
	if output.Len() != 0 || renderer.rendered {
		t.Fatalf("closed dashboard rendered on tick: %q", output.String())
	}
}

func TestBoundedLineKeepsShortTextAndTruncatesByRunes(t *testing.T) {
	t.Parallel()
	if got := boundedLine("short", 5); got != "short" {
		t.Fatalf("short line = %q", got)
	}
	if got := boundedLine("日本語abcdef", 5); got != "日本語a…" || len([]rune(got)) != 5 {
		t.Fatalf("bounded Unicode line = %q", got)
	}
}

func TestDashboardResumingTheBaselineForgetsTheCountItHad(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	tick := make(chan time.Time)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	notes := NewDashboard(&output, DashboardOptions{Now: func() time.Time { return now }, Tick: tick})
	renderer := notes.(*dashboard)

	notes.Note("baseline-progress", "2/3")
	if renderer.baselineDone != 2 || renderer.baselineTotal != 3 {
		t.Fatalf("baseline progress state = %+v", renderer)
	}
	notes.Note("resume-baseline", "round 2")
	if renderer.baselineDone != 0 || renderer.baselineTotal != 0 {
		t.Errorf("a resumed baseline kept %d/%d from the round before it",
			renderer.baselineDone, renderer.baselineTotal)
	}
	if renderer.detail != "round 2" {
		t.Errorf("detail = %q, want the resume's own", renderer.detail)
	}

	notes.Note("baseline-progress", "1/4")
	notes.Note("mutation-target", "4396 mutants")
	if renderer.baselineDone != 1 || renderer.baselineTotal != 4 {
		t.Errorf("a kind that is not resume-baseline cleared the count: %+v", renderer)
	}
}

func TestDashboardShowsACountOnlyOnceTheTotalIsKnown(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		kind    string
		detail  string
		counted bool
	}{
		{"a baseline that has not counted yet", "baseline-progress", "not-a-fraction", false},
		{"a baseline with a total", "baseline-progress", "0/3", true},
		{"a mutation that has not counted yet", "mutation-progress", "not-a-fraction", false},
		{"a mutation with a total", "mutation-progress", "0/9", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
			notes := NewDashboard(&output, DashboardOptions{Now: func() time.Time { return now }, Tick: make(chan time.Time)})
			notes.Note(test.kind, test.detail)
			if counted := strings.Contains(output.String(), "/"); counted != test.counted {
				t.Errorf("a status line with %q showed a count = %t, want %t: %q",
					test.detail, counted, test.counted, output.String())
			}
		})
	}
}

func TestDashboardOmitsADetailItDoesNotHave(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	notes := NewDashboard(&output, DashboardOptions{Now: func() time.Time { return now }, Tick: make(chan time.Time)})

	notes.Note("baseline-progress", "1/3")
	line := strings.TrimRight(output.String(), " ")
	if strings.Contains(line, "\u00b7 \u00b7") || strings.HasSuffix(line, "\u00b7") {
		t.Errorf("the status line holds a separator with nothing after it: %q", output.String())
	}
	if !strings.HasSuffix(line, "1/3") {
		t.Errorf("the status line = %q, want it to end at the count when there is no detail", output.String())
	}
}

func TestDashboardShowsAnEstimateOnlyOnceItHasOne(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	notes := NewDashboard(&output, DashboardOptions{Now: func() time.Time { return now }, Tick: make(chan time.Time)})

	notes.Note("mutation-progress", "0/1000")
	if strings.Contains(output.String(), "eta") {
		t.Errorf("the first mutation progress carried an estimate: %q", output.String())
	}
	output.Reset()
	now = now.Add(time.Minute)
	notes.Note("mutation-progress", "100/1000")
	if !strings.Contains(output.String(), "eta") {
		t.Errorf("a second progress with time behind it carried none: %q", output.String())
	}
}

const defaultTickerDeadline = 3 * time.Second

type syncBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (writer *syncBuffer) Write(p []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.buffer.Write(p)
}

func (writer *syncBuffer) len() int {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.buffer.Len()
}

func (writer *syncBuffer) String() string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.buffer.String()
}
