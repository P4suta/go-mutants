// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "goatest-tempowner-tests-")
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
	owner, err := tempowner.Claim(root, tempowner.Marker{RunID: "tempowner-tests"}, time.Now())
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

func TestFixtureScratchRootIsKept(t *testing.T) {
	t.Parallel()
	marker, err := tempowner.ReadMarker(os.TempDir())
	if err != nil || marker.Schema != tempowner.Schema || !marker.Kept {
		t.Fatalf("fixture scratch ownership = %+v, error = %v; want a kept parent", marker, err)
	}
}
