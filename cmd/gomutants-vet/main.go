// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command gomutants-vet runs this repository's own static checks, the ones
// that need types rather than text.
//
// It is a separate binary rather than another test under internal/devgates
// because the checks it carries are go/analysis passes, and a pass is a value:
// the same one loads into `go vet -vettool`, into golangci-lint, and into this
// driver, over either module, without being written three times. The scans
// under internal/devgates stay where they are — they are cheap, they run in the
// unit tier, and a scan that needs no toolchain is the first line of defence.
// This is the second, and it can see what a scan cannot.
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
