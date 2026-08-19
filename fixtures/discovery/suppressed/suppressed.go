// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package suppressed puts a candidate in every context discovery refuses to
// mutate, next to a live one in the nearest context it does. Every declaration
// here is contrived on purpose: these positions rarely hold a value expression
// at all, which is exactly why the suppressions have to be exercised rather
// than assumed.
package suppressed

import (
	_ "embed"
)

// A constant expression must stay constant, iota included.
const (
	// Enabled is a boolean literal in a const block.
	Enabled = true
	// Bigger is a comparison in a const block.
	Bigger = 2 > 1
	// Ordinal is here so that the block holds an iota that the same edit would
	// renumber.
	Ordinal = iota
)

// Single is a comparison in a const declaration of its own.
const Single = 3 <= 4

// Buffer's length is the only place a value expression can hide inside an
// array type: len of an array-typed composite literal is a constant, so the
// comparison and the literal below are evaluated by the compiler and never at
// run time.
type Buffer [len([2]bool{1 < 2, true})]byte

// Size returns the length of a Buffer so that the type is used.
func Size() int { return len(Buffer{}) }

// Threshold is a comparison in a package-level variable initialiser:
// initialisation order is a global property that a per-mutant guard cannot
// express in v1.
var Threshold = 3 < 5

// Verbose is a boolean literal in one.
var Verbose = true

// Late holds a comparison inside a function literal inside a package-level
// initialiser. v1 suppresses the whole initialiser expression, function bodies
// included, rather than reasoning about when it runs.
var Late = func() bool { return 1 == 2 }

// Data is a go:embed variable: an embedded file has no initialiser to
// suppress, and the declaration must neither produce a candidate nor stop the
// snapshot from building.
//
//go:embed data.txt
var Data string

// Embedded returns the embedded bytes so that Data is used.
func Embedded() string { return Data }

// Local proves that a const block inside a function body is suppressed for the
// same reason as one at package level, and that the statements around it are
// not.
func Local(a int) int {
	const limit = 3 > 2
	if limit {
		return a
	}
	return 0
}

// Switch keeps its case labels off limits and its bodies fair game, in an
// expression switch and in a type switch.
func Switch(a, b int, ok bool) string {
	switch {
	case a == b:
		if ok == true {
			return "equal and ok"
		}
	case ok == false:
		return "not ok"
	}
	switch v := any(a).(type) {
	case int:
		if v > b {
			return "greater"
		}
	case string:
		return v
	}
	return "none"
}

// Select keeps its communication clause off limits and its body fair game.
func Select(ch chan bool, a, b int) string {
	select {
	case ch <- (a < b):
		return "sent"
	case v := <-ch:
		if v == true {
			return "received"
		}
	}
	return "none"
}
