// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cgopkg imports "C" and is therefore excluded from mutation whole.
//
// The package deliberately holds a plain Go file as well. With CGO_ENABLED=0
// the go command excludes this file by build constraint and the package is
// still a package, thanks to that plain file; with cgo on it is a cgo file
// whose compiled form lives in the build cache. Discovery has to reach the same
// verdict either way, and the plain file is what gives it something to reach a
// verdict about on a machine with no C compiler.
//
// Nothing in this module imports the package. That is deliberate too: a cgo
// package's own build failure is exempt from the load gate, but anything
// depending on one is not, and this fixture must not turn a machine without a C
// compiler into a failing test suite.
package cgopkg

import "C"

// Equal would be a comparison candidate in a package that could be mutated.
func Equal(a, b int) bool { return a == b }
