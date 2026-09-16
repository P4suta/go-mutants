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

// ReuseFile is the licensing manifest for the files that cannot carry an inline
// header.
const ReuseFile = "REUSE.toml"

// ReusePaths is every path pattern REUSE.toml's annotations name, in file
// order.
//
// It is hand-parsed rather than decoded, and the reason is a constraint rather
// than a preference: this is the harness, every test binary in the repository
// links it, and TestTheHarnessImportsNothingFromThisModule holds it to the
// standard library plus go-cmp for exactly that reason. A TOML decoder here
// would be compiled into two hundred binaries to read two lines.
//
// Hand parsing means this depends on how REUSE.toml is *formatted* and not only
// on what it says, which is the weaker thing to depend on -- `taplo fmt` is free
// to move a quoted string onto another line, and a reader that then found
// nothing would be a gate that had stopped looking. Two things stand against
// that. The gate fails closed in one direction: a pattern this misses is a file
// reported as unlicensed, loudly, rather than a file quietly excused.
//
// It is the other direction that needed help, and it was not hypothetical. This
// takes every double-quoted string on a `path` line, so a trailing comment --
// `path = ["go.mod"] # not a "source file"` -- adds `source file` to the
// annotated set, and an annotated path that matches nothing excuses nothing
// today and excuses a real unlicensed file the day somebody creates one by that
// name. Nothing in the repository would have mentioned it.
// internal/devgates, which nothing links and which may therefore decode,
// compares what this returns against what a decoder returns; that comparison is
// what found the comment case, and it is why the agreement is checked rather
// than assumed.
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

// quotedIn is every double-quoted string on one line.
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

// ReuseMatch reports whether one REUSE.toml path pattern covers one file.
//
// It exists because [path.Match] is wrong here in the shape that is worst: it
// is right about `*` and partly right about `**`. `path.Match` does not let a
// wildcard cross a separator, so `testdata/**` matches `testdata/a.txt` and
// does not match `testdata/deep/a.txt` -- the pattern covers something, so it
// is never reported as the stale annotation this package refuses, and the file
// one directory further down is reported as unlicensed instead. A reader would
// be looking for a missing header on a file that REUSE says is annotated.
//
// The semantics here were taken from the reference implementation rather than
// from a reading of the specification, because a reading is what produced the
// wrong answer the first time. With `path = ["data/**"]`, `reuse lint` calls a
// project holding `data/deep/nested.txt` compliant; changing the pattern to
// `data/*` makes it name that exact file under MISSING COPYRIGHT AND LICENSING
// INFORMATION. So `**` crosses separators and `*` does not, observed in both
// directions against reuse 3.3.
//
// Every other wildcard [path.Match] would accept -- `?`, a character class --
// is refused rather than approximated. Not because REUSE forbids them, but
// because nothing here has been shown what REUSE does with them, and a matcher
// that guesses is how this function came to be needed.
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

// ReuseCovering is every annotation pattern that covers one file, not the
// first.
//
// It is not a bug fix. The gate's staleness verdict does not rest on this
// bookkeeping -- [ReuseAnnotationCoversATrackedFile] re-derives it -- and a
// first-match version was tried against a manifest holding a wide glob beside a
// narrow path it already covers, which is the shape a merged manifest takes,
// and the gate stayed green. What the shape buys is that a caller cannot mark
// only the first pattern live without writing a loop that says so.
//
// A manifest holding the same path string twice is invisible either way, and
// that is worth knowing rather than guessing at: two identical strings are one
// key in the map the gate keeps, so nothing downstream can tell them apart, and
// nothing can accuse them.
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

// ReuseAnnotationCoversATrackedFile reports whether a pattern claims any file
// git holds, asked of every one of them.
//
// It does not decide whether an annotation is live. The coverage pass already
// answers that, and answers it correctly: it walks the files carrying no inline
// header, which are exactly the files an annotation is for.
//
// What this decides is which true sentence the gate prints when an annotation
// covers none of them. A row can be dead two ways -- the files it described are
// gone, or they are here and carry their own headers, which is outside what
// this manifest is for -- and both are a row to delete while neither is a file
// without a licence, which is the other thing this gate reports. Reading one as
// the other is the mistake worth spending a second tree walk on.
//
// Answering the verdict from this instead was tried, and produces a sentence
// that is confident, names a real path, and is false:
//
//	REUSE.toml annotates "internal/cache/key.go", which no committed file matches
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
