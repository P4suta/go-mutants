// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const (
	theOpeningEventAndTheRecord = 2
	aHeadingAndOneRow           = 2
)

func TestReadEventsAcceptsEveryRecordItsGuardsAreAbout(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		line string
	}{
		{
			name: "a route that counted its file candidates",
			line: `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":` +
				`{"mutant_id":"m-1","rule":"eq-to-neq","path":"a.go","line":1,"column":1,` +
				`"granularity":"file","reason":"coverage-reaching","plan":["individual:TestOne"],` +
				`"reaching_targets":["TestOne"],"file_candidates":3}}`,
		},
		{
			name: "a route whose suite controls name one package",
			line: `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":` +
				`{"mutant_id":"m-1","rule":"eq-to-neq","path":"a.go","line":1,"column":1,` +
				`"granularity":"block","reason":"coverage-reaching","plan":["individual:TestOne"],` +
				`"reaching_targets":["TestOne"],"suite_coverage":"package-suite-coverage:example.com/app",` +
				`"suite_reached":true,"suite_probe":"package-suite:example.com/app","probed":true}}`,
		},
		{
			name: "an exact original preflight that carries neither a suite nor an infection",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":` +
				`{"target":"mutation-control:example.com/app","package":"example.com/app","control":true,` +
				`"exit_code":0,"outcome":"measured"}}`,
		},
		{
			name: "a probe stopped by an error that names it",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":` +
				`{"target":"TestOne","package":"example.com/app","exit_code":0,"error":"the process would not start"}}`,
		},
		{
			name: "a suite probe of the package it names",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":` +
				`{"target":"package-suite:example.com/app","package":"example.com/app","suite":true,` +
				`"exit_code":0,"outcome":"measured"}}`,
		},
		{
			name: "a target probe that is neither a suite nor a control",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":` +
				`{"target":"TestOne","package":"example.com/app","exit_code":0,"outcome":"measured"}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events, err := readEvents(strings.NewReader(stream(runStart, test.line)))
			if err != nil {
				t.Fatalf("readEvents refused %s: %v", test.name, err)
			}
			if len(events) != theOpeningEventAndTheRecord {
				t.Fatalf("readEvents read %d events, want the opening one and the record", len(events))
			}
		})
	}
}

func TestReadEventsRefusesALineItCannotDecodeAtAll(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{name: "a line that is not JSON", line: "{", want: "line 2"},
		{name: "a line that is not an object", line: `["run-end"]`, want: "line 2"},
		{name: "a line of two values", line: `{"seq":2,"type":"run-end"} {"seq":3}`, want: "more than one value"},
		{
			name: "a payload that is not an object",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":7}`,
			want: "line 2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := readEvents(strings.NewReader(stream(runStart, test.line)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readEvents reported %v, want one containing %q", err, test.want)
			}
		})
	}
}

func TestRenderingATableWithNoRowsWritesItsHeadingAlone(t *testing.T) {
	t.Parallel()
	columns := []column{{"left", false}, {"right", true}}
	lines := renderTable(columns, nil)
	if len(lines) != 1 {
		t.Fatalf("a table of no row rendered %q, want the heading alone", lines)
	}
	if !strings.Contains(lines[0], "left") || !strings.Contains(lines[0], "right") {
		t.Fatalf("the heading reads %q, want both columns", lines[0])
	}
	withRow := renderTable(columns, [][]string{{"a", "b"}})
	if len(withRow) != aHeadingAndOneRow {
		t.Fatalf("a table of one row rendered %q, want a heading and the row", withRow)
	}
}

func TestPrepareTotalsReadOnlyThePrepareEventsAndTheDurationsTheyCarry(t *testing.T) {
	t.Parallel()
	events := []trace.Event{
		{Type: trace.TypePrepare, Prepare: &trace.PrepareRecord{
			Phase: trace.PreparePhaseDiscovery, State: trace.PrepareStateStarted,
		}},
		{Type: trace.TypePrepare, Prepare: &trace.PrepareRecord{
			Phase: trace.PreparePhaseDiscovery, State: trace.PrepareStateFinished,
			Result: trace.PrepareResultSucceeded,
		}},
		{Type: trace.TypeExec, Prepare: &trace.PrepareRecord{
			Phase: trace.PreparePhaseBinaryBuild, State: trace.PrepareStateStarted,
		}},
		{Type: trace.TypePrepare},
	}
	totals := prepareTotals(events)
	if len(totals) != 1 || totals[0].phase != trace.PreparePhaseDiscovery {
		t.Fatalf("prepareTotals read %+v, want only the prepare events of one phase", totals)
	}
	if totals[0].duration != 0 || totals[0].finished != 1 || totals[0].succeeded != 1 {
		t.Fatalf("a finished phase that named no duration totalled %+v", totals[0])
	}
}

func TestReadEventsRefusesAProbeWhoseEvidenceContradictsItself(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		probe string
		want  string
	}{
		{
			name: "infections recorded beside the error that stopped the execution",
			probe: `{"target":"TestOne","package":"example.com/app","exit_code":0,` +
				`"error":"the process would not start","infected":["m-1"]}`,
			want: "recorded infections with outcome",
		},
		{
			name: "a suite probe named by the bare package-suite prefix",
			probe: `{"target":"` + trace.PackageSuiteProbePrefix + `","package":"example.com/app","suite":true,` +
				`"exit_code":0,"outcome":"measured"}`,
			want: "want the package-suite identity of that exact package",
		},
		{
			name: "a suite probe of one package named by another package's identity",
			probe: `{"target":"` + trace.PackageSuiteProbePrefix + `example.com/other","package":"example.com/app",` +
				`"suite":true,"exit_code":0,"outcome":"measured"}`,
			want: "want the package-suite identity of that exact package",
		},
		{
			name: "a suite probe that is the bare prefix and names no package at all",
			probe: `{"target":"` + trace.PackageSuiteProbePrefix + `","suite":true,` +
				`"exit_code":0,"outcome":"measured"}`,
			want: "want the package-suite identity of that exact package",
		},
		{
			name: "a suite probe that names no package at all",
			probe: `{"target":"` + trace.PackageSuiteProbePrefix + `example.com/app","suite":true,` +
				`"exit_code":0,"outcome":"measured"}`,
			want: "want the package-suite identity of that exact package",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := readEvents(strings.NewReader(stream(runStart, probeLine(test.probe))))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readEvents reported %v, want one containing %q", err, test.want)
			}
		})
	}
}
