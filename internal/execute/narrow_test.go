// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// testRunOf is the `-test.run` value a call was given, or "" when it was given
// none.
func testRunOf(c call) string {
	for _, a := range c.Argv {
		if rest, ok := strings.CutPrefix(a, "-test.run="); ok {
			return rest
		}
	}
	return ""
}

// TestRunOneSelectsOnlyTheNamedTestsOfEachBinary is the whole of test-level
// narrowing as this package sees it: a binary the run names tests for is
// started with exactly those tests selected, by an anchored alternation of
// their escaped names, and a binary it names none for runs whole. The
// selection is placed with the harness-owned flags, ahead of the caller's
// arguments, and what was selected is reported back on the attempt.
func TestRunOneSelectsOnlyTheNamedTestsOfEachBinary(t *testing.T) {
	t.Parallel()

	// The second selected binary kills, so this is a kill rather than a
	// survivor and no whole-binary confirmation follows (see RunOne): the
	// argv this pins is the narrowed pass's own.
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/c.test" {
			return failed("--- FAIL: TestSomething\n")
		}
		return passed()
	}}
	opts := options(f, 1)
	bins := testBins("example.com/a", "example.com/b", "example.com/c")
	selected := map[string][]string{"example.com/a": {"TestOne", "Test.Two"}}

	attempt := execute.RunOne(t.Context(), opts, execute.MutantRun{
		ID:       "abc123",
		Timeout:  mutantTimeout,
		Binaries: []int{0, 2},
		Tests:    selected,
		Args:     []string{"-test.count=1"},
	}, bins)
	if attempt.Outcome != mutation.OutcomeKilled {
		t.Fatalf("outcome = %s, want %s (%v)", attempt.Outcome, mutation.OutcomeKilled, attempt.Err)
	}
	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("started %d processes, want the two selected binaries: %v", len(seen), f.programs())
	}
	wantA := []string{"example.com/a.test", "-test.timeout=14s", `-test.run=^(TestOne|Test\.Two)$`, "-test.count=1"}
	if !slices.Equal(seen[0].Argv, wantA) {
		t.Errorf("argv for a = %q, want %q", seen[0].Argv, wantA)
	}
	wantC := []string{"example.com/c.test", "-test.timeout=14s", "-test.count=1"}
	if !slices.Equal(seen[1].Argv, wantC) {
		t.Errorf("argv for c = %q, want %q: no tests were named for it, so it runs whole", seen[1].Argv, wantC)
	}
	if !maps.EqualFunc(attempt.Tests, selected, slices.Equal) {
		t.Errorf("attempt.Tests = %v, want %v", attempt.Tests, selected)
	}
	if want := []string{"example.com/a", "example.com/c"}; !slices.Equal(attempt.Binaries, want) {
		t.Errorf("attempt.Binaries = %v, want %v", attempt.Binaries, want)
	}
}

// TestRunOneRefusesTestSelectionsItCannotHonour pins the two ways a selection
// can be wrong, and that both are refused before anything starts: a selection
// for a binary the run does not start would describe a measurement never made,
// and an empty selection would start a binary that runs nothing and passes —
// the same flattering green an empty binary set is refused for.
func TestRunOneRefusesTestSelectionsItCannotHonour(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tests map[string][]string
		want  string
	}{
		{
			name:  "a binary the run does not start",
			tests: map[string][]string{"example.com/b": {"TestOne"}},
			want:  "example.com/b",
		},
		{
			name:  "no tests at all",
			tests: map[string][]string{"example.com/a": {}},
			want:  "example.com/a",
		},
		{
			// An empty name would anchor to nothing, and the binary would
			// pass having run nothing.
			name:  "an empty name",
			tests: map[string][]string{"example.com/a": {"TestOne", ""}},
			want:  `""`,
		},
		{
			// A subtest is selected through its parent; inside the anchored
			// alternation the binary's own splitting of -test.run at the
			// slash would never find it.
			name:  "a subtest",
			tests: map[string][]string{"example.com/a": {"TestOne/case_3"}},
			want:  `"TestOne/case_3"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
			attempt := execute.RunOne(t.Context(), options(f, 1), execute.MutantRun{
				ID:       "abc123",
				Timeout:  mutantTimeout,
				Binaries: []int{0},
				Tests:    test.tests,
			}, testBins("example.com/a", "example.com/b"))
			if attempt.Outcome != mutation.OutcomeErrored {
				t.Fatalf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeErrored)
			}
			if code := execute.CodeOf(attempt.Err); code != execute.CodeMutantInvalid {
				t.Errorf("code = %q, want %q (%v)", code, execute.CodeMutantInvalid, attempt.Err)
			}
			if !strings.Contains(attempt.Err.Error(), test.want) {
				t.Errorf("the refusal does not name the binary: %v", attempt.Err)
			}
			if len(f.seen()) != 0 {
				t.Errorf("started %v before refusing", f.programs())
			}
		})
	}
}

// TestRunOneRefusesACallerSuppliedTestRunWhileNarrowing: two selections on one
// command line would compose as the test binary's flag package composes them —
// the last one wins — and whichever won, the measurement would not be the one
// the selection described. Without a selection the flag stays the caller's to
// pass, as it always has been.
func TestRunOneRefusesACallerSuppliedTestRunWhileNarrowing(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	attempt := execute.RunOne(t.Context(), options(f, 1), execute.MutantRun{
		ID:      "abc123",
		Timeout: mutantTimeout,
		Tests:   map[string][]string{"example.com/a": {"TestOne"}},
		Args:    []string{"-test.run", "TestOther"},
	}, testBins("example.com/a"))
	if attempt.Outcome != mutation.OutcomeErrored {
		t.Fatalf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeErrored)
	}
	if code := execute.CodeOf(attempt.Err); code != execute.CodeMutantInvalid {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeMutantInvalid, attempt.Err)
	}
	if len(f.seen()) != 0 {
		t.Errorf("started %v before refusing", f.programs())
	}
}

// TestRunControlSelectsTheNamedTests: a control of a narrowed measurement runs
// the same tests the measurement ran, or it is a control of something else.
func TestRunControlSelectsTheNamedTests(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	attempt := execute.RunControl(t.Context(), options(f, 1), execute.ControlRun{
		Timeout: mutantTimeout,
		Tests:   map[string][]string{"example.com/a": {"TestOne", "TestTwo"}},
	}, testBins("example.com/a", "example.com/b"))
	if attempt.Err != nil || attempt.ExitCode != 0 {
		t.Fatalf("control: exit %d, %v", attempt.ExitCode, attempt.Err)
	}
	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("started %d processes, want both binaries: %v", len(seen), f.programs())
	}
	if got, want := testRunOf(seen[0]), "^(TestOne|TestTwo)$"; got != want {
		t.Errorf("a was given -test.run=%q, want %q", got, want)
	}
	if got := testRunOf(seen[1]); got != "" {
		t.Errorf("b was given -test.run=%q, want none: no tests were named for it", got)
	}
	if active := seen[0].active(); active != "" {
		t.Errorf("the control activated %q", active)
	}

	refused := execute.RunControl(t.Context(), options(f, 1), execute.ControlRun{
		Timeout: mutantTimeout,
		Tests:   map[string][]string{"example.com/c": {"TestOne"}},
	}, testBins("example.com/a"))
	if code := execute.CodeOf(refused.Err); code != execute.CodeControlInvalid {
		t.Errorf("a selection for a binary the control does not run: code %q, want %q (%v)",
			code, execute.CodeControlInvalid, refused.Err)
	}
}

// TestScheduleRecordsTheTestsOfEveryAttempt: the recording names the tests an
// attempt was narrowed to, as `<import path> <name>` labels in one order, and
// says nothing for an attempt that ran its binaries whole.
func TestScheduleRecordsTheTestsOfEveryAttempt(t *testing.T) {
	t.Parallel()

	// The narrowed mutant is killed by its first binary, so its attempt keeps
	// the selection it ran; a survivor would be confirmed against the whole
	// binary and record no tests.
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		// Binary a passes so the run reaches b; b kills, so the mutant is
		// killed rather than survived and no whole-binary confirmation
		// follows -- its attempt keeps both binaries' selections.
		if activeOf(c) == "narrowed" && c.program() == "example.com/b.test" {
			return failed("--- FAIL: TestB\n")
		}
		return passed()
	}}
	opts, sink := traced(t, f, options(f, 1))
	queue := mutants(mutantTimeout, "narrowed", "whole")
	queue[0].Tests = map[string][]string{
		"example.com/b": {"TestB"},
		"example.com/a": {"TestZ", "TestA"},
	}

	if _, err := execute.Schedule(t.Context(), opts, queue, testBins("example.com/a", "example.com/b"), execute.Hooks{}); err != nil {
		t.Fatalf("scheduling: %v", err)
	}
	recorded := map[string][]string{}
	for _, event := range eventsOf(sink, trace.TypeMutantExec) {
		recorded[event.Mutant.ID] = event.Mutant.Tests
	}
	want := map[string][]string{
		"narrowed": {"example.com/a TestA", "example.com/a TestZ", "example.com/b TestB"},
		"whole":    nil,
	}
	if !maps.EqualFunc(recorded, want, slices.Equal) {
		t.Errorf("the attempts recorded tests %v, want %v", recorded, want)
	}
}

// TestRunOneConfirmsANarrowedSurvivorAgainstTheWholeBinary is the soundness
// step ADR 0010 turns on: a mutant whose covering test passes can still be
// killed by a test that does not cover its line but observes, through shared
// state, that the covering test behaved differently under the mutant. RunOne
// re-runs a narrowed survivor against the whole binary, so that kill is not
// lost — and the attempt it returns is the whole-binary one, naming no tests.
func TestRunOneConfirmsANarrowedSurvivorAgainstTheWholeBinary(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		// The narrowed run selects one test and passes; the whole-binary run,
		// which selects none, fails — the shared-state kill the narrowing could
		// not see.
		if testRunOf(c) != "" {
			return passed()
		}
		return failed("--- FAIL: TestUnrelated\n")
	}}
	attempt := execute.RunOne(t.Context(), options(f, 1), execute.MutantRun{
		ID:      "abc123",
		Timeout: mutantTimeout,
		Tests:   map[string][]string{"example.com/a": {"TestCovering"}},
	}, testBins("example.com/a"))

	if attempt.Outcome != mutation.OutcomeKilled {
		t.Fatalf("outcome = %s, want %s: the whole binary catches it (%v)", attempt.Outcome, mutation.OutcomeKilled, attempt.Err)
	}
	if attempt.Tests != nil {
		t.Errorf("the confirmed attempt names tests %v, want none: it ran the whole binary", attempt.Tests)
	}
	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("started %d processes, want the narrowed run and the whole-binary confirmation", len(seen))
	}
	if testRunOf(seen[0]) == "" || testRunOf(seen[1]) != "" {
		t.Errorf("want a narrowed run then a whole-binary run, got selectors %q then %q",
			testRunOf(seen[0]), testRunOf(seen[1]))
	}
}

// TestRunOneDoesNotConfirmAKilledNarrowedRun: only survival needs the whole
// binary. A narrowed run that kills has already run the test that decided it,
// so RunOne returns it without a second pass — and keeps its test selection.
func TestRunOneDoesNotConfirmAKilledNarrowedRun(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return failed("--- FAIL: TestCovering\n") }}
	sel := map[string][]string{"example.com/a": {"TestCovering"}}
	attempt := execute.RunOne(t.Context(), options(f, 1), execute.MutantRun{
		ID:      "abc123",
		Timeout: mutantTimeout,
		Tests:   sel,
	}, testBins("example.com/a"))

	if attempt.Outcome != mutation.OutcomeKilled {
		t.Fatalf("outcome = %s, want killed", attempt.Outcome)
	}
	if len(f.seen()) != 1 {
		t.Errorf("started %d processes, want just the narrowed run that killed it", len(f.seen()))
	}
	if !maps.EqualFunc(attempt.Tests, sel, slices.Equal) {
		t.Errorf("the kill's attempt.Tests = %v, want the narrowed selection %v", attempt.Tests, sel)
	}
}

// TestRunOneConfirmsAWholeBinarySurvivor: a narrowed survivor the whole binary
// also survives stays a survivor, reported as the whole-binary attempt.
func TestRunOneConfirmsAWholeBinarySurvivor(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	attempt := execute.RunOne(t.Context(), options(f, 1), execute.MutantRun{
		ID:      "abc123",
		Timeout: mutantTimeout,
		Tests:   map[string][]string{"example.com/a": {"TestCovering"}},
	}, testBins("example.com/a"))

	if attempt.Outcome != mutation.OutcomeSurvived {
		t.Fatalf("outcome = %s, want survived", attempt.Outcome)
	}
	if attempt.Tests != nil {
		t.Errorf("the confirmed survivor names tests %v, want none: the reported run is the whole binary", attempt.Tests)
	}
	if len(f.seen()) != 2 {
		t.Errorf("started %d processes, want the narrowed run and the whole-binary confirmation", len(f.seen()))
	}
}
