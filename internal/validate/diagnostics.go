// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

type diagnostic struct {
	Path   string
	Inside bool
	Line   int
	Column int
	Text   string
}

var diagnosticLine = regexp.MustCompile(`^(.*?):(\d+)(?::(\d+))?:(?:[ \t](.*))?$`)

func parseDiagnostics(output, root string) []diagnostic {
	var out []diagnostic
	current := -1
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.TrimSpace(line) == "":
			current = -1
			continue
		case strings.HasPrefix(line, "#"):
			current = -1
			continue
		case line[0] == '\t' || line[0] == ' ':
			if current >= 0 {
				out[current].Text += "\n" + line
			}
			continue
		}

		match := diagnosticLine.FindStringSubmatch(line)
		if match == nil {
			current = -1
			continue
		}
		lineNo, _ := strconv.Atoi(match[2])
		column, _ := strconv.Atoi(match[3])
		rel, inside := normalizePath(match[1], root)
		if !inside {
			rel = match[1]
		}
		out = append(out, diagnostic{
			Path:   rel,
			Inside: inside,
			Line:   lineNo,
			Column: column,
			Text:   line,
		})
		current = len(out) - 1
	}
	return out
}

func normalizePath(raw, root string) (string, bool) {
	p := strings.TrimSpace(slashed(raw))
	if p == "" {
		return "", false
	}
	if isAbsolutePath(p) {
		rest, ok := underRoot(p, strings.TrimRight(slashed(root), "/"))
		if !ok {
			return "", false
		}
		p = rest
	}
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	if p == "" {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || isAbsolutePath(clean) {
		return "", false
	}
	return clean, true
}

func slashed(p string) string { return strings.ReplaceAll(p, `\`, "/") }

func underRoot(p, root string) (string, bool) {
	if root == "" || len(p) <= len(root) {
		return "", false
	}
	if p[len(root)] != '/' || !equalPath(p[:len(root)], root) {
		return "", false
	}
	return p[len(root)+1:], true
}

func equalPath(a, b string) bool { return equalPathOn(runtime.GOOS, a, b) }

func equalPathOn(goos, a, b string) bool {
	if goos == "windows" || hasVolume(a) || hasVolume(b) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasVolume(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isAbsolutePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return hasVolume(p) && (len(p) == 2 || p[2] == '/')
}

func chooseDiagnostic(diags []diagnostic, file string, startLine, endLine int) string {
	var within []string
	nearest, best := -1, 0
	for i, d := range diags {
		if !d.Inside || d.Path != file {
			continue
		}
		if d.Line >= startLine && d.Line <= endLine {
			within = append(within, d.Text)
			continue
		}
		if distance := abs(d.Line - startLine); nearest < 0 || distance < best {
			nearest, best = i, distance
		}
	}
	switch {
	case len(within) > 0:
		return strings.Join(within, "\n")
	case len(diags) > 0:
		return diags[max(nearest, 0)].Text
	default:
		return ""
	}
}

func blamedPaths(diags []diagnostic) []string {
	seen := make(map[string]bool, len(diags))
	var out []string
	for _, d := range diags {
		if !d.Inside || seen[d.Path] {
			continue
		}
		seen[d.Path] = true
		out = append(out, d.Path)
	}
	return out
}

func abs(n int) int { return max(n, -n) }
