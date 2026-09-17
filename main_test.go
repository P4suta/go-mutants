// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !integration

package gomutants_test

import (
	"os"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}
