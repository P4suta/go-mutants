// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testsupport

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func CacheDir(t *testing.T) string {
	t.Helper()
	return testkit.Env(t).Cache
}
