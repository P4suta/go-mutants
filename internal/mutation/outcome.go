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

// SkipReason explains why a possible mutation site was never turned into a
// candidate. Skips are recorded and surfaced by `--explain`; a site is never
// silently ignored, because "go-mutants found nothing here" and "go-mutants
// refuses to mutate this" are very different statements about a codebase.
type SkipReason string

// The v1 skip reasons, matching docs/operators.md. Discovery and
// instrumentation own the decision to emit each one; the list lives here so
// that reports, schemas, and the CLI share one spelling.
const (
	SkipConstDecl           SkipReason = "const-decl"
	SkipIotaExpr            SkipReason = "iota-expr"
	SkipArrayLength         SkipReason = "array-length"
	SkipStructTag           SkipReason = "struct-tag"
	SkipTypeParamList       SkipReason = "type-param-list"
	SkipTypeArg             SkipReason = "type-arg"
	SkipSwitchCase          SkipReason = "switch-case"
	SkipSelectCase          SkipReason = "select-case"
	SkipPackageLevelVarInit SkipReason = "package-level-var-init"
	SkipGoEmbedDecl         SkipReason = "go-embed-decl"
	SkipLabelOrGoto         SkipReason = "label-or-goto"
	SkipCgoPackage          SkipReason = "cgo-package"
	SkipGeneratedFile       SkipReason = "generated-file"
	SkipTestFile            SkipReason = "test-file"
	SkipUnnameableDeclType  SkipReason = "unnameable-decl-type"
)

// String returns the reason as it appears in reports.
func (r SkipReason) String() string { return string(r) }

// KnownSkipReasons returns the documented skip reasons in the order of
// docs/operators.md.
func KnownSkipReasons() []SkipReason {
	return []SkipReason{
		SkipConstDecl,
		SkipIotaExpr,
		SkipArrayLength,
		SkipStructTag,
		SkipTypeParamList,
		SkipTypeArg,
		SkipSwitchCase,
		SkipSelectCase,
		SkipPackageLevelVarInit,
		SkipGoEmbedDecl,
		SkipLabelOrGoto,
		SkipCgoPackage,
		SkipGeneratedFile,
		SkipTestFile,
		SkipUnnameableDeclType,
	}
}

// Known reports whether r is one of the documented skip reasons. Unknown
// reasons are not rejected — discovery may need a new one before the docs
// catch up — but reports can flag them.
func (r SkipReason) Known() bool {
	for _, known := range KnownSkipReasons() {
		if known == r {
			return true
		}
	}
	return false
}

// RejectReason explains why a candidate that was catalogued could not be
// executed. Unlike a skip, a rejection is discovered late: the mutant existed
// on paper and then failed to survive compilation or instrumentation.
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
