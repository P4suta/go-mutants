// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cross is the module whose tests measure another module's code, which
// is the one thing a workspace run can do that three separate runs cannot.
//
// It imports `fixture.example/workspace/lib`, whose own test asserts nothing —
// see [lib_test.go] — so every mutant of [lib.Differs] is killed here or not at
// all. That is the observation: a run at the workspace root reports them killed,
// and a run that measured each module on its own would report three survivors
// in a module whose tests are green.
//
// Nothing but the `use` lines puts `lib` on this module's import path: its
// `go.mod` requires it neither directly nor through a replace, so inside the
// workspace it resolves to the sibling directory and outside it this module
// does not build at all. A fixture that built either way would not be able to
// say which of the two happened.
package cross

import "fixture.example/workspace/lib"

// Differing reports whether a and b differ, by asking the sibling module.
func Differing(a, b int) bool { return lib.Differs(a, b) }
