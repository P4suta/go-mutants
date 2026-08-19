// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package legacy is ordinary code. It exists so that a test can remove it with
// a pattern and see the difference, which is the only thing that separates it
// from any other package here.
package legacy

// Equal holds the one candidate the exclusion tests move in and out of the
// catalogue.
func Equal(a, b int) bool { return a == b }
