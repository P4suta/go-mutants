// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testflag

import "strings"

func Match(argument, name string) bool {
	short := "-" + name
	long := "--" + name
	return argument == short || strings.HasPrefix(argument, short+"=") ||
		argument == long || strings.HasPrefix(argument, long+"=")
}
