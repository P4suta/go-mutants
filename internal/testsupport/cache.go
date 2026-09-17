// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testsupport is the previous home of the shared test helpers, kept as.
package testsupport

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func CacheDir(t *testing.T) string {
	t.Helper()
	return testkit.Env(t).Cache
}
