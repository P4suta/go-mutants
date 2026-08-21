// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"slices"
	"strconv"
	"strings"
)

// The markers of a unified diff, as git writes one.
const (
	fileMarker  = "diff --git "
	targetMaker = "+++ "
	hunkMarker  = "@@ "
	devNull     = "/dev/null"
	dstPrefix   = "b/"
)

// parseDiff reads `git diff -U0` output into a changed-line set.
//
// The whole of the parsing difficulty is that a diff body is arbitrary source
// code, and source code contains lines that look like diff headers. Under `-U0`
// every body line begins with `+` or `-`, so an added line whose content starts
// with `++ ` arrives as `+++ ` and is indistinguishable from a file header
// unless the reader knows where it is. So this is a state machine rather than a
// scan: `+++` is a header only before the first hunk of a file, and a `diff
// --git` line is what starts a file. That is also why the `---` line is not
// read at all — the destination name is the only one that matters, and reading
// one fewer header is one fewer line to be confused by.
//
// Paths arrive relative to the repository root and leave relative to the
// workspace root, which is what prefix is for. Anything outside the workspace is
// dropped: the pathspec already asked git for the subtree, and a path that
// escapes it anyway is not a file this run can mutate.
func parseDiff(out, prefix string) (map[string][]Range, error) {
	files := make(map[string][]Range)
	path := ""
	inHunks := false

	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, fileMarker):
			path, inHunks = "", false
		case !inHunks && strings.HasPrefix(line, targetMaker):
			name, err := targetPath(line)
			if err != nil {
				return nil, err
			}
			path = relative(name, prefix)
		case strings.HasPrefix(line, hunkMarker):
			inHunks = true
			first, count, err := hunkLines(line)
			if err != nil {
				return nil, err
			}
			// A pure deletion adds no lines and therefore touches none: the
			// text that was there is gone, and there is nothing left to mutate.
			// A file with only deletions produces no entry at all, which is
			// why an empty range list is never stored.
			if path == "" || count == 0 {
				continue
			}
			files[path] = append(files[path], Range{First: first, Last: first + count - 1})
		}
	}

	for path, ranges := range files {
		files[path] = merge(ranges)
	}
	return files, nil
}

// targetPath reads the destination path out of a `+++ ` header.
//
// `/dev/null` is a deleted file and yields the empty path, which suppresses the
// hunks underneath it — the same effect as a hunk that adds nothing, reached
// from the other direction.
func targetPath(line string) (string, error) {
	name := strings.TrimPrefix(line, targetMaker)
	if name == devNull {
		return "", nil
	}
	if !strings.HasPrefix(name, dstPrefix) {
		return "", &Error{
			Code: CodeMalformedDiff,
			Message: "a diff header names no destination file, which go-mutants asked git to guarantee with --dst-prefix: " +
				strconv.Quote(line),
		}
	}
	return unquote(strings.TrimPrefix(name, dstPrefix)), nil
}

// hunkLines reads the destination side of a hunk header: `@@ -a,b +c,d @@`,
// where each count is omitted when it is 1.
//
// A count of zero is a pure deletion at that point and is reported as such
// rather than as a one-line range, which is the difference between "these lines
// are new" and "something used to be here".
func hunkLines(line string) (first, count int, err error) {
	rest := strings.TrimPrefix(line, hunkMarker)
	end := strings.Index(rest, " @@")
	if end < 0 {
		return 0, 0, malformedHunk(line)
	}
	var spec string
	for _, field := range strings.Fields(rest[:end]) {
		if strings.HasPrefix(field, "+") {
			spec = strings.TrimPrefix(field, "+")
			break
		}
	}
	if spec == "" {
		return 0, 0, malformedHunk(line)
	}

	startText, countText, hasCount := strings.Cut(spec, ",")
	first, err = strconv.Atoi(startText)
	if err != nil || first < 0 {
		return 0, 0, malformedHunk(line)
	}
	count = 1
	if hasCount {
		count, err = strconv.Atoi(countText)
		if err != nil || count < 0 {
			return 0, 0, malformedHunk(line)
		}
	}
	// A hunk that adds lines always starts at a real line. Zero with a non-zero
	// count would be a line number no file has.
	if count > 0 && first < 1 {
		return 0, 0, malformedHunk(line)
	}
	return first, count, nil
}

// malformedHunk builds the error for a hunk header this parser cannot read.
func malformedHunk(line string) error {
	return &Error{
		Code:    CodeMalformedDiff,
		Message: "go-mutants cannot read the diff hunk header " + strconv.Quote(line),
	}
}

// relative maps a repository-relative path onto a workspace-relative one, and
// returns "" for anything outside the workspace.
func relative(path, prefix string) string {
	if path == "" || prefix == "" {
		return path
	}
	rest, inside := strings.CutPrefix(path, prefix)
	if !inside || rest == "" {
		return ""
	}
	return rest
}

// merge sorts a file's ranges and joins the ones that touch or overlap, so that
// the stored set is canonical: two diffs describing the same lines produce the
// same ranges whatever order git emitted the hunks in.
func merge(ranges []Range) []Range {
	slices.SortFunc(ranges, func(x, y Range) int {
		if c := x.First - y.First; c != 0 {
			return c
		}
		return x.Last - y.Last
	})
	out := ranges[:0]
	for _, r := range ranges {
		if n := len(out); n > 0 && r.First <= out[n-1].Last+1 {
			out[n-1].Last = max(out[n-1].Last, r.Last)
			continue
		}
		out = append(out, r)
	}
	return out
}

// unquote undoes git's C-style quoting of a path.
//
// `core.quotePath=false` is passed on every invocation, so non-ASCII paths
// arrive literally; what remains quoted is a path containing a quotation mark,
// a backslash, or a control character. Those are vanishingly rare and are still
// paths a run has to be able to name, so they are decoded rather than refused.
// Anything that is not quoted is returned untouched, which is every ordinary
// path.
func unquote(path string) string {
	if len(path) < 2 || path[0] != '"' || path[len(path)-1] != '"' {
		return path
	}
	body := path[1 : len(path)-1]
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' || i+1 >= len(body) {
			b.WriteByte(body[i])
			continue
		}
		i++
		switch c := body[i]; c {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// A three-digit octal escape, which is how git writes a byte it
			// will not print. Anything shorter is not one, and is written back
			// as it was found rather than guessed at.
			if i+2 < len(body) && isOctal(body[i+1]) && isOctal(body[i+2]) {
				value, err := strconv.ParseUint(body[i:i+3], 8, 8)
				if err == nil {
					b.WriteByte(byte(value))
					i += 2
					continue
				}
			}
			b.WriteByte(c)
		default:
			// Covers `\"` and `\\`, and leaves an escape nobody defined as the
			// character it escaped.
			b.WriteByte(c)
		}
	}
	return b.String()
}

// isOctal reports whether c is an octal digit.
func isOctal(c byte) bool { return c >= '0' && c <= '7' }
