// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/P4suta/go-mutants/internal/analysis/exhaustive"
)

func main() {
	multichecker.Main(
		exhaustive.Analyzer,
	)
}
