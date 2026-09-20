// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const traceReadEventCount = 3

func TestAllZeroHoldsOnlyWhenEveryValueIsTheZeroOfItsType(t *testing.T) {
	t.Parallel()
	if !allZero(map[string]int(nil)) {
		t.Error("a map with nothing in it was said to hold a value")
	}
	if !allZero(map[string]int{"a": 0, "b": 0}) {
		t.Error("a map of zeroes was said to hold a value")
	}
	if allZero(map[string]int{"a": 0, "b": 1}) {
		t.Error("a map holding one value was said to hold none")
	}
	if allZero(map[string]int{"a": -1}) {
		t.Error("a map holding a negative value was said to hold none")
	}
	if !allZero(map[string]int64{"a": 0}) {
		t.Error("a map of zero durations was said to hold one")
	}
	if allZero(map[string]int64{"a": 1}) {
		t.Error("a map holding one duration was said to hold none")
	}
}

func TestATraceDiffIsUnchangedOnlyWhenNothingAtAllMoved(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*trace.SummaryDiff)
		want   string
	}{
		{name: "nothing moved", want: "unchanged"},
		{name: "one more event", change: func(d *trace.SummaryDiff) { d.EventsDelta = 1 }, want: "changed"},
		{
			name:   "one more missing sequence",
			change: func(d *trace.SummaryDiff) { d.MissingSequencesDelta = 1 }, want: "changed",
		},
		{
			name:   "one more dropped event",
			change: func(d *trace.SummaryDiff) { d.EventsDroppedDelta = 1 }, want: "changed",
		},
		{
			name:   "another verdict",
			change: func(d *trace.SummaryDiff) { d.AfterVerdict = "DEFECT" }, want: "changed",
		},
		{
			name:   "a run that ended this time",
			change: func(d *trace.SummaryDiff) { d.AfterRunEnd = true }, want: "changed",
		},
		{
			name:   "one more event of a kind",
			change: func(d *trace.SummaryDiff) { d.CountDelta = map[string]int{"exec": 1} }, want: "changed",
		},
		{
			name:   "a phase that took longer",
			change: func(d *trace.SummaryDiff) { d.PhaseDurationDeltaMS = map[string]int64{"baseline": 1} },
			want:   "changed",
		},
		{
			name:   "a preparation that took longer",
			change: func(d *trace.SummaryDiff) { d.PrepareDurationDeltaMS = map[string]int64{"discovery": 1} },
			want:   "changed",
		},
		{
			name: "counts that are all zero",
			change: func(d *trace.SummaryDiff) {
				d.CountDelta = map[string]int{"exec": 0}
				d.PhaseDurationDeltaMS = map[string]int64{"baseline": 0}
				d.PrepareDurationDeltaMS = map[string]int64{"discovery": 0}
			},
			want: "unchanged",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			difference := trace.SummaryDiff{BeforeVerdict: "ASSURED", AfterVerdict: "ASSURED"}
			if test.change != nil {
				test.change(&difference)
			}
			if got := traceDiffStatus(difference); got != test.want {
				t.Fatalf("a diff of %+v is %q, want %q", difference, got, test.want)
			}
		})
	}
}

func TestReadingANamedTraceRefusesEveryRunItCannotTrust(t *testing.T) {
	root := t.TempDir()
	traceRoot := filepath.Join(root, ".goatest", "trace")
	if err := os.MkdirAll(traceRoot, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}

	if _, _, err := readNamedTrace(traceRoot, "not-a-run"); err == nil {
		t.Fatal("a name no run could carry was read")
	}
	link := filepath.Join(traceRoot, "20260901T120000Z-1234")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	_, _, err := readNamedTrace(traceRoot, "20260901T120000Z-1234")
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("reading a run behind a symbolic link reported %v, want it refused", err)
	}
}

func TestReadingTheLatestTraceIgnoresEveryEntryThatIsNotARun(t *testing.T) {
	root := t.TempDir()
	traceRoot := filepath.Join(root, ".goatest", "trace")
	if err := os.MkdirAll(filepath.Join(traceRoot, "not-a-run"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(traceRoot, "20260901T120000Z-1234"),
		[]byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	summary, name, err := readNamedTrace(traceRoot, "")
	if err != nil {
		t.Fatalf("a trace root holding nothing readable reported %v, want no run at all", err)
	}
	if name != "" || summary.Events != 0 {
		t.Fatalf("a trace root holding nothing readable named %q with %d events", name, summary.Events)
	}
}

func writeTraceRun(t *testing.T, traceRoot, name string, events int) {
	t.Helper()
	directory := filepath.Join(traceRoot, name)
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	var stream strings.Builder
	for index := range events {
		stream.WriteString(`{"seq":` + strconv.Itoa(index+1) +
			`,"type":"` + trace.TypeProgress +
			`","timestamp":"2026-01-01T00:00:00Z","elapsed_ms":0,"progress":{"kind":"note"}}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(directory, trace.FileName),
		[]byte(stream.String()), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
}

func TestReadingTheLatestTraceTakesTheNewestRunItCanTrust(t *testing.T) {
	traceRoot := filepath.Join(t.TempDir(), "trace")
	writeTraceRun(t, traceRoot, "20260901T120000Z-1", 1)
	writeTraceRun(t, traceRoot, "20260902T120000Z-2", traceReadEventCount)
	if err := os.MkdirAll(filepath.Join(traceRoot, "20260903T120000Z-0"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}

	summary, name, err := readNamedTrace(traceRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "20260902T120000Z-2" {
		t.Fatalf("the latest trace is %q, want the newest run whose name is one", name)
	}
	if summary.Events != traceReadEventCount {
		t.Errorf("the latest trace holds %d events, want %d", summary.Events, traceReadEventCount)
	}
}

func TestATraceDiffNeedsExactlyTwoRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	traceRoot := filepath.Join(root, ".goatest", "trace")
	writeTraceRun(t, traceRoot, "20260901T120000Z-1", 1)
	writeTraceRun(t, traceRoot, "20260902T120000Z-2", traceReadEventCount)
	service := Service{Root: root}
	for _, test := range []struct {
		name string
		runs []string
		want string
	}{
		{name: "no run at all", want: "requires two runs"},
		{name: "one run", runs: []string{"20260901T120000Z-1"}, want: "requires two runs"},
		{
			name: "three runs",
			runs: []string{"20260901T120000Z-1", "20260902T120000Z-2", "20260901T120000Z-1"},
			want: "requires two runs",
		},
		{name: "two runs", runs: []string{"20260901T120000Z-1", "20260902T120000Z-2"}},
		{
			name: "a first run nothing can read",
			runs: []string{"not-a-run", "20260902T120000Z-2"}, want: "invalid trace run",
		},
		{
			name: "a second run nothing can read",
			runs: []string{"20260901T120000Z-1", "not-a-run"}, want: "invalid trace run",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := service.readTrace(root, "diff", test.runs)
			if test.want == "" {
				if err != nil {
					t.Fatalf("%s reported %v, want a diff", test.name, err)
				}
				if len(result.Evidence) == 0 {
					t.Fatal("a diff of two runs stated no evidence")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s reported %v, want %q", test.name, err, test.want)
			}
		})
	}
}
