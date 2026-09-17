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

func testRunOf(c call) string {
	for _, a := range c.Argv {
		if rest, ok := strings.CutPrefix(a, "-test.run="); ok {
			return rest
		}
	}
	return ""
}

func TestRunOneSelectsOnlyTheNamedTestsOfEachBinary(t *testing.T) {
	t.Parallel()

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
	wantA := []string{
		"example.com/a.test", "-test.timeout=14s", execute.FailFastFlag,
		`-test.run=^(TestOne|Test\.Two)$`, "-test.count=1",
	}
	if !slices.Equal(seen[0].Argv, wantA) {
		t.Errorf("argv for a = %q, want %q", seen[0].Argv, wantA)
	}
	wantC := []string{"example.com/c.test", "-test.timeout=14s", execute.FailFastFlag, "-test.count=1"}
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
			name:  "an empty name",
			tests: map[string][]string{"example.com/a": {"TestOne", ""}},
			want:  `""`,
		},
		{
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

func TestScheduleRecordsTheTestsOfEveryAttempt(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
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

func TestRunOneConfirmsANarrowedSurvivorAgainstTheWholeBinary(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
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
