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

func TestNormalizeRefusesAnAssuranceOwnedFlagInEitherSpelling(t *testing.T) {
	t.Parallel()

	for _, argument := range []string{"-test.cpu=2", "--test.cpu=2"} {
		_, err := testargs.Normalize([]string{argument})
		if err == nil || !strings.Contains(err.Error(), "assurance-owned") {
			t.Errorf("Normalize(%q) error = %v, want a refusal naming the assurance-owned flag", argument, err)
		}
	}
}

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
