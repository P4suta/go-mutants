// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	gomutants "github.com/P4suta/go-mutants"
)

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

const diffPathPrefix = "+++ b/"

func changedLineRanges(ctx context.Context, root, reference string, changed []string) (map[string][]gomutants.LineRange, bool) {
	tracked, untracked, listed := splitTrackedChanges(ctx, root, changed)
	if !listed {
		return nil, false
	}
	ranges := make(map[string][]gomutants.LineRange, len(changed))
	for _, path := range untracked {
		lines, ok := fileLineCount(filepath.Join(root, filepath.FromSlash(path)))
		if !ok {
			return nil, false
		}
		if lines == 0 {
			continue
		}
		ranges[path] = []gomutants.LineRange{{First: 1, Last: lines}}
	}
	if len(tracked) == 0 {
		return ranges, true
	}
	base := reference
	if base == "" {
		base = "HEAD"
	}
	arguments := append([]string{
		"-c", "core.quotepath=false", "diff", "--no-ext-diff", "--no-renames",
		"--unified=0", "--diff-filter=ACM", base, "--",
	}, tracked...)
	output, err := gitNamesOutput(ctx, root, arguments)
	if err != nil || ctx.Err() != nil {
		return nil, false
	}
	known := make(map[string]bool, len(tracked))
	for _, path := range tracked {
		known[path] = true
	}
	current := ""
	for _, line := range strings.Split(string(output), "\n") {
		if after, found := strings.CutPrefix(line, diffPathPrefix); found {
			if !known[after] {
				return nil, false
			}
			current = after
			continue
		}
		match := hunkHeader.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if current == "" {
			return nil, false
		}
		span, ok := hunkSpan(match)
		if !ok {
			return nil, false
		}
		if span.Last < span.First {
			continue
		}
		ranges[current] = append(ranges[current], span)
	}
	return ranges, true
}

func hunkSpan(match []string) (gomutants.LineRange, bool) {
	first, err := strconv.Atoi(match[1])
	if err != nil || first < 1 {
		return gomutants.LineRange{}, false
	}
	count := 1
	if match[2] != "" {
		count, err = strconv.Atoi(match[2])
		if err != nil || count < 0 {
			return gomutants.LineRange{}, false
		}
	}
	if count == 0 {
		return gomutants.LineRange{First: first, Last: first - 1}, true
	}
	return gomutants.LineRange{First: first, Last: first + count - 1}, true
}

func splitTrackedChanges(ctx context.Context, root string, changed []string) (tracked, untracked []string, listed bool) {
	present := make([]string, 0, len(changed))
	for _, path := range changed {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			continue
		}
		present = append(present, path)
	}
	if len(present) == 0 {
		return nil, nil, true
	}
	names, ok := runImpactGitNames(ctx, root, append([]string{"ls-files", "-z", "--"}, present...))
	if !ok {
		return nil, nil, false
	}
	known := make(map[string]bool, len(names))
	for _, name := range names {
		normalized, valid := safeChangedPath(name)
		if !valid {
			return nil, nil, false
		}
		known[normalized] = true
	}
	for _, path := range present {
		if known[path] {
			tracked = append(tracked, path)
			continue
		}
		untracked = append(untracked, path)
	}
	return tracked, untracked, true
}

func fileLineCount(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if len(data) == 0 {
		return 0, true
	}
	lines := strings.Count(string(data), "\n")
	if !strings.HasSuffix(string(data), "\n") {
		lines++
	}
	return lines, true
}

func mutationSelection(selection impactSelection) *gomutants.Selection {
	if selection.broad || selection.ranges == nil {
		return nil
	}
	lines := make(map[string][]gomutants.LineRange, len(selection.ranges))
	for _, path := range selection.changed {
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
	}
	for path, spans := range selection.ranges {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		lines[path] = slices.Clone(spans)
	}
	if len(lines) == 0 {
		return nil
	}
	return &gomutants.Selection{Lines: lines}
}
