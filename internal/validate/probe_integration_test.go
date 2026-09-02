// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Compile validation of the probe tree, against a real toolchain.
//
// The phase does not know it is validating a different tree: it instruments,
// builds, and bisects the same way whichever mode it was handed. What is new is
// what a rejection then means — "this mutant's probe site does not compile", so
// that mutant is not probed — and the claim worth a real compiler is that the
// rejection stays as small as it always was. One statement whose probe cannot
// be written must cost that statement's mutants their probe and nothing else,
// because the alternative is one awkward `return` costing every mutant in its
// file their measurement.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/validate/...
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
	"github.com/P4suta/go-mutants/internal/validate"
)

// shadowedSource is a module with one probe that cannot be written.
//
// `int := 3` shadows the predeclared name inside the function body, so the
// result type discovery spells — "int", which is what the signature says — is
// not a type at the point the probe would declare its temporary with it. The
// mutant tree is untroubled: its guard is `if __gm.M[k] { return 0 } else {
// return int }`, which never names a type at all. That asymmetry is the whole
// reason a probe site has to be able to fail on its own.
//
// Everything else in the file is ordinary, and is what the rejection must not
// touch.
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

// TestValidateProbeTreeRejectsOnlyTheSiteThatCannotCompile validates one module
// twice, in both modes, and watches the rejection appear in exactly one of
// them.
//
// Both halves are the claim. The mutant tree accepting everything is what says
// the fixture is ordinary Go and the rejection is about the probe rather than
// about the code; the probe tree rejecting one candidate and keeping the rest
// is what says a site that cannot be written costs its own mutant and no more.
func TestValidateProbeTreeRejectsOnlyTheSiteThatCannotCompile(t *testing.T) {
	toolchain := locateToolchain(t)

	for _, c := range []struct {
		name string
		mode instrument.Mode
		// rejected names the candidates that must be refused, by the rule and
		// the bytes it replaces.
		rejected []string
	}{
		{name: "the mutant tree", mode: 0},
		{name: "the probe tree", mode: instrument.ModeProbe, rejected: []string{"return-zero-numeric int"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			snap, found, catalog := shadowedFixture(t, toolchain)

			result, err := validate.Validate(t.Context(), validate.Options{
				Snap:         snap,
				Catalog:      catalog,
				Hints:        fixtureHints(t, found),
				ModulePath:   found.ModulePath,
				Toolchain:    toolchain,
				BuildTimeout: stepTimeout,
				Env:          fixtureEnv(""),
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

			// Whatever was left on disk has to build, in either mode: the phase
			// promises a tree the next one can use, and a probe tree that did
			// not build would stop a run rather than merely measure less of it.
			build := goInSnapshot(t, toolchain, snap.Root, "", "build", "./...")
			requireExit(t, build, 0, "`go build ./...` after validation")
		})
	}
}

// shadowedFixture snapshots [shadowedSource] and discovers it, insisting on the
// catalogue the assertions above are written against.
func shadowedFixture(t *testing.T, toolchain gocmd.Toolchain) (*snapshot.Snapshot, discover.Result, *mutation.Catalog) {
	t.Helper()

	const modulePath = "fixture.example/shadowed"
	source := t.TempDir()
	writeModuleFile(t, source, "go.mod", "module "+modulePath+"\n\ngo 1.26\n")
	writeModuleFile(t, source, "shadowed.go", shadowedSource)

	snap, err := snapshot.Create(source, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatalf("snapshotting the shadowed module: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("cleaning up the snapshot at %s: %v", snap.Root, cleanupErr)
		}
	})

	found, err := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
	})
	if err != nil {
		t.Fatalf("discovering the shadowed module: %v", err)
	}
	catalog, err := discover.BuildCatalog(found)
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	// Pinned, because every assertion above names a candidate by its rule and
	// the bytes it replaces: a fixture that grew one would otherwise change
	// what "everything else was accepted" counts.
	want := []string{
		"return-zero-numeric int",
		"return-zero-numeric n + 1",
		"add-to-sub +",
	}
	if got := candidateLines(catalog); !slices.Equal(got, want) {
		t.Fatalf("catalogue =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	// The fixture says nothing at all unless the probe really was asked for at
	// the shadowed statement.
	if !hasReturnHint(found, "int") {
		t.Fatal("discovery computed no probe hint for the shadowed return, so nothing here would fail to compile")
	}
	return snap, found, catalog
}

// candidateLines renders a catalogue as the rule and the bytes it replaces,
// which is what names a candidate in this file.
func candidateLines(catalog *mutation.Catalog) []string {
	out := make([]string, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		out = append(out, m.Rule.Name+" "+m.Original)
	}
	return out
}

// rejectionLines renders rejections the same way, in the order the phase
// reported them.
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

// hasReturnHint reports whether discovery attached a probe hint to the
// candidate replacing particular bytes.
func hasReturnHint(found discover.Result, original string) bool {
	for _, c := range found.Candidates {
		if c.Original == original && c.Guard.Return != nil {
			return true
		}
	}
	return false
}
