// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package snapshot

func ExtendedPath(p string) string { return p }
