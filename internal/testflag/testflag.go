// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testflag recognises flags passed directly to a Go test binary.
package testflag

import "strings"

// Match reports whether argument names the supplied test-binary flag, with or
// without an attached value. The standard flag package accepts one or two
// leading dashes, so safety checks must treat both spellings identically.
func Match(argument, name string) bool {
	short := "-" + name
	long := "--" + name
	return argument == short || strings.HasPrefix(argument, short+"=") ||
		argument == long || strings.HasPrefix(argument, long+"=")
}
