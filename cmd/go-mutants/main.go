// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"

	"github.com/P4suta/go-mutants/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
