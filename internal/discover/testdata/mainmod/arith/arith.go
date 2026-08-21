// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package arith holds one live example of every arithmetic rule, next to the
// operand types that keep those rules away from operators they must not touch.
//
// Every result is written into a slice rather than returned, so that each
// statement carries exactly the candidates the arithmetic families put there
// and the expectation table stays readable.
package arith

// Celsius is a named type over a float. The float rules still apply to it:
// what an operator does is decided by the underlying type, and refusing named
// types would mean refusing the domain types a well-typed program is made of.
type Celsius float64

// Count is the same thing for the integer rules.
type Count int

// Ints exercises all five integer rules.
func Ints(a, b int, out []int) {
	out[0] = a + b
	out[1] = a - b
	out[2] = a * b
	out[3] = a / b
	out[4] = a % b
}

// Floats exercises all four float rules. There is no float remainder in Go, so
// the family has four rules where the integer one has five.
func Floats(a, b float64, out []float64) {
	out[0] = a + b
	out[1] = a - b
	out[2] = a * b
	out[3] = a / b
}

// Named proves the gates read through a named type to its underlying one.
func Named(a, b Count, c, d Celsius, counts []Count, temps []Celsius) {
	counts[0] = a + b
	temps[0] = c * d
}

// Strings is the exclusion that has to be a type decision and not a spelling
// one: `+` here is concatenation, and no arithmetic rule may claim it.
func Strings(a, b string, out []string) {
	out[0] = a + b
}

// Complexes is the other exclusion. Complex arithmetic is numeric and is still
// out of scope for v1, which is why the float gate asks for a floating-point
// type rather than for a numeric one.
func Complexes(a, b complex128, out []complex128) {
	out[0] = a + b
	out[1] = a * b
}
