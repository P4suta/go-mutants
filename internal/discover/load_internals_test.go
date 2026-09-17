// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
)

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

func TestEnvironmentFromPlacesTheToolchainAheadOfPath(t *testing.T) {
	t.Parallel()

	dir := filepath.FromSlash("/opt/go/bin")
	bin := filepath.Join(dir, "go")
	tc := gocmd.Toolchain{GoBin: bin}
	sep := string(filepath.ListSeparator)

	t.Run("prepends to an existing PATH", func(t *testing.T) {
		t.Parallel()
		env := environmentFrom([]string{"PATH=/usr/bin", "HOME=/h"}, tc, false)
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
		env := environmentFrom([]string{"PATH=" + dir + sep + "/usr/bin"}, tc, false)
		path := lookupEnv(env, "PATH")
		if len(path) != 1 || path[0] != dir+sep+"/usr/bin" {
			t.Errorf("PATH = %v, want it left unchanged", path)
		}
	})

	t.Run("a workspace run removes GOWORK rather than switching it off", func(t *testing.T) {
		t.Parallel()
		env := environmentFrom([]string{"GOWORK=/elsewhere/go.work", "HOME=/h"}, tc, true)
		if got := lookupEnv(env, "GOWORK"); len(got) != 0 {
			t.Errorf("GOWORK = %v, want it absent so the snapshot's own go.work is found", got)
		}
		if got := lookupEnv(env, "HOME"); len(got) != 1 || got[0] != "/h" {
			t.Errorf("HOME = %v, want the rest of the base carried through", got)
		}
	})

	t.Run("adds a PATH when the base has none", func(t *testing.T) {
		t.Parallel()
		env := environmentFrom([]string{"HOME=/h"}, tc, false)
		path := lookupEnv(env, "PATH")
		if len(path) != 1 || path[0] != dir {
			t.Errorf("PATH = %v, want [%q]", path, dir)
		}
	})
}

func TestSameEnvKeyOnFollowsTheNamedPlatform(t *testing.T) {
	t.Parallel()

	if !sameEnvKeyOn("windows", "Path", "PATH") {
		t.Error("on windows, Path and PATH are the same variable")
	}
	if sameEnvKeyOn("linux", "Path", "PATH") {
		t.Error("off windows, Path and PATH are different variables")
	}
	if !sameEnvKeyOn("linux", "PATH", "PATH") {
		t.Error("a variable always matches its own spelling")
	}
	if !sameEnvKeyOn("windows", "PATH", "PATH") {
		t.Error("a variable always matches its own spelling on windows too")
	}
}

func TestPathsEqualOnFollowsTheNamedPlatform(t *testing.T) {
	t.Parallel()

	if !pathsEqualOn("windows", `C:\Go\bin`, `c:\go\bin`) {
		t.Error("on windows, paths differing only in case are equal")
	}
	if pathsEqualOn("linux", "/Go/bin", "/go/bin") {
		t.Error("off windows, paths differing in case are distinct")
	}
	if !pathsEqualOn("linux", "/go/bin", "/go/bin") {
		t.Error("a path always equals itself")
	}
}
