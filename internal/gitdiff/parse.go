// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

const (
	fileMarker  = "diff --git "
	targetMaker = "+++ "
	hunkMarker  = "@@ "
	devNull     = "/dev/null"
	dstPrefix   = "b/"
)

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
			if path == "" || count == 0 {
				continue
			}
			files[path] = append(files[path], Range{First: first, Last: first + count - 1})
		}
	}

	for path, ranges := range files {
		files[path] = Merge(ranges)
	}
	return files, nil
}

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

func hunkLines(line string) (first, count int, err error) {
	rest := strings.TrimPrefix(line, hunkMarker)
	ranges, _, closed := strings.Cut(rest, " @@")
	if !closed {
		return 0, 0, malformedHunk(line)
	}
	var spec string
	for _, field := range strings.Fields(ranges) {
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
	if count > 0 && first < 1 {
		return 0, 0, malformedHunk(line)
	}
	return first, count, nil
}

func malformedHunk(line string) error {
	return &Error{
		Code:    CodeMalformedDiff,
		Message: "go-mutants cannot read the diff hunk header " + strconv.Quote(line),
	}
}

func relative(path, prefix string) string {
	rest, inside := strings.CutPrefix(path, prefix)
	if !inside || rest == "" {
		return ""
	}
	return rest
}

func Merge(ranges []Range) []Range {
	slices.SortFunc(ranges, func(x, y Range) int {
		return cmp.Or(cmp.Compare(x.First, y.First), cmp.Compare(x.Last, y.Last))
	})
	out := ranges[:0]
	for _, r := range ranges {
		if n := len(out); n > 0 && r.First-1 <= out[n-1].Last {
			out[n-1].Last = max(out[n-1].Last, r.Last)
			continue
		}
		out = append(out, r)
	}
	return out
}

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
			if i+2 < len(body) {
				if value, err := strconv.ParseUint(body[i:i+3], 8, 8); err == nil {
					b.WriteByte(byte(value))
					i += 2
					continue
				}
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
