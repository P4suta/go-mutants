// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cgopkg

// Same is ordinary Go in a package that also has a cgo file. It is skipped
// with everything else here, because a cgo package is excluded whole and not
// file by file — which is also what makes the exclusion the same under either
// setting of CGO_ENABLED.
func Same(a, b int) bool { return a == b }
