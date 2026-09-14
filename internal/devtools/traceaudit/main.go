// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build ignore

// Command traceaudit is [traceaudit.Audit] on the command line.
//
//	go run ./internal/devtools/traceaudit REPORT TRACE
//
// It exits 0 when the two documents agree, 1 when they do not, and 2 when it
// could not read one of them. Findings the recording could not settle are
// printed and do not change the exit code, because "I cannot check this" is not
// "this is broken".
package main

import (
	"fmt"
	"os"

	"github.com/P4suta/go-mutants/internal/devtools/traceaudit"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: traceaudit REPORT TRACE")
		os.Exit(2)
	}
	result, err := traceaudit.Audit(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "traceaudit: %v\n", err)
		os.Exit(2)
	}
	for _, finding := range result.Findings {
		fmt.Println(finding)
	}
	fmt.Fprintf(os.Stderr, "%d mutants, %d audited, %d violations, %d unaudited\n",
		result.Mutants, result.Audited, len(result.Violations()), len(result.Unaudited()))
	if len(result.Violations()) != 0 {
		os.Exit(1)
	}
}
