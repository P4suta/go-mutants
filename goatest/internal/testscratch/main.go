// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testscratch

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
)

func Main(m *testing.M) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root, err := os.MkdirTemp(home, ".goatest-test-scratch-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	exitCode := runWithOwnedScratch(m, root)
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

func runWithOwnedScratch(m *testing.M, root string) int {
	owner, err := tempowner.Claim(root, tempowner.Marker{RunID: "goatest-tests"}, time.Now())
	if err == nil {
		err = owner.Keep()
	}
	if err != nil {
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
