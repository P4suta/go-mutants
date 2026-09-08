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
