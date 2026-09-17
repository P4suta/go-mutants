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

// ceilingCorpus are the modules whose work is counted.
//
// Small ones, deliberately. The point of a ceiling is that it moves when the
// engine changes what it does, and a fixture large enough to be interesting on
// its own would move it for reasons about the fixture instead. `simple` is a
// green run end to end; `killable` has survivors, which cost a whole-binary
// confirmation each under ADR 0010; `coverage` has an uncovered mutant, which
// costs nothing at all and is the one a regression would start executing; and
// `runaway` holds a mutant that does not leave its loop, which is the only
// fixture where the difference between measuring a timeout once and measuring
// it twice is a number.
var ceilingCorpus = []string{"simple", "killable", "coverage", "runaway"}

// TestTheWorkAWholeRunDoesIsCounted is the ratchet on how much a run does.
//
// Every subprocess go-mutants starts goes through one choke point and is
// recorded there with a kind, which ADR 0002 exists to guarantee. Counting them
// is therefore free, and this is the thing worth counting: **how many children
// a run starts is a property of the engine, and how long they take is a
// property of the machine.** A duration cannot be ratcheted -- it moves with
// load, with the disk, with whatever else the runner is doing -- and a count
// does not move at all unless the engine's behaviour does.
//
// So the golden holds counts and nothing else. No durations, no sizes, no
// wall clock. A change to it is a change to what a run does, and the diff is
// the review: an optimisation that skips work shows up as smaller numbers, and
// a regression that starts running something twice shows up as larger ones --
// on a laptop, on a loaded runner, and on a machine nobody has built yet.
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

// TestEveryCountedKindIsOneTheContractNames keeps the ceiling from recording a
// label the schema would refuse.
//
// A kind is the one thing internal/runner cannot work out for itself, so it is
// the one thing every call site has to supply. An unlabelled spec is accepted
// by the runner and refused by the schema -- a missing diagnostic must not
// become a failed run -- which means a call site that forgot could show up here
// as a blank column rather than as an error anywhere.
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

// execCounts runs one corpus module with its recording on disk and returns how
// many commands of each kind it started.
//
// The count alone. ExecTally also carries a duration, and taking it would make
// this a test about the machine.
func execCounts(t *testing.T, name string) map[string]int {
	t.Helper()

	return countExecs(t, recordRun(t, name))
}

// recordRun runs one corpus module with a recording and returns the stream.
//
// Split out of [execCounts] so that a second question can be asked of the same
// run. The counts are one reading of a recording and not the only one worth
// taking: the events carry the argument vector and the environment names too,
// and starting a fourth run to look at those would be paying for the same work
// twice.
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
	// A recording that lost events would under-count, and an under-count is
	// the one direction this ratchet must never move silently.
	if !summary.HasRunEnd {
		t.Fatalf("the recording of %s has no run-end, so it is not the whole run", name)
	}
	if summary.EventsDropped != 0 || summary.MissingSequences != 0 {
		t.Fatalf("the recording of %s is lossy: %d dropped, %d missing",
			name, summary.EventsDropped, summary.MissingSequences)
	}
	return filepath.Join(traceRoot, "run", trace.FileName)
}

// countExecs is how many commands of each kind a recording holds.
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

// TestEveryGoCommandNamesItsWorkspaceDecision refuses a `go` command that never
// decided what to do about a workspace.
//
// Every child this engine starts runs inside a snapshot, and a `go.work` above
// that snapshot -- or named by $GOWORK in the environment this process
// inherited -- is a file the snapshot does not contain. So every command has to
// say which it is: GOWORK=off for a run of one module, and GOWORK removed for a
// run of a workspace, which is the one case where the go command is meant to
// find the file the run is about.
//
// Both spellings are a decision and neither is a default. What this refuses is
// the third state, where the variable is simply whatever the parent had -- and
// the reason to refuse it here rather than to read the two helpers that set it
// is that helpers are added. A command written next year through a third path
// would inherit whatever was around it, and the failure would be a package list
// that quietly described a workspace nobody asked about.
//
// The recording is what makes this checkable at all: `env_names` carries the
// names a child could see, without the values, so a test can ask what was
// decided without the recording having to hold a secret.
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

// usersOwnCommand are the kinds whose argv the user wrote.
//
// internal/engine/workspace.go draws this line and gives the reason: a command
// go-mutants assembles is one it may decide the workspace question for, and a
// command that is the user's program is not. Their test command runs in the
// snapshot and may legitimately want whatever $GOWORK the run inherited, so
// pinning it here would be go-mutants answering a question it was not asked.
//
// Two entries and no more. A kind added here is a `go` command this gate stops
// looking at, so the list shrinking is ordinary and the list growing is a claim
// that something else is the user's program too.
var usersOwnCommand = map[string]bool{
	trace.ExecKindBaselineTest:         true,
	trace.ExecKindInstrumentedBaseline: true,
}

// resolvesNoWorkspace are the kinds that ask the go tool about itself.
//
// `go version` prints which toolchain this is and reads no module and no
// workspace, so there is nothing for GOWORK to change about its answer. It is
// also the command that locates the toolchain, which happens before a run knows
// whether it is measuring a workspace at all -- so requiring an answer here
// would be requiring one before the question exists.
var resolvesNoWorkspace = map[string]bool{
	trace.ExecKindGoVersion: true,
}

// isGoCommand reports whether an argv[0] is the go tool, by the name it ends
// with rather than by the path it was found at.
func isGoCommand(program string) bool {
	base := filepath.Base(program)
	return base == "go" || base == "go.exe"
}
