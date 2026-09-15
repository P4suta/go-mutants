// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package glob_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/P4suta/go-mutants/internal/glob"
)

// The laws the documented semantics imply, checked against the matcher rather
// than against a second reading of it.
//
// fuzz_test.go already holds this package to a naive reference matcher, which
// is the strongest statement there is that the implementation *reads the
// pattern the obvious way*. What it cannot say is whether the obvious way is
// the one the package documents, because the reference is the same reading
// written twice. These are about the documented rules themselves: `**` matching
// zero elements, a trailing `**` naming the directory it excludes, and a
// pattern of literals matching exactly itself. Each of them is a decision
// third-party globbers disagree about, and each of them changes which mutants a
// run produces.

// elementGen draws one path element: short, from a small alphabet, and
// including the bytes the language treats specially so that a drawn *path* can
// hold an asterisk the pattern cannot demand.
func elementGen() *rapid.Generator[string] {
	return rapid.StringOfN(rapid.RuneFrom([]rune("ab.*?")), 1, 3, -1)
}

// pathGen draws a slash-separated path of one to four elements.
func pathGen() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		return strings.Join(rapid.SliceOfN(elementGen(), 1, 4).Draw(t, "elements"), "/")
	})
}

// compiled compiles a pattern, or fails the property with the refusal.
func compiled(t *rapid.T, pattern string) glob.Pattern {
	p, err := glob.Compile(pattern)
	if err != nil {
		t.Fatalf("Compile(%q): %v", pattern, err)
	}
	return p
}

// TestADoubleStarMayMatchNoElementsAtAll is the rule every other globber
// disagrees about, stated as an implication rather than as a table of paths.
//
// `**/*.go` has to match `a.go`, because the alternative is a pattern that
// means "at least one directory deep" and silently excludes every file at the
// root of a module. The law is that prefixing a pattern with `**/` can only
// ever add paths: whatever the original matched, the prefixed one matches too.
func TestADoubleStarMayMatchNoElementsAtAll(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		pattern := pathGen().Draw(rt, "pattern")
		path := pathGen().Draw(rt, "path")

		plain := compiled(rt, pattern)
		prefixed := compiled(rt, "**/"+pattern)
		if plain.Match(path) && !prefixed.Match(path) {
			rt.Fatalf("%q matches %q and %q does not, so ** did not match zero elements",
				pattern, path, "**/"+pattern)
		}
		// And prefixing twice says nothing more than prefixing once: `**` is
		// zero or more elements, and two of them in a row are still zero or
		// more.
		twice := compiled(rt, "**/**/"+pattern)
		if got, want := twice.Match(path), prefixed.Match(path); got != want {
			rt.Fatalf("%q matches %q = %v and %q = %v", "**/**/"+pattern, path, got, "**/"+pattern, want)
		}
	})
}

// TestATrailingDoubleStarNamesTheDirectoryItExcludes is the other end of the
// same rule, and the one somebody excluding a tree is relying on.
//
// `vendor/**` has to match the bare path `vendor` as well as everything under
// it — that is what gitignore means by it, and it is what a person writing
// `exclude = ["vendor/**"]` means. The law: a path a pattern matches is matched
// by that pattern with `/**` appended, and so is every path below it.
func TestATrailingDoubleStarNamesTheDirectoryItExcludes(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		pattern := pathGen().Draw(rt, "pattern")
		path := pathGen().Draw(rt, "path")
		below := rapid.SliceOfN(elementGen(), 1, 2).Draw(rt, "below")

		plain := compiled(rt, pattern)
		tree := compiled(rt, pattern+"/**")
		if !plain.Match(path) {
			return
		}
		if !tree.Match(path) {
			rt.Fatalf("%q matches %q and %q does not name the directory itself", pattern, path, pattern+"/**")
		}
		deeper := path + "/" + strings.Join(below, "/")
		if !tree.Match(deeper) {
			rt.Fatalf("%q does not match %q, which is under %q", pattern+"/**", deeper, path)
		}
	})
}

// TestAPatternOfLiteralsMatchesExactlyItself is the floor the rest of the
// language sits on: a pattern with no wildcard in it is a path, and a path
// matches itself and nothing else.
//
// It is worth stating because the wildcards are the part with rules. A matcher
// that read `.` or `\` as special — a character class, an escape — would still
// pass every table of `*` and `?` cases and would quietly stop matching the
// files a user named outright.
func TestAPatternOfLiteralsMatchesExactlyItself(t *testing.T) {
	t.Parallel()

	literal := rapid.StringOfN(rapid.RuneFrom([]rune("ab.\\-_")), 1, 3, -1)
	literalPath := rapid.Custom(func(t *rapid.T) string {
		return strings.Join(rapid.SliceOfN(literal, 1, 3).Draw(t, "elements"), "/")
	})

	rapid.Check(t, func(rt *rapid.T) {
		path := literalPath.Draw(rt, "path")
		other := literalPath.Draw(rt, "other")

		pattern := compiled(rt, path)
		if !pattern.Match(path) {
			rt.Fatalf("the literal pattern %q does not match itself", path)
		}
		if got := pattern.Match(other); got != (other == path) {
			rt.Fatalf("the literal pattern %q matches %q = %v", path, other, got)
		}
	})
}

// TestAWildcardNeverCrossesASeparator is what keeps `a/*` from meaning `a/**`.
//
// A single-element pattern can never match a multi-element path, whatever
// wildcards it holds, and a pattern of *n* elements with no `**` in it can
// match only paths of exactly *n*. Without it, `include = ["internal/*.go"]`
// would quietly select every file of every subpackage, and a scope somebody
// wrote to be narrow would be the widest one there is.
func TestAWildcardNeverCrossesASeparator(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		pattern := pathGen().Draw(rt, "pattern")
		path := pathGen().Draw(rt, "path")

		if strings.Contains(pattern, "**") {
			// Not this law's subject: a `**` element is the one thing that may
			// stand for a different number of elements than it is written as.
			// The generator draws `*` and `?` only, but an element of exactly
			// `**` is reachable from them.
			return
		}
		if !compiled(rt, pattern).Match(path) {
			return
		}
		if got, want := strings.Count(path, "/"), strings.Count(pattern, "/"); got != want {
			rt.Fatalf("%q matches %q, which has %d separators to the pattern's %d",
				pattern, path, got, want)
		}
	})
}

// TestCompilingTwiceAnswersTwice is the determinism a catalogue rests on.
//
// A mutant catalogue has to be a property of the pattern and the tree alone. If
// a compiled pattern carried state — a memo keyed on the last path, a cursor
// into the elements — two runs over one tree could select different files, and
// the digest a cache is keyed on would disagree with itself.
func TestCompilingTwiceAnswersTwice(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		pattern := pathGen().Draw(rt, "pattern")
		paths := rapid.SliceOfN(pathGen(), 1, 5).Draw(rt, "paths")

		first := compiled(rt, pattern)
		second := compiled(rt, pattern)
		for _, path := range paths {
			if first.Match(path) != second.Match(path) {
				rt.Fatalf("two compilations of %q disagree about %q", pattern, path)
			}
		}
		// And one compilation asked twice about one path, which is the other
		// way a memo could show itself.
		for _, path := range paths {
			once := first.Match(path)
			if again := first.Match(path); again != once {
				rt.Fatalf("%q answered %v about %q and then %v", pattern, once, path, again)
			}
		}
	})
}
