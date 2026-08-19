// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command go-mutants is the mutation testing CLI for Go modules.
//
// This entry point is deliberately two lines. Everything worth testing — the
// command tree, flag validation, error rendering, and the exit code mapping —
// lives in internal/cli, where a test can drive it in process with its own
// streams. main's only job is to be the one place the process ends.
package main

import (
	"os"

	"github.com/P4suta/go-mutants/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
