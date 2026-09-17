// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

var ceilingCorpus = []string{"simple", "killable", "coverage", "runaway"}

func TestTheWorkAWholeRunDoesIsCounted(t *testing.T) {
	t.Parallel()

	var rendered strings.Builder
	for _, name := range ceilingCorpus {
		counts := execCounts(t, name)
		fmt.Fprintf(&rendered, "%s\n", name)
		kinds := make([]string, 0, len(counts))
		for kind := range counts {
			kinds = append(kinds, kind)
		}
		slices.Sort(kinds)
		for _, kind := range kinds {
			fmt.Fprintf(&rendered, "\t%s %d\n", kind, counts[kind])
		}
	}
	testkit.Golden(t, "work-ceiling.golden.txt", []byte(rendered.String()))
}

func TestEveryCountedKindIsOneTheContractNames(t *testing.T) {
	t.Parallel()

	known := trace.ExecKinds()
	for _, name := range ceilingCorpus {
		for kind := range execCounts(t, name) {
			if !slices.Contains(known, kind) {
				t.Errorf("%s started a command of kind %q, which trace.ExecKinds does not name: %v",
					name, kind, known)
			}
		}
	}
}

func execCounts(t *testing.T, name string) map[string]int {
	t.Helper()

	return countExecs(t, recordRun(t, name))
}

func recordRun(t *testing.T, name string) string {
	t.Helper()

	root := testkit.Copy(t, name)
	opts := optionsAt(t, root)

	traceRoot := filepath.Join(testkit.Scratch(t), "trace", name)
	sink, err := trace.NewDirSink(traceRoot, "run", trace.Filesystem{})
	if err != nil {
		t.Fatalf("opening a recording for %s: %v", name, err)
	}
	opts.TraceSink = sink

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("running %s: %v", name, err)
	}
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("closing the recording for %s: %v", name, closeErr)
	}
	if outcome.Report == nil {
		t.Fatalf("%s published no report", name)
	}

	summary, err := trace.ReadSummary(filepath.Join(traceRoot, "run", trace.FileName))
	if err != nil {
		t.Fatalf("reading the recording of %s: %v", name, err)
	}
	if summary.Missing {
		t.Fatalf("the recording of %s is not there", name)
	}
	if !summary.HasRunEnd {
		t.Fatalf("the recording of %s has no run-end, so it is not the whole run", name)
	}
	if summary.EventsDropped != 0 || summary.MissingSequences != 0 {
		t.Fatalf("the recording of %s is lossy: %d dropped, %d missing",
			name, summary.EventsDropped, summary.MissingSequences)
	}
	return filepath.Join(traceRoot, "run", trace.FileName)
}

func countExecs(t *testing.T, stream string) map[string]int {
	t.Helper()

	summary, err := trace.ReadSummary(stream)
	if err != nil {
		t.Fatalf("reading %s: %v", stream, err)
	}

	counts := make(map[string]int, len(summary.ExecByKind))
	for kind, tally := range summary.ExecByKind {
		counts[kind] = tally.Count
	}
	if len(counts) == 0 {
		t.Fatalf("%s recorded no commands at all, so this counts nothing", stream)
	}
	return counts
}

func TestEveryGoCommandNamesItsWorkspaceDecision(t *testing.T) {
	t.Parallel()

	events, err := trace.Read(recordRun(t, "simple"))
	if err != nil {
		t.Fatalf("reading the recording: %v", err)
	}

	commands := 0
	for _, event := range events {
		if event.Exec == nil || len(event.Exec.Argv) == 0 {
			continue
		}
		if !isGoCommand(event.Exec.Argv[0]) {
			continue
		}
		if usersOwnCommand[event.Exec.Kind] || resolvesNoWorkspace[event.Exec.Kind] {
			continue
		}
		commands++
		if !slices.Contains(event.Exec.EnvNames, "GOWORK") {
			t.Errorf("seq %d ran %v with no GOWORK among %v;\n"+
				"\ta go command that did not decide about a workspace is one that took the "+
				"decision this process was started with", event.Seq, event.Exec.Argv, event.Exec.EnvNames)
		}
	}
	if commands == 0 {
		t.Fatal("the recording holds no go commands, so this test compared nothing")
	}
}

var usersOwnCommand = map[string]bool{
	trace.ExecKindBaselineTest:         true,
	trace.ExecKindInstrumentedBaseline: true,
}

var resolvesNoWorkspace = map[string]bool{
	trace.ExecKindGoVersion: true,
}

func isGoCommand(program string) bool {
	base := filepath.Base(program)
	return base == "go" || base == "go.exe"
}
