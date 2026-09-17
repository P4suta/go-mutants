// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import "testing"

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

func TestReuseMatchRefusesAWildcardItDoesNotImplement(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"data/?.txt", "data/[ab].txt", "data/x].txt"} {
		if _, err := ReuseMatch(pattern, "data/a.txt"); err == nil {
			t.Errorf("ReuseMatch accepted %q, and nothing here knows what REUSE does with it", pattern)
		}
	}
}

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

func TestReuseCoveringRefusesRatherThanSkippingAPatternItCannotRead(t *testing.T) {
	t.Parallel()

	patterns := []string{"go.mod", "data/[ab].txt"}
	if _, err := ReuseCovering(patterns, "go.mod"); err == nil {
		t.Error("ReuseCovering answered over a pattern it cannot read")
	}
}

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
