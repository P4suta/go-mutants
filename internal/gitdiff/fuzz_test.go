// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"errors"
	"testing"
)

func FuzzParseDiff(f *testing.F) {
	f.Add("diff --git a/a.go b/a.go\n@@ -1,0 +2,3 @@\n", "")
	f.Add("diff --git a/a.go b/a.go\n@@ -1 +1 @@\n", "")
	f.Add("diff --git a/x/a.go b/x/a.go\n@@ -0,0 +1,2 @@\n", "x/")
	f.Add("", "")
	f.Add("\n", "")
	f.Add("@@ -1 +1 @@\n", "")
	f.Add("diff --git a/a.go b/a.go\n", "")
	f.Add("diff --git a/a.go b/a.go\n@@\n", "")
	f.Add("diff --git a/a.go b/a.go\n@@ -1 +0,0 @@\n", "")
	f.Add("diff --git a/a.go b/a.go\n@@ -1 +-1 @@\n", "")
	f.Add("diff --git a/a.go b/a.go\n@@ -1 +99999999999999999999 @@\n", "")
	f.Add("diff --git \"a/a\\nb.go\" \"b/a\\nb.go\"\n@@ -1 +1 @@\n", "")
	f.Add("diff --git a/a.go b/b.go\n@@ -1 +1 @@\n", "")
	f.Add("diff --git a/a.go b/a.go\n@@ -1 +1 @@\n@@ -2 +2 @@\n", "")
	f.Add("diff --git a/a.go b/a.go\n@@ -1 +1 @@\ndiff --git a/b.go b/b.go\n", "")
	f.Add("\x00\x00\x00", "")
	f.Add("diff --git a/\xff.go b/\xff.go\n@@ -1 +1 @@\n", "")

	f.Fuzz(func(t *testing.T, diff, prefix string) {
		byPath, err := parseDiff(diff, prefix)
		if err != nil {
			var refusal *Error
			if !errors.As(err, &refusal) {
				t.Fatalf("parseDiff returned %T, want an *Error a caller can branch on: %v", err, err)
			}
			if refusal.Code == "" {
				t.Fatalf("parseDiff refused with no code")
			}
			if byPath != nil {
				t.Fatalf("parseDiff refused and returned %d paths anyway", len(byPath))
			}
			return
		}
		for path, ranges := range byPath {
			if path == "" {
				t.Fatalf("parseDiff produced a range set for the empty path")
			}
			for _, r := range ranges {
				if r.First < 1 {
					t.Fatalf("%s: a range starts at line %d, and a file's first line is 1", path, r.First)
				}
				if r.First > r.Last {
					t.Fatalf("%s: a range runs from %d to %d, which selects nothing", path, r.First, r.Last)
				}
			}
			merged := Merge(ranges)
			for i, r := range merged {
				if r.First > r.Last {
					t.Fatalf("%s: Merge produced %d..%d", path, r.First, r.Last)
				}
				if i > 0 && merged[i-1].Last >= r.First {
					t.Fatalf("%s: Merge left %d..%d and %d..%d overlapping or unordered",
						path, merged[i-1].First, merged[i-1].Last, r.First, r.Last)
				}
			}
		}
	})
}
