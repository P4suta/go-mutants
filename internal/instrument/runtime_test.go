// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// generatedMarker is the convention every Go code generator follows
// (https://go.dev/s/generatedcode) and the exact pattern internal/discover
// refuses to mutate on. The generated runtime has to match it: a later run over
// a tree that somehow kept it must skip it rather than mutate the machinery it
// is mutating with.
var generatedMarker = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// runtimeSample is the source the runtime fixture's catalogue is built from. It
// is inline rather than a testdata file because the fixture pins mutant IDs,
// which are hashed from these bytes: keeping them next to the assertion is what
// makes a fixture diff explainable.
const runtimeSample = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package sample is the three-mutant catalogue the runtime fixture is built
// from.
package sample

// Less reports whether a is less than b.
func Less(a, b int) bool {
	return a < b
}
`

// TestRuntimeGolden pins the generated activation package for a three-mutant
// catalogue.
//
// Everything in it is load-bearing somewhere else: the dense indices are what
// the guards read, the full IDs are what the runner sets in the environment,
// and the exit status is what tells the supervisor a stale catalogue was
// activated rather than a mutant surviving. A fixture is how a change to any of
// them becomes a diff somebody has to justify.
func TestRuntimeGolden(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), []byte(runtimeSample))
	catalog := catalogOf(t, threeAlternatives(t, []byte(runtimeSample)))
	if catalog.Len() != 3 {
		t.Fatalf("the fixture catalogue holds %d mutants, want 3", catalog.Len())
	}

	result := instrumentSnapshot(t, root, catalog)
	generated := filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go")
	out := readFile(t, generated)

	golden := filepath.Join("testdata", "runtime.golden")
	if *updateGolden {
		writeFile(t, golden, out)
	}
	if want := readFile(t, golden); !bytes.Equal(out, want) {
		t.Errorf("the generated runtime does not match its fixture\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}

	if _, err := parser.ParseFile(token.NewFileSet(), generated, out, parser.SkipObjectResolution); err != nil {
		t.Errorf("the generated runtime does not parse: %v", err)
	}
	if !generatedMarker.Match(out) {
		t.Error("the generated runtime does not carry the standard generated-code marker")
	}
	if !bytes.HasPrefix(out, []byte("// SPDX-FileCopyrightText:")) {
		t.Error("the generated runtime does not carry an SPDX header")
	}
	for _, want := range []string{
		"package gomutants_rt\n",
		"var M [3]bool",
		`const activeEnv = "` + instrument.ActiveEnv + `"`,
		"const unknownMutantExit = 97",
		"os.Exit(unknownMutantExit)",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("the generated runtime does not contain %q", want)
		}
	}
	for _, m := range catalog.Mutants() {
		if !bytes.Contains(out, []byte(`"`+m.ID+`": `)) {
			t.Errorf("the generated runtime does not map mutant %s", m.DisplayID)
		}
	}
}

// TestRuntimeIsGeneratedForAnEmptyCatalogue proves the activation array is
// never zero-length.
//
// An empty catalogue is a real case — a run whose filters selected nothing —
// and `var M [0]bool` is legal Go that leaves the package's only export
// unusable. One element keeps the generated source one shape instead of two.
func TestRuntimeIsGeneratedForAnEmptyCatalogue(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	result := instrumentSnapshot(t, root, catalogOf(t, nil))

	if len(result.FilesInstrumented) != 0 {
		t.Errorf("FilesInstrumented = %v, want none", result.FilesInstrumented)
	}
	out := readFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
	if !bytes.Contains(out, []byte("var M [1]bool")) {
		t.Errorf("an empty catalogue generated an activation array that is not [1]bool:\n%s", out)
	}
	if !bytes.Contains(out, []byte("var ids = map[string]uint32{}")) {
		t.Errorf("an empty catalogue generated a non-empty id table:\n%s", out)
	}
}

// TestRuntimeDirectoryIsBumpedOnCollision keeps the instrumenter out of a
// directory the snapshot already had.
//
// The snapshot is a copy of somebody's repository, and a directory named after
// this tool in it is theirs until proven otherwise. Bumping is also why the
// import path is reported back rather than assumed by the caller.
func TestRuntimeDirectoryIsBumpedOnCollision(t *testing.T) {
	t.Parallel()

	in := readFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), in)
	writeFile(t, filepath.Join(root, "gomutants_rt", "theirs.go"), []byte("package theirs\n"))

	result := instrumentSnapshot(t, root, catalogOf(t, candidatesFor(t, nil, in)))

	if got, want := result.RuntimeDir, "gomutants_rt1"; got != want {
		t.Errorf("RuntimeDir = %q, want %q", got, want)
	}
	if got, want := result.RuntimeImport, testModule+"/gomutants_rt1"; got != want {
		t.Errorf("RuntimeImport = %q, want %q", got, want)
	}
	if got := readFile(t, filepath.Join(root, "gomutants_rt", "theirs.go")); string(got) != "package theirs\n" {
		t.Errorf("the existing directory was written into: %q", got)
	}
	out := readFile(t, filepath.Join(root, sampleFile))
	if !bytes.Contains(out, []byte(`"`+result.RuntimeImport+`"`)) {
		t.Errorf("the instrumented file does not import the bumped runtime:\n%s", out)
	}
	generated := readFile(t, filepath.Join(root, "gomutants_rt1", "gomutants_rt1.go"))
	if !bytes.Contains(generated, []byte("package gomutants_rt1\n")) {
		t.Errorf("the bumped runtime declares the wrong package:\n%s", generated)
	}
}

// TestRuntimeDirectoryIsVisibleToTheGoTool guards the one thing about the
// directory name that is not cosmetic: the go tool ignores directories whose
// name begins with "_" or "." outright, so a runtime hidden in one would never
// be built and every instrumented package would fail to resolve its import.
func TestRuntimeDirectoryIsVisibleToTheGoTool(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	result := instrumentSnapshot(t, root, catalogOf(t, nil))
	for _, prefix := range []string{"_", ".", "testdata"} {
		if strings.HasPrefix(result.RuntimeDir, prefix) {
			t.Errorf("RuntimeDir = %q, which the go tool would ignore", result.RuntimeDir)
		}
	}
	if info, err := os.Stat(filepath.Join(root, result.RuntimeDir)); err != nil || !info.IsDir() {
		t.Errorf("the runtime directory %q was not created: %v", result.RuntimeDir, err)
	}
}

// threeAlternatives is the three-rule catalogue the runtime fixture pins: one
// operator, three distinct replacements, three dense indices.
func threeAlternatives(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()

	start := uint32(bytes.IndexByte(src, '<'))
	digest := mutation.Digest(src)
	var out []mutation.Candidate
	for _, swap := range []struct{ rule, replacement string }{
		{"eq-to-neq", "!="},
		{"neq-to-eq", "=="},
		{"lt-to-le", "<="},
	} {
		out = append(out, mutation.Candidate{
			Path:         sampleFile,
			Rule:         lookupRule(t, swap.rule),
			Span:         mutation.Span{StartByte: start, EndByte: start + 1},
			Original:     "<",
			Replacement:  swap.replacement,
			SourceDigest: digest,
		})
	}
	return out
}
