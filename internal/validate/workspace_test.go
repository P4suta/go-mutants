// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/internal/validate"
)

// The two modules of the workspace this file validates.
const (
	firstModulePath  = "example.com/ws/first"
	secondModulePath = "example.com/ws/second"

	firstBody  = "package first\n\nfunc Equal(a, b int) bool { return a == b }\n"
	secondBody = "package second\n\nfunc Wider(a, b int) bool { return a > b }\n"
)

// TestValidatingAWorkspaceInstrumentsEveryModuleWhereItStands is the phase's
// half of workspace support, and it is three claims that a single-module
// validation cannot make.
//
// Every module is instrumented, each at its own root, because a module's files
// can only import a runtime its own module declares. Every path the phase
// reports is relative to the *snapshot*, not to a module, because that is what
// the drift gate compares against and what the compiler names in a diagnostic
// when the build runs at the workspace root. And the runtimes are reported one
// per module, because there is no single one to name.
func TestValidatingAWorkspaceInstrumentsEveryModuleWhereItStands(t *testing.T) {
	t.Parallel()

	snap := workspaceSnapshot(t)
	catalog, hints := workspaceCatalogAndHints(t)
	fake := mutantkit.FakeGo(t)
	fake.On("build").Exit(0)

	result, err := validate.Validate(t.Context(), validate.Options{
		Snap:    snap,
		Catalog: catalog,
		Hints:   hints,
		Modules: []validate.Module{
			{Dir: "first", Path: firstModulePath},
			{Dir: "second", Path: secondModulePath},
		},
		Toolchain:    gocmd.Toolchain{GoBin: fake.Bin()},
		Env:          fake.Env(testkit.Compose(t, testkit.Scratch(t))),
		BuildTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(result.AcceptedIDs) != catalog.Len() {
		t.Errorf("accepted %d of %d mutants on a green build", len(result.AcceptedIDs), catalog.Len())
	}

	want := []string{"first/first.go", "second/second.go"}
	if got := result.Instrumented.FilesInstrumented; !slices.Equal(got, want) {
		t.Errorf("FilesInstrumented = %v, want %v -- snapshot-relative, which is what the drift "+
			"gate and the compiler both speak", got, want)
	}
	for _, path := range want {
		if got := result.Instrumented.GuardsByFile[path]; got != 1 {
			t.Errorf("GuardsByFile[%q] = %d, want 1", path, got)
		}
		if body := readSnapshotFile(t, snap.Root, path); !strings.Contains(body, "__gm.M[") {
			t.Errorf("%s carries no guard:\n%s", path, body)
		}
	}

	if len(result.Runtimes) != 2 {
		t.Fatalf("Runtimes = %+v, want one per module", result.Runtimes)
	}
	for i, module := range []string{firstModulePath, secondModulePath} {
		runtime := result.Runtimes[i]
		if !strings.HasPrefix(runtime.RuntimeImport, module+"/") {
			t.Errorf("Runtimes[%d].RuntimeImport = %q, want a package of %s",
				i, runtime.RuntimeImport, module)
		}
		dir := filepath.Join(snap.Root, filepath.FromSlash(runtime.RuntimeDir))
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Errorf("the runtime of %s is not on disk at %s: %v", module, runtime.RuntimeDir, statErr)
		}
	}
}

// TestValidatingRefusesModulesThatDoNotMatchTheCatalogue is fail-closed about
// the pairings that would validate a tree nobody instrumented.
//
// A catalogue naming modules and a single module given would rewrite no file at
// all -- and every mutant would then be accepted, because a tree with no guards
// in it compiles. The other way round is the same mistake. Both are a caller
// that lost track of which kind of run it is in, and neither is a state any run
// produces.
func TestValidatingRefusesModulesThatDoNotMatchTheCatalogue(t *testing.T) {
	t.Parallel()

	snap := workspaceSnapshot(t)
	workspace, workspaceHints := workspaceCatalogAndHints(t)
	fake := mutantkit.FakeGo(t)
	fake.On("build").Exit(0)

	for _, tc := range []struct {
		name    string
		modules []validate.Module
		says    string
	}{
		{
			name:    "no module at all",
			modules: nil,
			says:    "no module",
		},
		{
			name:    "a module the catalogue does not name",
			modules: []validate.Module{{Dir: ".", Path: "example.com/elsewhere"}},
			says:    firstModulePath,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := validate.Validate(t.Context(), validate.Options{
				Snap:         snap,
				Catalog:      workspace,
				Hints:        workspaceHints,
				Modules:      tc.modules,
				Toolchain:    gocmd.Toolchain{GoBin: fake.Bin()},
				Env:          fake.Env(testkit.Compose(t, testkit.Scratch(t))),
				BuildTimeout: time.Minute,
			})
			if err == nil {
				t.Fatal("Validate accepted it")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the message does not say %q: %v", tc.says, err)
			}
		})
	}
}

// workspaceSnapshot is a two-module workspace, snapshotted.
func workspaceSnapshot(t *testing.T) *snapshot.Snapshot {
	t.Helper()

	root := filepath.Join(testkit.Scratch(t), "workspace")
	testkit.NewModuleAt(t, filepath.Join(root, "first")).
		Module(firstModulePath).
		Source("first.go", firstBody)
	testkit.NewModuleAt(t, filepath.Join(root, "second")).
		Module(secondModulePath).
		Source("second.go", secondBody)
	testkit.WriteFile(t, filepath.Join(root, "go.work"),
		[]byte("go 1.26\n\nuse (\n\t./first\n\t./second\n)\n"))

	snap, err := snapshot.Create(root, snapshot.Options{DestParent: testkit.Scratch(t)})
	if err != nil {
		t.Fatalf("snapshotting the workspace: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("removing the snapshot: %v", cleanupErr)
		}
	})
	return snap
}

// workspaceCatalogAndHints is one mutable comparison in each module.
func workspaceCatalogAndHints(t *testing.T) (*mutation.Catalog, instrument.Hints) {
	t.Helper()

	located := []discover.Located{
		workspaceSite(t, firstModulePath, "first.go", sourceOf(firstBody), "eq-to-neq", "a == b", "==", "!="),
		workspaceSite(t, secondModulePath, "second.go", sourceOf(secondBody), "gt-to-ge", "a > b", ">", ">="),
	}
	builder := mutation.NewBuilder()
	for _, l := range located {
		if err := builder.Add(l.Candidate); err != nil {
			t.Fatalf("cataloguing %s: %v", l.Where(), err)
		}
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	hints, err := instrument.HintsOf(located)
	if err != nil {
		t.Fatalf("HintsOf: %v", err)
	}
	return catalog, hints
}

// sourceOf is a fixture body as it lands on disk: with the header every file of
// a synthesized module carries, because the digests and spans are of the bytes
// that are actually there.
func sourceOf(body string) string { return testkit.SPDXHeader + body }

// workspaceSite is one candidate and the Form C guard that rewrites it.
func workspaceSite(
	t *testing.T,
	module, path, source, rule, site, original, replacement string,
) discover.Located {
	t.Helper()

	found, ok := mutation.CanonicalRegistry().Lookup(rule)
	if !ok {
		t.Fatalf("unknown rule %q", rule)
	}
	siteStart := strings.Index(source, site)
	start := strings.Index(source, original)
	if siteStart < 0 || start < 0 {
		t.Fatalf("%q does not hold %q and %q", source, site, original)
	}
	return discover.Located{
		Candidate: mutation.Candidate{
			ModulePath:   module,
			Path:         path,
			Rule:         found,
			Span:         mutation.Span{StartByte: uint32(start), EndByte: uint32(start + len(original))},
			Original:     original,
			Replacement:  replacement,
			SourceDigest: mutation.DigestString(source),
		},
		Line:    5,
		Column:  1,
		Package: module,
		Guard: discover.Guard{
			Form: discover.GuardFormC,
			SiteSpan: mutation.Span{
				StartByte: uint32(siteStart),
				EndByte:   uint32(siteStart + len(site)),
			},
		},
	}
}

// readSnapshotFile reads one snapshot-relative file.
func readSnapshotFile(t *testing.T, root, rel string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}
