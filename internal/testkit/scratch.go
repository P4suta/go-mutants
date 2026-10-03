// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"os"
	"testing"
)

func OwnedMain(m *testing.M, keep func(string) error) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root, err := os.MkdirTemp(home, ".go-mutants-test-scratch-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	exitCode := runWithOwnedScratch(m, root, keep)
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

func runWithOwnedScratch(m *testing.M, root string, keep func(string) error) int {
	if err := keep(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, name := range []string{"TMPDIR", "TEMP", "TMP"} {
		if err := os.Setenv(name, root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}
