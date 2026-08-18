// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package glob_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/glob"
)

// The reference matcher below is the specification written the obvious way:
// plain recursion, one decision per call, no table and no memo. It is
// deliberately the algorithm the package refuses to ship, because a reference
// that shared the dynamic program's structure would agree with it for the same
// wrong reasons. Fuzzing the two against each other is what turns "the DP is
// equivalent to the naive reading" from a comment into a checked claim.
//
// Being non-memoized, it is exponential on nested wildcards, which is why the
// fuzz target only consults it on small inputs.

// referenceMatch is the naive reading of Pattern.Match. The pattern is assumed
// to have already compiled, so only the path needs validating here.
func referenceMatch(pattern, path string) bool {
	pathElements := strings.Split(path, "/")
	for _, element := range pathElements {
		if element == "" {
			return false
		}
	}
	return referenceElements(strings.Split(pattern, "/"), pathElements)
}

// referenceElements walks the element lists, branching on "**" the obvious
// way: try consuming nothing, otherwise swallow one more path element.
func referenceElements(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		if referenceElements(pattern[1:], path) {
			return true
		}
		return len(path) > 0 && referenceElements(pattern, path[1:])
	}
	if len(path) == 0 {
		return false
	}
	if !referenceElement(pattern[0], path[0]) {
		return false
	}
	return referenceElements(pattern[1:], path[1:])
}

// referenceElement is the naive reading of one non-"**" element against one
// path element, byte by byte.
func referenceElement(pattern, element string) bool {
	if pattern == "" {
		return element == ""
	}
	switch pattern[0] {
	case '*':
		if referenceElement(pattern[1:], element) {
			return true
		}
		return element != "" && referenceElement(pattern, element[1:])
	case '?':
		return element != "" && referenceElement(pattern[1:], element[1:])
	default:
		return element != "" && element[0] == pattern[0] && referenceElement(pattern[1:], element[1:])
	}
}

// referenceIsAffordable keeps the exponential reference away from the inputs
// that would make it run for minutes. Every star in a pattern is a branch the
// reference re-explores whenever the answer is false, so the star budget
// matters more than the raw length.
func referenceIsAffordable(pattern, path string) bool {
	const (
		maxLength = 20
		maxStars  = 6
	)
	return len(pattern) <= maxLength &&
		len(path) <= maxLength &&
		strings.Count(pattern, "*") <= maxStars
}

// TestReferenceMatchesContractTable checks the checker. The reference is only
// worth fuzzing against if it independently reproduces the documented
// semantics, so it runs the same contract table the implementation does.
func TestReferenceMatchesContractTable(t *testing.T) {
	t.Parallel()
	for _, testCase := range matchCases {
		if got := referenceMatch(testCase.pattern, testCase.path); got != testCase.want {
			t.Errorf("referenceMatch(%q, %q) = %t, want %t (%s)",
				testCase.pattern, testCase.path, got, testCase.want, testCase.name)
		}
	}
}

// FuzzMatch asserts three things at once: Compile and Match never panic on
// arbitrary bytes, a rejected pattern always comes back as a *SyntaxError
// pointing inside the pattern, and an accepted pattern matches exactly what
// the naive reference says it should.
//
// The seed corpus is the f.Add set below: every row of the contract table,
// every rejected pattern, and a handful of shapes chosen to be awkward for a
// matcher rather than for a reader. Native Go fuzzing treats those seeds as
// the corpus, so there is no testdata/fuzz directory to keep in sync.
func FuzzMatch(f *testing.F) {
	for _, testCase := range matchCases {
		f.Add(testCase.pattern, testCase.path)
	}
	for _, testCase := range compileErrorCases {
		f.Add(testCase.pattern, "a/b.go")
	}

	extraSeeds := []struct{ pattern, path string }{
		{"**", "**"},
		{"*", "**"},
		{"**/**/*a", "b/b/b"},
		{"*a*a*b", "aaaaa"},
		{"?*?", "ab"},
		{"a/**/**/b", "a/b"},
		{"**/a", "a"},
		{"a/**", "a"},
		{"\x00", "\x00"},
		{"\xff", "\xff"},
		{"?", "\xc3\xa9"},
		{"*", "\xed\xa0\x80"},
		{"a\nb", "a\nb"},
		{"**/*", "a/b/c/d/e"},
		{" ", " "},
		{"a", ""},
	}
	for _, seed := range extraSeeds {
		f.Add(seed.pattern, seed.path)
	}

	f.Fuzz(func(t *testing.T, pattern, path string) {
		compiled, err := glob.Compile(pattern)
		if err != nil {
			var syntaxErr *glob.SyntaxError
			if !errors.As(err, &syntaxErr) {
				t.Fatalf("Compile(%q) returned %T, want *glob.SyntaxError", pattern, err)
			}
			if syntaxErr.Pattern != pattern {
				t.Fatalf("SyntaxError.Pattern = %q, want %q", syntaxErr.Pattern, pattern)
			}
			// The empty pattern has no byte to blame, so it points at the
			// column a first byte would have occupied.
			highest := len(pattern)
			if highest == 0 {
				highest = 1
			}
			if syntaxErr.Column < 1 || syntaxErr.Column > highest {
				t.Fatalf("Compile(%q) blamed column %d, want it within [1, %d]", pattern, syntaxErr.Column, highest)
			}
			if syntaxErr.Message == "" {
				t.Fatalf("Compile(%q) returned a SyntaxError with no message", pattern)
			}
			if compiled.String() != "" {
				t.Fatalf("Compile(%q) returned Pattern %q alongside its error", pattern, compiled.String())
			}
			return
		}

		if compiled.String() != pattern {
			t.Fatalf("Compile(%q).String() = %q, want the original text", pattern, compiled.String())
		}

		got := compiled.Match(path)
		if again := compiled.Match(path); again != got {
			t.Fatalf("Compile(%q).Match(%q) returned %t then %t; matching must be pure", pattern, path, got, again)
		}

		if !referenceIsAffordable(pattern, path) {
			return
		}
		if want := referenceMatch(pattern, path); got != want {
			t.Fatalf("Compile(%q).Match(%q) = %t, reference says %t", pattern, path, got, want)
		}
	})
}
