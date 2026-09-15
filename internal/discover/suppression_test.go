// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Which reason a position is off limits for, when more than one says so.
//
// A boolean literal inside an array length inside a type parameter list is
// covered by three regions at once, and the answer has to be the outermost:
// that is the region a walker would have refused to descend into, and the
// reason that remains true whatever the narrower construct inside it turns out
// to be. Telling a user their `true` was skipped because array lengths are
// constant, when the whole type parameter list is not value code, is a true
// sentence about the wrong subject.
//
// Both the order and the tie-break are frozen rather than incidental: the same
// bytes scanned twice have to produce the same skip counts, and two regions
// covering exactly the same span have to resolve the same way every time.

// region builds one suppression, so that a table reads as coordinates.
func region(start, end int, reason SkipReason) suppression {
	return suppression{start: token.Pos(start), end: token.Pos(end), reason: reason}
}

// TestTheWidestRegionCoveringAPositionIsTheOneReported pins
// [fileScan.suppressed] and the tie-break under it.
func TestTheWidestRegionCoveringAPositionIsTheOneReported(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		regions []suppression
		pos     int
		want    SkipReason
	}{
		{
			name:    "one region",
			regions: []suppression{region(10, 20, SkipConstDecl)},
			pos:     12, want: SkipConstDecl,
		},
		{
			name: "the outer of two nested ones",
			regions: []suppression{
				region(10, 20, SkipArrayLength),
				region(5, 40, SkipTypeParam),
			},
			pos: 12, want: SkipTypeParam,
		},
		{
			name: "the outer of two, whichever order they arrive in",
			regions: []suppression{
				region(5, 40, SkipTypeParam),
				region(10, 20, SkipArrayLength),
			},
			pos: 12, want: SkipTypeParam,
		},
		{
			// The start is inside and the end is not: a region covers
			// [start, end), so a position at the end byte belongs to whatever
			// comes after it.
			name:    "a position at the end byte",
			regions: []suppression{region(10, 20, SkipConstDecl)},
			pos:     20, want: "",
		},
		{
			name:    "a position at the start byte",
			regions: []suppression{region(10, 20, SkipConstDecl)},
			pos:     10, want: SkipConstDecl,
		},
		{
			name:    "a position before every region",
			regions: []suppression{region(10, 20, SkipConstDecl)},
			pos:     9, want: "",
		},
		{
			name:    "no regions at all",
			regions: nil,
			pos:     12, want: "",
		},
		{
			// Two regions of one width covering one position, differing only
			// in where they start. The earlier start wins, and the rule exists
			// so that the answer does not depend on the order the collecting
			// walk reached them in.
			name: "two of one width",
			regions: []suppression{
				region(11, 21, SkipArrayLength),
				region(10, 20, SkipConstDecl),
			},
			pos: 15, want: SkipConstDecl,
		},
		{
			// And two covering exactly the same bytes, which the collecting
			// walk really does produce: the lower-ranked reason wins, which is
			// the order AllSkipReasons declares.
			name: "two over one span",
			regions: []suppression{
				region(10, 20, SkipTypeParam),
				region(10, 20, SkipConstDecl),
			},
			pos: 15, want: lowerRanked(SkipTypeParam, SkipConstDecl),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			s := &fileScan{suppressions: c.regions}
			reason, ok := s.suppressed(token.Pos(c.pos))
			if ok != (c.want != "") {
				t.Fatalf("suppressed(%d) = (%q, %v), want %q", c.pos, reason, ok, c.want)
			}
			if reason != c.want {
				t.Errorf("suppressed(%d) = %q, want %q", c.pos, reason, c.want)
			}
		})
	}
}

// lowerRanked is whichever of two reasons AllSkipReasons declares first, which
// is the tie-break the table above asserts without restating the order.
func lowerRanked(a, b SkipReason) SkipReason {
	if reasonRank[a] <= reasonRank[b] {
		return a
	}
	return b
}

// TestEveryKeyOfTheWiderTieBreakDecidesSomething separates the three keys, each
// with a pair that agrees on every key before it.
//
// It is a total order on purpose. Two regions this comparison called equal
// would be resolved by whichever the loop reached first, and the loop's order
// is the collecting walk's -- which is a fact about go/ast rather than about
// this file.
func TestEveryKeyOfTheWiderTieBreakDecidesSomething(t *testing.T) {
	t.Parallel()

	base := region(10, 20, SkipTypeParam)
	for _, c := range []struct {
		key    string
		narrow suppression
	}{
		{key: "the width", narrow: region(12, 18, SkipTypeParam)},
		{key: "the start", narrow: region(11, 21, SkipTypeParam)},
		{key: "the reason", narrow: region(10, 20, higherRanked(SkipTypeParam))},
	} {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()

			if !wider(base, c.narrow) {
				t.Errorf("wider(%v, %v) = false, want true on %s", base, c.narrow, c.key)
			}
			if wider(c.narrow, base) {
				t.Errorf("wider is not antisymmetric on %s", c.key)
			}
		})
	}

	if wider(base, base) {
		t.Error("wider(x, x) = true, and a region is not wider than itself")
	}
}

// higherRanked is a reason AllSkipReasons declares after the given one.
func higherRanked(after SkipReason) SkipReason {
	best := after
	for _, reason := range AllSkipReasons() {
		if reasonRank[reason] > reasonRank[after] && (best == after || reasonRank[reason] < reasonRank[best]) {
			best = reason
		}
	}
	return best
}

// TestSuppressionsComeOutInOneOrderWhateverOrderTheyWereFound is the sort, one
// key at a time.
//
// The regions are collected by an `ast.Inspect`, which visits in an order that
// is a fact about go/ast; the catalogue that comes out of the walk has to be a
// fact about the file. So the list is sorted, and each key below is separated
// by a pair agreeing on every key before it: outermost first, which is start
// ascending and then end *descending*, and the declared rank last.
func TestSuppressionsComeOutInOneOrderWhateverOrderTheyWereFound(t *testing.T) {
	t.Parallel()

	wide := SkipTypeParam
	narrow := higherRanked(wide)
	unsorted := []suppression{
		region(10, 20, narrow),
		region(10, 20, wide),
		region(10, 30, wide),
		region(5, 40, wide),
	}
	want := []suppression{
		region(5, 40, wide),
		region(10, 30, wide),
		region(10, 20, wide),
		region(10, 20, narrow),
	}

	got := slices.Clone(unsorted)
	sortSuppressions(got)
	if render(got) != render(want) {
		t.Errorf("sorted to\n%s\nwant\n%s", render(got), render(want))
	}

	// And the same list handed over backwards sorts to the same thing, which
	// is the property the sort exists for.
	backwards := slices.Clone(unsorted)
	slices.Reverse(backwards)
	sortSuppressions(backwards)
	if render(backwards) != render(want) {
		t.Errorf("the reversed list sorted to\n%s\nwant\n%s", render(backwards), render(want))
	}
}

// render writes a suppression list for a failure message.
func render(list []suppression) string {
	var parts []string
	for _, one := range list {
		parts = append(parts, strconv.Itoa(int(one.start))+".."+strconv.Itoa(int(one.end))+" "+string(one.reason))
	}
	return strings.Join(parts, "\n")
}

// TestAGeneratedFileSaysSoBeforeItsPackageClause pins [isGenerated], and both
// halves of the convention it implements.
//
// https://go.dev/s/generatedcode fixes the line exactly -- anchored, with that
// trailing full stop -- and requires it to appear *before* the package clause.
// Both halves matter here rather than in a standard-library helper, because
// "generated" is a skip reason this package reports and has to keep reporting
// the same way: a file mistaken for generated is a file nobody mutates and
// nobody is told about, and one mistaken for handwritten is a catalogue of
// mutants in a file a tool will overwrite.
func TestAGeneratedFileSaysSoBeforeItsPackageClause(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "the marker",
			src:  "// Code generated by hand. DO NOT EDIT.\n\npackage pkg\n",
			want: true,
		},
		{
			name: "the marker under a licence header",
			src:  "// SPDX-License-Identifier: MIT\n\n// Code generated by hand. DO NOT EDIT.\n\npackage pkg\n",
			want: true,
		},
		{
			name: "the marker with no tool named",
			src:  "// Code generated  DO NOT EDIT.\n\npackage pkg\n",
			want: true,
		},
		{
			// Past the package clause it is a comment about the file rather
			// than a declaration by its generator, which is the half a search
			// for the text alone would get wrong.
			name: "the marker after the package clause",
			src:  "package pkg\n\n// Code generated by hand. DO NOT EDIT.\n",
		},
		{
			name: "the marker in a doc comment of a declaration",
			src:  "package pkg\n\n// Code generated by hand. DO NOT EDIT.\nfunc Widest() {}\n",
		},
		{name: "no comment at all", src: "package pkg\n"},
		{name: "an ordinary comment", src: "// Package pkg does things.\npackage pkg\n"},
		{
			name: "the marker without its full stop",
			src:  "// Code generated by hand. DO NOT EDIT\n\npackage pkg\n",
		},
		{
			name: "the marker with something after it on the line",
			src:  "// Code generated by hand. DO NOT EDIT. really\n\npackage pkg\n",
		},
		{
			name: "the marker indented",
			src:  "//  Code generated by hand. DO NOT EDIT.\n\npackage pkg\n",
		},
		{
			name: "the marker in a block comment",
			src:  "/* Code generated by hand. DO NOT EDIT. */\n\npackage pkg\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := isGenerated(parseProbe(t, c.src)); got != c.want {
				t.Errorf("isGenerated = %v, want %v for:\n%s", got, c.want, c.src)
			}
		})
	}
}

// TestWhichRegionsOfAFileHoldNoMutableExpression pins [collectSuppressions],
// one construct at a time.
//
// The regions are collected rather than enforced during the emitting walk
// because two of them cover only *part* of a node -- an array's length but not
// its element type, an explicit type argument but not the expression it indexes
// -- and a walk that had to remember which child slot it was in would be one
// `switch` away from silently mutating a type.
func TestWhichRegionsOfAFileHoldNoMutableExpression(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		// inside is a piece of the source every collected region must cover.
		inside string
		reason SkipReason
	}{
		{
			name:   "a package-level variable initialiser",
			src:    "package pkg\n\nvar total = 1 + 2\n",
			inside: "1 + 2", reason: SkipPackageVarInit,
		},
		{
			name:   "a constant declaration",
			src:    "package pkg\n\nconst limit = 1 + 2\n",
			inside: "1 + 2", reason: SkipConstDecl,
		},
		{
			name:   "a constant declaration in a function body",
			src:    "package pkg\n\nfunc probe() {\n\tconst limit = 1 + 2\n\t_ = limit\n}\n",
			inside: "1 + 2", reason: SkipConstDecl,
		},
		{
			name:   "an array length",
			src:    "package pkg\n\nfunc probe() {\n\tvar xs [1 + 2]int\n\t_ = xs\n}\n",
			inside: "1 + 2", reason: SkipArrayLength,
		},
		{
			name:   "a function's type parameters",
			src:    "package pkg\n\nfunc probe[T any](v T) T { return v }\n",
			inside: "T any", reason: SkipTypeParam,
		},
		{
			name:   "a type's type parameters",
			src:    "package pkg\n\ntype box[T any] struct{ v T }\n",
			inside: "T any", reason: SkipTypeParam,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			p := parsedAt(t, "scan.go", c.src)
			regions := collectSuppressions(p.file, p.info)
			start := strings.Index(c.src, c.inside)
			if start < 0 {
				t.Fatalf("the fixture does not hold %q", c.inside)
			}
			covered := false
			for _, one := range regions {
				if one.reason != c.reason {
					continue
				}
				from := p.fset.Position(one.start).Offset
				to := p.fset.Position(one.end).Offset
				if from <= start && to >= start+len(c.inside) {
					covered = true
				}
			}
			if !covered {
				t.Errorf("no %s region covers %q; the regions are\n%s",
					c.reason, c.inside, renderRegions(p, regions))
			}
		})
	}

	// A `switch` suppresses neither kind of label any more, which is the
	// blanket that used to hide a whole family of sites. Both kinds are here so
	// that reinstating either is a failure rather than a quiet narrowing.
	for _, c := range []struct {
		name string
		src  string
	}{
		{
			name: "a tagless switch's labels",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tswitch {\n\tcase a < b:\n\t}\n}\n",
		},
		{
			name: "a tagged switch's labels",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tswitch a {\n\tcase b + 1:\n\t}\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			p := parsedAt(t, "scan.go", c.src)
			for _, one := range collectSuppressions(p.file, p.info) {
				t.Errorf("a %s region covers a case label: %s", one.reason, renderRegions(p, []suppression{one}))
			}
		})
	}
}

// renderRegions writes a region list with source offsets, for a failure
// message that can be read against the fixture.
func renderRegions(p parsed, regions []suppression) string {
	var lines []string
	for _, one := range regions {
		lines = append(lines, strconv.Itoa(p.fset.Position(one.start).Offset)+".."+
			strconv.Itoa(p.fset.Position(one.end).Offset)+" "+string(one.reason))
	}
	if len(lines) == 0 {
		return "  (none)"
	}
	return "  " + strings.Join(lines, "\n  ")
}
