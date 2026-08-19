// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package second is the sibling module of the workspace. Nothing requires it:
// the only thing that puts it on the first module's import path is the
// `use ./second` line in the workspace file above, which is what makes it the
// probe for whether discovery is obeying that file.
package second

// Zero is the one symbol the first module imports.
func Zero() int { return 0 }
