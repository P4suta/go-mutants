// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build ignore

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
