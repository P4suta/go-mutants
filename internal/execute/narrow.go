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

func testRunSelector(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, regexp.QuoteMeta(name))
	}
	return testRunFlag + "^(" + strings.Join(quoted, "|") + ")$"
}

func suppliesTestRun(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.run")
	})
}

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

func selectableTest(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/ \t\n")
}

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
