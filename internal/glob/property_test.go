// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package glob_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/P4suta/go-mutants/internal/glob"
)

func elementGen() *rapid.Generator[string] {
	return rapid.StringOfN(rapid.RuneFrom([]rune("ab.*?")), 1, 3, -1)
}

func pathGen() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		return strings.Join(rapid.SliceOfN(elementGen(), 1, 4).Draw(t, "elements"), "/")
	})
}

func compiled(t *rapid.T, pattern string) glob.Pattern {
	p, err := glob.Compile(pattern)
	if err != nil {
		t.Fatalf("Compile(%q): %v", pattern, err)
	}
	return p
}

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
		twice := compiled(rt, "**/**/"+pattern)
		if got, want := twice.Match(path), prefixed.Match(path); got != want {
			rt.Fatalf("%q matches %q = %v and %q = %v", "**/**/"+pattern, path, got, "**/"+pattern, want)
		}
	})
}

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

func TestAWildcardNeverCrossesASeparator(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		pattern := pathGen().Draw(rt, "pattern")
		path := pathGen().Draw(rt, "path")

		if strings.Contains(pattern, "**") {
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
		for _, path := range paths {
			once := first.Match(path)
			if again := first.Match(path); again != once {
				rt.Fatalf("%q answered %v about %q and then %v", pattern, once, path, again)
			}
		}
	})
}
