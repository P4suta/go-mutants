// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The neutral-value family is the one rule in the catalogue whose replacement
// text is *computed from a type* rather than written as a constant beside the
// rule name, so these tests are about two separate things: which returns get a
// candidate at all, and what the candidate's replacement says. The second half
// matters more than it looks -- the replacement string is hashed into the
// mutant identity (internal/mutation/id.go), so a change in how a type renders
// reissues every mutant of this family.

// TestASliceReturnOffersBothNeutralValues is the shape the family exists for:
// `nil` and `[]T{}` are different programs that `len(x) == 0` cannot tell
// apart, so both have to be offered at the same return.
func TestASliceReturnOffersBothNeutralValues(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Names(in []string) []string {
	return in
}
`)
	if !got.has("return-nil", "in", "nil") {
		t.Errorf("scan found %v, want a return-nil candidate", got.rules())
	}
	if !got.has("return-empty-slice", "in", "[]string{}") {
		t.Errorf("scan found %v, want a return-empty-slice candidate", got.rules())
	}
}

// TestAMapReturnOffersTheEmptyMap is the same fact for the other half of the
// family, and it pins the rule name apart from the slice one: a survivor
// printed as `return-empty-slice` when the type is a map would be a lie in
// every console line that names it.
func TestAMapReturnOffersTheEmptyMap(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Counts(in map[string]int) map[string]int {
	return in
}
`)
	if !got.has("return-empty-map", "in", "map[string]int{}") {
		t.Errorf("scan found %v, want a return-empty-map candidate", got.rules())
	}
	for _, rule := range got.rules() {
		if rule == "return-empty-slice in->map[string]int{}" {
			t.Errorf("a map return produced a slice-named candidate: %v", got.rules())
		}
	}
}

// TestTheEmptyValueIsSpelledWithTheNameTheFileUses is why this rule reuses the
// guard resolver's speller rather than printing the type itself. A named slice
// type has to be written by its name -- `[]string{}` would compile at a `Lines`
// result but says something the source never said, and a *defined* type whose
// underlying type is unexported would not compile at all.
func TestTheEmptyValueIsSpelledWithTheNameTheFileUses(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

type Lines []string

func Body(in Lines) Lines {
	return in
}
`)
	if !got.has("return-empty-slice", "in", "Lines{}") {
		t.Errorf("scan found %v, want the empty value spelled Lines{}", got.rules())
	}
}

// TestTheEmptyValueQualifiesAnImportedElement is the same speller against the
// other half of its job: an element type from another package has to carry the
// name *this file* binds that package to, not the import path.
func TestTheEmptyValueQualifiesAnImportedElement(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

import bb "bytes"

func Buffers(in []bb.Buffer) []bb.Buffer {
	return in
}
`)
	if !got.has("return-empty-slice", "in", "[]bb.Buffer{}") {
		t.Errorf("scan found %v, want the element qualified as the file binds it", got.rules())
	}
}

// TestAnUnspellableElementIsRecordedRatherThanGuessed is the fail-closed half.
// A dot import binds the package's names without binding a name for the
// package, so `Buffer` is in scope but `[]Buffer{}` is not something the
// speller will claim -- and the honest answer is the same one Form D gives when
// it cannot name a declared type, which is a recorded skip and no candidate.
func TestAnUnspellableElementIsRecordedRatherThanGuessed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

import . "bytes"

func Buffers(in []Buffer) []Buffer {
	return in
}
`)
	for _, rule := range got.rules() {
		if len(rule) >= len("return-empty-slice") && rule[:len("return-empty-slice")] == "return-empty-slice" {
			t.Errorf("scan guessed a spelling for a type it cannot name: %v", got.rules())
		}
	}
	if !got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan recorded %v, want a %s site", got.skips(), SkipUnnameableDeclType)
	}
	// The other rule at the same return is unaffected: `nil` needs no spelling.
	if !got.has("return-nil", "in", "nil") {
		t.Errorf("scan found %v, want return-nil to survive the unspellable element", got.rules())
	}
}

// TestAReturnThatIsAlreadyEmptyProducesNothing is the first of the family's two
// silent refusals. `[]string{}` mutated to `[]string{}` is not a place
// go-mutants declined to mutate; it is a place where the mutation and the
// source are the same program, which is exactly the argument replaceReturn
// already makes for `return nil`.
func TestAReturnThatIsAlreadyEmptyProducesNothing(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"return []string{}",
		"return make([]string, 0)",
		"return make([]string, 0, 0)",
	} {
		got := scanSource(t, `package pkg

func Names() []string {
	`+source+`
}
`)
		for _, rule := range got.rules() {
			if len(rule) >= len("return-empty-slice") && rule[:len("return-empty-slice")] == "return-empty-slice" {
				t.Errorf("`%s` produced %v, want no empty-slice candidate", source, got.rules())
			}
		}
	}
}

// TestAReturnWithACapacityIsNotAlreadyEmpty draws the other side of that line.
// `make([]string, 0, 8)` and `[]string{}` differ in `cap`, which is observable,
// so this is a real mutant and refusing it would be a silent loss.
func TestAReturnWithACapacityIsNotAlreadyEmpty(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Names() []string {
	return make([]string, 0, 8)
}
`)
	if !got.has("return-empty-slice", "make([]string, 0, 8)", "[]string{}") {
		t.Errorf("scan found %v, want a candidate: a capacity is observable", got.rules())
	}
}

// TestANilReturnedBesideAnErrorProducesNothing is the second silent refusal,
// and the one that carries the family's weight. `if err != nil { return nil,
// err }` is the commonest `return nil` for a slice in Go, and by universal
// convention a caller that sees an error does not look at the other results --
// so the mutant is equivalent, and without this gate most of the family's
// output would be it. This is an argument from convention, not a proof, and
// docs/operators.md says so where the gate is documented.
func TestANilReturnedBesideAnErrorProducesNothing(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Read(err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	return []string{"ok"}, nil
}
`)
	for _, rule := range got.rules() {
		if rule == "return-empty-slice nil->[]string{}" {
			t.Errorf("scan mutated the error companion: %v", got.rules())
		}
	}
}

// TestASliceReturnedBesideANilErrorIsNotGated is the same gate's other
// direction, and it is the one the family is worth the most at: the success
// path, where a function that means `[]string{}` and writes `nil` is the actual
// bug this rule finds.
func TestASliceReturnedBesideANilErrorIsNotGated(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Read(in []string) ([]string, error) {
	return in, nil
}
`)
	if !got.has("return-empty-slice", "in", "[]string{}") {
		t.Errorf("scan found %v, want the success path mutated", got.rules())
	}
}

// TestOnlySlicesAndMapsHaveASecondNeutralValue is the line the family is drawn
// at: a type is in when it has two distinct neutral values that the standard
// library treats differently and that `len()` cannot separate. Every other
// nillable type keeps `return-nil` alone.
func TestOnlySlicesAndMapsHaveASecondNeutralValue(t *testing.T) {
	t.Parallel()

	for name, signature := range map[string]string{
		"pointer":   "*int",
		"channel":   "chan int",
		"function":  "func()",
		"interface": "interface{ Read() }",
	} {
		got := scanSource(t, `package pkg

func Value(in `+signature+`) `+signature+` {
	return in
}
`)
		if !got.has("return-nil", "in", "nil") {
			t.Errorf("%s: scan found %v, want return-nil", name, got.rules())
		}
		for _, rule := range got.rules() {
			if rule != "return-nil in->nil" {
				t.Errorf("%s: scan found %v, want return-nil alone", name, got.rules())
			}
		}
	}
}

// TestATypeParameterSlicedIsStillASlice is here because the type gate
// deliberately refuses a bare type parameter -- its type set may hold a slice
// and a map at once -- while `[]T` is a slice whatever T is. The distinction is
// worth pinning: refusing `[]T` would lose the rule inside every generic
// helper, and the spelling is the type parameter's own name, which is in scope
// exactly where the edit goes.
func TestATypeParameterSlicedIsStillASlice(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Values[T any](in []T) []T {
	return in
}
`)
	if !got.has("return-empty-slice", "in", "[]T{}") {
		t.Errorf("scan found %v, want a candidate spelled []T{}", got.rules())
	}
}

// TestABareTypeParameterOffersNoNeutralValue is that gate's other side. `T`
// alone may be instantiated as a slice, a map, a pointer or a struct, and
// `T{}` is not legal Go for any of them.
func TestABareTypeParameterOffersNoNeutralValue(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Value[T ~[]int](in T) T {
	return in
}
`)
	for _, rule := range got.rules() {
		if len(rule) >= len("return-empty") && rule[:len("return-empty")] == "return-empty" {
			t.Errorf("scan found %v, want no neutral value for a bare type parameter", got.rules())
		}
	}
}

// TestTheNeutralValueRulesCarryNoProbeHint pins a deliberate absence. The probe
// form for a return compares the returned value against the replacement, and a
// slice is not comparable -- `r0 != []string{}` is not legal Go. The
// `return-nil` beside it keeps its hint, because `r0 != nil` is legal for both.
func TestTheNeutralValueRulesCarryNoProbeHint(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Names(in []string) []string {
	return in
}
`)
	for _, candidate := range got.candidates {
		switch candidate.Rule.Name {
		case "return-empty-slice", "return-empty-map":
			if candidate.Guard.Return != nil {
				t.Errorf("%s carries a return probe hint, which cannot be written for an "+
					"incomparable type", candidate.Rule.Name)
			}
		case "return-nil":
			if candidate.Guard.Return == nil {
				t.Errorf("return-nil lost its probe hint, which is legal for a slice")
			}
		}
	}
}

// TestTheNeutralValueFamilyIsNotInTheBalancedProfile is the reason the family
// is its own family rather than two rules bolted onto return-replacement. Every
// function in a tree that returns a slice or a map gains a mutant here, most of
// which survive a suite that only ever asserts `len`, so the family is `strong`
// -- and a family's tier is the tier of every rule in it, which is why a new
// tier means a new family.
func TestTheNeutralValueFamilyIsNotInTheBalancedProfile(t *testing.T) {
	t.Parallel()

	found := map[string]mutation.Rule{}
	for _, rule := range SupportedRules() {
		switch rule.Name {
		case "return-empty-slice", "return-empty-map":
			found[rule.Name] = rule
		}
	}
	for _, name := range []string{"return-empty-slice", "return-empty-map"} {
		rule, ok := found[name]
		if !ok {
			t.Fatalf("SupportedRules() does not name %s", name)
		}
		if rule.Family != mutation.FamilyNeutralValue {
			t.Errorf("%s is in family %q, want %q", name, rule.Family, mutation.FamilyNeutralValue)
		}
		if rule.Tier != mutation.TierStrong {
			t.Errorf("%s is tier %q, want %q -- a balanced run must not gain these",
				name, rule.Tier, mutation.TierStrong)
		}
	}
}

// TestASelectionThatDoesNotNameTheRuleProducesNothing is the same fact from the
// walk's side: the rules are matched through the selection like every other, so
// a balanced profile reaches this file and emits nothing here.
func TestASelectionThatDoesNotNameTheRuleProducesNothing(t *testing.T) {
	t.Parallel()

	source := `package pkg

func Names(in []string) []string {
	return in
}
`
	balanced := scanWith(t, source, func(rule mutation.Rule) bool {
		return rule.Tier == mutation.TierBalanced
	})
	for _, rule := range balanced.rules() {
		if strings.HasPrefix(rule, "return-empty") {
			t.Errorf("a balanced selection produced %v", balanced.rules())
		}
	}
	if !balanced.has("return-nil", "in", "nil") {
		t.Errorf("a balanced selection found %v, want return-nil", balanced.rules())
	}
}
