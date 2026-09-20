// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const readerSequence int64 = 1

func summaryOfLines(t *testing.T, lines ...string) (Summary, error) {
	t.Helper()
	directory := t.TempDir()
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(directory, FileName), []byte(data), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	return ReadSummary(directory)
}

func TestReadSummaryNamesWhatEachEnvelopeGuardRefused(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{
			name: "a sequence at zero",
			line: `{"seq":0,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}`,
			want: "has an invalid envelope",
		},
		{
			name: "a sequence below zero",
			line: `{"seq":-1,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}`,
			want: "has an invalid envelope",
		},
		{
			name: "no type at all",
			line: `{"seq":1,"type":"","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}`,
			want: "has an invalid envelope",
		},
		{
			name: "no timestamp at all",
			line: `{"seq":1,"type":"progress","timestamp":"","elapsed_ms":0,"progress":{"kind":"note"}}`,
			want: "has an invalid envelope",
		},
		{
			name: "a negative elapsed time",
			line: `{"seq":1,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":-1,"progress":{"kind":"note"}}`,
			want: "has an invalid envelope",
		},
		{
			name: "an unknown type",
			line: `{"seq":1,"type":"invented","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: `has unknown type "invented"`,
		},
		{
			name: "a run-start of another schema",
			line: `{"seq":1,"type":"run-start","schema":"goatest-trace-v0","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "has unsupported schema",
		},
		{
			name: "a run-start carrying a payload",
			line: `{"seq":1,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}`,
			want: "has 1 payloads",
		},
		{
			name: "an event carrying two payloads",
			line: `{"seq":1,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"},"artifact":{"kind":"k","path":"p"}}`,
			want: "has 2 payloads",
		},
		{
			name: "a phase event with no phase payload",
			line: `{"seq":1,"type":"phase-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing phase payload",
		},
		{
			name: "an exec event with no exec payload",
			line: `{"seq":1,"type":"exec","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing exec payload",
		},
		{
			name: "a mutant event with no mutant payload",
			line: `{"seq":1,"type":"mutant-exec","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing mutant payload",
		},
		{
			name: "a route event with no route payload",
			line: `{"seq":1,"type":"route","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing route payload",
		},
		{
			name: "a progress event with no progress payload",
			line: `{"seq":1,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing progress payload",
		},
		{
			name: "an artifact event with no artifact payload",
			line: `{"seq":1,"type":"artifact","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing artifact payload",
		},
		{
			name: "a probe event with no probe payload",
			line: `{"seq":1,"type":"probe-exec","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing probe payload",
		},
		{
			name: "a prepare event carrying a second payload",
			line: `{"seq":1,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"started"},"progress":{"kind":"note"}}`,
			want: "has 2 payloads",
		},
		{
			name: "a run-end with no run payload",
			line: `{"seq":1,"type":"run-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
			want: "is missing valid run payload",
		},
		{
			name: "a run-end that emitted a negative number of events",
			line: `{"seq":1,"type":"run-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"run":{"events_emitted":-1,"events_dropped":0}}`,
			want: "is missing valid run payload",
		},
		{
			name: "a run-end that dropped a negative number of events",
			line: `{"seq":1,"type":"run-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"run":{"events_emitted":0,"events_dropped":-1}}`,
			want: "is missing valid run payload",
		},
		{
			name: "a prepare event of an unknown phase",
			line: `{"seq":1,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"invented","state":"started"}}`,
			want: "is missing valid prepare payload",
		},
		{
			name: "a started preparation carrying a result",
			line: `{"seq":1,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"started","result":"succeeded"}}`,
			want: "has an invalid started prepare payload",
		},
		{
			name: "a finished preparation with no duration",
			line: `{"seq":1,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"finished","result":"succeeded"}}`,
			want: "has an invalid finished prepare duration",
		},
		{
			name: "a finished preparation of an unknown result",
			line: `{"seq":1,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"finished","result":"invented","duration_ms":1}}`,
			want: "has an invalid finished prepare result",
		},
		{
			name: "a preparation in an unknown state",
			line: `{"seq":1,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"waiting"}}`,
			want: "has an invalid prepare state",
		},
		{
			name: "an event with more than one value on its line",
			line: `{"seq":1,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}} {}`,
			want: "has trailing data",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			summary, err := summaryOfLines(t, test.line)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadSummary = (%+v, %v), want %q", summary, err, test.want)
			}
		})
	}
}

func TestReadSummaryCountsTheSequencesNobodyWrote(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		first   int64
		second  int64
		missing int64
	}{
		{name: "a recording that opens where it should", first: 1, second: 2},
		{name: "a recording that opens late", first: 3, second: 4, missing: 2},
		{name: "a gap in the middle", first: 1, second: 5, missing: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			summary, err := summaryOfLines(t,
				progressLine(test.first), progressLine(test.second))
			if err != nil || summary.MissingSequences != test.missing {
				t.Fatalf("ReadSummary = (%+v, %v), want %d missing", summary, err, test.missing)
			}
			if summary.FirstSequence != test.first || summary.LastSequence != test.second || summary.Events != 2 {
				t.Fatalf("ReadSummary = %+v", summary)
			}
		})
	}
}

func TestReadSummaryRefusesASequenceThatDoesNotFollow(t *testing.T) {
	t.Parallel()
	for _, second := range []int64{readerSequence + 1, readerSequence} {
		summary, err := summaryOfLines(t, progressLine(readerSequence+1), progressLine(second))
		if err == nil || !strings.Contains(err.Error(), "does not follow") {
			t.Fatalf("ReadSummary = (%+v, %v)", summary, err)
		}
	}
}

func TestReadSummarySkipsABlankLineWithoutCountingIt(t *testing.T) {
	t.Parallel()
	summary, err := summaryOfLines(t, progressLine(1), "   ", progressLine(2))
	if err != nil || summary.Events != 2 {
		t.Fatalf("ReadSummary = (%+v, %v), want the blank line passed over", summary, err)
	}
}

func TestReadSummaryRefusesASecondRunEnd(t *testing.T) {
	t.Parallel()
	end := `{"seq":%d,"type":"run-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"run":{"events_emitted":1,"events_dropped":0}}`
	summary, err := summaryOfLines(t, sequenced(end, 1), sequenced(end, 2))
	if err == nil || !strings.Contains(err.Error(), "more than one run-end event") {
		t.Fatalf("ReadSummary = (%+v, %v)", summary, err)
	}
}

func progressLine(sequence int64) string {
	return sequenced(`{"seq":%d,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}`, sequence)
}

func sequenced(format string, sequence int64) string {
	return strings.Replace(format, "%d", strconv.FormatInt(sequence, decimal), 1)
}

const decimal = 10

func TestReadSummaryAcceptsOneEventOfEveryKindItKnows(t *testing.T) {
	t.Parallel()
	summary, err := summaryOfLines(t,
		`{"seq":1,"type":"run-start","schema":"goatest-trace-v1","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0}`,
		`{"seq":2,"type":"phase-start","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"phase":{"name":"baseline","duration_ms":100}}`,
		`{"seq":3,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"started"}}`,
		`{"seq":4,"type":"exec","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"exec":{"argv":["go","test"],"exit_code":0}}`,
		`{"seq":5,"type":"mutant-exec","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"mutant":{"id":"m-1"}}`,
		`{"seq":6,"type":"route","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"route":{"path":"a.go","reason":"unreached","granularity":"file"}}`,
		`{"seq":7,"type":"probe-exec","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"probe":{"target":"TestOne","exit_code":0}}`,
		`{"seq":8,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}`,
		`{"seq":9,"type":"artifact","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"artifact":{"kind":"report","path":"p"}}`,
		`{"seq":10,"type":"phase-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"phase":{"name":"baseline","duration_ms":7}}`,
		`{"seq":11,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"finished","result":"succeeded","duration_ms":3}}`,
		`{"seq":12,"type":"run-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"run":{"verdict":"assured","events_emitted":12,"events_dropped":0}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Events != knownEventKinds || summary.MissingSequences != 0 || !summary.HasRunEnd || summary.Verdict != "assured" {
		t.Fatalf("ReadSummary = %+v", summary)
	}
	if summary.PhaseDurationMS["baseline"] != phaseDurationFixture {
		t.Fatalf("phase durations = %v, want the phase-end counted", summary.PhaseDurationMS)
	}
	if summary.PrepareDurationMS["discovery"] != prepareDurationFixture {
		t.Fatalf("prepare durations = %v, want only the finished preparation counted", summary.PrepareDurationMS)
	}
}

const (
	knownEventKinds              = 12
	phaseDurationFixture   int64 = 7
	prepareDurationFixture int64 = 3
)

func TestReadSummaryAcceptsARunEndThatEmittedAndDroppedNothing(t *testing.T) {
	t.Parallel()
	summary, err := summaryOfLines(t,
		`{"seq":1,"type":"run-end","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"run":{"events_emitted":0,"events_dropped":0}}`)
	if err != nil || !summary.HasRunEnd {
		t.Fatalf("ReadSummary = (%+v, %v), want a run-end that counted nothing accepted", summary, err)
	}
}

func TestReadSummaryAcceptsAPreparationThatTookNoTimeAtAll(t *testing.T) {
	t.Parallel()
	summary, err := summaryOfLines(t,
		`{"seq":1,"type":"prepare","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"prepare":{"phase":"discovery","state":"finished","result":"succeeded","duration_ms":0}}`)
	if err != nil || summary.PrepareDurationMS["discovery"] != 0 {
		t.Fatalf("ReadSummary = (%+v, %v), want a preparation of no duration accepted", summary, err)
	}
}

func TestReadSummaryRefusesEveryGranularityButTheTwoItKnows(t *testing.T) {
	t.Parallel()
	for _, granularity := range []string{"block", "file"} {
		summary, err := summaryOfLines(t,
			`{"seq":1,"type":"route","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"route":{"path":"a.go","reason":"unreached","granularity":"`+granularity+`"}}`)
		if err != nil || summary.Events != 1 {
			t.Fatalf("ReadSummary of granularity %q = (%+v, %v)", granularity, summary, err)
		}
	}
	summary, err := summaryOfLines(t,
		`{"seq":1,"type":"route","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"route":{"path":"a.go","reason":"unreached","granularity":"line"}}`)
	if err == nil || !strings.Contains(err.Error(), "invalid route granularity") {
		t.Fatalf("ReadSummary = (%+v, %v)", summary, err)
	}
}

func TestReadSummaryNamesTheEventItCouldNotDecode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		lines []string
		want  string
	}{
		{name: "the first", lines: []string{"{"}, want: "trace event 1:"},
		{name: "a later one", lines: []string{progressLine(1), progressLine(2), "{"}, want: "trace event 3:"},
		{
			name:  "one with more than one value on its line",
			lines: []string{progressLine(1), progressLine(2) + " {}"},
			want:  "trace event 2 has trailing data",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			summary, err := summaryOfLines(t, test.lines...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadSummary = (%+v, %v), want %q", summary, err, test.want)
			}
		})
	}
}

func TestReadSummaryReadsTheTraceBesideADirectoryAndTheFileItIsGiven(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, FileName)
	if err := os.WriteFile(path, []byte(progressLine(1)+"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	fromDirectory, err := ReadSummary(directory)
	if err != nil || fromDirectory.Path != path || fromDirectory.Events != 1 {
		t.Fatalf("ReadSummary of a directory = (%+v, %v)", fromDirectory, err)
	}
	fromFile, err := ReadSummary(path)
	if err != nil || fromFile.Path != path || fromFile.Events != 1 {
		t.Fatalf("ReadSummary of a file = (%+v, %v)", fromFile, err)
	}
}

func TestReadSummaryReportsATraceItCannotOpen(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	occupied := filepath.Join(directory, "occupied")
	if err := os.WriteFile(occupied, []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	summary, err := ReadSummary(filepath.Join(occupied, FileName))
	if err == nil || !strings.Contains(err.Error(), "open trace") {
		t.Fatalf("ReadSummary = (%+v, %v)", summary, err)
	}
}

func TestDiffSubtractsTheEarlierRecordingFromTheLater(t *testing.T) {
	t.Parallel()
	before := Summary{
		Events: 2, MissingSequences: 1, EventsDropped: 1, HasRunEnd: true, Verdict: "insufficient",
		Counts:            map[string]int{"progress": 2, "route": 1},
		PhaseDurationMS:   map[string]int64{"baseline": 5, "race": 2},
		PrepareDurationMS: map[string]int64{"discovery": 4},
	}
	after := Summary{
		Events: 5, MissingSequences: 0, EventsDropped: 4, Verdict: "assured",
		Counts:            map[string]int{"progress": 5, "probe-exec": 1},
		PhaseDurationMS:   map[string]int64{"baseline": 9},
		PrepareDurationMS: map[string]int64{"discovery": 1},
	}
	diff := Diff(before, after)
	if diff.EventsDelta != 3 || diff.MissingSequencesDelta != -1 || diff.EventsDroppedDelta != 3 {
		t.Fatalf("diff counts = %+v", diff)
	}
	if diff.BeforeVerdict != "insufficient" || diff.AfterVerdict != "assured" || !diff.BeforeRunEnd || diff.AfterRunEnd {
		t.Fatalf("diff verdicts = %+v", diff)
	}
	for kind, want := range map[string]int{"progress": 3, "route": -1, "probe-exec": 1} {
		if diff.CountDelta[kind] != want {
			t.Fatalf("count delta for %q = %d, want %d (%v)", kind, diff.CountDelta[kind], want, diff.CountDelta)
		}
	}
	for phase, want := range map[string]int64{"baseline": 4, "race": -2} {
		if diff.PhaseDurationDeltaMS[phase] != want {
			t.Fatalf("phase delta for %q = %d, want %d (%v)", phase, diff.PhaseDurationDeltaMS[phase], want, diff.PhaseDurationDeltaMS)
		}
	}
	if diff.PrepareDurationDeltaMS["discovery"] != -3 {
		t.Fatalf("prepare delta = %v", diff.PrepareDurationDeltaMS)
	}
}

func TestReadSummaryReportsALineItCannotHold(t *testing.T) {
	t.Parallel()
	long := `{"seq":1,"type":"progress","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"` +
		strings.Repeat("x", traceMaximumLineBytes) + `"}}`
	summary, err := summaryOfLines(t, long)
	if err == nil || !strings.Contains(err.Error(), "read trace") {
		t.Fatalf("ReadSummary of a line it cannot hold = (%+v, %v)", summary, err)
	}
}
