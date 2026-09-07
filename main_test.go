// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !integration

// The unit tier's TestMain, which exists for one reason: fakego_test.go runs
// this test binary as a scripted `go`.
//
// It carries the negation of the integration tag rather than no tag at all
// because a package may have only one TestMain, and the integration tier has its
// own — api_integration_test.go's, which releases the sessions the tagged files
// share. That one dispatches to the same place before it runs anything, so the
// scripted toolchain works in both tiers and neither tier runs the other's
// TestMain.

package gomutants_test

import (
	"os"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}
