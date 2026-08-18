// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command go-mutants is the mutation testing CLI for Go modules.
//
// This entry point is intentionally a stub: the real command tree lives in
// internal/cli and arrives with the CLI phase. main stays thin forever, so
// that exit-code policy and flag validation remain unit-testable.
package main

import "fmt"

// version is the development version string. Releases stamp it at link time.
const version = "0.1.0-dev"

func main() {
	fmt.Println("go-mutants " + version)
}
