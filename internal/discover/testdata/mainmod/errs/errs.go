// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package errs holds the error-swallowing family and the line it draws against
// the return-replacement one.
package errs

// Wrapped is a concrete error type. It is here so that a function declared to
// return `error` can return something that is not an error interface value,
// which is the case the two nil rules have to divide between them.
type Wrapped struct{ Op string }

// Error makes Wrapped an error.
func (w *Wrapped) Error() string { return w.Op }

// Swallow returns a value whose static type is exactly `error`, which is the
// case return-err-to-nil owns and the failure mode Go test suites miss most.
func Swallow(err error) error { return err }

// Concrete returns a concrete pointer from a function declared to return
// `error`. The value is not an error interface value, so this is an ordinary
// nillable result and return-nil owns it.
func Concrete(op string) error { return &Wrapped{Op: op} }

// Branch holds the nil comparison in both operand orders. Each one is replaced
// whole by `false`, because the point is a branch that stops firing: swapping
// the operator would only move the failure to the other arm, which the
// comparison family already does.
func Branch(err error, out []int) {
	if err != nil {
		out[0] = 1
	}
	if nil != err {
		out[1] = 2
	}
}

// Pointer is the exclusion. A pointer that is not an error compares against
// nil with the same syntax and is not this family's business.
func Pointer(p *int, out []int) {
	if p != nil {
		out[0] = 1
	}
}
