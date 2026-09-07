// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
)

// A setVerifier holds the distinct sets of tests that mutants are narrowed to
// and checks each one, before any mutant runs, with a control: the same tests,
// no mutant. A set that passes test by test can still fail when its tests are
// run together — one leaves state another needs — and a mutant narrowed to
// such a set would be reported killed by a failure that is not the mutant's.
// The control is what tells the two apart, and running it up front means the
// runs handed to the scheduler are already the ones whose verdicts can be
// trusted.
//
// Each distinct set is checked once, however many mutants share it, because the
// question — do these tests pass together without a mutant — has one answer per
// set.
type setVerifier struct {
	sets map[string]verifierSet
}

// A verifierSet is one distinct selection to check, and the budget to check it
// under: a control is measured under the same bound the executions it licenses
// are.
type verifierSet struct {
	tests   map[string][]string
	timeout time.Duration
	memory  int64
	args    []string
}

// newSetVerifier returns an empty verifier.
func newSetVerifier() *setVerifier {
	return &setVerifier{sets: make(map[string]verifierSet)}
}

// want records that some mutant is narrowed to tests, under run's budget. The
// selection is canonicalised so that the same set from two mutants is checked
// once.
func (v *setVerifier) want(tests map[string][]string, run execute.MutantRun) {
	key := setKey(tests)
	if _, seen := v.sets[key]; seen {
		return
	}
	v.sets[key] = verifierSet{
		tests:   cloneSelection(tests),
		timeout: run.Timeout,
		memory:  run.MemoryLimit,
		// The same arguments the mutant runs with: an accepted flag such as
		// -test.short changes what the tests do, so a control without it would
		// be a control of a different invocation.
		args: slices.Clone(run.Args),
	}
}

// run checks every recorded set with a control and returns the verdicts. A set
// whose control passed is reliable; a set whose control failed, timed out or
// could not run is not, and its mutants are widened to their whole binaries.
//
// The controls go through the scheduler's own worker pool, so the checks cost
// what the machine can hold at once rather than one after another. A control
// that cannot even start is treated as an unreliable set rather than a failed
// run, for the reason the whole phase fails open: an optimisation that cannot
// be verified is one the run does without.
func (v *setVerifier) run(ctx context.Context, opts execute.Options, bins []execute.TestBinary) setVerdicts {
	verdicts := setVerdicts{reliable: make(map[string]bool, len(v.sets))}
	if len(v.sets) == 0 {
		return verdicts
	}
	index := binaryIndex(bins)

	keys := slices.Sorted(maps.Keys(v.sets))
	controls := make([]execute.ControlRun, 0, len(keys))
	for _, key := range keys {
		set := v.sets[key]
		controls = append(controls, execute.ControlRun{
			Timeout:     set.timeout,
			MemoryLimit: set.memory,
			Binaries:    indicesOf(slices.Sorted(maps.Keys(set.tests)), index),
			Tests:       set.tests,
			Args:        slices.Clone(set.args),
		})
	}

	attempts := execute.RunControls(ctx, opts, controls, bins)
	unreliable := make(map[string]bool)
	for i, key := range keys {
		attempt := attempts[i]
		if attempt.Err == nil && attempt.ExitCode == 0 && !attempt.TimedOut {
			verdicts.reliable[key] = true
			continue
		}
		for _, name := range setLabels(v.sets[key].tests) {
			unreliable[name] = true
		}
	}
	verdicts.unreliable = slices.Sorted(maps.Keys(unreliable))
	return verdicts
}

// setVerdicts is which sets a [setVerifier] found reliable.
type setVerdicts struct {
	reliable map[string]bool
	// unreliable is the sorted `<import path> <name>` labels of every test in a
	// set that failed its control, for the one warning that names them.
	unreliable []string
}

// ok reports whether the set of these tests passed its control. An empty set
// is trivially ok: it narrows nothing and there was no control to fail.
func (s setVerdicts) ok(tests map[string][]string) bool {
	if len(tests) == 0 {
		return true
	}
	return s.reliable[setKey(tests)]
}

// setKey canonicalises a selection into one string: import paths sorted, names
// sorted within each, so that the same set always keys the same however it was
// built.
func setKey(tests map[string][]string) string {
	var b strings.Builder
	for _, importPath := range slices.Sorted(maps.Keys(tests)) {
		b.WriteString(importPath)
		b.WriteByte('\x00')
		names := slices.Clone(tests[importPath])
		slices.Sort(names)
		for _, name := range names {
			b.WriteString(name)
			b.WriteByte('\x01')
		}
		b.WriteByte('\x02')
	}
	return b.String()
}

// setLabels renders a selection as sorted `<import path> <name>` labels.
func setLabels(tests map[string][]string) []string {
	var labels []string
	for importPath, names := range tests {
		for _, name := range names {
			labels = append(labels, importPath+" "+name)
		}
	}
	slices.Sort(labels)
	return labels
}

// cloneSelection deep-copies a selection.
func cloneSelection(tests map[string][]string) map[string][]string {
	out := make(map[string][]string, len(tests))
	for importPath, names := range tests {
		out[importPath] = slices.Clone(names)
	}
	return out
}
