// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"slices"
	"testing"
)

func TestMutationOutcomeFieldsNamesExactlyWhatEachOutcomeCarries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		outcome string
		want    []string
	}{
		{outcome: MutationOutcomeKilled, want: []string{"killed_by"}},
		{outcome: MutationOutcomeSurvived, want: []string{"exhausted", "finding"}},
		{outcome: MutationOutcomeUnreached, want: []string{"suite", "finding"}},
		{outcome: "retired"},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			t.Parallel()
			got := mutationOutcomeFields(test.outcome)
			if test.want == nil {
				if got != nil {
					t.Fatalf("mutationOutcomeFields(%q) = %v, want no shape at all", test.outcome, got)
				}
				return
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("mutationOutcomeFields(%q) = %v, want %v", test.outcome, got, test.want)
			}
		})
	}
}

func TestCompareTargetKeysOrdersByPackageThenNameThenKind(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		first  TargetKey
		second TargetKey
		want   int
	}{
		{name: "by package", first: TargetKey{Package: "a"}, second: TargetKey{Package: "b"}, want: -1},
		{name: "by package, reversed", first: TargetKey{Package: "b"}, second: TargetKey{Package: "a"}, want: 1},
		{
			name:  "by name within a package",
			first: TargetKey{Package: "p", Name: "a"}, second: TargetKey{Package: "p", Name: "b"}, want: -1,
		},
		{
			name:  "by name, reversed",
			first: TargetKey{Package: "p", Name: "b"}, second: TargetKey{Package: "p", Name: "a"}, want: 1,
		},
		{
			name:  "by kind within a name",
			first: TargetKey{Package: "p", Name: "n", Kind: "a"}, second: TargetKey{Package: "p", Name: "n", Kind: "b"}, want: -1,
		},
		{
			name:  "identical",
			first: TargetKey{Package: "p", Name: "n", Kind: "k"}, second: TargetKey{Package: "p", Name: "n", Kind: "k"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareTargetKeys(test.first, test.second); got != test.want {
				t.Fatalf("compareTargetKeys(%+v, %+v) = %d, want %d", test.first, test.second, got, test.want)
			}
		})
	}
}

func TestIsDigestAcceptsExactlyLowercaseHexOfASha256(t *testing.T) {
	t.Parallel()
	const width = 64
	pad := func(character rune) string {
		value := make([]rune, width)
		for index := range value {
			value[index] = '0'
		}
		value[width-1] = character
		return string(value)
	}
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "the first digit", value: pad('0'), want: true},
		{name: "the last digit", value: pad('9'), want: true},
		{name: "the first letter", value: pad('a'), want: true},
		{name: "the last letter", value: pad('f'), want: true},
		{name: "below the digits", value: pad('/')},
		{name: "above the digits", value: pad(':')},
		{name: "below the letters", value: pad('`')},
		{name: "above the letters", value: pad('g')},
		{name: "uppercase", value: pad('A')},
		{name: "too short", value: pad('0')[1:]},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := isDigest(test.value); got != test.want {
				t.Fatalf("isDigest(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}
