// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import "testing"

// TestReuseMatchCrossesASeparatorOnlyForTheDoubleStar pins the semantics this
// matcher was written to have, and names where they came from.
//
// The row that matters is the deep one. [path.Match], which this replaced, says
// false there while `reuse lint` calls the same project compliant -- and
// because the shallow row is true either way, the annotation still covers
// something and is never reported as stale. The only symptom was one file
// reported as carrying no header, which is a true sentence about the wrong
// subject.
//
// The expectations are the reference implementation's answers, observed against
// reuse 3.3 in both directions: `data/**` compliant with `data/deep/nested.txt`
// present, `data/*` naming that file under MISSING COPYRIGHT AND LICENSING
// INFORMATION.
func TestReuseMatchCrossesASeparatorOnlyForTheDoubleStar(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"a double star takes one level", "data/**", "data/shallow.txt", true},
		{"a double star takes two", "data/**", "data/deep/nested.txt", true},
		{"a double star takes many", "data/**", "data/a/b/c/d.txt", true},
		{"a single star takes one level", "data/*", "data/shallow.txt", true},
		{"a single star stops at the separator", "data/*", "data/deep/nested.txt", false},
		{"a literal path is itself", "go.mod", "go.mod", true},
		{"a literal path is not a prefix", "go.mod", "go.modules", false},
		{"a pattern is anchored at the front", "data/*", "other/data/x.txt", false},
		{"a star inside a name stays inside it", "data/*.json", "data/a.json", true},
		{"a star inside a name does not descend", "data/*.json", "data/deep/a.json", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReuseMatch(test.pattern, test.path)
			if err != nil {
				t.Fatalf("ReuseMatch(%q, %q): %v", test.pattern, test.path, err)
			}
			if got != test.want {
				t.Errorf("ReuseMatch(%q, %q) = %t, want %t", test.pattern, test.path, got, test.want)
			}
		})
	}
}

// TestReuseMatchRefusesAWildcardItDoesNotImplement is the fail-closed half.
//
// `?` and a character class are shapes [path.Match] accepts and nothing here
// has been shown REUSE's answer for. Returning false for them would be the same
// mistake in a new place: a pattern that covers a file, read as one that does
// not, reported as a missing header.
func TestReuseMatchRefusesAWildcardItDoesNotImplement(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"data/?.txt", "data/[ab].txt", "data/x].txt"} {
		if _, err := ReuseMatch(pattern, "data/a.txt"); err == nil {
			t.Errorf("ReuseMatch accepted %q, and nothing here knows what REUSE does with it", pattern)
		}
	}
}

// TestReuseCoveringNamesEveryAnnotationThatClaimsAFile pins the shape rather
// than a bug.
//
// A first-match version was tried against the real manifest with a wide glob
// beside a narrow path the glob already covers -- the shape a manifest merged
// from two products takes -- and the gate stayed green, because the staleness
// verdict is re-derived by [ReuseAnnotationCoversATrackedFile] and never rested
// on this. So this is not a regression test for something that broke; it is the
// list being a list, so that a caller cannot quietly answer for one pattern
// when it was asked about four.
func TestReuseCoveringNamesEveryAnnotationThatClaimsAFile(t *testing.T) {
	t.Parallel()

	patterns := []string{"go.mod", "data/**", "go.mod", "other/*"}
	got, err := ReuseCovering(patterns, "go.mod")
	if err != nil {
		t.Fatalf("ReuseCovering: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ReuseCovering named %q; two of the four patterns claim go.mod", got)
	}

	if got, err = ReuseCovering(patterns, "data/deep/a.txt"); err != nil {
		t.Fatalf("ReuseCovering: %v", err)
	}
	if len(got) != 1 || got[0] != "data/**" {
		t.Errorf("ReuseCovering named %q for a file one pattern claims", got)
	}

	if got, err = ReuseCovering(patterns, "nothing/claims.me"); err != nil {
		t.Fatalf("ReuseCovering: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReuseCovering named %q for a file no pattern claims", got)
	}
}

// TestReuseCoveringRefusesRatherThanSkippingAPatternItCannotRead is the
// fail-closed half.
//
// Returning the patterns it could read and dropping the one it could not would
// be the worst of the three answers: the file looks covered, the unreadable
// entry looks stale, and neither report names the pattern that caused either.
func TestReuseCoveringRefusesRatherThanSkippingAPatternItCannotRead(t *testing.T) {
	t.Parallel()

	patterns := []string{"go.mod", "data/[ab].txt"}
	if _, err := ReuseCovering(patterns, "go.mod"); err == nil {
		t.Error("ReuseCovering answered over a pattern it cannot read")
	}
}

// TestAnAnnotationOfAFileThatCarriesAHeaderStillCoversSomething is the property
// the gate's apparently redundant `||` exists for.
//
// The coverage pass skips a file that carries an inline header, so it never
// writes down that an annotation also claims it. An annotation claiming only
// such files is therefore absent from that bookkeeping while being perfectly
// live, and a staleness check reading the bookkeeping alone accuses it of
// matching nothing. Observed, with REUSE.toml annotating `internal/cache/key.go`:
//
//	REUSE.toml annotates "internal/cache/key.go", which no committed file matches
//
// Nothing about that sentence is a clue to what is wrong, which is what makes
// this worth a test rather than a comment.
func TestAnAnnotationOfAFileThatCarriesAHeaderStillCoversSomething(t *testing.T) {
	t.Parallel()

	tracked := []string{"internal/cache/key.go", "go.mod"}
	live, err := ReuseAnnotationCoversATrackedFile("internal/cache/key.go", tracked)
	if err != nil {
		t.Fatalf("ReuseAnnotationCoversATrackedFile: %v", err)
	}
	if !live {
		t.Error("an annotation of a committed file was read as covering nothing")
	}

	if live, err = ReuseAnnotationCoversATrackedFile("internal/cache/gone.go", tracked); err != nil {
		t.Fatalf("ReuseAnnotationCoversATrackedFile: %v", err)
	}
	if live {
		t.Error("an annotation of a path this tree does not hold was read as live")
	}

	if _, err = ReuseAnnotationCoversATrackedFile("internal/[ab].go", tracked); err == nil {
		t.Error("a pattern this matcher cannot read was answered for rather than refused")
	}
}
