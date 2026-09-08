// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
)

// TestPluralAgreesWithTheCount pins plural: one is singular and named without a
// count of its own spelling elsewhere, and any other count is the number
// followed by the plural noun. The zero and two cases pin the `n == 1` guard,
// and both rendered strings pin that neither branch returns the empty string.
func TestPluralAgreesWithTheCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		n    int
		noun string
		want string
	}{
		{1, "package", "1 package"},
		{0, "package", "0 packages"},
		{2, "package", "2 packages"},
	}
	for _, tc := range cases {
		if got := plural(tc.n, tc.noun); got != tc.want {
			t.Errorf("plural(%d, %q) = %q, want %q", tc.n, tc.noun, got, tc.want)
		}
	}
}

// TestToolchainHintNamesTheLocatedBinary pins toolchainHint: it is empty when
// no toolchain was located, and otherwise a parenthetical naming the binary, so
// a load failure can tell the reader which `go` the loader was pointed at. The
// empty case pins the `GoBin == ""` guard and the non-empty case pins that the
// binary path is actually in the string.
func TestToolchainHintNamesTheLocatedBinary(t *testing.T) {
	t.Parallel()

	if got := toolchainHint(gocmd.Toolchain{}); got != "" {
		t.Errorf("hint without a toolchain = %q, want empty", got)
	}
	got := toolchainHint(gocmd.Toolchain{GoBin: "/opt/go/bin/go"})
	if !strings.Contains(got, "/opt/go/bin/go") {
		t.Errorf("hint = %q, want it to name the located binary", got)
	}
}

// TestEnvironmentFromPlacesTheToolchainAheadOfPath pins the three outcomes of
// environmentFrom's PATH handling over a controlled base, none of which may
// return a nil environment: the toolchain directory is prepended to an existing
// PATH, an entry that already leads with it is left alone, and a base with no
// PATH gains one. GOWORK is switched off in every case.
func TestEnvironmentFromPlacesTheToolchainAheadOfPath(t *testing.T) {
	t.Parallel()

	dir := filepath.FromSlash("/opt/go/bin")
	bin := filepath.Join(dir, "go")
	tc := gocmd.Toolchain{GoBin: bin}
	sep := string(filepath.ListSeparator)

	t.Run("prepends to an existing PATH", func(t *testing.T) {
		t.Parallel()
		env := environmentFrom([]string{"PATH=/usr/bin", "HOME=/h"}, tc)
		path := lookupEnv(env, "PATH")
		if len(path) != 1 || path[0] != dir+sep+"/usr/bin" {
			t.Errorf("PATH = %v, want the toolchain dir prepended once", path)
		}
		if got := lookupEnv(env, "GOWORK"); len(got) != 1 || got[0] != "off" {
			t.Errorf("GOWORK = %v, want [off]", got)
		}
	})

	t.Run("leaves a leading toolchain alone", func(t *testing.T) {
		t.Parallel()
		env := environmentFrom([]string{"PATH=" + dir + sep + "/usr/bin"}, tc)
		path := lookupEnv(env, "PATH")
		if len(path) != 1 || path[0] != dir+sep+"/usr/bin" {
			t.Errorf("PATH = %v, want it left unchanged", path)
		}
	})

	t.Run("adds a PATH when the base has none", func(t *testing.T) {
		t.Parallel()
		env := environmentFrom([]string{"HOME=/h"}, tc)
		path := lookupEnv(env, "PATH")
		if len(path) != 1 || path[0] != dir {
			t.Errorf("PATH = %v, want [%q]", path, dir)
		}
	})
}

// TestSameEnvKeyOnFollowsTheNamedPlatform pins both branches of the env-key
// rule on whatever host runs it: Windows answers a variable to any spelling of
// its name, and every other platform matches exactly. A test bound to the
// running platform could only reach one branch; naming the OS reaches both.
func TestSameEnvKeyOnFollowsTheNamedPlatform(t *testing.T) {
	t.Parallel()

	if !sameEnvKeyOn("Path", "PATH", "windows") {
		t.Error("on windows, Path and PATH are the same variable")
	}
	if sameEnvKeyOn("Path", "PATH", "linux") {
		t.Error("off windows, Path and PATH are different variables")
	}
	if !sameEnvKeyOn("PATH", "PATH", "linux") {
		t.Error("a variable always matches its own spelling")
	}
	if !sameEnvKeyOn("PATH", "PATH", "windows") {
		t.Error("a variable always matches its own spelling on windows too")
	}
}

// TestPathsEqualOnFollowsTheNamedPlatform pins both branches of the path rule
// the same way: Windows compares paths case-insensitively, every other platform
// exactly.
func TestPathsEqualOnFollowsTheNamedPlatform(t *testing.T) {
	t.Parallel()

	if !pathsEqualOn(`C:\Go\bin`, `c:\go\bin`, "windows") {
		t.Error("on windows, paths differing only in case are equal")
	}
	if pathsEqualOn("/Go/bin", "/go/bin", "linux") {
		t.Error("off windows, paths differing in case are distinct")
	}
	if !pathsEqualOn("/go/bin", "/go/bin", "linux") {
		t.Error("a path always equals itself")
	}
}
