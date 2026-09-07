// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/testflag"
)

// testRunSelector is the `-test.run` value that selects exactly the named
// tests: each name escaped and anchored, joined as one alternation.
//
// The anchors are the difference between a selection and a prefix search —
// `-test.run=TestA` also selects TestAB and every subtest of both — and the
// escaping is what keeps a name that happens to hold a regular-expression
// metacharacter from selecting something else. Subtests of a selected test
// still run: `^TestX$` matches the parent, and a parent that matches runs its
// children.
func testRunSelector(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, regexp.QuoteMeta(name))
	}
	return testRunFlag + "^(" + strings.Join(quoted, "|") + ")$"
}

// suppliesTestRun reports whether a target's arguments carry their own
// selection, in either spelling the flag package accepts.
func suppliesTestRun(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.run")
	})
}

// validateTestSelection refuses a selection a run could not honour: one for a
// binary the run does not start, one that names no test, or one that names
// something a selector cannot select — an empty name, which matches no
// top-level test and lets the binary pass having run nothing, or a subtest,
// which the binary's own `-test.run` splitting would not find inside the
// anchored alternation and which is selected through its parent anyway. Each
// is refused through the caller's own error, because the two callers — a
// mutant run and a control — refuse under different codes and have to say so
// about different things; the rule is the same and lives once.
func validateTestSelection(
	tests map[string][]string,
	selected []TestBinary,
	refuse func(importPath, why string) error,
) error {
	if len(tests) == 0 {
		return nil
	}
	started := make(map[string]bool, len(selected))
	for _, bin := range selected {
		started[bin.ImportPath] = true
	}
	// Sorted, so that a refusal names the same binary whichever way the map
	// iterates.
	for _, importPath := range slices.Sorted(maps.Keys(tests)) {
		switch {
		case !started[importPath]:
			return refuse(importPath, "which is not among the binaries it starts; the selection and the binaries have drifted apart")
		case len(tests[importPath]) == 0:
			return refuse(importPath, "but names none of them; a binary told to run no tests passes having run nothing")
		}
		for _, name := range tests[importPath] {
			if !selectableTest(name) {
				return refuse(importPath, "including "+strconv.Quote(name)+
					", which is not the name of a top-level test; a subtest is selected through its parent")
			}
		}
	}
	return nil
}

// selectableTest reports whether name is something an anchored `-test.run`
// alternation selects: a non-empty top-level name. A subtest name holds a
// slash, and a space or a tab cannot occur in a Go identifier and would break
// the `<import path> <name>` labels a recording carries.
func selectableTest(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/ \t\n")
}

// testLabels flattens a selection into `<import path> <name>` labels, sorted,
// which is the form a recording carries: one list, one order, and both halves
// recoverable because an import path holds no space.
func testLabels(tests map[string][]string) []string {
	if len(tests) == 0 {
		return nil
	}
	labels := make([]string, 0, len(tests))
	for importPath, names := range tests {
		for _, name := range names {
			labels = append(labels, importPath+" "+name)
		}
	}
	slices.Sort(labels)
	return labels
}
