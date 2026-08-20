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

// A diagnostic is one located compiler message.
//
// Path is the module-relative, forward-slashed path when the message names a
// file inside the snapshot, and the token exactly as the compiler printed it
// when it does not — a file in the module cache, in the standard library, or
// anywhere else this run may not touch. Inside says which, and every consumer
// here checks it: blaming a file outside the snapshot on a mutant would have
// the search rejecting candidates until it ran out of them.
type diagnostic struct {
	Path   string
	Inside bool
	Line   int
	Column int
	// Text is the message as printed, first line and continuations, with the
	// location prefix left in place. It is what a [Rejection] carries, and a
	// user reading one wants the coordinates as much as the words.
	Text string
}

// diagnosticLine matches the `file:line[:col]: message` shape the go tool and
// the compiler both print.
//
// The path group is non-greedy, and that is the whole trick: `C:\src\a.go:12:5`
// cannot be split on its first colon, or on its last, and a leftmost-first
// match with a non-greedy prefix walks forwards until the rest of the pattern
// fits, which is precisely "the first colon that is followed by a line number
// and a message". The column is optional because not every diagnostic has one,
// and so is the message, because `too many errors` has been printed both with
// and without a location prefix over the years.
var diagnosticLine = regexp.MustCompile(`^(.*?):(\d+)(?::(\d+))?:(?:[ \t](.*))?$`)

// parseDiagnostics reads compiler output into located messages, in the order
// they were printed.
//
// Three kinds of line are not diagnostics and are treated as such: a `# import/path`
// header, which says which package the following messages belong to and names
// no file; a continuation, which the compiler indents under the message it
// elaborates ("have"/"want" lines) and which therefore belongs to the previous
// diagnostic rather than being one; and anything else, which ends the current
// message so that an unindented `too many errors` cannot be folded into the
// diagnostic above it.
//
// Nothing here tries to compensate for truncated output. `too many errors`
// means the compiler stopped listing, not that the tree holds no more of them,
// and the search that consumes this is a loop for exactly that reason: a
// truncated round is a shorter round, and the next build lists what this one
// did not.
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
		// The submatches cannot fail to convert: the pattern admits digits
		// only. A line number of a million digits would, and errors.Atoi's zero
		// is the right answer for a file that long — there is no such file.
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

// normalizePath maps the file token of a diagnostic onto a module-relative
// path with forward slashes, and reports whether it names a file inside the
// snapshot at all.
//
// Both spellings the go tool produces have to land in the same coordinate
// system as a catalogue path. A build run with the snapshot as its working
// directory prints `.\pkg\file.go` on Windows and `./pkg/file.go` elsewhere,
// and other paths — the module cache, the standard library, a package outside
// the module — come through absolute. Separators are translated
// unconditionally rather than by platform: this parser reads output from the
// toolchain of the host it runs on, but its tests read Windows output on every
// platform, and a Go source file whose name contains a literal backslash is not
// a case worth being wrong about the common one for.
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

// slashed rewrites every backslash as a forward slash.
func slashed(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// underRoot returns the part of an absolute path that lies below root, or
// reports that it does not lie below it at all.
//
// The comparison is case-insensitive whenever either path carries a drive
// letter, and on Windows always. A run's snapshot root and the path a
// diagnostic prints for a file inside it can differ in case on a case-insensitive
// filesystem — a temporary directory reached as C:\Users\… and printed as
// c:\users\… is the ordinary way it happens — and a file that failed to match
// would be treated as outside the snapshot and never blamed on the mutant that
// broke it.
func underRoot(p, root string) (string, bool) {
	if root == "" || len(p) <= len(root) {
		return "", false
	}
	if p[len(root)] != '/' || !equalPath(p[:len(root)], root) {
		return "", false
	}
	return p[len(root)+1:], true
}

// equalPath compares two path prefixes under the case rules of the platform
// they name.
func equalPath(a, b string) bool {
	if runtime.GOOS == "windows" || hasVolume(a) || hasVolume(b) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// hasVolume reports whether a slash-normalized path begins with a drive letter.
func hasVolume(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// isAbsolutePath reports whether a slash-normalized path is rooted, in either
// platform's sense. It is spelled here rather than taken from path/filepath
// because filepath answers for the host, and this parser reads paths written by
// a compiler that may have been describing another one.
func isAbsolutePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return hasVolume(p) && (len(p) == 2 || p[2] == '/')
}

// chooseDiagnostic picks the compiler lines that belong to one rejected mutant.
//
// The candidate's own line range is the first choice, and it is a good one
// because instrumentation preserves lines: the guard that failed to compile
// sits on the line its expression sat on, so the compiler's coordinates and the
// catalogue's agree. Where they do not — a diagnostic reported at the enclosing
// statement, at the function's result list, or at a line the type checker
// reached for while explaining — the nearest message about the same file is
// still about the same problem, and the first message of all is still better
// than telling a user their mutant was rejected and declining to say why. The
// three tiers exist so that this always returns something.
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
	case nearest >= 0:
		return diags[nearest].Text
	case len(diags) > 0:
		return diags[0].Text
	default:
		return ""
	}
}

// blamedPaths returns the snapshot files a build's output names, deduplicated
// and in the order they were first named.
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

// abs is integer absolute value.
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
