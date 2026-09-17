// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
)

func FuzzParseVersion(f *testing.F) {
	f.Add("go version go1.26.5 darwin/arm64\n")
	f.Add("go version go1.26.5 linux/amd64")
	f.Add("go version devel go1.27-a1b2c3d4 Mon Jan 1 00:00:00 2026 +0000 linux/amd64\n")
	f.Add("go version go1.26.5 X:nocoverageredesign linux/amd64\n")
	f.Add("")
	f.Add("\n")
	f.Add("   \n")
	f.Add("go version\n")
	f.Add("go version go1.26.5\n")
	f.Add("go version devel linux/amd64\n")
	f.Add("go version go1.26.5 linux/\n")
	f.Add("go version go1.26.5 /amd64\n")
	f.Add("go version go1.26.5 linuxamd64\n")
	f.Add("go version go1.26.5 a/b/c\n")
	f.Add("not a go toolchain\n")
	f.Add("go version go1.26.5 linux/amd64\nsecond line\n")
	f.Add("\tgo version go1.26.5 linux/amd64\t\n")
	f.Add("go  version   go1.26.5   linux/amd64\n")
	f.Add("\x00 version go1.26.5 linux/amd64\n")
	f.Add(strings.Repeat("x", 4096) + "\n")

	f.Fuzz(func(t *testing.T, output string) {
		version, err := gocmd.ParseVersion(output)
		if err != nil {
			var refusal *gocmd.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("ParseVersion returned %T, want an *Error a caller can branch on: %v", err, err)
			}
			if refusal.Code != gocmd.CodeVersionUnparsable {
				t.Fatalf("ParseVersion refused with %q, want %q", refusal.Code, gocmd.CodeVersionUnparsable)
			}
			if version != (gocmd.Version{}) {
				t.Fatalf("ParseVersion refused and returned %+v anyway", version)
			}
			return
		}

		first, _, _ := strings.Cut(output, "\n")
		if want := strings.TrimSpace(first); version.Raw != want {
			t.Fatalf("Raw = %q, want the trimmed first line %q", version.Raw, want)
		}
		if version.String() != version.Raw {
			t.Fatalf("String() = %q and Raw is %q", version.String(), version.Raw)
		}
		if version.Release == "" || version.GOOS == "" || version.GOARCH == "" {
			t.Fatalf("ParseVersion accepted %q as %+v, and a report cannot name that toolchain",
				version.Raw, version)
		}
		if strings.ContainsAny(version.GOOS, "/") || strings.ContainsAny(version.GOARCH, "/") {
			t.Fatalf("the target split to %q/%q, which is not one os and one arch",
				version.GOOS, version.GOARCH)
		}
		if version.IsDevel() != strings.HasPrefix(version.Release, "devel") {
			t.Fatalf("IsDevel() = %v for the release %q", version.IsDevel(), version.Release)
		}
	})
}
