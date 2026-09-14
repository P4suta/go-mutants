// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package second is the sibling module of the workspace. Nothing requires it:
// the only thing that puts it on the first module's import path is the
// `use ./second` line in the workspace file above, which is what makes it the
// probe for whether discovery is obeying that file.
//
// It has two jobs, and the second one arrived with workspace discovery. [Zero]
// is the symbol the first module imports, and it is deliberately unmutable --
// `return 0` is what `return-zero-numeric` would write, so the rule refuses it
// as a no-op. [Wider] is here so that this module has a candidate at all: a
// workspace pass that discovered only the module it was pointed at would
// otherwise be indistinguishable from one that discovered both, since the
// second one would have found nothing either way.
package second

// Zero is the one symbol the first module imports.
func Zero() int { return 0 }

// Wider reports whether a is more than b, and holds this module's candidates:
// `gt-to-ge` and `gt-to-lt` on the comparison, `return-true` and
// `return-false` on the returned expression.
func Wider(a, b int) bool { return a > b }
