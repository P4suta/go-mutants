// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const ReuseFile = "REUSE.toml"

func ReusePaths(t testing.TB, root string) []string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(root, ReuseFile))
	if err != nil {
		t.Fatalf("reading %s: %v", ReuseFile, err)
	}
	var patterns []string
	inList := false
	for _, raw := range strings.Split(string(source), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !inList {
			rest, ok := strings.CutPrefix(line, "path")
			if !ok {
				continue
			}
			rest = strings.TrimSpace(rest)
			rest, ok = strings.CutPrefix(rest, "=")
			if !ok {
				continue
			}
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "["))
			patterns = append(patterns, quotedIn(rest)...)
			inList = !strings.Contains(raw, "]")
			continue
		}
		patterns = append(patterns, quotedIn(line)...)
		if strings.Contains(line, "]") {
			inList = false
		}
	}
	return patterns
}

func quotedIn(line string) []string {
	var out []string
	for {
		open := strings.Index(line, `"`)
		if open < 0 {
			return out
		}
		rest := line[open+1:]
		close := strings.Index(rest, `"`)
		if close < 0 {
			return out
		}
		out = append(out, rest[:close])
		line = rest[close+1:]
	}
}

func ReuseMatch(pattern, name string) (bool, error) {
	var b strings.Builder
	b.WriteString(`\A`)
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(`.*`)
				i++
				continue
			}
			b.WriteString(`[^/]*`)
		case '?', '[', ']':
			return false, fmt.Errorf("%s pattern %q uses %q, which this matcher does not implement", ReuseFile, pattern, string(c))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`\z`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false, fmt.Errorf("%s pattern %q does not compile: %w", ReuseFile, pattern, err)
	}
	return re.MatchString(name), nil
}

func ReuseCovering(patterns []string, name string) ([]string, error) {
	var covering []string
	for _, pattern := range patterns {
		ok, err := ReuseMatch(pattern, name)
		if err != nil {
			return nil, err
		}
		if ok {
			covering = append(covering, pattern)
		}
	}
	return covering, nil
}

func ReuseAnnotationCoversATrackedFile(pattern string, tracked []string) (bool, error) {
	for _, rel := range tracked {
		ok, err := ReuseMatch(pattern, rel)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
