// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"
)

func boolProbeOf(t *testing.T, got scanned, rule string) *ProbeSite {
	t.Helper()

	for _, candidate := range got.candidates {
		if candidate.Rule.Name == rule {
			return candidate.Guard.Probe
		}
	}
	t.Fatalf("the scan found %v, and none of them is %s", got.rules(), rule)
	return nil
}

func TestAComparisonIsProbedWhereItStands(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Above reports whether a exceeds b.
func Above(a, b int) bool {
	if a > b {
		return true
	}
	return false
}
`)
	site := boolProbeOf(t, got, "gt-to-ge")
	if site == nil {
		t.Fatal("a comparison of two parameters is not probed")
	}
	if site.Form != ProbeFormBool {
		t.Errorf("form = %q, want %q", site.Form, ProbeFormBool)
	}
	if len(site.Types) != 0 {
		t.Errorf("Types = %q, and a boolean site's type is bool by construction", site.Types)
	}
}

func TestASiteWithAnEffectInItIsNotProbed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// count is the effect.
func count() int { return 1 }

// Above reports whether a exceeds a counter.
func Above(a int) bool {
	if a > count() {
		return true
	}
	return false
}
`)
	if site := boolProbeOf(t, got, "gt-to-ge"); site != nil {
		t.Errorf("probe site = %+v, and evaluating this site twice would call count() twice", site)
	}
}

func TestASiteAMutantCouldPanicInIsNotProbed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Box holds a number.
type Box struct{ n int }

// Positive reports whether the box holds a positive number.
func Positive(x *Box) bool {
	if x != nil && x.n > 0 {
		return true
	}
	return false
}
`)
	for _, rule := range []string{"and-to-or", "gt-to-ge"} {
		if site := boolProbeOf(t, got, rule); site != nil {
			t.Errorf("%s: probe site = %+v, and a reading of this site can dereference nil", rule, site)
		}
	}
	if site := boolProbeOf(t, got, "neq-to-eq"); site == nil {
		t.Error("the nil check is not probed, though neither reading of it dereferences anything")
	}
}

func TestAGuardedDereferenceIsProbedOnceItIsGuardedByAValue(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Positive reports whether both numbers are positive.
func Positive(a, b int) bool {
	if a > 0 && b > 0 {
		return true
	}
	return false
}
`)
	if site := boolProbeOf(t, got, "and-to-or"); site == nil {
		t.Error("a conjunction of two safe comparisons is not probed")
	}
}

func TestTheWholeConditionAndEachHalfAreTheirOwnSites(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Between reports whether v is inside the bounds.
func Between(v, lo, hi int) bool {
	if v > lo && v < hi {
		return true
	}
	return false
}
`)
	whole := boolProbeOf(t, got, "and-to-or")
	left := boolProbeOf(t, got, "gt-to-ge")
	right := boolProbeOf(t, got, "lt-to-le")
	if whole == nil || left == nil || right == nil {
		t.Fatalf("not every part is probed: whole=%v left=%v right=%v", whole, left, right)
	}
	if !whole.Span.Contains(left.Span) || !whole.Span.Contains(right.Span) {
		t.Errorf("the conjunction's site %s does not contain both halves %s and %s",
			whole.Span, left.Span, right.Span)
	}
	if left.Span == whole.Span || right.Span == whole.Span {
		t.Errorf("a half shares the whole's site: whole=%s left=%s right=%s",
			whole.Span, left.Span, right.Span)
	}
}

func TestANonBooleanSiteIsNotProbedByThisForm(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

// Flag is a named boolean type.
type Flag bool

// Check reports the flag a comparison yields.
func Check(a, b int) Flag {
	var f Flag = Flag(a > b)
	if f {
		return f
	}
	return f
}
`)
	for _, candidate := range got.candidates {
		site := candidate.Guard.Probe
		if site == nil || site.Form != ProbeFormBool {
			continue
		}
		if candidate.Guard.Form != GuardFormC {
			t.Errorf("%s has a boolean probe at a %s site, which the helper cannot take",
				candidate.Rule.Name, candidate.Guard.Form)
		}
	}
}
