// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

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
	if !got.has("return-nil", "in", "nil") {
		t.Errorf("scan found %v, want return-nil to survive the unspellable element", got.rules())
	}
}

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
			if candidate.Guard.Probe != nil {
				t.Errorf("%s carries a return probe hint, which cannot be written for an "+
					"incomparable type", candidate.Rule.Name)
			}
		case "return-nil":
			if candidate.Guard.Probe == nil {
				t.Errorf("return-nil lost its probe hint, which is legal for a slice")
			}
		}
	}
}

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
