// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testsupport is the previous home of the shared test helpers, kept as
// a forwarder while its call sites move onto internal/testkit.
//
// Everything that was here — the four cache directory variables every platform
// reads, the go telemetry mode file below a moved HOME, pinning the go command's
// own directories before HOME moves — is now part of [testkit.Env], which does
// all of it and the rest of the hermetic policy besides. This package exists so
// that the fourteen call sites of [CacheDir] keep compiling and passing until
// they are migrated, and it is deleted in the same change that migrates the last
// of them.
//
// It is imported only from _test files, which is why a file here may import the
// test harness at all; the import gate in internal/testkit names it explicitly
// as the one exception, with the same expiry.
package testsupport

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// CacheDir redirects [os.UserCacheDir] into a directory of the test's own and
// returns the directory it now resolves to.
//
// The returned path is the cache *root* — the directory go-mutants puts its
// `go-mutants` directory in, not that directory itself — because that is what
// os.UserCacheDir returns and what the code under test joins onto.
//
// It now redirects rather more than the cache: see [testkit.Env] for the whole
// policy. Nothing that used this helper needed the wider environment to be
// anything in particular, and everything that used it wanted the parts of the
// policy it did not know to ask for — a private temporary directory, a
// GOCACHE that is not the developer's, no GO_MUTANTS_ variable inherited from
// the shell.
func CacheDir(t *testing.T) string {
	t.Helper()
	return testkit.Env(t).Cache
}
