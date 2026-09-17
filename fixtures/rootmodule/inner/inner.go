// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package inner is the second module of the rootmodule fixture.
package inner

// Doubled returns n twice over.
func Doubled(n int) int { return n + n }

// Above reports whether n is above the limit, which is the same comparison the
// root module has so that one rule finds a candidate in both.
func Above(n, limit int) bool { return n > limit }
