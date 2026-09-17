// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// How far a refusal travels, and from which arm of the walk.
//
// The walk emits candidates from a dozen places and every one of them returns
// its error rather than collecting it: an `ast.Inspect` that carried on past a
// span mismatch would go on proposing candidates against a file whose bytes it
// has just been told it cannot trust, and the catalogue that came out would be
// spans into a file nobody has. So each arm is reached on its own, by
// corrupting exactly the bytes that arm's candidate names and leaving every
// other candidate in the file intact.
//
// That is what makes the fixtures below look the way they do. A `switch` tag is
// an expression with no candidate of its own around it -- the walk has no case
// for a `switch` statement -- so an edit anchored there is the only candidate
// in the file, and corrupting it reaches exactly one emitter.

// corrupting returns the source with the first occurrence of one token replaced
// by the same number of bytes of something else.
//
// Same length on purpose: a shorter file makes every span past the edit reach
// off the end, which is a different refusal. What this produces is a file of
// the right size holding the wrong bytes, which is what a file edited between
// the read and the parse looks like.
func corrupting(t *testing.T, src, token string) []byte {
	t.Helper()

	index := strings.Index(src, token)
	if index < 0 {
		t.Fatalf("the fixture does not hold %q:\n%s", token, src)
	}
	out := []byte(src)
	for i := range token {
		out[index+i] = '~'
	}
	return out
}

// TestEveryEmittingArmOfTheWalkCarriesItsRefusalOut is one row per arm.
//
// The `says` column is what separates them: every refusal quotes the text the
// rule said it was replacing, so a row that reached the wrong arm fails on the
// quotation rather than passing quietly.
func TestEveryEmittingArmOfTheWalkCarriesItsRefusalOut(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		src     string
		corrupt string
	}{
		{
			name:    "a comparison",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tswitch a < b {\n\t}\n}\n",
			corrupt: "<",
		},
		{
			name:    "a connective",
			src:     "package pkg\n\nfunc probe(a, b bool) {\n\tswitch a && b {\n\t}\n}\n",
			corrupt: "&&",
		},
		{
			name:    "integer arithmetic",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tswitch a + b {\n\t}\n}\n",
			corrupt: "+",
		},
		{
			name:    "floating arithmetic",
			src:     "package pkg\n\nfunc probe(a, b float64) {\n\tswitch a + b {\n\t}\n}\n",
			corrupt: "+",
		},
		{
			name:    "a bitwise operator",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tswitch a & b {\n\t}\n}\n",
			corrupt: "&",
		},
		{
			name:    "a boolean literal",
			src:     "package pkg\n\nfunc probe() {\n\tswitch true {\n\t}\n}\n",
			corrupt: "true",
		},
		{
			name:    "an if condition",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tif a < b {\n\t\treturn\n\t}\n}\n",
			corrupt: "a < b",
		},
		{
			name:    "a loop condition",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tfor a < b {\n\t\tbreak\n\t}\n}\n",
			corrupt: "a < b",
		},
		{
			name:    "a compound assignment",
			src:     "package pkg\n\nfunc probe(n int) {\n\tn += 1\n\t_ = n\n}\n",
			corrupt: "+=",
		},
		{
			name:    "an increment",
			src:     "package pkg\n\nfunc probe(n int) {\n\tn++\n\t_ = n\n}\n",
			corrupt: "++",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			err := scanBytes(t, c.src, corrupting(t, c.src, c.corrupt))
			if err == nil {
				t.Fatalf("a scan over corrupted bytes succeeded:\n%s", c.src)
			}
			if code := CodeOf(err); code != CodeSpanMismatch {
				t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSpanMismatch)
			}
		})
	}
}

// TestEveryStatementShapedCandidateRefusesAFileThatGotShorter is the other half
// of the walk's emitters, reached the other way.
//
// A statement deletion names the whole statement as what it replaces, so it
// reads its own original out of the file rather than carrying a literal: there
// is no text for the bytes to disagree with, and corrupting them changes what
// the candidate says it replaces rather than making it wrong. What such a
// candidate can still meet is a file that got *shorter* between the read and
// the parse, which leaves the node starting inside the file and ending past its
// last byte -- and the answer has to be a refusal rather than a slice.
func TestEveryStatementShapedCandidateRefusesAFileThatGotShorter(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		// at is the text the statement begins with; the file is cut one byte
		// into it, so that everything before it is intact.
		at string
	}{
		{
			name: "an assignment",
			src:  "package pkg\n\nfunc probe(n int) {\n\tn = 1\n\t_ = n\n}\n",
			at:   "n = 1",
		},
		{
			name: "an increment",
			src:  "package pkg\n\nfunc probe(n int) {\n\tn++\n\t_ = n\n}\n",
			at:   "n++",
		},
		{
			name: "a call statement",
			src:  "package pkg\n\nfunc use() {}\n\nfunc probe() {\n\tuse()\n}\n",
			at:   "use()\n",
		},
		{
			// The label has to name something other than the nearest breakable
			// target, or the mutation and the source are the same program and
			// there is no candidate to refuse. A `break` inside the switch
			// leaves the switch; `break outer` leaves the loop.
			name: "a labelled branch",
			src: "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tswitch {\n" +
				"\t\tdefault:\n\t\t\tbreak outer\n\t\t}\n\t}\n}\n",
			at: "break outer",
		},
		{
			name: "a numeric result",
			src:  "package pkg\n\nfunc probe(n int) int {\n\treturn n + 1\n}\n",
			at:   "n + 1",
		},
		{
			name: "a boolean result",
			src:  "package pkg\n\nfunc probe(ok bool) bool {\n\treturn ok\n}\n",
			at:   "ok\n}",
		},
		{
			name: "a nillable result",
			src:  "package pkg\n\nfunc probe(xs []int) []int {\n\treturn xs\n}\n",
			at:   "xs\n}",
		},
		{
			name: "an error result",
			src:  "package pkg\n\nfunc probe(err error) error {\n\treturn err\n}\n",
			at:   "err\n}",
		},
		{
			name: "a string result",
			src:  "package pkg\n\nfunc probe(s string) string {\n\treturn s + \"x\"\n}\n",
			at:   "s + \"x\"",
		},
		{
			name: "a negation",
			src:  "package pkg\n\nfunc probe(a bool) {\n\tswitch !a {\n\t}\n}\n",
			at:   "!a",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			cut := strings.Index(c.src, c.at)
			if cut < 0 {
				t.Fatalf("the fixture does not hold %q:\n%s", c.at, c.src)
			}
			err := scanBytes(t, c.src, []byte(c.src[:cut+1]))
			if err == nil {
				t.Fatalf("a scan over a file that got shorter succeeded:\n%s", c.src)
			}
			if code := CodeOf(err); code != CodeSpanMismatch {
				t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSpanMismatch)
			}
			if !strings.Contains(err.Error(), "past the end of the file") {
				t.Errorf("the refusal %q does not say the span left the file", err)
			}
		})
	}
}

// TestANodeThatReachesPastTheEndOfTheFileIsRefused is the other half of the
// span check, and the half a statement-shaped candidate reaches.
//
// A statement deletion names the whole statement as what it replaces, so its
// span is the node's rather than one token's. A file that got shorter between
// the read and the parse leaves such a node ending past the last byte, and the
// refusal has to say so rather than slice.
func TestANodeThatReachesPastTheEndOfTheFileIsRefused(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\nfunc probe(n int) {\n\tn++\n\t_ = n\n}\n"
	// Everything up to and including the first byte of `n++`, so that the
	// statement starts inside the file and ends outside it.
	cut := strings.Index(src, "n++") + 1
	err := scanBytes(t, src, []byte(src[:cut]))
	if err == nil {
		t.Fatal("a scan over a file that got shorter succeeded")
	}
	if code := CodeOf(err); code != CodeSpanMismatch {
		t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSpanMismatch)
	}
	if !strings.Contains(err.Error(), "past the end of the file") {
		t.Errorf("the refusal %q does not say the span left the file", err)
	}
}

// TestARefusalInOneFileStopsThePackage is the last leg of the journey.
//
// A package is walked file by file and a package list package by package, and
// neither loop collects: a file whose bytes cannot be trusted makes the whole
// pass untrustworthy, so the first refusal is the run's answer. Carrying on
// would produce a catalogue that is partly a catalogue of a tree nobody has,
// with nothing in it to say which part.
func TestARefusalInOneFileStopsThePackage(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := root + "/pkg/widest.go"
	if err := writeSource(t, path, walkFixture); err != nil {
		t.Fatalf("writing the source: %v", err)
	}
	// The file on disk holds one byte sequence and the tree was parsed from
	// another, which is exactly what a file edited between the two looks like.
	// The walk reads the file itself, so this is the shape it will meet.
	if err := writeSource(t, path, string(corrupting(t, walkFixture, "<="))); err != nil {
		t.Fatalf("corrupting the source: %v", err)
	}

	d := newWalkDiscovery(t, root)
	loaded, pkg := loadedPackage(t, path, walkFixture)
	if err := CodeOf(d.pkg(loaded, pkg)); err != CodeSpanMismatch {
		t.Fatalf("walking the package answered %q, want %q", err, CodeSpanMismatch)
	}
	// And the same refusal through the package loop above it.
	d = newWalkDiscovery(t, root)
	loaded.packages = []*packages.Package{pkg}
	if err := CodeOf(d.run(t.Context(), loaded)); err != CodeSpanMismatch {
		t.Fatalf("running the discovery answered %q, want %q", err, CodeSpanMismatch)
	}
}
