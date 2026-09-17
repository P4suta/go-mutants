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

type setVerifier struct {
	sets map[string]verifierSet
}

type verifierSet struct {
	tests   map[string][]string
	timeout time.Duration
	memory  int64
	args    []string
	known   bool
}

func newSetVerifier() *setVerifier {
	return &setVerifier{sets: make(map[string]verifierSet)}
}

func (v *setVerifier) want(tests map[string][]string, run execute.MutantRun) {
	key := setKey(tests)
	if _, seen := v.sets[key]; seen {
		return
	}
	v.sets[key] = verifierSet{
		tests:   cloneSelection(tests),
		timeout: run.Timeout,
		memory:  run.MemoryLimit,
		args:    slices.Clone(run.Args),
		known:   len(run.Args) == 0 && countTests(tests) == 1,
	}
}

func countTests(tests map[string][]string) int {
	total := 0
	for _, names := range tests {
		total += len(names)
	}
	return total
}

func (v *setVerifier) run(ctx context.Context, opts execute.Options, bins []execute.TestBinary) setVerdicts {
	verdicts := setVerdicts{reliable: make(map[string]bool, len(v.sets))}
	if len(v.sets) == 0 {
		return verdicts
	}

	var keys []string
	for _, key := range slices.Sorted(maps.Keys(v.sets)) {
		if v.sets[key].known {
			verdicts.reliable[key] = true
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return verdicts
	}
	index := binaryIndex(bins)

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

type setVerdicts struct {
	reliable   map[string]bool
	unreliable []string
}

func (s setVerdicts) ok(tests map[string][]string) bool {
	if len(tests) == 0 {
		return true
	}
	return s.reliable[setKey(tests)]
}

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

func cloneSelection(tests map[string][]string) map[string][]string {
	out := make(map[string][]string, len(tests))
	for importPath, names := range tests {
		out[importPath] = slices.Clone(names)
	}
	return out
}
