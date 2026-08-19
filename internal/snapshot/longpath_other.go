// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package snapshot

// extendedPath returns p unchanged. Only Windows has an extended-length path
// syntax; POSIX path limits are per-component and per-call, and nothing about
// spelling a path differently raises them.
func extendedPath(p string) string { return p }
