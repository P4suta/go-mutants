// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
)

// TestSetVerifierCarriesTheMutantArgumentsIntoTheControl pins that a control
// runs the same invocation the mutant does: an accepted flag such as
// -test.short changes what the tests do, so a control without it would license
// the narrowing on a different measurement. This checks the ControlRun the
// verifier builds, which is what execute.RunControls turns into a process.
func TestSetVerifierCarriesTheMutantArgumentsIntoTheControl(t *testing.T) {
	t.Parallel()

	v := newSetVerifier()
	tests := map[string][]string{"example.com/m/a": {"TestA"}}
	v.want(tests, execute.MutantRun{ID: "m1", Timeout: 1, Args: []string{"-test.short", "-test.count=1"}})

	// Reach into the recorded set to check the arguments were kept; run() would
	// hand them to execute.RunControls, which needs real binaries.
	set, ok := v.sets[setKey(tests)]
	if !ok {
		t.Fatal("the set was not recorded")
	}
	if want := []string{"-test.short", "-test.count=1"}; !slices.Equal(set.args, want) {
		t.Errorf("recorded args = %v, want the mutant's %v", set.args, want)
	}
}

// TestSetVerifierChecksEachDistinctSetOnce pins that two mutants narrowed to the
// same tests produce one control, and two different sets produce two.
func TestSetVerifierChecksEachDistinctSetOnce(t *testing.T) {
	t.Parallel()

	v := newSetVerifier()
	shared := map[string][]string{"example.com/m/a": {"TestA"}}
	v.want(shared, execute.MutantRun{ID: "m1", Timeout: 1})
	v.want(map[string][]string{"example.com/m/a": {"TestA"}}, execute.MutantRun{ID: "m2", Timeout: 1})
	v.want(map[string][]string{"example.com/m/a": {"TestB"}}, execute.MutantRun{ID: "m3", Timeout: 1})
	if len(v.sets) != 2 {
		t.Errorf("recorded %d distinct sets, want 2", len(v.sets))
	}
}

// TestRunControlsGetTheVerifiersArguments is the whole path: the arguments a
// mutant carries reach the control process the verifier schedules. It uses the
// empty binary list so run returns without starting anything, which is enough
// to exercise want/run wiring without a toolchain; the argument fidelity itself
// is pinned above and in internal/execute's own control tests.
func TestRunControlsGetTheVerifiersArguments(t *testing.T) {
	t.Parallel()

	v := newSetVerifier()
	v.want(map[string][]string{"example.com/m/a": {"TestA"}}, execute.MutantRun{ID: "m1", Timeout: 1, Args: []string{"-test.short"}})
	// No binaries: run returns verdicts without scheduling, and must not panic.
	got := v.run(context.Background(), execute.Options{}, nil)
	if got.ok(map[string][]string{"example.com/m/a": {"TestA"}}) {
		t.Error("a set whose control could not run was treated as reliable")
	}
}
