// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testargs_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/testargs"
)

func TestNormalizeCanonicalizesShortClonesAndPreservesCustomFlags(t *testing.T) {
	t.Parallel()
	input := []string{"-short", "--short=false", "--test.short", "--test.parallel=3", "-custom=value"}
	got, err := testargs.Normalize(input)
	if err != nil || !slices.Equal(got, []string{"-test.short=true", "-test.short=false", "-test.short=true", "-test.parallel=3", "-custom=value"}) {
		t.Fatalf("Normalize = %v, %v", got, err)
	}
	input[0] = "changed"
	if got[0] != "-test.short=true" {
		t.Fatal("Normalize aliases its input")
	}
}

func TestNormalizeRejectsEveryAssuranceOwnedFlag(t *testing.T) {
	t.Parallel()
	for _, argument := range []string{
		"-test.run=TestOther", "--test.run=TestOther", "-test.fuzz", "-test.fuzztime=1x", "-test.fuzzcachedir=tmp",
		"-test.coverprofile=other", "-test.timeout=0", "-test.count=9", "-test.v=true", "-test.skip=Slow", "-test.list=.", "-test.shuffle=on",
	} {
		if _, err := testargs.Normalize([]string{argument}); err == nil || !strings.Contains(err.Error(), "assurance-owned") {
			t.Errorf("Normalize(%q) error = %v", argument, err)
		}
	}
}

// TestNormalizeAnswersEverySpellingItsSwitchNames walks the arms of the switch
// one at a time, with the exact argument each one exists to recognise.
//
// The two tests above are about the shapes a user writes; this one is about the
// shapes the code distinguishes, and they are not the same set. Every arm here
// was reachable and unreached: the `-short` clones were covered by two of their
// four spellings, the already-canonical `-test.short=` and `-test.parallel`
// pass-throughs by none, and an arm nothing names is an arm whose condition can
// be inverted without any test noticing.
//
// A pass-through is asserted as identity rather than as "no error", because
// that is the whole of what those arms do: they recognise an argument in order
// to leave it alone, and a test that only checked the error would pass just as
// happily if the argument were rewritten.
func TestNormalizeAnswersEverySpellingItsSwitchNames(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		given string
		want  string
	}{
		{"bare -short", "-short", "-test.short=true"},
		{"bare --short", "--short", "-test.short=true"},
		{"bare -test.short", "-test.short", "-test.short=true"},
		{"bare --test.short", "--test.short", "-test.short=true"},
		{"-short with a value", "-short=false", "-test.short=false"},
		{"--short with a value", "--short=true", "-test.short=true"},
		{"-test.short is already canonical", "-test.short=false", "-test.short=false"},
		{"--test.short loses one dash", "--test.short=true", "-test.short=true"},
		{"bare --test.parallel loses one dash", "--test.parallel", "-test.parallel"},
		{"--test.parallel with a value", "--test.parallel=4", "-test.parallel=4"},
		{"bare -test.parallel is already canonical", "-test.parallel", "-test.parallel"},
		{"-test.parallel with a value is already canonical", "-test.parallel=4", "-test.parallel=4"},
		{"an argument the assurance run does not own", "-custom=value", "-custom=value"},
		{"a bare word", "positional", "positional"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := testargs.Normalize([]string{test.given})
			if err != nil {
				t.Fatalf("Normalize(%q) = _, %v", test.given, err)
			}
			if !slices.Equal(got, []string{test.want}) {
				t.Errorf("Normalize(%q) = %v, want %v", test.given, got, []string{test.want})
			}
		})
	}
}

// TestNormalizeRefusesAnAssuranceOwnedFlagInEitherSpelling is the refusing arm
// from the side the table above cannot reach: it is the only arm that returns
// rather than continuing, and the only one that cares which of the two `-test.`
// prefixes it saw.
//
// The test beside it covers a dozen flag names and all but two of them through
// the single-dash spelling, so the double-dash half of that condition was being
// carried by one case. Both halves are named here, against the same flag, so
// that the pair is the subject rather than a coincidence of the list.
func TestNormalizeRefusesAnAssuranceOwnedFlagInEitherSpelling(t *testing.T) {
	t.Parallel()

	for _, argument := range []string{"-test.cpu=2", "--test.cpu=2"} {
		_, err := testargs.Normalize([]string{argument})
		if err == nil || !strings.Contains(err.Error(), "assurance-owned") {
			t.Errorf("Normalize(%q) error = %v, want a refusal naming the assurance-owned flag", argument, err)
		}
	}
}

// TestNormalizeRefusesOnTheFirstOwnedFlagAndKeepsNothing pins the shape of the
// refusal: it returns nil rather than the arguments it had already rewritten.
//
// Half a normalisation is the one result a caller cannot use, and nothing else
// in this package's tests said the slice was nil rather than partial.
func TestNormalizeRefusesOnTheFirstOwnedFlagAndKeepsNothing(t *testing.T) {
	t.Parallel()

	got, err := testargs.Normalize([]string{"-short", "-test.run=TestX", "--short"})
	if err == nil {
		t.Fatalf("Normalize = %v, nil; want a refusal", got)
	}
	if got != nil {
		t.Errorf("Normalize returned %v beside its refusal; a partial rewrite is the one answer a caller cannot use", got)
	}
}
