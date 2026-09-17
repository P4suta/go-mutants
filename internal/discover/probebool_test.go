// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"
)

// The boolean probe form measures a Form C site where it stands, by evaluating
// both readings of it and yielding the original's. That is what makes its
// conditions different in kind from the return form's: the mutant there *skips*
// an operand, so only that operand has to be inert, while here both readings
// run and the whole site has to be.
//
// The second half of that is the one a reader would miss, and it is what these
// tests are mostly about: a mutant may evaluate operands the original
// short-circuited past, so a site is only probeable when *every* operand in it
// is safe to evaluate — whichever way the operators are arranged.

// boolProbeOf returns the probe hint of the one candidate of a rule.
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

// TestAComparisonIsProbedWhereItStands is the ordinary case, and most of what a
// run catalogues.
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

// TestASiteWithAnEffectInItIsNotProbed is the first condition, and the reason
// it is asked of the whole site.
//
// Both readings are evaluated, so a call anywhere in the expression would be
// made twice — and a probe tree that called a function twice would not be the
// program it claims to be running.
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

// TestASiteAMutantCouldPanicInIsNotProbed is the condition a reader would miss,
// and the one that makes short-circuiting safe.
//
// `x != nil && x.n > 0` never dereferences a nil pointer: the left operand
// guards the right. Its `and-to-or` mutant is `x != nil || x.n > 0`, which
// reads `x.n` exactly when x is nil. The probe evaluates both readings, so it
// would panic where the original never could — and the mutant's own execution
// would panic too, which is a difference the run finds out by running it rather
// than one a probe may quietly record as "no infection".
//
// Nothing here asks about short-circuiting at all. [guardResolver.panicFree]
// walks the whole expression and refuses a field reached through a pointer, so
// every rearrangement of the operators is covered by one question asked once.
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
	// The nil check itself is probed, and the contrast is the whole reason the
	// condition is about a *site* rather than about a statement. `x != nil` is
	// its own Form C site; both readings of it compare a pointer with nil and
	// neither touches what it points at, so evaluating it twice is exactly as
	// safe as evaluating it once. What the refusals above have in common is not
	// the `&&` — it is that the site's own bytes hold a dereference.
	if site := boolProbeOf(t, got, "neq-to-eq"); site == nil {
		t.Error("the nil check is not probed, though neither reading of it dereferences anything")
	}
}

// TestAGuardedDereferenceIsProbedOnceItIsGuardedByAValue is the same shape with
// the hazard removed, so that the refusal above is about the dereference rather
// than about the `&&`.
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

// TestTheWholeConditionAndEachHalfAreTheirOwnSites is what the nesting rests
// on: an `&&` is a site and so is each comparison inside it, so the rewrite has
// to compose one inside the other.
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

// TestANonBooleanSiteIsNotProbedByThisForm keeps the helper's signature
// honest: it takes `bool`, so only the universe bool may reach it.
//
// A named boolean type is a Form C' site rather than a Form C one, and passing
// one to a `func(uint32, bool, bool) bool` would not compile. The guard form is
// what decides, which is why nothing here re-derives the type.
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
