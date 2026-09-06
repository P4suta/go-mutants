// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
)

func TestInstrumentationEnvironmentSupportsOverlayPathWithWhitespace(t *testing.T) {
	t.Parallel()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go toolchain is unavailable: %v", err)
	}
	root := filepath.Join(t.TempDir(), "module root")
	if mkdirErr := os.MkdirAll(root, privateDirectoryMode); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	for name, contents := range map[string]string{
		"go.mod":                "module fixture.example/space\n\ngo 1.26.0\n",
		"value.go":              "package space\n",
		"overlay manifest.json": `{"Replace":{}}`,
	} {
		if writeErr := os.WriteFile(filepath.Join(root, name), []byte(contents), privateFileMode); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	overlay := filepath.Join(root, "overlay manifest.json")
	environment, err := instrumentationEnvironment(os.Environ(), overlay)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), goBinary, "list", "./...")
	command.Dir = root
	command.Env = append(environment, "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "fixture.example/space" {
		t.Fatalf("go list through spaced overlay = (%q, %v)", output, err)
	}
}

func TestResolvePrepareOptionsScopesDiscoveryIndependently(t *testing.T) {
	t.Parallel()
	resolved, err := resolvePrepareOptions(PrepareOptions{
		DiscoveryPackages:  []string{"./candidate"},
		Packages:           []string{"./tests"},
		ProbeCoverPackages: []string{"example.com/module/..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resolved.DiscoveryPackages, []string{"./candidate"}) ||
		!slices.Equal(resolved.Packages, []string{"./tests"}) ||
		!slices.Equal(resolved.ProbeCoverPackages, []string{"example.com/module/..."}) {
		t.Fatalf("resolved package scopes = discovery %q tests %q", resolved.DiscoveryPackages, resolved.Packages)
	}
	defaults, err := resolvePrepareOptions(PrepareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(defaults.DiscoveryPackages, []string{"./..."}) ||
		!slices.Equal(defaults.Packages, []string{"./..."}) {
		t.Fatalf("default package scopes = discovery %q tests %q", defaults.DiscoveryPackages, defaults.Packages)
	}
	if _, outsideErr := resolvePrepareOptions(PrepareOptions{DiscoveryPackages: []string{"../outside"}}); outsideErr == nil {
		t.Fatal("outside discovery package was accepted")
	}
	if _, commaErr := resolvePrepareOptions(PrepareOptions{ProbeCoverPackages: []string{"a,b"}}); commaErr == nil {
		t.Fatal("comma-separated probe coverage package was accepted")
	}
	skipped, err := resolvePrepareOptions(PrepareOptions{SkipVerify: true})
	if err != nil || len(skipped.Verify.Argv) != 0 {
		t.Fatalf("skip verify resolved as (%q, %v)", skipped.Verify.Argv, err)
	}
	if _, err := resolvePrepareOptions(PrepareOptions{SkipVerify: true, Verify: Command{Argv: []string{"go", "test"}}}); err == nil {
		t.Fatal("skip verify accepted a verification command")
	}
}

// macOS exposes its temporary directory through both /var and /private/var.
// Go subprocesses may canonicalise that spelling even though os.MkdirTemp did
// not, so fuzz workspace containment must compare filesystem identities rather
// than the two lexical paths.
func TestPrepareFuzzWorkspaceAcceptsAliasedSnapshotParent(t *testing.T) {
	realParent := t.TempDir()
	realRoot := filepath.Join(realParent, "snapshot")
	packageDir := filepath.Join(realRoot, "subject")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "subject.go"), []byte("package subject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	aliasRoot := filepath.Join(aliasParent, "snapshot")
	destination := filepath.Join(t.TempDir(), "fuzz-workspace")

	got, err := prepareFuzzWorkspace(aliasRoot, destination, []execute.TestBinary{{
		ImportPath: "fixture.example/subject",
		Dir:        packageDir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Dir != filepath.Join(destination, "subject") {
		t.Fatalf("fuzz workspace binaries = %+v", got)
	}
}

func TestSelectTestPackagesAcceptsAliasedSnapshotRoot(t *testing.T) {
	realParent := t.TempDir()
	realRoot := filepath.Join(realParent, "snapshot")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	aliasRoot := filepath.Join(aliasParent, "snapshot")

	got, err := selectTestPackages(aliasRoot, []execute.TestBinary{{
		ImportPath: "fixture.example/root",
		Dir:        realRoot,
	}}, ".", "exec")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("selected package indexes = %v, want [0]", got)
	}
}

// TestBranchProofReachesTheEngineAPI is the consumer-facing half of the branch
// proof. An embedder reads the public catalogue and never sees a discovery
// type, so the proof has to survive the conversion makeCatalog performs — and a
// mutant discovery proved nothing about has to keep carrying nothing.
func TestBranchProofReachesTheEngineAPI(t *testing.T) {
	digest := mutation.DigestString("package a\n")
	located := func(name string, start, end uint32, original, replacement string, branch *discover.BranchProof) discover.Located {
		t.Helper()
		rule, ok := mutation.CanonicalRegistry().Lookup(name)
		if !ok {
			t.Fatalf("the canonical registry does not know %s", name)
		}
		span, err := mutation.NewSpan(start, end)
		if err != nil {
			t.Fatalf("building the span: %v", err)
		}
		return discover.Located{
			Candidate: mutation.Candidate{
				Path:         "a/a.go",
				Rule:         rule,
				Span:         span,
				Original:     original,
				Replacement:  replacement,
				SourceDigest: digest,
			},
			Line:    3,
			Column:  9,
			Package: "example.com/mini/a",
			Branch:  branch,
		}
	}
	found := discover.Result{
		Candidates: []discover.Located{
			located("le-to-lt", 20, 22, "<=", "<", &discover.BranchProof{
				Direction:       discover.BranchDecreasing,
				BodyStartLine:   3,
				BodyStartColumn: 12,
				BodyEndLine:     5,
				BodyEndColumn:   2,
			}),
			located("lt-to-le", 30, 31, "<", "<=", nil),
		},
		ModulePath: "example.com/mini",
		GoVersion:  "1.26",
	}
	catalog, err := discover.BuildCatalog(found)
	if err != nil {
		t.Fatalf("cataloguing: %v", err)
	}

	public, _ := makeCatalog("", gocmd.Toolchain{}, "balanced", found, catalog, nil, nil, nil, nil)
	if len(public.Mutants) != 2 {
		t.Fatalf("the catalogue holds %d mutants, want 2", len(public.Mutants))
	}
	proved, plain := public.Mutants[0], public.Mutants[1]
	if proved.Rule != "le-to-lt" || plain.Rule != "lt-to-le" {
		t.Fatalf("the catalogue lists %s then %s, want le-to-lt then lt-to-le", proved.Rule, plain.Rule)
	}
	if proved.Branch == nil {
		t.Fatalf("the proved mutant carries no branch: %+v", proved)
	}
	want := BranchProof{
		Direction:       BranchDecreasing,
		BodyStartLine:   3,
		BodyStartColumn: 12,
		BodyEndLine:     5,
		BodyEndColumn:   2,
	}
	if *proved.Branch != want {
		t.Errorf("branch = %+v, want %+v", *proved.Branch, want)
	}
	if plain.Branch != nil {
		t.Errorf("the unproved mutant carries a branch: %+v", *plain.Branch)
	}

	// Session.Catalog hands out a copy, and a proof is the one pointer in a
	// mutant, so the copy has to carry its own: an aliased proof would leave a
	// caller one assignment away from rewriting the session's catalogue.
	clone := cloneCatalog(public)
	if clone.Mutants[0].Branch == proved.Branch {
		t.Errorf("cloneCatalog aliased the branch proof")
	}
	if *clone.Mutants[0].Branch != want {
		t.Errorf("cloneCatalog changed the branch proof: %+v", *clone.Mutants[0].Branch)
	}
	if clone.Mutants[1].Branch != nil {
		t.Errorf("cloneCatalog gave the unproved mutant a branch: %+v", *clone.Mutants[1].Branch)
	}
}
