// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
)

func TestSetVerifierCarriesTheMutantArgumentsIntoTheControl(t *testing.T) {
	t.Parallel()

	v := newSetVerifier()
	tests := map[string][]string{"example.com/m/a": {"TestA"}}
	v.want(tests, execute.MutantRun{ID: "m1", Timeout: 1, Args: []string{"-test.short", "-test.count=1"}})

	set, ok := v.sets[setKey(tests)]
	if !ok {
		t.Fatal("the set was not recorded")
	}
	if want := []string{"-test.short", "-test.count=1"}; !slices.Equal(set.args, want) {
		t.Errorf("recorded args = %v, want the mutant's %v", set.args, want)
	}
}

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

func TestRunControlsGetTheVerifiersArguments(t *testing.T) {
	t.Parallel()

	v := newSetVerifier()
	v.want(map[string][]string{"example.com/m/a": {"TestA"}}, execute.MutantRun{ID: "m1", Timeout: 1, Args: []string{"-test.short"}})
	got := v.run(context.Background(), execute.Options{}, nil)
	if got.ok(map[string][]string{"example.com/m/a": {"TestA"}}) {
		t.Error("a set whose control could not run was treated as reliable")
	}
}
