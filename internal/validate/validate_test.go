// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/validate"
)

// TestCodesAreWellFormed keeps the diagnostic codes usable as the stable
// handles they are advertised to be: unique, sorted, and inside the block this
// package owns. A duplicated code makes two different failures
// indistinguishable to anyone searching for one.
func TestCodesAreWellFormed(t *testing.T) {
	t.Parallel()

	codes := validate.Codes()
	if len(codes) == 0 {
		t.Fatal("Codes() is empty")
	}
	if !slices.IsSorted(codes) {
		t.Errorf("Codes() = %v, want them in numeric order", codes)
	}
	seen := map[validate.Code]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Errorf("code %s appears twice", c)
		}
		seen[c] = true

		rest, ok := strings.CutPrefix(string(c), "GOM")
		if !ok || len(rest) != 4 {
			t.Errorf("code %q is not of the form GOM####", c)
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			t.Errorf("code %q does not end in a number", c)
			continue
		}
		if n < 7400 || n > 7499 {
			t.Errorf("code %q is outside the GOM74xx block this package owns", c)
		}
	}
}

// TestValidateRefusesBadOptions covers every way the phase can be pointed at
// something it cannot validate.
//
// Each of these is caught before a single file is read, which is the point: an
// options mistake that surfaced from the first build would arrive as a
// complaint about a program name or a missing directory, describing the symptom
// and not the mistake.
func TestValidateRefusesBadOptions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	snap := &snapshot.Snapshot{Root: root}
	catalog := emptyCatalog(t)
	toolchain := gocmd.Toolchain{GoBin: "/usr/bin/go"}

	cases := []struct {
		name string
		opts validate.Options
	}{
		{"no snapshot", validate.Options{Catalog: catalog, ModulePath: "m", Toolchain: toolchain}},
		{"a snapshot with no root", validate.Options{
			Snap: &snapshot.Snapshot{}, Catalog: catalog, ModulePath: "m", Toolchain: toolchain,
		}},
		{"no catalogue", validate.Options{Snap: snap, ModulePath: "m", Toolchain: toolchain}},
		{"no module path", validate.Options{Snap: snap, Catalog: catalog, Toolchain: toolchain}},
		{"no toolchain", validate.Options{Snap: snap, Catalog: catalog, ModulePath: "m"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			result, err := validate.Validate(t.Context(), c.opts)
			if err == nil {
				t.Fatal("Validate accepted the options, want a refusal")
			}
			if got := validate.CodeOf(err); got != validate.CodeOptions {
				t.Errorf("Validate failed with %s, want %s: %v", got, validate.CodeOptions, err)
			}
			if result.Builds != 0 {
				t.Errorf("Validate spent %d builds on options it refused, want 0", result.Builds)
			}
		})
	}
}

// TestErrorRenders pins how a validation failure reads, because a GOM code is
// only a stable handle if it is printed the same way every time.
func TestErrorRenders(t *testing.T) {
	t.Parallel()

	cause := errors.New("exit status 2")
	err := &validate.Error{
		Code:    validate.CodeNotMutantInduced,
		Message: "the snapshot does not build with every mutant removed",
		Output:  "./a.go:1:1: undefined: x",
		Err:     cause,
	}
	want := "GOM7420: the snapshot does not build with every mutant removed: exit status 2\n" +
		"./a.go:1:1: undefined: x"
	if got := err.Error(); got != want {
		t.Errorf("Error() =\n%s\nwant\n%s", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if got := validate.CodeOf(err); got != validate.CodeNotMutantInduced {
		t.Errorf("CodeOf = %s, want %s", got, validate.CodeNotMutantInduced)
	}
	if got := validate.CodeOf(cause); got != "" {
		t.Errorf("CodeOf of a foreign error = %s, want the empty code", got)
	}
}

// emptyCatalog builds the catalogue of no candidates, which is all the options
// checks above need: they are refused before anything looks inside it.
func emptyCatalog(t *testing.T) *mutation.Catalog {
	t.Helper()
	catalog, err := mutation.NewBuilder().Build()
	if err != nil {
		t.Fatalf("building an empty catalogue: %v", err)
	}
	return catalog
}
