// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"testing"
)

func TestModuleRefusesAbsolutePatterns(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		query ModuleQuery
		want  string
	}{
		{
			name:  "a unix absolute path",
			query: ModuleQuery{Packages: []string{"/home/me/project"}},
			want:  `gomutants: module: invalid query: package pattern "/home/me/project" is absolute; patterns are module-relative`,
		},
		{
			name:  "an absolute path beside a good one",
			query: ModuleQuery{Packages: []string{"./internal/...", "/etc"}},
			want:  `gomutants: module: invalid query: package pattern "/etc" is absolute; patterns are module-relative`,
		},
		{
			name:  "a drive-letter path with a backslash",
			query: ModuleQuery{Packages: []string{`C:\src\thing`}},
			want:  `gomutants: module: invalid query: package pattern "C:\\src\\thing" is absolute; patterns are module-relative`,
		},
		{
			name:  "a drive-letter path with a slash",
			query: ModuleQuery{Packages: []string{"c:/src/thing"}},
			want:  `gomutants: module: invalid query: package pattern "c:/src/thing" is absolute; patterns are module-relative`,
		},
		{
			name:  "an extended-length path",
			query: ModuleQuery{Packages: []string{`\\?\C:\src\thing`}},
			want:  `gomutants: module: invalid query: package pattern "\\\\?\\C:\\src\\thing" is absolute; patterns are module-relative`,
		},
		{
			name:  "a UNC share",
			query: ModuleQuery{Packages: []string{`\\server\share\thing`}},
			want:  `gomutants: module: invalid query: package pattern "\\\\server\\share\\thing" is absolute; patterns are module-relative`,
		},
		{
			name:  "a drive-relative path",
			query: ModuleQuery{Packages: []string{"C:src"}},
			want:  `gomutants: module: invalid query: package pattern "C:src" is not module-relative; use "." or a "./" pattern`,
		},
		{
			name:  "a pattern that escapes the module",
			query: ModuleQuery{Packages: []string{"./../sibling/..."}},
			want:  `gomutants: module: invalid query: package pattern "./../sibling/..." escapes the module`,
		},
		{
			name:  "a bare parent",
			query: ModuleQuery{Packages: []string{".."}},
			want:  `gomutants: module: invalid query: package pattern ".." escapes the module`,
		},
		{
			name:  "an import path rather than a module-relative pattern",
			query: ModuleQuery{Packages: []string{"example.com/other/..."}},
			want:  `gomutants: module: invalid query: package pattern "example.com/other/..." is not module-relative; use "." or a "./" pattern`,
		},
		{
			name:  "a flag wearing a pattern's place",
			query: ModuleQuery{Packages: []string{"-covermode=atomic"}},
			want:  `gomutants: module: invalid query: package pattern "-covermode=atomic" begins with a dash, which the go command reads as a flag`,
		},
		{
			name:  "an empty pattern",
			query: ModuleQuery{Packages: []string{"./...", "  "}},
			want:  `gomutants: module: invalid query: a package pattern is empty`,
		},
		{
			name:  "a tag holding two",
			query: ModuleQuery{Tags: []string{"special,other"}},
			want:  `gomutants: module: invalid query: build tag "special,other" must be one tag, with no comma and no whitespace`,
		},
		{
			name:  "an empty tag",
			query: ModuleQuery{Tags: []string{""}},
			want:  `gomutants: module: invalid query: build tag "" must be one tag, with no comma and no whitespace`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			workspace := &Workspace{scratch: t.TempDir(), closeDone: make(chan struct{})}
			module, err := workspace.Module(t.Context(), c.query)
			if got := errorText(err); got != c.want {
				t.Errorf("Module = %q, want %q", got, c.want)
			}
			if !errors.Is(err, ErrInvalidQuery) {
				t.Errorf("Module error does not carry ErrInvalidQuery: %v", err)
			}
			if len(module.Packages) != 0 || module.Path != "" {
				t.Errorf("a refused query still answered with %+v", module)
			}
		})
	}
}

func TestAListingEveryCallerAbandonedIsNotRemembered(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		module Module
		err    error
	}{
		{name: "the listing succeeded anyway", module: Module{Path: "fixture.example/m"}},
		{name: "the listing failed", err: errors.New("gomutants: module: nope")},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			workspace := &Workspace{}
			const key = `"./..." -tags `
			answer, leading := workspace.joinModule(key, t.Context())
			if !leading {
				t.Fatal("the first caller of an empty memo is not the one that lists")
			}
			workspace.leaveModule(key, answer)
			workspace.settleModule(key, answer, c.module, c.err)

			if got := workspace.ModuleInterest(); got != 0 {
				t.Errorf("ModuleInterest() = %d after every caller left, want 0", got)
			}
			next, leadingAgain := workspace.joinModule(key, t.Context())
			if !leadingAgain {
				t.Fatal("the next caller was handed a listing every caller had abandoned," +
					" rather than making one of its own")
			}
			if next == answer {
				t.Error("the next caller joined the abandoned listing's own entry")
			}
		})
	}
}
