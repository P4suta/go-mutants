// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package validate_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/internal/validate"
)

const shadowedSource = "// Package shadowed holds one return whose result type is shadowed where a\n" +
	"// probe would have to name it, and one ordinary return beside it.\n" +
	"package shadowed\n\n" +
	"// Shadowed returns a local variable whose name is the result type's.\n" +
	"func Shadowed() int {\n" +
	"\tint := 3\n" +
	"\treturn int\n" +
	"}\n\n" +
	"// Plain returns an ordinary value, and is the control.\n" +
	"func Plain(n int) int {\n" +
	"\treturn n + 1\n" +
	"}\n"

func TestValidateProbeTreeRejectsOnlyTheSiteThatCannotCompile(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)

	for _, c := range []struct {
		name     string
		mode     instrument.Mode
		rejected []string
	}{
		{name: "the mutant tree", mode: 0},
		{name: "the probe tree", mode: instrument.ModeProbe, rejected: []string{"return-zero-numeric int"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			env := testkit.Compose(t, testkit.Scratch(t))
			snap, found, catalog := shadowedFixture(t, toolchain, env)

			result, err := validate.Validate(t.Context(), validate.Options{
				Snap:         snap,
				Catalog:      catalog,
				Hints:        mutantkit.Hints(t, found),
				Modules:      []validate.Module{{Dir: ".", Path: found.ModulePath}},
				Toolchain:    toolchain,
				BuildTimeout: mutantkit.StepTimeout,
				Env:          env,
				Trace:        mutantkit.Trace(t),
				Mode:         c.mode,
			})
			if err != nil {
				t.Fatalf("validating the shadowed module: %v", err)
			}

			if got := rejectionLines(t, catalog, result.Rejected); !slices.Equal(got, c.rejected) {
				t.Errorf("rejected %v, want %v", got, c.rejected)
			}
			if got, want := len(result.AcceptedIDs), catalog.Len()-len(c.rejected); got != want {
				t.Errorf("accepted %d candidates, want %d", got, want)
			}
			for _, r := range result.Rejected {
				if strings.TrimSpace(r.Diagnostic) == "" {
					t.Errorf("rejection %s carries no diagnostic", r.DisplayID)
				}
				if r.Path != "shadowed.go" {
					t.Errorf("rejection %s names %q, want shadowed.go", r.DisplayID, r.Path)
				}
			}

			build := mutantkit.RunGo(t, toolchain, snap.Root, env, "build", "./...")
			mutantkit.RequireExit(t, build, 0, "`go build ./...` after validation")
		})
	}
}

func shadowedFixture(t *testing.T, toolchain gocmd.Toolchain, env []string) (*snapshot.Snapshot, discover.Result, *mutation.Catalog) {
	t.Helper()

	const modulePath = "fixture.example/shadowed"
	source := testkit.NewModule(t).Module(modulePath).Source("shadowed.go", shadowedSource).Root()

	snap := mutantkit.SnapshotOf(t, source)
	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	catalog := mutantkit.Catalog(t, found)
	want := []string{
		"return-zero-numeric int",
		"return-zero-numeric n + 1",
		"add-to-sub +",
	}
	if got := candidateLines(catalog); !slices.Equal(got, want) {
		t.Fatalf("catalogue =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	if !hasReturnHint(found, "int") {
		t.Fatal("discovery computed no probe hint for the shadowed return, so nothing here would fail to compile")
	}
	return snap, found, catalog
}

func candidateLines(catalog *mutation.Catalog) []string {
	out := make([]string, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		out = append(out, m.Rule.Name+" "+m.Original)
	}
	return out
}

func rejectionLines(t *testing.T, catalog *mutation.Catalog, rejected []validate.Rejection) []string {
	t.Helper()

	out := make([]string, 0, len(rejected))
	for _, r := range rejected {
		m, ok := catalog.ByID(r.ID)
		if !ok {
			t.Errorf("rejection %s names a mutant the catalogue does not hold", r.DisplayID)
			continue
		}
		out = append(out, m.Rule.Name+" "+m.Original)
	}
	return out
}

func hasReturnHint(found discover.Result, original string) bool {
	for _, c := range found.Candidates {
		if c.Original == original && c.Guard.Probe != nil {
			return true
		}
	}
	return false
}
