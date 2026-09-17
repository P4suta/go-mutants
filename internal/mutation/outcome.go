// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"fmt"
)

type Outcome uint8

const (
	OutcomeNotRun Outcome = iota
	OutcomeKilled
	OutcomeSurvived
	OutcomeTimedOut
	OutcomeInconclusive
	OutcomeErrored
)

var ErrUnknownOutcome = errors.New("mutation: unknown outcome")

var outcomeNames = map[Outcome]string{
	OutcomeNotRun:       "not_run",
	OutcomeKilled:       "killed",
	OutcomeSurvived:     "survived",
	OutcomeTimedOut:     "timed_out",
	OutcomeInconclusive: "inconclusive",
	OutcomeErrored:      "errored",
}

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

func (o Outcome) String() string {
	if name, ok := outcomeNames[o]; ok {
		return name
	}
	return fmt.Sprintf("outcome(%d)", uint8(o))
}

func (o Outcome) Valid() bool {
	_, ok := outcomeNames[o]
	return ok
}

func (o Outcome) Detected() bool {
	return o == OutcomeKilled || o == OutcomeTimedOut
}

func ParseOutcome(s string) (Outcome, error) {
	for _, o := range Outcomes() {
		if o.String() == s {
			return o, nil
		}
	}
	return OutcomeNotRun, fmt.Errorf("%w: %q", ErrUnknownOutcome, s)
}

func (o Outcome) MarshalText() ([]byte, error) {
	if !o.Valid() {
		return nil, fmt.Errorf("%w: %d", ErrUnknownOutcome, uint8(o))
	}
	return []byte(o.String()), nil
}

func (o *Outcome) UnmarshalText(text []byte) error {
	parsed, err := ParseOutcome(string(text))
	if err != nil {
		return err
	}
	*o = parsed
	return nil
}

type RejectReason string

const (
	RejectCompileError   RejectReason = "compile-error"
	RejectFlattenFailure RejectReason = "flatten-failure"
)

func (r RejectReason) String() string { return string(r) }
