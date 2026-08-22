// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"fmt"
)

// Outcome is what happened to one mutant in one run.
//
// The zero value is OutcomeNotRun on purpose. A result struct that was never
// filled in must never read as a kill: forgetting to record an outcome should
// deflate the score, not inflate it.
type Outcome uint8

// The v1 outcomes.
const (
	// OutcomeNotRun means the mutant was never executed: another shard owned
	// it, `--changed` excluded it, the run was interrupted, or coverage
	// analysis proved no test reaches it. Excluded from the score.
	OutcomeNotRun Outcome = iota
	// OutcomeKilled means at least one test failed with the mutant active.
	// Detected.
	OutcomeKilled
	// OutcomeSurvived means the whole test suite passed with the mutant
	// active. Counted in the denominator unless the expectations ledger
	// predicted it.
	OutcomeSurvived
	// OutcomeTimedOut means a *confirmed* timeout: the mutant exceeded the
	// timeout, was retried serially, and exceeded it again. Two consecutive
	// timeouts are treated as detection, because an infinite loop introduced
	// by a mutant is a behaviour change the tests noticed. A single timeout
	// is OutcomeInconclusive, never this.
	OutcomeTimedOut
	// OutcomeInconclusive means the run could not decide: one timeout that
	// did not reproduce, or a flaky failure that also fails on the
	// instrumented baseline. Excluded from the score in both directions, and
	// on its own it never fails the build.
	OutcomeInconclusive
	// OutcomeErrored means the harness itself failed for this mutant — the
	// test binary could not start, the runtime rejected the activation ID,
	// the process died on a signal the supervisor did not send. Excluded
	// from the score and escalated to exit 2, because the run cannot claim
	// to have measured anything about this mutant.
	OutcomeErrored
)

// ErrUnknownOutcome reports an outcome value or name that is not defined.
var ErrUnknownOutcome = errors.New("mutation: unknown outcome")

// outcomeNames are the canonical wire names, used in JSON reports, in the
// outcome cache, and in console output. They are snake_case and stable.
var outcomeNames = map[Outcome]string{
	OutcomeNotRun:       "not_run",
	OutcomeKilled:       "killed",
	OutcomeSurvived:     "survived",
	OutcomeTimedOut:     "timed_out",
	OutcomeInconclusive: "inconclusive",
	OutcomeErrored:      "errored",
}

// Outcomes returns every outcome in declaration order.
func Outcomes() []Outcome {
	return []Outcome{
		OutcomeNotRun,
		OutcomeKilled,
		OutcomeSurvived,
		OutcomeTimedOut,
		OutcomeInconclusive,
		OutcomeErrored,
	}
}

// String returns the canonical wire name.
func (o Outcome) String() string {
	if name, ok := outcomeNames[o]; ok {
		return name
	}
	return fmt.Sprintf("outcome(%d)", uint8(o))
}

// Valid reports whether o is a defined outcome.
func (o Outcome) Valid() bool {
	_, ok := outcomeNames[o]
	return ok
}

// Detected reports whether the outcome counts as the tests catching the
// mutant: a kill or a confirmed timeout.
func (o Outcome) Detected() bool {
	return o == OutcomeKilled || o == OutcomeTimedOut
}

// ParseOutcome resolves a canonical wire name.
func ParseOutcome(s string) (Outcome, error) {
	for _, o := range Outcomes() {
		if o.String() == s {
			return o, nil
		}
	}
	return OutcomeNotRun, fmt.Errorf("%w: %q", ErrUnknownOutcome, s)
}

// MarshalText implements encoding.TextMarshaler so outcomes serialise as
// their wire names rather than as integers, in any encoder.
func (o Outcome) MarshalText() ([]byte, error) {
	if !o.Valid() {
		return nil, fmt.Errorf("%w: %d", ErrUnknownOutcome, uint8(o))
	}
	return []byte(o.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (o *Outcome) UnmarshalText(text []byte) error {
	parsed, err := ParseOutcome(string(text))
	if err != nil {
		return err
	}
	*o = parsed
	return nil
}

// Skip reasons are deliberately not declared in this package. A skip is a
// decision discovery makes about a source position, so the vocabulary lives
// with the decision: internal/discover's SkipReason and AllSkipReasons are the
// single source of truth for the strings `list` prints and for the `skips[]`
// rows a report carries. The `reason` enumeration of the run report schema is
// a superset rather than a copy of that list: internal/report checks every
// reason discovery emits against the enumeration, and the enumeration reserves
// two further names — `struct-tag` and `label-or-goto` — that no Go constant
// in this tree declares, so that landing them is a code change and not a
// schema change. The type and the canonical list stay in internal/discover
// even for a reason another phase emits; whoever lands one adds it there.
//
// This package used to carry a second copy of that vocabulary, spelled
// differently — `generated-file` against discovery's `generated`,
// `cgo-package` against `cgo`, `switch-case` and `select-case` against the one
// `case-label` — pinned only by a test that retyped the same list. Nothing
// outside these files ever read it, so nothing could disagree with it, and a
// vocabulary nothing disagrees with is a vocabulary nothing keeps honest.
// Anything here that comes to need a skip reason takes a plain string; it does
// not start a second list, and it cannot import internal/discover for the
// type, because internal/discover imports this package. A skip reason that
// wants a Go type of its own belongs there rather than here.

// RejectReason explains why a candidate that was catalogued could not be
// executed. Unlike a skip — which discovery records before a candidate exists
// at all, see the note above — a rejection is discovered late: the mutant
// existed on paper and then failed to survive compilation or instrumentation.
//
// The set stays open. Validation (the compile-and-bisect phase) owns the full
// list and attaches a compiler diagnostic to each rejection; the constants
// here are the ones the pure core already needs to name.
type RejectReason string

// The rejection reasons defined so far.
const (
	// RejectCompileError means the instrumented snapshot did not compile
	// with this mutant spliced in, as isolated by the delta-debugging pass.
	RejectCompileError RejectReason = "compile-error"
	// RejectFlattenFailure means the mutated statement could not be
	// flattened onto one line without changing its meaning.
	RejectFlattenFailure RejectReason = "flatten-failure"
)

// String returns the reason as it appears in reports.
func (r RejectReason) String() string { return string(r) }
