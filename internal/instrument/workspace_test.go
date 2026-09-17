// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

const (
	firstModule  = "example.com/ws/first"
	secondModule = "example.com/ws/second"

	firstSource = `package first

func Equal(a, b int) bool { return a == b }
`
	secondSource = `package second

func Wider(a, b int) bool { return a > b }
`
)

func TestInstrumentingOneModuleOfAWorkspaceRewritesOnlyItsOwnFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspaceModule(t, root, "first", firstSource)
	writeWorkspaceModule(t, root, "second", secondSource)
	catalog, hints := workspaceCatalog(t)

	result, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: filepath.Join(root, "first"),
		ModulePath:   firstModule,
		Module:       firstModule,
		Catalog:      catalog,
		Hints:        hints,
	})
	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	if got := result.FilesInstrumented; len(got) != 1 || got[0] != "first.go" {
		t.Errorf("FilesInstrumented = %v, want just the one file of this module", got)
	}

	rewritten := readWorkspaceFile(t, filepath.Join(root, "first", "first.go"))
	if !strings.Contains(rewritten, "__gm.M[") {
		t.Errorf("the module's own file was not instrumented:\n%s", rewritten)
	}
	if untouched := readWorkspaceFile(t, filepath.Join(root, "second", "second.go")); untouched != secondSource {
		t.Errorf("the sibling module's file was rewritten:\n%s", untouched)
	}
	if _, err := os.Stat(filepath.Join(root, "second", result.RuntimeDir)); err == nil {
		t.Errorf("a runtime was written into the sibling module")
	}

	runtime := readWorkspaceFile(t, filepath.Join(root, "first", result.RuntimeDir, result.RuntimeDir+".go"))
	for _, mutant := range catalog.Mutants() {
		if !strings.Contains(runtime, mutant.ID) {
			t.Errorf("the runtime of %s does not know %s, a mutant of %s",
				firstModule, mutant.ID[:8], mutant.ModulePath)
		}
	}
}

func TestInstrumentingRefusesACatalogueAndAModuleThatDisagree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspaceModule(t, root, "first", firstSource)
	writeWorkspaceModule(t, root, "second", secondSource)
	workspace, workspaceHints := workspaceCatalog(t)
	alone, aloneHints := singleModuleCatalog(t)

	for _, tc := range []struct {
		name    string
		catalog *mutation.Catalog
		hints   instrument.Hints
		module  string
		says    string
	}{
		{
			name:    "a workspace catalogue and no module",
			catalog: workspace,
			hints:   workspaceHints,
			says:    "names the module",
		},
		{
			name:    "a single-module catalogue and a module",
			catalog: alone,
			hints:   aloneHints,
			module:  firstModule,
			says:    "names no module",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := instrument.Instrument(instrument.Options{
				SnapshotRoot: filepath.Join(root, "first"),
				ModulePath:   firstModule,
				Module:       tc.module,
				Catalog:      tc.catalog,
				Hints:        tc.hints,
			})
			if err == nil {
				t.Fatal("Instrument accepted it")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the message does not say %q: %v", tc.says, err)
			}
		})
	}
}

func workspaceCatalog(t *testing.T) (*mutation.Catalog, instrument.Hints) {
	t.Helper()

	return workspaceCatalogOf(t,
		workspaceEdit(t, firstModule, "first.go", firstSource, "eq-to-neq", "a == b", "==", "!="),
		workspaceEdit(t, secondModule, "second.go", secondSource, "gt-to-ge", "a > b", ">", ">="))
}

func singleModuleCatalog(t *testing.T) (*mutation.Catalog, instrument.Hints) {
	t.Helper()

	return workspaceCatalogOf(t, workspaceEdit(t, "", "first.go", firstSource, "eq-to-neq", "a == b", "==", "!="))
}

func workspaceEdit(t *testing.T, module, path, source, rule, site, original, replacement string) discover.Located {
	t.Helper()

	siteStart := strings.Index(source, site)
	start := strings.Index(source, original)
	return discover.Located{
		Candidate: mutation.Candidate{
			ModulePath:   module,
			Path:         path,
			Rule:         lookupRule(t, rule),
			Span:         mutation.Span{StartByte: uint32(start), EndByte: uint32(start + len(original))},
			Original:     original,
			Replacement:  replacement,
			SourceDigest: mutation.DigestString(source),
		},
		Line:    3,
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

func workspaceCatalogOf(t *testing.T, located ...discover.Located) (*mutation.Catalog, instrument.Hints) {
	t.Helper()

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

func writeWorkspaceModule(t *testing.T, root, dir, source string) {
	t.Helper()

	moduleRoot := filepath.Join(root, dir)
	if err := os.MkdirAll(moduleRoot, 0o755); err != nil {
		t.Fatalf("making %s: %v", moduleRoot, err)
	}
	name := strings.SplitN(source, "\n", 2)[0]
	name = strings.TrimPrefix(name, "package ")
	if err := os.WriteFile(filepath.Join(moduleRoot, name+".go"), []byte(source), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	gomod := "module example.com/ws/" + dir + "\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
}

func readWorkspaceFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
