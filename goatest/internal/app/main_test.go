// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/testscratch"
)

func TestMain(m *testing.M) {
	if _, selected := os.LookupEnv(doctorHelperVariable); selected {
		os.Exit(m.Run())
	}
	testscratch.Main(m)
}
