// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestReadEventsNamesTheFieldEachGuardRefused(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{
			name: "a progress event with no kind at all",
			line: `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{}}`,
			want: `missing required field "progress.kind"`,
		},
		{
			name: "a progress event whose kind is empty",
			line: `{"seq":2,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":""}}`,
			want: "progress.kind is empty",
		},
		{
			name: "an artifact event with no kind at all",
			line: `{"seq":2,"type":"artifact","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"artifact":{"path":"p"}}`,
			want: `missing required field "artifact.kind"`,
		},
		{
			name: "an artifact event with no path at all",
			line: `{"seq":2,"type":"artifact","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"artifact":{"kind":"k"}}`,
			want: `missing required field "artifact.path"`,
		},
		{
			name: "a run event that emitted a negative number of events",
			line: `{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":{"events_emitted":-1,"events_dropped":0}}`,
			want: "run.events_emitted is -1",
		},
		{
			name: "a run event that dropped a negative number of events",
			line: `{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":{"events_emitted":0,"events_dropped":-1}}`,
			want: "run.events_dropped is -1",
		},
		{
			name: "a payload that is not an object",
			line: `{"seq":2,"type":"run-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"run":[]}`,
			want: "line 2",
		},
		{
			name: "an event with no timestamp at all",
			line: `{"seq":2,"type":"progress","timestamp":"","elapsed_ms":1,"progress":{"kind":"note"}}`,
			want: "timestamp is empty",
		},
		{
			name: "an event whose timestamp is no moment",
			line: `{"seq":2,"type":"progress","timestamp":"whenever","elapsed_ms":1,"progress":{"kind":"note"}}`,
			want: "is no RFC 3339 moment",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := readEvents(strings.NewReader(stream(runStart, test.line)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

func TestReadEventsRefusesEveryShapeASuiteProbeIdentityCanBeWrong(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{
			name: "a suite probe with no package-suite prefix",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":{"target":"TestOne","package":"example.com/app","suite":true,"exit_code":0,"outcome":"measured"}}`,
			want: "want the package-suite identity",
		},
		{
			name: "a suite probe that names no package after the prefix",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":{"target":"package-suite:","package":"example.com/app","suite":true,"exit_code":0,"outcome":"measured"}}`,
			want: "want the package-suite identity",
		},
		{
			name: "a suite probe with no package at all",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":{"target":"package-suite:example.com/app","package":"","suite":true,"exit_code":0,"outcome":"measured"}}`,
			want: "want the package-suite identity",
		},
		{
			name: "a suite probe naming another package",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":{"target":"package-suite:example.com/lib","package":"example.com/app","suite":true,"exit_code":0,"outcome":"measured"}}`,
			want: "want the package-suite identity",
		},
		{
			name: "a probe stopped by an error that names none",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":{"target":"TestOne","package":"example.com/app","exit_code":0,"error":""}}`,
			want: "probe.error is empty",
		},
		{
			name: "infections recorded by an execution that measured nothing",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":{"target":"TestOne","package":"example.com/app","exit_code":0,"outcome":"unavailable","infected":["m-1"]}}`,
			want: "only a measured execution observed a mutant",
		},
		{
			name: "an infection that names no mutant",
			line: `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":{"target":"TestOne","package":"example.com/app","exit_code":0,"outcome":"measured","infected":[""]}}`,
			want: "probe.infected is empty",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := readEvents(strings.NewReader(stream(runStart, test.line)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

func TestReadEventsAcceptsAProbeThatRecordedNoInfectionField(t *testing.T) {
	t.Parallel()
	line := `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":` +
		`{"target":"TestOne","package":"example.com/app","exit_code":0,"outcome":"unavailable"}}`
	events, err := readEvents(strings.NewReader(stream(runStart, line)))
	if err != nil || len(events) != 2 {
		t.Fatalf("read (%d events, %v), want the run-start and the probe", len(events), err)
	}
}

func routeLine(route string) string {
	return `{"seq":2,"type":"route","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"route":` + route + `}`
}

func TestReadEventsNamesTheExactFieldEveryPayloadGuardRefused(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{
			name: "a sequence below the one a recording opens with",
			line: `{"seq":0,"type":"progress","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"progress":{"kind":"note"}}`,
			want: "seq 0 is below 1",
		},
		{
			name: "an event that carries no payload of its own",
			line: `{"seq":2,"type":"phase-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1}`,
			want: "a phase-end event carries no phase payload",
		},
		{
			name: "a phase event with no name at all",
			line: `{"seq":2,"type":"phase-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":{"duration_ms":0}}`,
			want: `missing required field "phase.name"`,
		},
		{
			name: "a phase event whose name is empty",
			line: `{"seq":2,"type":"phase-end","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"phase":{"name":"","duration_ms":0}}`,
			want: "phase.name is empty",
		},
		{
			name: "a mutant event with no id at all",
			line: `{"seq":2,"type":"mutant-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"mutant":{"outcome":"killed"}}`,
			want: `missing required field "mutant.id"`,
		},
		{
			name: "a mutant event whose id is empty",
			line: `{"seq":2,"type":"mutant-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"mutant":{"id":"","outcome":"killed"}}`,
			want: "mutant.id is empty",
		},
		{
			name: "a route with a negative count of file candidates",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"file","file_candidates":-1}`),
			want: "route.file_candidates is -1",
		},
		{
			name: "a positive probe route naming nothing",
			line: routeLine(`{"path":"a.go","reason":"probe-reaching","granularity":"block","probed":true,"probe_reaching":[]}`),
			want: "route probe_reaching is empty",
		},
		{
			name: "probe-reaching targets under another reason",
			line: routeLine(`{"path":"a.go","reason":"coverage-reaching","granularity":"block","probed":true,"reaching_targets":["TestOne"],"probe_reaching":["TestOne"]}`),
			want: "want reason",
		},
		{
			name: "probe-reaching targets on a route that probed nothing",
			line: routeLine(`{"path":"a.go","reason":"probe-reaching","granularity":"block","reaching_targets":["TestOne"],"probe_reaching":["TestOne"]}`),
			want: "probed=false",
		},
		{
			name: "a probe-reaching target that names nothing",
			line: routeLine(`{"path":"a.go","reason":"probe-reaching","granularity":"block","probed":true,"reaching_targets":["TestOne"],"probe_reaching":[""]}`),
			want: "is empty, repeated, or absent",
		},
		{
			name: "a probe-reaching target named twice",
			line: routeLine(`{"path":"a.go","reason":"probe-reaching","granularity":"block","probed":true,"reaching_targets":["TestOne"],"probe_reaching":["TestOne","TestOne"]}`),
			want: "is empty, repeated, or absent",
		},
		{
			name: "a probe-reaching target nothing reaches",
			line: routeLine(`{"path":"a.go","reason":"probe-reaching","granularity":"block","probed":true,"reaching_targets":["TestOne"],"probe_reaching":["TestTwo"]}`),
			want: "is empty, repeated, or absent",
		},
		{
			name: "a suite coverage identity with no prefix",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","suite_coverage":"example.com/app"}`),
			want: "want a package-suite-coverage identity",
		},
		{
			name: "a suite coverage identity naming no package",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","suite_coverage":"package-suite-coverage:"}`),
			want: "want a package-suite-coverage identity",
		},
		{
			name: "a suite coverage control on file granularity",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"file","suite_coverage":"package-suite-coverage:example.com/app"}`),
			want: "want a package-suite-coverage identity",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := readEvents(strings.NewReader(stream(runStart, test.line)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

func probeLine(probe string) string {
	return `{"seq":2,"type":"probe-exec","timestamp":"2026-01-01T00:00:01Z","elapsed_ms":1,"probe":` + probe + `}`
}

func TestReadEventsRefusesEveryShapeARouteOrAControlCanBeWrong(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{
			name: "a suite reach claimed without a coverage control",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","suite_reached":true}`),
			want: "without a suite_coverage control",
		},
		{
			name: "a suite reach denied beside a coverage control",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","suite_coverage":"package-suite-coverage:example.com/app","suite_reached":false}`),
			want: "without a suite_coverage control",
		},
		{
			name: "a suite probe identity with no prefix",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","probed":true,"suite_probe":"example.com/app"}`),
			want: "want a package-suite identity",
		},
		{
			name: "a suite probe identity naming no package",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","probed":true,"suite_probe":"package-suite:"}`),
			want: "want a package-suite identity",
		},
		{
			name: "two suite controls naming different packages",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","probed":true,"suite_coverage":"package-suite-coverage:example.com/app","suite_reached":true,"suite_probe":"package-suite:example.com/lib"}`),
			want: "name different packages",
		},
		{
			name: "a reuse the plan does not say",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","reused":true,"plan":["mutant"]}`),
			want: "want the plan",
		},
		{
			name: "a plan of reuse the route does not say",
			line: routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","plan":["reused"]}`),
			want: "without saying it was reused",
		},
		{
			name: "a preflight control naming a package it is not for",
			line: probeLine(`{"target":"mutation-control:example.com/lib","package":"example.com/app","control":true,"exit_code":0,"outcome":"measured"}`),
			want: "want ",
		},
		{
			name: "a preflight control for no package that names one",
			line: probeLine(`{"target":"mutation-control:example.com/app","package":"","control":true,"exit_code":0,"outcome":"measured"}`),
			want: "want ",
		},
		{
			name: "a preflight control carrying a routing suite",
			line: probeLine(`{"target":"mutation-control:all","package":"","control":true,"suite":false,"exit_code":0,"outcome":"measured"}`),
			want: "neither a routing suite nor a source of infection facts",
		},
		{
			name: "a preflight control carrying infection facts",
			line: probeLine(`{"target":"mutation-control:all","package":"","control":true,"infected":[],"exit_code":0,"outcome":"measured"}`),
			want: "neither a routing suite nor a source of infection facts",
		},
		{
			name: "a probe with a control identity that is no control",
			line: probeLine(`{"target":"mutation-control:example.com/app","package":"example.com/app","exit_code":0,"outcome":"measured"}`),
			want: "without control=true",
		},
		{
			name: "a probe that names no target",
			line: probeLine(`{"target":"","package":"example.com/app","exit_code":0,"outcome":"measured"}`),
			want: "probe.target is empty",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := readEvents(strings.NewReader(stream(runStart, test.line)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

func TestReadEventsAcceptsARouteThatSaysItsReuseInBothFields(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","reused":true,"plan":["reused"]}`),
		routeLine(`{"path":"a.go","reason":"unreached","granularity":"block","plan":["mutant"]}`),
	} {
		events, err := readEvents(strings.NewReader(stream(runStart, line)))
		if err != nil || len(events) != 2 {
			t.Fatalf("read (%d events, %v) for %s", len(events), err, line)
		}
	}
}
