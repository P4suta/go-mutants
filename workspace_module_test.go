// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"testing"
)

// The query [Workspace.Module] refuses before it starts a `go` command, driven
// against a synthetic workspace.
//
// It belongs in the unit tier because the answer is decided before the
// snapshot, the toolchain or the frozen tree are touched, so a test that opened
// a workspace to establish it would be paying a snapshot and a version probe
// for an argument check. The *lifecycle* half of Module's refusals is in
// workspace_lifecycle_test.go, beside Exec's and Prepare's, because those three
// are one state machine and a second table for one of them would be a second
// place to forget a state.

// TestModuleRefusesAbsolutePatterns is the whole of the query check: every
// pattern shape the engine will not hand to `go list`, refused with
// [ErrInvalidQuery] and a sentence naming the one that was wrong.
//
// It is a refusal rather than an empty answer for the reason
// [ErrInvalidSelection] is: a pattern nobody can resolve selects no package,
// and a consumer that asked about `/home/me/project` and was told the module
// holds nothing would believe it. The leading dash is the one that is not
// merely wrong but dangerous — the go command reads a first positional
// argument beginning with `-` as a flag — so it is named separately from the
// paths.
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
		// The Windows shapes, refused on every operating system and with the
		// same sentence. A query is composed from a consumer's own
		// configuration, and a path typed on Windows reaches a Linux runner
		// unchanged — so a refusal that read "is absolute" on one and "is not
		// module-relative" on the other would be two answers to one mistake, and
		// only one of them says what is actually wrong with it.
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
			// Drive-*relative*, which is not absolute at all: it is refused for
			// what it really is rather than mislabelled for looking similar.
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

// TestAListingEveryCallerAbandonedIsNotRemembered pins the memo's one race, on
// the side of it that has to be safe.
//
// A listing runs on the caller that started it, and its entry is dropped the
// moment the last caller interested in it goes away — a cancelled context, a
// deadline — so that the child can be cut off rather than left holding a
// workspace nobody is waiting for. The listing goroutine is *still running*
// when that happens, and what it does next is settle: it comes back with a
// package set or with the cancellation, and either way it publishes the result
// for the waiters it no longer has.
//
// So settling must not put back an entry the memo has already let go of. If it
// did, the next caller would be handed a listing that was produced for nobody
// and was being torn down while it ran — in place of the fresh one it asked
// for. Both outcomes are driven, because "it failed anyway" is the easy half
// and "it succeeded anyway" is the one a memo is tempted by.
//
// It is driven against the three calls directly rather than through a
// workspace, because what is under test is their order against one map — an
// order a real listing reaches only by losing a race.
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
			// The only caller goes away, which is what drops the entry and cuts
			// the child off; then the listing goroutine, still running, returns.
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
