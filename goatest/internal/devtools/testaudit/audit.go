// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

type event struct {
	Action string

	Package string

	Test string

	Output string
}

type tally struct {
	pkg string

	passed  int
	failed  int
	skipped int

	noTestFiles bool

	skippedNames []string

	failedNames []string

	narrowed []string
}

type failure struct {
	pkg  string
	test string

	output []string
}

type result struct {
	packages []tally

	passed  int
	failed  int
	skipped int

	failures []failure
}

func audit(input io.Reader) (result, error) {
	tallies := make(map[string]*tally)
	output := make(map[string][]string)
	lookup := func(pkg string) *tally {
		found, ok := tallies[pkg]
		if !ok {
			found = &tally{pkg: pkg}
			tallies[pkg] = found
		}
		return found
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, initialLineBuffer), maximumLineBuffer)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if !strings.HasPrefix(text, "{") {
			return result{}, fmt.Errorf("goatest: test event line %d is not JSON: %.80s", line, text)
		}
		var record event
		if err := json.Unmarshal([]byte(text), &record); err != nil {
			return result{}, fmt.Errorf("goatest: decode test event line %d: %w", line, err)
		}
		if record.Package == "" {
			continue
		}
		counted := lookup(record.Package)
		key := record.Package + " " + record.Test
		switch {
		case record.Action == "output" && record.Test != "":
			output[key] = append(output[key], strings.TrimRight(record.Output, "\n"))
		case record.Action == "output":
			if line := strings.TrimRight(record.Output, "\n"); strings.Contains(line, NarrowedFilterMarker) {
				counted.narrowed = append(counted.narrowed, strings.TrimSpace(line))
			}
		case record.Action == "pass" && record.Test != "":
			counted.passed++
			delete(output, key)
		case record.Action == "fail" && record.Test != "":
			counted.failed++
			counted.failedNames = append(counted.failedNames, record.Test)
		case record.Action == "skip" && record.Test != "":
			delete(output, key)
			counted.skipped++
			counted.skippedNames = append(counted.skippedNames, record.Test)
		case record.Action == "skip":
			counted.noTestFiles = true
		}
	}
	if err := scanner.Err(); err != nil {
		return result{}, fmt.Errorf("goatest: read the test event stream: %w", err)
	}
	return summarize(tallies, output), nil
}

func summarize(tallies map[string]*tally, output map[string][]string) result {
	var summary result
	for _, counted := range tallies {
		summary.packages = append(summary.packages, *counted)
		summary.passed += counted.passed
		summary.failed += counted.failed
		summary.skipped += counted.skipped
	}
	sort.Slice(summary.packages, func(first, second int) bool {
		return summary.packages[first].pkg < summary.packages[second].pkg
	})
	for _, counted := range summary.packages {
		for _, name := range counted.failedNames {
			summary.failures = append(summary.failures, failure{
				pkg:    counted.pkg,
				test:   name,
				output: output[counted.pkg+" "+name],
			})
		}
	}
	return summary
}

func (summary result) silentPackages() []string {
	var silent []string
	for _, counted := range summary.packages {
		if counted.noTestFiles || counted.skipped == 0 {
			continue
		}
		if counted.passed == 0 && counted.failed == 0 {
			silent = append(silent, counted.pkg)
		}
	}
	return silent
}

func (summary result) narrowedPackages() []string {
	var narrowed []string
	for _, counted := range summary.packages {
		for _, line := range counted.narrowed {
			narrowed = append(narrowed, counted.pkg+": "+line)
		}
	}
	return narrowed
}

func (summary result) unrecordedSkips(allowed []string) []string {
	permitted := make(map[string]struct{}, len(allowed))
	for _, entry := range allowed {
		permitted[entry] = struct{}{}
	}
	var unrecorded []string
	for _, counted := range summary.packages {
		for _, name := range counted.skippedNames {
			entry := counted.pkg + " " + topLevelTest(name)
			if _, ok := permitted[entry]; ok {
				continue
			}
			unrecorded = append(unrecorded, entry)
		}
	}
	slices.Sort(unrecorded)
	return slices.Compact(unrecorded)
}

func topLevelTest(name string) string {
	parent, _, _ := strings.Cut(name, "/")
	return parent
}

func render(out io.Writer, summary result) {
	for _, failed := range summary.failures {
		_, _ = fmt.Fprintf(out, "--- FAIL: %s %s\n", failed.pkg, failed.test)
		for _, line := range failed.output {
			_, _ = fmt.Fprintf(out, "    %s\n", line)
		}
	}
	_, _ = fmt.Fprintf(out, "tests: %d passed, %d failed, %d skipped, across %d package(s)\n",
		summary.passed, summary.failed, summary.skipped, len(summary.packages))
	for _, counted := range summary.packages {
		if counted.skipped == 0 {
			continue
		}
		_, _ = fmt.Fprintf(out, "  %s: %d skipped (%s)\n",
			counted.pkg, counted.skipped, strings.Join(counted.skippedNames, ", "))
	}
}
