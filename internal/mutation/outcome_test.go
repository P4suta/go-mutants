// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestZeroOutcomeIsNotRun is the reason OutcomeNotRun is the zero value: a
// result struct nobody filled in must not read as a kill and inflate the
// score.
func TestZeroOutcomeIsNotRun(t *testing.T) {
	t.Parallel()

	var zero Outcome
	if zero != OutcomeNotRun {
		t.Fatalf("the zero Outcome is %v, want %v", zero, OutcomeNotRun)
	}
	if zero.Detected() {
		t.Fatal("the zero Outcome must not count as a detection")
	}
}

func TestOutcomeNames(t *testing.T) {
	t.Parallel()

	want := map[Outcome]string{
		OutcomeNotRun:       "not_run",
		OutcomeKilled:       "killed",
		OutcomeSurvived:     "survived",
		OutcomeTimedOut:     "timed_out",
		OutcomeInconclusive: "inconclusive",
		OutcomeErrored:      "errored",
	}
	got := make(map[Outcome]string, len(want))
	for _, o := range Outcomes() {
		got[o] = o.String()
		if !o.Valid() {
			t.Errorf("%v should be valid", o)
		}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("outcome wire names changed (-want +got):\n%s", diff)
	}
	if len(Outcomes()) != len(want) {
		t.Fatalf("Outcomes() returns %d values, want %d", len(Outcomes()), len(want))
	}
}

func TestOutcomeRoundTrip(t *testing.T) {
	t.Parallel()

	for _, o := range Outcomes() {
		parsed, err := ParseOutcome(o.String())
		if err != nil {
			t.Fatalf("ParseOutcome(%q) error = %v", o.String(), err)
		}
		if parsed != o {
			t.Errorf("ParseOutcome(%q) = %v, want %v", o.String(), parsed, o)
		}

		text, err := o.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText() error = %v", err)
		}
		var back Outcome
		if err := back.UnmarshalText(text); err != nil {
			t.Fatalf("UnmarshalText(%q) error = %v", text, err)
		}
		if back != o {
			t.Errorf("round trip of %v produced %v", o, back)
		}
	}
}

func TestOutcomeRejectsUnknownValues(t *testing.T) {
	t.Parallel()

	if _, err := ParseOutcome("exploded"); !errors.Is(err, ErrUnknownOutcome) {
		t.Errorf("ParseOutcome(%q) error = %v, want ErrUnknownOutcome", "exploded", err)
	}
	unknown := Outcome(200)
	if unknown.Valid() {
		t.Error("Outcome(200) should not be valid")
	}
	if _, err := unknown.MarshalText(); !errors.Is(err, ErrUnknownOutcome) {
		t.Errorf("MarshalText() of an unknown outcome error = %v, want ErrUnknownOutcome", err)
	}
	if got := unknown.String(); got != "outcome(200)" {
		t.Errorf("String() = %q, want %q", got, "outcome(200)")
	}
	var target Outcome
	if err := target.UnmarshalText([]byte("nope")); !errors.Is(err, ErrUnknownOutcome) {
		t.Errorf("UnmarshalText() error = %v, want ErrUnknownOutcome", err)
	}
}

// TestDetectedOutcomes pins the score's definition of detection: a confirmed
// timeout counts, a single unconfirmed one is inconclusive and does not.
func TestDetectedOutcomes(t *testing.T) {
	t.Parallel()

	want := map[Outcome]bool{
		OutcomeKilled:       true,
		OutcomeTimedOut:     true,
		OutcomeSurvived:     false,
		OutcomeInconclusive: false,
		OutcomeErrored:      false,
		OutcomeNotRun:       false,
	}
	for outcome, detected := range want {
		if got := outcome.Detected(); got != detected {
			t.Errorf("%v.Detected() = %v, want %v", outcome, got, detected)
		}
	}
}

func TestSkipReasons(t *testing.T) {
	t.Parallel()

	want := []SkipReason{
		"const-decl",
		"iota-expr",
		"array-length",
		"struct-tag",
		"type-param-list",
		"type-arg",
		"switch-case",
		"select-case",
		"package-level-var-init",
		"go-embed-decl",
		"label-or-goto",
		"cgo-package",
		"generated-file",
		"test-file",
		"unnameable-decl-type",
	}
	if diff := cmp.Diff(want, KnownSkipReasons()); diff != "" {
		t.Fatalf("documented skip reasons changed (-want +got):\n%s", diff)
	}
	for _, reason := range KnownSkipReasons() {
		if !reason.Known() {
			t.Errorf("%q should be known", reason)
		}
		if reason.String() != string(reason) {
			t.Errorf("String() = %q, want %q", reason.String(), string(reason))
		}
	}
	if SkipReason("invented").Known() {
		t.Error("an undocumented reason should not report as known")
	}
}

func TestRejectReasons(t *testing.T) {
	t.Parallel()

	if got := RejectCompileError.String(); got != "compile-error" {
		t.Errorf("RejectCompileError = %q, want %q", got, "compile-error")
	}
	if got := RejectFlattenFailure.String(); got != "flatten-failure" {
		t.Errorf("RejectFlattenFailure = %q, want %q", got, "flatten-failure")
	}
}
