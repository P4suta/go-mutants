// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package brokentests type-checks; its test file does not. Discovery reads no
// test file's type information, so the catalogue it produces here is the same
// one it would produce beside a test file that compiles.
package brokentests

// Count returns one more than n.
func Count(n int) int {
	return n + 1
}
