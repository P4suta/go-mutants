// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func goModAt(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(path, []byte(contents), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestModuleFromGoModReadsTheDirectiveAndNothingThatLooksLikeIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		contents string
		want     string
	}{
		{name: "the plain directive", contents: "module example.com/app\n", want: "example.com/app"},
		{name: "a quoted path", contents: "module \"example.com/app\"\n", want: "example.com/app"},
		{name: "a tab after the keyword", contents: "module\texample.com/app\n", want: "example.com/app"},
		{name: "a trailing comment", contents: "module example.com/app // the app\n", want: "example.com/app"},
		{name: "a line commented out first", contents: "// module example.com/other\nmodule example.com/app\n", want: "example.com/app"},
		{name: "a later directive after go", contents: "go 1.26\n\nmodule example.com/app\n", want: "example.com/app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := moduleFromGoMod(goModAt(t, test.contents))
			if err != nil || got != test.want {
				t.Fatalf("moduleFromGoMod = (%q, %v), want %q", got, err, test.want)
			}
		})
	}
}

func TestModuleFromGoModRefusesWhatNamesNoModule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		contents string
	}{
		{name: "nothing at all", contents: ""},
		{name: "a word that starts the same", contents: "modulepath example.com/app\n"},
		{name: "the keyword with nothing after it", contents: "module\n"},
		{name: "the keyword and a comment", contents: "module // example.com/app\n"},
		{name: "a quoted empty path", contents: "module \"\"\n"},
		{name: "only a commented directive", contents: "// module example.com/app\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := moduleFromGoMod(goModAt(t, test.contents))
			if err == nil || !strings.Contains(err.Error(), "names no module") {
				t.Fatalf("moduleFromGoMod = (%q, %v), want it refused", got, err)
			}
		})
	}
}

func TestModuleFromGoModReportsAFileItCannotRead(t *testing.T) {
	t.Parallel()
	got, err := moduleFromGoMod(filepath.Join(t.TempDir(), "absent", "go.mod"))
	if err == nil || !strings.Contains(err.Error(), "read the module path") {
		t.Fatalf("moduleFromGoMod = (%q, %v)", got, err)
	}
}
